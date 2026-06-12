package agent

import (
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"strings"
)

type messageSelection struct {
	Sent    int
	Total   int
	Dropped int
}

func selectAgentMessages(messages []history.Message, system string, budget int) ([]llm.Message, messageSelection) {
	return selectAgentMessagesWithSummary(messages, system, budget, 0)
}

func selectAgentMessagesWithSummary(messages []history.Message, system string, budget, summaryTokens int) ([]llm.Message, messageSelection) {
	window := ContextWindow{
		System:        system,
		Budget:        budget,
		SummaryTokens: summaryTokens,
		Messages:      conversationMessages(messages),
	}
	return window.Fit()
}

func messageSliceTokens(messages []llm.Message) int {
	return messageSliceTokensForProvider(messages, "")
}

func messageSliceTokensForProvider(messages []llm.Message, provider string) int {
	total := 0
	for _, message := range messages {
		total += estimateLLMMessageTokensForProvider(message, provider)
	}
	return total
}

func summarizeDroppedMessages(messages []llm.Message, maxTokens int) string {
	if maxTokens <= 0 || len(messages) == 0 {
		return ""
	}
	maxChars := maxTokens * 4
	var lines []string
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		role := strings.ToUpper(firstNonEmpty(message.Role, "context"))
		lines = append(lines, role+": "+compact(content, 600))
	}
	text := strings.Join(lines, "\n")
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	head := maxChars * 2 / 3
	tail := maxChars - head
	return string(runes[:head]) + "\n[…middle context compacted…]\n" + string(runes[len(runes)-tail:])
}

func estimateRequestTokens(req llm.Request) int {
	return estimateRequestTokensForProvider(req, "")
}

func estimateRequestTokensForProvider(req llm.Request, provider string) int {
	total := estimateVisibleTokensForProvider(req.System, provider) + 4
	for _, message := range req.Messages {
		total += estimateLLMMessageTokensForProvider(message, provider)
	}
	return total
}

func estimateLLMMessageTokens(message llm.Message) int {
	return estimateLLMMessageTokensForProvider(message, "")
}

func estimateLLMMessageTokensForProvider(message llm.Message, provider string) int {
	total := estimateVisibleTokensForProvider(message.Role, provider) + estimateVisibleTokensForProvider(message.Content, provider) + 4
	for _, call := range message.ToolCalls {
		total += estimateVisibleTokensForProvider(call.ID, provider) + estimateVisibleTokensForProvider(call.Name, provider) + estimateVisibleTokensForProvider(marshalArgs(call.Arguments), provider) + 6
	}
	if message.ToolResult != nil {
		result := message.ToolResult
		total += estimateVisibleTokensForProvider(result.ID, provider) + estimateVisibleTokensForProvider(result.Name, provider) + estimateVisibleTokensForProvider(result.Output, provider) + estimateVisibleTokensForProvider(result.Error, provider) + 8
	}
	return total
}

func selectNativeTurns(turns []llm.Message, budget int) []llm.Message {
	return selectNativeTurnsForProvider(turns, budget, "")
}

func selectNativeTurnsForProvider(turns []llm.Message, budget int, provider string) []llm.Message {
	if len(turns) == 0 || budget <= 0 {
		return nil
	}
	type group struct {
		messages []llm.Message
		cost     int
	}
	var groups []group
	for _, message := range turns {
		if message.Role == "assistant" || len(groups) == 0 {
			groups = append(groups, group{})
		}
		last := len(groups) - 1
		groups[last].messages = append(groups[last].messages, message)
		groups[last].cost += estimateLLMMessageTokensForProvider(message, provider)
	}
	used := 0
	start := len(groups)
	for index := len(groups) - 1; index >= 0; index-- {
		if used+groups[index].cost > budget && start < len(groups) {
			break
		}
		if used+groups[index].cost > budget {
			continue
		}
		used += groups[index].cost
		start = index
	}
	if start == len(groups) {
		return nil
	}
	var selected []llm.Message
	for _, item := range groups[start:] {
		selected = append(selected, item.messages...)
	}
	return selected
}

func estimateVisibleTokens(text string) int {
	return estimateVisibleTokensForProvider(text, "")
}

func estimateVisibleTokensForProvider(text, provider string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runeEstimate := (len([]rune(text)) + 3) / 4
	words := len(strings.Fields(text))
	factor := 0.0
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		factor = 1.30
	case "openai", "codex":
		factor = 1.35
	case "google", "gemini":
		factor = 1.25
	}
	if factor == 0 || words == 0 {
		return maxInt(1, runeEstimate)
	}
	wordEstimate := int(float64(words)*factor + 0.5)
	// Code, paths, and JSON are commonly undercounted by word-only heuristics.
	// Taking the larger estimate keeps context fitting conservative.
	return maxInt(1, maxInt(runeEstimate, wordEstimate))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
