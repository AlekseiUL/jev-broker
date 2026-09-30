package broker

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const ToolDescription = "JEV is an optional probabilistic decision tool, not a search engine or answer writer. " +
	"Choose your own evidence (state or items), named questions and modes. noul judges a precise yes/no condition; " +
	"choice selects among your explicit options (add none_unknown if none may fit); score rates against your ordered rubric. " +
	"Each item is a separate paid provider call and batches are sequential. Check consequential judgments against primary evidence, " +
	"keep uncertainty visible, and never send credentials or material you have no right to share. " +
	"The provider key, model and endpoint are controlled by the broker, not by tool arguments."

func Handler(registry Registry, service *Service) http.Handler {
	perProfile := make(map[string]http.Handler, len(registry.Callers))
	for _, profile := range registry.Callers {
		id := profile.ID
		server := mcp.NewServer(&mcp.Implementation{Name: "jev-broker", Version: "1.0.0"}, nil)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "evaluate",
			Description: ToolDescription,
			InputSchema: ToolSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
		}, func(ctx context.Context, request *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			result, err := service.Evaluate(ctx, id, request.Params.Arguments)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(result)}}}, nil, nil
		})
		perProfile[id] = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
			MaxRequestBodyBytes:          MaxInputBytes + 64<<10,
			Stateless:                    true,
			PropagateRequestCancellation: true,
		})
	}
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(r.Header.Values("Authorization")) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		caller := registry.Authenticate(r.Header.Get("Authorization"))
		if caller == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// SDK v1.7 only ties a provider handler to the POST context for the
		// >= 2026-07-28 protocol on a stateless transport. Reject older
		// clients before any tool can trigger an unbounded orphaned call.
		if r.Method != http.MethodPost || r.Header.Get("Mcp-Protocol-Version") != "2026-07-28" {
			http.Error(w, "MCP protocol 2026-07-28 required", http.StatusBadRequest)
			return
		}
		perProfile[caller.ID].ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(authenticated))
	return mux
}

// EncodeExample is intentionally not an installer or a provider call. It
// returns the same input shape advertised through MCP tools/list.
func EncodeExample() []byte {
	value := map[string]any{
		"state": map[string]any{"goal": "Find relevant public product feedback"},
		"items": map[string]any{"post_a": map[string]any{"text": "A synthetic feedback example"}},
		"questions": map[string]any{
			"relevant": map[string]any{"mode": "noul", "task": "Does this post describe the requested product issue?"},
			"category": map[string]any{"mode": "choice", "task": "Choose the best category", "options": map[string]any{"complaint": "A concrete complaint", "none_unknown": "No clear match"}},
			"priority": map[string]any{"mode": "score", "task": "Rate the evidence", "rubric": []string{"not relevant", "possible", "strong direct evidence"}},
		},
	}
	encoded, _ := json.Marshal(value)
	return encoded
}
