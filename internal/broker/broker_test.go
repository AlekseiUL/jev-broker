package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "synthetic-token-0123456789-abcdefghijklmnopqrstuvwxyz"

func testRegistry(t *testing.T) Registry {
	t.Helper()
	digest := sha256.Sum256([]byte(testToken))
	return Registry{Callers: []Caller{{ID: "agent-one", Digest: digest}}}
}

func testAudit(t *testing.T) *Audit {
	t.Helper()
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRegistryPrivateConfigAndAuthentication(t *testing.T) {
	digest := sha256.Sum256([]byte(testToken))
	path := filepath.Join(t.TempDir(), "clients.json")
	good := `{"profiles":[{"id":"agent-one","token_sha256":"` + hex.EncodeToString(digest[:]) + `"}]}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(path)
	if err != nil || len(r.Callers) != 1 {
		t.Fatalf("valid config rejected: %v", err)
	}
	if r.Authenticate("Bearer "+testToken) == nil || r.Authenticate("Bearer "+"wrong-token-0123456789-abcdefghijklmnopqrstuvwxyz") != nil {
		t.Fatal("bad bearer result")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("world-readable config accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"profiles":[{"id":"agent-one","token_sha256":"`+hex.EncodeToString(digest[:])+`","bearer":"secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("raw bearer field accepted")
	}
}

func TestPlanDynamicModesAndCredentialGate(t *testing.T) {
	raw := []byte(`{"state":{"goal":"Classify synthetic feedback"},"items":{"a":{"record_id":9007199254740993,"text":"Synthetic feedback"}},"questions":{"is_relevant":{"mode":"noul","task":"Does this match the goal?"},"kind":{"mode":"choice","task":"Choose category","options":{"complaint":"Complaint","none_unknown":"No clear match"}},"quality":{"mode":"score","task":"Rate evidence","rubric":["none","possible","strong"]}}}`)
	plan, err := PlanInput(raw, DefaultModel)
	if err != nil || len(plan.Items) != 1 || len(plan.ProviderQuestions) != 3 {
		t.Fatalf("valid plan failed: %v", err)
	}
	if !bytes.Contains(plan.Items["a"].State, []byte("9007199254740993")) {
		t.Fatal("record ID rounded")
	}
	if plan.Items["a"].Model != DefaultModel {
		t.Fatal("server model missing")
	}
	for _, candidate := range [][]byte{
		[]byte(`{"state":"x","state":"y","questions":{"q":{"mode":"noul","task":"?"}}}`),
		[]byte(`{"state":{"api_key":"synthetic-key-value"},"questions":{"q":{"mode":"noul","task":"?"}}}`),
		[]byte(`{"state":{"api\u005fkey":"synthetic-key-value"},"questions":{"q":{"mode":"noul","task":"?"}}}`),
		[]byte(`{"state":"x","questions":{"q":{"mode":"choice","task":"?","options":{"one":"One"}}}}`),
		[]byte(`{"state":"x","questions":{"q":{"mode":"score","task":"?","rubric":["only"]}}}`),
		[]byte(`{"state":"x","questions":{"q":{"mode":"score","task":"?","rubric":["0","1","2","3","4","5","6","7","8","9","10"]}}}`),
		[]byte(`{"state":"x","model":"attacker-model","questions":{"q":{"mode":"noul","task":"?"}}}`),
	} {
		if _, err := PlanInput(candidate, DefaultModel); err == nil {
			t.Fatalf("invalid plan accepted: %s", candidate)
		}
	}
}

func mixedQuestions() map[string]Question {
	return map[string]Question{
		"yes":    {Mode: "noul", Task: json.RawMessage(`"Relevant?"`)},
		"kind":   {Mode: "choice", Task: json.RawMessage(`"Choose kind"`), Options: json.RawMessage(`{"complaint":"Complaint","none_unknown":"No clear match"}`)},
		"weight": {Mode: "score", Task: json.RawMessage(`"Rate evidence"`), Rubric: json.RawMessage(`["none","strong"]`)},
	}
}

const mixedReply = `{"answers":{"yes":{"type":"noul","noul":0.81},"kind":{"type":"choice","choice":"complaint","confidence":0.8,"probabilities":{"complaint":0.9,"none_unknown":0.1}},"weight":{"type":"score","score":0.8,"probabilities":{"0":0.2,"1":0.8},"legend":{"0":"none","1":"strong"}}},"usage":{"input_tokens":10,"output_tokens":4,"cost":0.00001},"state":"provider-echo-must-not-return"}`

func TestValidateReplyTypedAndSanitized(t *testing.T) {
	reply, err := ValidateReply([]byte(mixedReply), mixedQuestions(), DefaultModel)
	if err != nil || len(reply.Answers) != 3 {
		t.Fatalf("valid reply failed: %v", err)
	}
	encoded, err := replyJSON(reply)
	if err != nil || bytes.Contains(encoded, []byte("provider-echo-must-not-return")) {
		t.Fatal("provider echo survived response shaping")
	}
	for _, bad := range []string{
		strings.Replace(mixedReply, `"choice":"complaint"`, `"choice":"invented"`, 1),
		strings.Replace(mixedReply, `"1":"strong"`, `"1":"wrong"`, 1),
		strings.Replace(mixedReply, `"score":0.8`, `"score":0.1`, 1),
		strings.Replace(mixedReply, `"noul":0.81`, `"noul":1.8`, 1),
		strings.Replace(mixedReply, `"answers":{`, `"answers":{"extra":{"type":"noul","noul":1},`, 1),
	} {
		if _, err := ValidateReply([]byte(bad), mixedQuestions(), DefaultModel); err == nil {
			t.Fatal("mismatched provider answer accepted")
		}
	}
}

type providerFunc func(context.Context, ProviderRequest) ([]byte, error)

func (f providerFunc) Evaluate(ctx context.Context, r ProviderRequest) ([]byte, error) {
	return f(ctx, r)
}

func TestServiceAuditsBeforePOSTAndDoesNotRetry(t *testing.T) {
	audit := testAudit(t)
	var calls int
	service, err := NewService(providerFunc(func(_ context.Context, r ProviderRequest) ([]byte, error) {
		calls++
		log, err := os.ReadFile(audit.path)
		if err != nil || !bytes.Contains(log, []byte(`"event":"attempt"`)) {
			t.Fatal("provider called before durable attempt audit")
		}
		return []byte(`{"answers":{"q":{"type":"noul","noul":0.8}},"usage":{"cost":0.00001}}`), nil
	}), audit, DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Evaluate(context.Background(), "agent-one", []byte(`{"state":"synthetic","questions":{"q":{"mode":"noul","task":"Relevant?"}}}`))
	if err != nil || !bytes.Contains(result, []byte(`"noul":0.8`)) || calls != 1 {
		t.Fatalf("offline call failed: calls=%d err=%v", calls, err)
	}
	service.Provider = providerFunc(func(context.Context, ProviderRequest) ([]byte, error) { calls++; return nil, ErrProvider })
	_, err = service.Evaluate(context.Background(), "agent-one", []byte(`{"state":"synthetic","questions":{"q":{"mode":"noul","task":"Relevant?"}}}`))
	if err == nil || calls != 2 {
		t.Fatalf("provider failure retried: calls=%d err=%v", calls, err)
	}
}

func TestBatchMakesOneOfflineProviderCallPerItem(t *testing.T) {
	audit := testAudit(t)
	var calls int
	service, err := NewService(providerFunc(func(_ context.Context, r ProviderRequest) ([]byte, error) {
		calls++
		if r.Model != DefaultModel || !bytes.Contains(r.State, []byte(`"item"`)) {
			t.Fatal("batch request lost fixed model or item context")
		}
		return []byte(`{"answers":{"q":{"type":"noul","noul":0.7}},"usage":{"cost":0.00001}}`), nil
	}), audit, DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := service.Evaluate(context.Background(), "agent-one", []byte(`{"state":{"goal":"synthetic"},"items":{"a":{"text":"first"},"b":{"text":"second"}},"questions":{"q":{"mode":"noul","task":"Relevant?"}}}`))
	if err != nil || calls != 2 || !bytes.Contains(answer, []byte(`"item_count":2`)) || !bytes.Contains(answer, []byte(`"succeeded":2`)) {
		t.Fatalf("batch mismatch: calls=%d err=%v answer=%s", calls, err, answer)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderNon2xxDoesNotLeakBodyOrRetry(t *testing.T) {
	p, err := NewOpenRouterProvider("synthetic-provider-key")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	p.client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer synthetic-provider-key" {
			t.Fatal("provider key absent")
		}
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"echo":"sensitive-input"}`))}, nil
	})}
	_, err = p.Evaluate(context.Background(), ProviderRequest{State: json.RawMessage(`"synthetic"`), Questions: map[string]providerQuestion{"q": {Type: "noul", Instructions: json.RawMessage(`"Relevant?"`)}}, Model: DefaultModel})
	if err == nil || strings.Contains(err.Error(), "sensitive-input") || calls != 1 {
		t.Fatalf("error leak or retry: calls=%d err=%v", calls, err)
	}
}

func TestHTTPMCPAuthAndTool(t *testing.T) {
	audit := testAudit(t)
	var calls atomic.Int32
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"answers":{"q":{"type":"noul","noul":0.9}}}`), nil
	}), audit, DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(testRegistry(t), service)
	for _, header := range []string{"", "Bearer " + "wrong-token-0123456789-abcdefghijklmnopqrstuvwxyz"} {
		r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(`{}`))
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized status=%d", response.Code)
		}
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.Header.Set("Authorization", "Bearer "+testToken)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, copy)
		return recorder.Result(), nil
	})}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "offline-test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1/mcp", HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "evaluate" {
		t.Fatalf("tool discovery failed: %v", err)
	}
	answer, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "evaluate", Arguments: json.RawMessage(`{"state":"synthetic","questions":{"q":{"mode":"noul","task":"Relevant?"}}}`)})
	if err != nil || answer.IsError || calls.Load() != 1 {
		t.Fatalf("authenticated tool call failed: %v", err)
	}
	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "evaluate", Arguments: json.RawMessage(`{"state":"synthetic","model":"attacker-choice","questions":{"q":{"mode":"noul","task":"Relevant?"}}}`)})
	if err != nil || !bad.IsError || calls.Load() != 1 {
		t.Fatalf("malformed MCP call reached provider: calls=%d err=%v", calls.Load(), err)
	}
}

func TestLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8768", ":8768", "192.0.2.1:8768", "example.com:8768"} {
		if ValidateLoopbackAddr(addr) == nil {
			t.Fatalf("non-loopback accepted: %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8768", "[::1]:8768"} {
		if ValidateLoopbackAddr(addr) != nil {
			t.Fatalf("loopback rejected: %s", addr)
		}
	}
}
