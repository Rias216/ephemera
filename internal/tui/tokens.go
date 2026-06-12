package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

type memorySourceState struct {
	Path     string
	Found    bool
	Size     int64
	Modified time.Time
	Tokens   int
	Preview  string
	Err      string
}

func (m Model) currentContextStats() contextStats {
	_, stats := buildRequestMessages(m.session.Messages, reasoning.SystemPrompt(m.cfg.Mode), m.cfg.ContextTokens)
	if draft := strings.TrimSpace(m.input.Value()); draft != "" && !m.busy && m.connect == nil && !strings.HasPrefix(draft, "/") {
		stats.EstimatedTokens += estimateTextTokens(draft) + 4
	}
	if m.liveAgent.Active && m.liveAgent.ContextTokens > 0 {
		stats.EstimatedTokens = m.liveAgent.ContextTokens + m.liveAgent.OutputTokens
		if m.liveAgent.TotalMessages > 0 {
			stats.SentMessages = m.liveAgent.SentMessages
			stats.TotalMessages = m.liveAgent.TotalMessages
			stats.DroppedMessages = m.liveAgent.DroppedMessages
		}
	}
	return stats
}

func (m Model) usageNotice(stats contextStats) string {
	trimLine := "No saved messages would be omitted."
	if stats.DroppedMessages > 0 {
		trimLine = fmt.Sprintf("%d oldest message(s) would be omitted from the next request.", stats.DroppedMessages)
	}
	memory := m.memorySourceSummary()
	return fmt.Sprintf(`### Usage

- Request context: ~%s / %s tokens
- Messages sent next request: %d / %d
- Output cap: %s tokens
- Tool output budget: %s tokens
- Memory sources: %s
- %s`,
		formatTokenCount(stats.EstimatedTokens),
		formatTokenCount(stats.Budget),
		stats.SentMessages,
		stats.TotalMessages,
		formatTokenCount(int(m.cfg.MaxTokens)),
		formatTokenCount(m.cfg.MaxToolOutputTokens),
		memory,
		trimLine,
	)
}

func (m Model) memorySourceSummary() string {
	var found []string
	for _, source := range m.memorySourceStates() {
		if source.Found && source.Err == "" {
			found = append(found, source.Path)
		}
	}
	if len(found) == 0 {
		return "none found"
	}
	return strings.Join(found, ", ")
}

func (m Model) memorySourceStates() []memorySourceState {
	root := m.workspaceRoot()
	candidates := []string{
		filepath.Join(".ephemera", "instructions.md"),
		filepath.Join(".ephemera", "memory.json"),
		"CLAUDE.md",
		"AGENTS.md",
	}
	states := make([]memorySourceState, 0, len(candidates))
	for _, candidate := range candidates {
		state := memorySourceState{Path: filepath.ToSlash(candidate)}
		if root == "" {
			state.Err = "workspace root is unknown"
			states = append(states, state)
			continue
		}
		path := filepath.Join(root, candidate)
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				state.Err = err.Error()
			}
			states = append(states, state)
			continue
		}
		if info.IsDir() {
			state.Err = "directory found where a file was expected"
			states = append(states, state)
			continue
		}
		state.Found = true
		state.Size = info.Size()
		state.Modified = info.ModTime()
		data, err := os.ReadFile(path)
		if err != nil {
			state.Err = err.Error()
			states = append(states, state)
			continue
		}
		content := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
		state.Tokens = estimateTextTokens(content)
		state.Preview = compactPreview(content, 900)
		states = append(states, state)
	}
	return states
}

func (m Model) workspaceRoot() string {
	root := strings.TrimSpace(m.cfg.WorkspaceRoot)
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root
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

func formatByteCount(bytes int64) string {
	if bytes >= 1_000_000 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/1_000_000)
	}
	if bytes >= 1000 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1000)
	}
	return fmt.Sprintf("%d B", bytes)
}

func compactPreview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:max(0, limit-3)])) + "..."
}

func parseContextBudget(value string) (int, error) {
	budget, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || budget < 512 {
		return 0, fmt.Errorf("Budget must be at least 512 tokens")
	}
	return budget, nil
}
