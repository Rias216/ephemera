package agent

import (
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/llm"
)

func TestContextWindowRecallsRelevantOlderTurn(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "The retry policy lives in internal/agent/provider_retry.go and must preserve context overflow handling."},
		{Role: "assistant", Content: "Acknowledged provider_retry.go."},
	}
	for index := 0; index < 14; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, llm.Message{Role: role, Content: strings.Repeat("unrelated recent conversation ", 20)})
	}
	window := ContextWindow{
		System:         strings.Repeat("s", 200),
		Budget:         900,
		SummaryTokens:  120,
		RecallMessages: 2,
		Query:          "continue fixing provider_retry.go context overflow",
		Messages:       messages,
	}
	selected, stats := window.Fit()
	if stats.Dropped == 0 {
		t.Fatal("expected a constrained context window")
	}
	joined := ""
	for _, message := range selected {
		joined += message.Content + "\n"
	}
	if !strings.Contains(joined, "provider_retry.go") {
		t.Fatalf("relevant older turn was not recalled:\n%s", joined)
	}
}

func TestContextWindowDeduplicatesNativeToolResults(t *testing.T) {
	turns := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read_file"}}},
		{Role: "tool", ToolResult: &llm.ToolResult{ID: "call-1", Name: "read_file", OK: true, Output: "first"}},
		{Role: "tool", ToolResult: &llm.ToolResult{ID: "call-1", Name: "read_file", OK: true, Output: "duplicate"}},
	}
	window := ContextWindow{System: "system", Budget: 1000, Messages: []llm.Message{{Role: "user", Content: "inspect"}}, NativeTurns: turns}
	selected, _ := window.Fit()
	results := 0
	for _, message := range selected {
		if message.ToolResult != nil {
			results++
		}
	}
	if results != 1 {
		t.Fatalf("native tool results = %d, want 1", results)
	}
}
