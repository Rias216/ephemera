package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type contextStats struct {
	Budget          int
	EstimatedTokens int
	SentMessages    int
	TotalMessages   int
	DroppedMessages int
}

func (m Model) currentContextStats() contextStats {
	_, stats := buildRequestMessages(m.session.Messages, reasoning.SystemPrompt(m.cfg.Mode), m.cfg.ContextTokens)
	return stats
}

func (m Model) usageNotice(stats contextStats) string {
	trimLine := "No saved messages would be omitted."
	if stats.DroppedMessages > 0 {
		trimLine = fmt.Sprintf("%d oldest message(s) would be omitted from the next request.", stats.DroppedMessages)
	}
	return fmt.Sprintf(`### Usage

- Request context: ~%s / %s tokens
- Messages sent next request: %d / %d
- Output cap: %s tokens
- %s`,
		formatTokenCount(stats.EstimatedTokens),
		formatTokenCount(stats.Budget),
		stats.SentMessages,
		stats.TotalMessages,
		formatTokenCount(int(m.cfg.MaxTokens)),
		trimLine,
	)
}

func buildRequestMessages(messages []history.Message, system string, budget int) ([]llm.Message, contextStats) {
	valid := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" || message.Role == "assistant" {
			valid = append(valid, llm.Message{Role: message.Role, Content: message.Content})
		}
	}

	if budget <= 0 {
		budget = 16_000
	}

	systemTokens := estimateTextTokens(system) + 4
	used := systemTokens
	selected := make([]llm.Message, 0, len(valid))
	for i := len(valid) - 1; i >= 0; i-- {
		cost := estimateMessageTokens(valid[i])
		if used+cost > budget && len(selected) > 0 {
			break
		}
		selected = append(selected, valid[i])
		used += cost
	}

	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	for len(selected) > 0 && selected[0].Role == "assistant" {
		used -= estimateMessageTokens(selected[0])
		selected = selected[1:]
	}

	return selected, contextStats{
		Budget:          budget,
		EstimatedTokens: used,
		SentMessages:    len(selected),
		TotalMessages:   len(valid),
		DroppedMessages: len(valid) - len(selected),
	}
}

func estimateMessageTokens(message llm.Message) int {
	return 4 + estimateTextTokens(message.Role) + estimateTextTokens(message.Content)
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return max(1, (utf8.RuneCountInString(text)+3)/4)
}

func contextSummary(stats contextStats) string {
	summary := fmt.Sprintf("ctx ~%s/%s · sent %d/%d", formatTokenCount(stats.EstimatedTokens), formatTokenCount(stats.Budget), stats.SentMessages, stats.TotalMessages)
	if stats.DroppedMessages > 0 {
		summary += fmt.Sprintf(" · trimmed %d old", stats.DroppedMessages)
	}
	return summary
}

func formatTokenCount(tokens int) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1000)
	}
	return strconv.Itoa(tokens)
}

func parseContextBudget(value string) (int, error) {
	budget, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || budget < 512 {
		return 0, fmt.Errorf("Budget must be at least 512 tokens")
	}
	return budget, nil
}
