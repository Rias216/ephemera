package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestDynamicToolRegistrationAndStreaming(t *testing.T) {
	name := "test_dynamic_stream_tool"
	err := Register(Tool{Name: name, Description: "test dynamic tool", Risk: RiskRead}, func(_ context.Context, _ Registry, _ Call, emit func(string)) Result {
		if emit != nil {
			emit("partial")
		}
		return ok(name, "complete")
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(config.Config{AgentSettings: config.AgentSettings{WorkspaceRoot: t.TempDir(), ApprovalPolicy: config.ApprovalAutoApprove, MaxToolOutputTokens: 100}})
	var chunks []string
	result := registry.ExecuteStream(context.Background(), Call{Name: name}, func(chunk string) { chunks = append(chunks, chunk) })
	if !result.OK || result.Output != "complete" || len(chunks) != 1 || chunks[0] != "partial" {
		t.Fatalf("result=%#v chunks=%#v", result, chunks)
	}
	found := false
	for _, tool := range Builtins() {
		if tool.Name == name {
			found = true
		}
	}
	if !found {
		t.Fatal("registered tool missing from catalog")
	}
}

func TestTruncationIncludesSummaryAndTail(t *testing.T) {
	value := strings.Repeat("head-line\n", 20) + "important-tail"
	output, summary := truncateApproxTokensWithSummary(value, 10)
	if summary == "" || !strings.Contains(output, "truncated output") || !strings.Contains(output, "important-tail") {
		t.Fatalf("output=%q summary=%q", output, summary)
	}
}
