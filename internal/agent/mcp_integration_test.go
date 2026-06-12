package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type agentMCPFixture struct {
	server       *httptest.Server
	mutateCalls  atomic.Int32
	inspectCalls atomic.Int32
}

func newAgentMCPFixture(t *testing.T) *agentMCPFixture {
	t.Helper()
	fixture := &agentMCPFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      int64          `json:"id"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", fmt.Sprintf("session-%d", request.ID))
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"agent-test","version":"1"}}}`, request.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[`+
				`{"name":"mutate","description":"perform one side effect","inputSchema":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false},"annotations":{"destructiveHint":true}},`+
				`{"name":"inspect","description":"read state","inputSchema":{"type":"object","properties":{},"additionalProperties":false},"annotations":{"readOnlyHint":true}}]}}`, request.ID)
		case "tools/call":
			name, _ := request.Params["name"].(string)
			text := "unknown"
			switch name {
			case "mutate":
				fixture.mutateCalls.Add(1)
				text = "mutated"
			case "inspect":
				fixture.inspectCalls.Add(1)
				text = "inspected"
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%q}]}}`, request.ID, text)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func mcpAgentConfig(t *testing.T, url string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	cfg.MCPServers = map[string]config.MCPServerConfig{
		"demo": {Transport: "http", URL: url, TimeoutSec: 10},
	}
	return cfg
}

func TestApprovedMCPActionIsDeduplicatedAcrossContinuation(t *testing.T) {
	fixture := newAgentMCPFixture(t)
	cfg := mcpAgentConfig(t, fixture.server.URL)
	call := llm.ToolCall{ID: "mcp-call-1", Name: "mcp__demo__mutate", Arguments: map[string]any{"value": "once"}}
	firstProvider := &nativeToolProvider{decisions: []llm.ToolDecision{{Text: "mutate", ToolCalls: []llm.ToolCall{call}}}}
	session := history.New("mcp-approval", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "perform the mutation once")

	first := NewRunner(cfg, firstProvider).Run(t.Context(), session)
	if first.Pending == nil || first.Pending.Call.Name != "mcp__demo__mutate" {
		t.Fatalf("pending = %#v", first.Pending)
	}
	if fixture.mutateCalls.Load() != 0 {
		t.Fatal("destructive MCP call executed before approval")
	}
	for _, event := range first.Events {
		session.AppendEvent(event)
	}
	approved := NewRunner(cfg, nil).ExecuteApproved(t.Context(), *first.Pending)
	if approved.Status == "error" {
		t.Fatalf("approved event = %#v", approved)
	}
	session.AppendEvent(approved)
	if fixture.mutateCalls.Load() != 1 {
		t.Fatalf("mutate calls after approval = %d", fixture.mutateCalls.Load())
	}

	resumedProvider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{Text: "repeat", ToolCalls: []llm.ToolCall{{ID: "mcp-call-2", Name: call.Name, Arguments: call.Arguments}}},
		{Text: `{"final":"MCP mutation completed once."}`},
	}}
	resumed := NewRunner(cfg, resumedProvider).Run(t.Context(), session)
	if resumed.Pending != nil || !strings.Contains(resumed.Text, "once") {
		t.Fatalf("resumed = %#v", resumed)
	}
	if fixture.mutateCalls.Load() != 1 {
		t.Fatalf("duplicate MCP mutation executed: %d", fixture.mutateCalls.Load())
	}
	var deduplicated bool
	for _, event := range resumed.Events {
		if event.Type == history.EventToolResult && metadataBool(event.Metadata, "deduplicated") {
			deduplicated = true
		}
	}
	if !deduplicated {
		t.Fatal("expected deduplicated MCP tool result")
	}
}

func TestReadOnlyMCPToolRunsWithoutWriteApprovalAndSchemaIsExposed(t *testing.T) {
	fixture := newAgentMCPFixture(t)
	cfg := mcpAgentConfig(t, fixture.server.URL)
	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{Text: "inspect", ToolCalls: []llm.ToolCall{{ID: "inspect-1", Name: "mcp__demo__inspect", Arguments: map[string]any{}}}},
		{Text: `{"final":"inspection complete"}`},
	}}
	session := history.New("mcp-read", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect MCP state")
	result := NewRunner(cfg, provider).Run(t.Context(), session)
	if result.Pending != nil || result.Text != "inspection complete" || fixture.inspectCalls.Load() != 1 {
		t.Fatalf("result=%#v inspect_calls=%d", result, fixture.inspectCalls.Load())
	}
	var found bool
	for _, spec := range provider.specs {
		if spec.Name == "mcp__demo__inspect" {
			found = true
			if spec.Parameters.Type != "object" {
				t.Fatalf("schema = %#v", spec.Parameters)
			}
		}
	}
	if !found {
		t.Fatalf("MCP tool missing from native provider specs: %#v", provider.specs)
	}
}
