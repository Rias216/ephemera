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

func TestContextWindowCacheInvalidatesOnWorkingMemoryMutation(t *testing.T) {
	cache := NewContextFitCache()
	window := ContextWindow{
		System:         "system",
		Budget:         1200,
		SummaryTokens:  120,
		RecallMessages: 2,
		Provider:       "openai",
		Query:          "fix auth",
		WorkingMemory:  "decision: inspect auth.go",
		Messages:       []llm.Message{{Role: "user", Content: "fix auth"}},
		Cache:          cache,
	}
	first, _ := window.Fit()
	window.WorkingMemory = "decision: inspect session.go"
	second, _ := window.Fit()

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("unexpected empty fits: first=%#v second=%#v", first, second)
	}
	if first[0].Content == second[0].Content {
		t.Fatalf("cache served stale working memory: %q", second[0].Content)
	}
}

func TestContextWindowCacheReturnsDefensiveCopy(t *testing.T) {
	cache := NewContextFitCache()
	window := ContextWindow{
		System:   "system",
		Budget:   1200,
		Messages: []llm.Message{{Role: "user", Content: "original"}},
		Cache:    cache,
	}
	first, _ := window.Fit()
	first[0].Content = "mutated by caller"
	second, _ := window.Fit()
	if second[0].Content != "original" {
		t.Fatalf("cached fit was mutated through returned slice: %q", second[0].Content)
	}
}

func TestAdaptiveSummaryBudgetGrowsAcrossRun(t *testing.T) {
	early := ContextWindow{SummaryTokens: 10_000, Iteration: 1, MaxIterations: 10}.adaptiveSummaryTokens(10_000)
	late := ContextWindow{SummaryTokens: 10_000, Iteration: 10, MaxIterations: 10}.adaptiveSummaryTokens(10_000)
	if early >= late {
		t.Fatalf("adaptive summary budget did not grow: early=%d late=%d", early, late)
	}
	if early != 1000 || late != 4000 {
		t.Fatalf("adaptive summary budgets = %d/%d, want 1000/4000", early, late)
	}
}

func TestProviderAwareEstimateProtectsCodeHeavyRequests(t *testing.T) {
	text := `{"path":"internal/agent/context_window.go","start_line":1,"end_line":200}`
	providerEstimate := estimateVisibleTokensForProvider(text, "openai")
	runeEstimate := estimateVisibleTokens(text)
	if providerEstimate < runeEstimate {
		t.Fatalf("provider estimate %d undercut conservative rune estimate %d", providerEstimate, runeEstimate)
	}
}

func TestContextWindowReservesLatestNativeToolGroupUnderPressure(t *testing.T) {
	turns := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "main.go"}}}},
		{Role: "tool", ToolResult: &llm.ToolResult{ID: "call-1", Name: "read_file", OK: true, Output: "package main"}},
	}
	messages := []llm.Message{
		{Role: "user", Content: strings.Repeat("older context ", 120)},
		{Role: "assistant", Content: strings.Repeat("older response ", 120)},
		{Role: "user", Content: "Continue editing main.go using the tool result."},
	}
	selected, _ := (ContextWindow{System: "system", Provider: "openai", Budget: 260, Messages: messages, NativeTurns: turns}).Fit()
	var sawCall, sawResult, sawLatestUser bool
	for _, message := range selected {
		sawCall = sawCall || len(message.ToolCalls) == 1
		sawResult = sawResult || message.ToolResult != nil
		sawLatestUser = sawLatestUser || strings.Contains(message.Content, "Continue editing main.go")
	}
	if !sawCall || !sawResult || !sawLatestUser {
		t.Fatalf("fit omitted required native history: call=%t result=%t user=%t messages=%#v", sawCall, sawResult, sawLatestUser, selected)
	}
}
