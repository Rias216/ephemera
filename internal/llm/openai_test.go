package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatibleGenerateWithToolsParsesToolCalls(t *testing.T) {
	var sawTools bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		var payload struct {
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		sawTools = len(payload.Tools) == 1
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"need a file\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"go.mod\\\"}\"}}]}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)

	provider := NewOpenAICompatible("local", server.URL, "")
	decision, err := provider.GenerateWithTools(context.Background(), Request{
		Model:     "test-model",
		System:    "system",
		Messages:  []Message{{Role: "user", Content: "inspect"}},
		MaxTokens: 128,
	}, []ToolSpec{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters: ToolSchema{
			Type:                 "object",
			Properties:           map[string]ToolProperty{"path": {Type: "string"}},
			Required:             []string{"path"},
			AdditionalProperties: false,
		},
	}}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !sawTools {
		t.Fatal("server did not receive tool specs")
	}
	if decision.Text != "need a file" || len(decision.ToolCalls) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.ToolCalls[0].Name != "read_file" || decision.ToolCalls[0].Arguments["path"] != "go.mod" {
		t.Fatalf("tool call = %#v", decision.ToolCalls[0])
	}
}
