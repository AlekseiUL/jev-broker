package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const syntheticQuestion = `"questions":{"q":{"mode":"noul","task":"Relevant?"}}`
const syntheticReply = `{"answers":{"q":{"type":"noul","noul":0.8}},"usage":{"cost":0.01}}`

func syntheticBatch(n int, state string) []byte {
	items := make(map[string]any, n)
	for i := range n {
		items[fmt.Sprintf("item%03d", i)] = map[string]any{"text": "synthetic"}
	}
	raw, _ := json.Marshal(map[string]any{
		"state":     state,
		"items":     items,
		"questions": map[string]any{"q": map[string]any{"mode": "noul", "task": "Relevant?"}},
	})
	return raw
}

func TestIssue1RejectsOversizedPaidBatchBeforeProvider(t *testing.T) {
	var calls atomic.Int32
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		calls.Add(1)
		return []byte(syntheticReply), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Evaluate(context.Background(), "agent-one", syntheticBatch(9, "synthetic")); !errors.Is(err, ErrCostLimit) {
		t.Fatalf("nine paid requests not rejected before POST: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("over-limit request reached provider")
	}
}

func TestIssue1PerProfileQuotaAndIsolation(t *testing.T) {
	var calls atomic.Int32
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		calls.Add(1)
		return []byte(syntheticReply), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"state":"synthetic",` + syntheticQuestion + `}`)
	for range 32 {
		if _, err := service.Evaluate(context.Background(), "agent-one", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Evaluate(context.Background(), "agent-one", raw); !errors.Is(err, ErrCostLimit) {
		t.Fatalf("profile quota did not block: %v", err)
	}
	if calls.Load() != 32 {
		t.Fatalf("unexpected paid attempts: %d", calls.Load())
	}
	if _, err := service.Evaluate(context.Background(), "agent-two", raw); err != nil {
		t.Fatalf("second profile blocked: %v", err)
	}
}

func TestIssue1OtherProfileCanUseItsOwnSlot(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	service, err := NewService(providerFunc(func(ctx context.Context, r ProviderRequest) ([]byte, error) {
		if strings.Contains(string(r.State), "hold") {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return []byte(syntheticReply), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = service.Evaluate(context.Background(), "agent-one", []byte(`{"state":"hold",`+syntheticQuestion+`}`))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter")
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Evaluate(context.Background(), "agent-two", []byte(`{"state":"other",`+syntheticQuestion+`}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("different profile blocked by busy first profile")
	}
}

func TestIssue1CredentialGateOnSyntheticShapes(t *testing.T) {
	inputs := []string{
		`{"HF_TOKEN":"hf_` + strings.Repeat("a", 24) + `"}`,
		`{"JEV_BROKER_TOKEN_ASSISTANT":"` + strings.Repeat("f", 64) + `"}`,
		`{"aws_secret_access_key":"SYNTHETIC-KEY-FOR-TEST"}`,
		`{"state":"postgres://fake:fake@localhost/fake"}`,
		`{"state":"eyJhbGciOiJub25lIn0.` + strings.Repeat("b", 24) + `.` + strings.Repeat("c", 24) + `"}`,
		`{"state":"glpat-` + strings.Repeat("x", 24) + `"}`,
		`{"state":"hf_` + strings.Repeat("y", 24) + `"}`,
		`{"state":"1234567890:` + strings.Repeat("A", 35) + `"}`,
		`{"пароль":"выдуманный_секрет"}`,
	}
	for i, input := range inputs {
		raw := []byte(`{"state":` + input + `,` + syntheticQuestion + `}`)
		if _, err := PlanInput(raw, DefaultModel); !errors.Is(err, ErrCredential) {
			t.Errorf("credential-shaped example %d accepted: %v", i, err)
		}
	}
}

func TestIssue1RejectsOutboundAmplificationBeforeProvider(t *testing.T) {
	if _, err := PlanInput(syntheticBatch(8, strings.Repeat("x", 600<<10)), DefaultModel); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("amplified payload accepted: %v", err)
	}
}

func TestIssue1OldProtocolRejectedBeforePaidCall(t *testing.T) {
	var calls atomic.Int32
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		calls.Add(1)
		return []byte(syntheticReply), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(testRegistry(t), service))
	defer server.Close()
	for _, version := range []string{"", "2025-11-25"} {
		body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"evaluate","arguments":{"state":"synthetic","questions":{"q":{"mode":"noul","task":"Relevant?"}}}}}`)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Protocol-Version", version)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("old protocol %q: status %d, provider calls %d", version, resp.StatusCode, calls.Load())
		}
	}
}

func TestIssue1CostOverflowFailsInsteadOfSuccessfulCorruptReply(t *testing.T) {
	service, err := NewService(providerFunc(func(context.Context, ProviderRequest) ([]byte, error) {
		return []byte(`{"answers":{"q":{"type":"noul","noul":0.8}},"usage":{"cost":1e308}}`), nil
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Evaluate(context.Background(), "agent-one", syntheticBatch(2, "synthetic"))
	if !errors.Is(err, ErrInvalidProviderReply) || len(result) != 0 {
		t.Fatalf("overflow produced a success: result=%q err=%v", result, err)
	}
}

func TestIssue1HTTPDisconnectCancelsMockProvider(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	service, err := NewService(providerFunc(func(ctx context.Context, _ ProviderRequest) ([]byte, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
			return []byte(syntheticReply), nil
		}
	}), testAudit(t), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(testRegistry(t), service))
	defer server.Close()
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer "+testToken)
		return http.DefaultTransport.RoundTrip(r)
	})}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "cancel-test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evaluate", Arguments: json.RawMessage(`{"state":"synthetic",` + syntheticQuestion + `}`)})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider not reached")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP cancel did not reach mock provider")
	}
	<-result
}
