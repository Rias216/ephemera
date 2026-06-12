package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	appmetrics "github.com/ephemera-ai/ephemera/internal/metrics"
)

func (m *Model) retryLast() tea.Cmd {
	if m.busy {
		m.status = "One thought at a time."
		return nil
	}

	lastUser := -1
	for i := len(m.session.Messages) - 1; i >= 0; i-- {
		if m.session.Messages[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		m.status = "Nothing to retry yet."
		return nil
	}

	m.session.Messages = m.session.Messages[:lastUser+1]
	m.lastAssistant = findLastAssistant(m.session.Messages)
	m.notice = ""
	_ = m.saveSession()
	m.busy = true
	m.status = "Retrying latest prompt..."
	return tea.Batch(m.spinner.Tick, m.generateCmd())
}

func (m *Model) undoLastMessage() {
	if len(m.session.Messages) == 0 {
		m.status = "Nothing to undo."
		return
	}
	checkpoint := cloneHistorySession(m.session)
	removedIndex := len(m.session.Messages) - 1
	removedMessage := m.session.Messages[removedIndex]
	cutoff := removedMessage.CreatedAt
	if removedMessage.Role == "assistant" {
		for index := removedIndex - 1; index >= 0; index-- {
			if m.session.Messages[index].Role == "user" {
				cutoff = m.session.Messages[index].CreatedAt
				break
			}
		}
	}
	m.session.Messages = m.session.Messages[:removedIndex]
	m.session.Events = eventsBefore(m.session.Events, cutoff)
	m.lastAssistant = findLastAssistant(m.session.Messages)
	m.notice = ""
	m.redoSession = &checkpoint
	m.selectedEvent = m.clampedSelectedEvent(len(m.visibleAgentEvents()))
	_ = m.saveSession()
	m.status = "Removed latest " + removedMessage.Role + " message and its agent events. /redo restores it."
}

func (m *Model) rewindLatestUserTurn() {
	lastUser := -1
	for index := len(m.session.Messages) - 1; index >= 0; index-- {
		if m.session.Messages[index].Role == "user" {
			lastUser = index
			break
		}
	}
	if lastUser < 0 {
		m.status = "Nothing to rewind."
		return
	}
	checkpoint := cloneHistorySession(m.session)
	prompt := m.session.Messages[lastUser]
	m.session.Messages = m.session.Messages[:lastUser]
	m.session.Events = eventsBefore(m.session.Events, prompt.CreatedAt)
	m.session.Agent = history.AgentSnapshot{}
	m.lastAssistant = findLastAssistant(m.session.Messages)
	m.notice = ""
	m.redoSession = &checkpoint
	m.input.SetValue(prompt.Content)
	m.input.CursorEnd()
	m.timelineFocus = false
	m.selectedEvent = m.clampedSelectedEvent(len(m.visibleAgentEvents()))
	_ = m.saveSession()
	m.status = "Latest user turn rewound into the composer. /redo restores the original run."
}

func (m *Model) redoLastUndo() {
	if m.redoSession == nil {
		m.status = "Nothing to redo."
		return
	}
	m.session = cloneHistorySession(*m.redoSession)
	m.redoSession = nil
	m.lastAssistant = findLastAssistant(m.session.Messages)
	m.notice = ""
	m.selectedEvent = m.clampedSelectedEvent(len(m.visibleAgentEvents()))
	_ = m.saveSession()
	m.status = "Restored the most recently undone transcript state."
}

func cloneHistorySession(session history.Session) history.Session {
	data, err := json.Marshal(session)
	if err != nil {
		return session
	}
	var cloned history.Session
	if json.Unmarshal(data, &cloned) != nil {
		return session
	}
	return cloned
}

func eventsBefore(events []history.Event, cutoff time.Time) []history.Event {
	if cutoff.IsZero() {
		return nil
	}
	kept := make([]history.Event, 0, len(events))
	for _, event := range events {
		if event.CreatedAt.Before(cutoff) {
			kept = append(kept, event)
		}
	}
	return kept
}

func (m *Model) copySelectedEvent(codeOnly bool) {
	events := m.visibleAgentEvents()
	if len(events) == 0 {
		m.status = "No agent event selected."
		return
	}
	event := events[m.clampedSelectedEvent(len(events))]
	content := strings.TrimSpace(event.Content)
	if codeOnly {
		content = extractFencedCode(content)
		if content == "" {
			m.status = "The selected event has no fenced code block."
			return
		}
	}
	if content == "" {
		m.status = "The selected event has no copyable content."
		return
	}
	if err := clipboard.WriteAll(content); err != nil {
		m.recordError("copy selected event failed", err, map[string]any{"event": event.Title})
		m.status = "Copy failed: " + err.Error()
		return
	}
	if codeOnly {
		m.status = "Copied code from " + m.eventDisplayTitle(event) + "."
	} else {
		m.status = "Copied " + m.eventDisplayTitle(event) + "."
	}
}

func extractFencedCode(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var blocks []string
	var current []string
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				blocks = append(blocks, strings.TrimSpace(strings.Join(current, "\n")))
				current = nil
			}
			inFence = !inFence
			continue
		}
		if inFence {
			current = append(current, line)
		}
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func (m *Model) copyReasoningSurface() {
	content := strings.TrimSpace(m.surfaceMarkdown())
	if content == "" {
		m.status = "No reasoning surface is available yet."
		return
	}
	if err := clipboard.WriteAll(content); err != nil {
		m.recordError("copy reasoning surface failed", err, nil)
		m.status = "Surface copy failed: " + err.Error()
		return
	}
	m.status = "Copied the full reasoning surface."
}

func (m Model) surfaceMarkdown() string {
	return strings.TrimSpace(m.surfaceNotice())
}

func (m Model) exportSurface(target string) (string, error) {
	path, err := exportTargetPath(target, m.session.Name+"-surface")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(m.surfaceMarkdown()+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m Model) exportTranscript(target string) (string, error) {
	path, err := exportTargetPath(target, m.session.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(m.transcriptMarkdown()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m Model) transcriptMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ephemera: %s\n\n", m.session.Name)
	fmt.Fprintf(&b, "- Provider: %s\n", m.providerName())
	fmt.Fprintf(&b, "- Model: %s\n", m.cfg.Model())
	fmt.Fprintf(&b, "- Mode: %s\n", m.cfg.Mode)
	fmt.Fprintf(&b, "- Messages: %d\n\n", len(m.session.Messages))
	for _, message := range m.session.Messages {
		switch message.Role {
		case "user":
			b.WriteString("## You\n\n")
		case "assistant":
			b.WriteString("## Ephemera\n\n")
		default:
			continue
		}
		b.WriteString(strings.TrimSpace(message.Content))
		b.WriteString("\n\n")
	}
	return b.String()
}

func exportTargetPath(target, sessionName string) (string, error) {
	target = strings.TrimSpace(strings.Trim(target, `"`))
	if target == "" {
		dir, err := config.Dir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "exports", history.Sanitize(sessionName)+".md"), nil
	}
	if strings.HasSuffix(target, string(os.PathSeparator)) || strings.HasSuffix(target, "/") || strings.HasSuffix(target, "\\") {
		target = filepath.Join(target, history.Sanitize(sessionName)+".md")
	} else if filepath.Ext(target) == "" {
		target += ".md"
	}
	if !filepath.IsAbs(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", err
		}
		target = abs
	}
	return target, nil
}

func (m Model) doctorNotice() string {
	stats := m.currentContextStats()
	metricRegistry := appmetrics.Default()
	metricSnapshot := metricRegistry.Snapshot()
	return fmt.Sprintf(`### Doctor

- Provider: %s
- Model: %s
- Active route: %s
- Remembered routes: %d
- Endpoint: %s
- Credentials: %s
- Mode/theme: %s / %s
- Subagent: %t · %s
- Director: %t · instrument %s · influence %d%%
- Context: ~%s / %s tokens, %d / %d messages sent
- Codex bridge: isolated · target %s tokens
- Metrics: enabled=%t · runs %.0f · tools %.0f · retries %.0f · tokens %.0f/%.0f
- Debug log: %s
- Session: %s`,
		m.providerName(),
		m.cfg.Model(),
		m.cfg.ActiveConnection,
		len(m.cfg.ConnectedConnections()),
		m.endpointStatus(),
		m.credentialStatus(),
		m.cfg.Mode,
		m.cfg.Theme,
		m.cfg.SubagentEnabled,
		m.roleModelLabel(m.cfg.SubagentProvider, m.cfg.SubagentModel, "inherit main"),
		m.cfg.DirectorEnabled,
		m.roleModelLabel(m.cfg.InstrumentProvider, m.cfg.InstrumentModel, "inherit director"),
		m.cfg.InstrumentWeight,
		formatTokenCount(stats.EstimatedTokens),
		formatTokenCount(stats.Budget),
		stats.SentMessages,
		stats.TotalMessages,
		formatTokenCount(int(m.cfg.CodexBridgeMaxTokens)),
		metricRegistry.Enabled(),
		metricSnapshot.Counters["agent_runs_total"],
		metricSnapshot.Counters["agent_tool_calls_total"],
		metricSnapshot.Counters["agent_provider_retries_total"],
		metricSnapshot.Counters["agent_tokens_input_total"],
		metricSnapshot.Counters["agent_tokens_output_total"],
		debugLogPath(),
		m.session.Name,
	)
}

func (m Model) endpointStatus() string {
	switch m.cfg.Provider {
	case "ollama":
		return m.cfg.OllamaURL
	case "compatible":
		return m.cfg.CompatibleURL
	case "openai":
		return "https://api.openai.com/v1"
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "codex":
		return "Codex CLI · isolated model bridge"
	default:
		return "unknown"
	}
}

func (m Model) credentialStatus() string {
	switch m.cfg.Provider {
	case "ollama":
		return "not required"
	case "openai":
		return keyStatus(m.cfg.OpenAIKey, "OPENAI_API_KEY")
	case "anthropic":
		return keyStatus(m.cfg.AnthropicKey, "ANTHROPIC_API_KEY")
	case "compatible":
		return keyStatus(m.cfg.CompatibleKey, config.DefaultAPIKeyEnv(m.cfg.CompatibleName), "EPHEMERA_API_KEY")
	default:
		return "unknown provider"
	}
}

func keyStatus(explicit string, envs ...string) string {
	if strings.TrimSpace(explicit) != "" {
		return "key loaded"
	}
	for _, env := range envs {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return env + " set"
		}
	}
	return strings.Join(envs, " or ") + " missing"
}
