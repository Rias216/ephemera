package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
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
	removed := m.session.Messages[len(m.session.Messages)-1].Role
	m.session.Messages = m.session.Messages[:len(m.session.Messages)-1]
	m.lastAssistant = findLastAssistant(m.session.Messages)
	m.notice = ""
	_ = m.saveSession()
	m.status = "Removed latest " + removed + " message."
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
	return fmt.Sprintf(`### Doctor

- Provider: %s
- Model: %s
- Active route: %s
- Remembered routes: %d
- Endpoint: %s
- Credentials: %s
- Mode/theme: %s / %s
- Context: ~%s / %s tokens, %d / %d messages sent
- Session: %s`,
		m.providerName(),
		m.cfg.Model(),
		m.cfg.ActiveConnection,
		len(m.cfg.ConnectedConnections()),
		m.endpointStatus(),
		m.credentialStatus(),
		m.cfg.Mode,
		m.cfg.Theme,
		formatTokenCount(stats.EstimatedTokens),
		formatTokenCount(stats.Budget),
		stats.SentMessages,
		stats.TotalMessages,
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
