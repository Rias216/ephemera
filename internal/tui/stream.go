package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type agentStreamMsg struct {
	update agent.StreamUpdate
	ok     bool
}

type liveAgentState struct {
	Active          bool
	Phase           string
	Iteration       int
	Tool            string
	Partial         string
	ReceivedChars   int
	ContextTokens   int
	OutputTokens    int
	SentMessages    int
	TotalMessages   int
	DroppedMessages int
	StartedAt       time.Time
	UpdatedAt       time.Time
	LastPaint       time.Time
	Err             string
	Goal            string
	Summary         string
	Plan            string
}

func waitAgentStream(ch <-chan agent.StreamUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-ch
		return agentStreamMsg{update: update, ok: ok}
	}
}

func (m *Model) generateCmd() tea.Cmd {
	if m.agentCancel != nil {
		m.agentCancel()
	}
	cfg := m.cfg
	session := m.session
	stream := make(chan agent.StreamUpdate, 128)
	ctx, cancel := context.WithCancel(context.Background())
	m.agentStream = stream
	m.agentCancel = cancel
	m.liveAgent = liveAgentState{
		Active:    true,
		Phase:     "starting",
		Iteration: 1,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	go func() {
		defer close(stream)
		emit := func(update agent.StreamUpdate) {
			select {
			case stream <- update:
			case <-ctx.Done():
			}
		}
		provider, err := llm.New(cfg)
		if err != nil {
			emit(agent.StreamUpdate{Kind: agent.StreamDone, Phase: "failed", Err: err, Text: "Provider setup failed: " + err.Error()})
			return
		}
		if cfg.AgentEnabled {
			agent.NewRunner(cfg, provider).RunStream(ctx, session, emit)
			return
		}

		system := reasoning.SystemPrompt(cfg.Mode)
		messages, stats := buildRequestMessages(session.Messages, system, cfg.ContextTokens)
		req := llm.Request{
			Model:       cfg.Model(),
			System:      system,
			Messages:    messages,
			MaxTokens:   cfg.MaxTokens,
			Temperature: cfg.Mode.Temperature(),
		}
		emit(agent.StreamUpdate{
			Kind:            agent.StreamStatus,
			Phase:           "requesting model",
			Iteration:       1,
			ContextTokens:   stats.EstimatedTokens,
			SentMessages:    stats.SentMessages,
			TotalMessages:   stats.TotalMessages,
			DroppedMessages: stats.DroppedMessages,
		})
		outputRunes := 0
		text, err := llm.GenerateStreaming(ctx, provider, req, func(delta string) error {
			outputRunes += len([]rune(delta))
			emit(agent.StreamUpdate{
				Kind:          agent.StreamDelta,
				Phase:         "receiving model",
				Iteration:     1,
				Delta:         delta,
				ContextTokens: stats.EstimatedTokens,
				OutputTokens:  (outputRunes + 3) / 4,
			})
			return nil
		})
		emit(agent.StreamUpdate{
			Kind:          agent.StreamDone,
			Phase:         completionPhase(err),
			Iteration:     1,
			Text:          text,
			Err:           err,
			ContextTokens: stats.EstimatedTokens,
			OutputTokens:  (outputRunes + 3) / 4,
		})
	}()

	return waitAgentStream(stream)
}

func completionPhase(err error) string {
	if err != nil {
		return "failed"
	}
	return "complete"
}

func (m *Model) applyAgentStream(update agent.StreamUpdate) tea.Cmd {
	now := time.Now()
	if update.Kind == agent.StreamStatus && update.Phase == "requesting model" &&
		(update.Iteration != m.liveAgent.Iteration || m.liveAgent.Phase != "requesting model") {
		m.liveAgent.Partial = ""
		m.liveAgent.ReceivedChars = 0
		m.liveAgent.OutputTokens = 0
		m.liveAgent.Goal = ""
		m.liveAgent.Summary = ""
		m.liveAgent.Plan = ""
	}
	if m.liveAgent.StartedAt.IsZero() {
		m.liveAgent.StartedAt = update.StartedAt
		if m.liveAgent.StartedAt.IsZero() {
			m.liveAgent.StartedAt = now
		}
	}
	m.liveAgent.Active = update.Kind != agent.StreamDone
	m.liveAgent.UpdatedAt = now
	if update.Phase != "" {
		m.liveAgent.Phase = update.Phase
	}
	if update.Iteration > 0 {
		m.liveAgent.Iteration = update.Iteration
	}
	if update.Tool != "" {
		m.liveAgent.Tool = update.Tool
	} else if update.Kind == agent.StreamStatus {
		m.liveAgent.Tool = ""
	}
	if update.ContextTokens > 0 {
		m.liveAgent.ContextTokens = update.ContextTokens
	}
	if update.OutputTokens > 0 {
		m.liveAgent.OutputTokens = update.OutputTokens
	}
	if update.TotalMessages > 0 || update.SentMessages > 0 || update.DroppedMessages > 0 {
		m.liveAgent.SentMessages = update.SentMessages
		m.liveAgent.TotalMessages = update.TotalMessages
		m.liveAgent.DroppedMessages = update.DroppedMessages
	}
	if update.Delta != "" {
		m.liveAgent.ReceivedChars += len([]rune(update.Delta))
		m.liveAgent.Partial += update.Delta
		const maxPartial = 96 * 1024
		if len(m.liveAgent.Partial) > maxPartial {
			m.liveAgent.Partial = m.liveAgent.Partial[len(m.liveAgent.Partial)-maxPartial:]
		}
		m.updateDecisionPreview()
	}

	atBottom := m.viewport.AtBottom()
	switch update.Kind {
	case agent.StreamStatus:
		m.status = m.liveStatusText()
		m.refreshViewport(atBottom)
	case agent.StreamDelta:
		m.status = m.liveStatusText()
		// Rebuilding the full transcript for every token is expensive. Repaint at
		// a terminal-friendly cadence while the footer still updates every event.
		if now.Sub(m.liveAgent.LastPaint) >= 40*time.Millisecond {
			m.liveAgent.LastPaint = now
			m.refreshViewport(atBottom)
		}
	case agent.StreamEvent:
		if update.Event != nil {
			m.upsertStreamEvent(*update.Event)
			_ = m.saveSession()
		}
		m.status = m.liveStatusText()
		m.refreshViewport(atBottom)
	case agent.StreamDone:
		m.finishAgentStream(update)
		return nil
	}
	if m.agentStream == nil {
		return nil
	}
	return waitAgentStream(m.agentStream)
}

func (m *Model) finishAgentStream(update agent.StreamUpdate) {
	m.busy = false
	m.pendingApproval = update.Pending
	m.liveAgent.Active = false
	m.liveAgent.Phase = update.Phase
	m.liveAgent.Tool = update.Tool
	m.liveAgent.UpdatedAt = time.Now()
	if update.Err != nil {
		m.liveAgent.Err = update.Err.Error()
		m.status = "The signal broke · " + compactLiveError(update.Err.Error())
		m.notice = "**Request failed:** " + escapeMarkdown(update.Err.Error())
	} else {
		text := strings.TrimSpace(update.Text)
		if text != "" {
			m.session.Append("assistant", text)
			m.lastAssistant = text
		}
		m.session.Provider = m.cfg.Provider
		m.session.Model = m.cfg.Model()
		m.session.Mode = m.cfg.Mode
		if m.pendingApproval != nil {
			m.status = "Approval needed · /approve or /reject"
		} else {
			m.status = "Saved · " + contextSummary(m.currentContextStats())
		}
	}
	if err := m.saveSession(); err != nil {
		m.status = "Completed, but session save failed: " + err.Error()
	}
	m.agentStream = nil
	m.agentCancel = nil
	m.refreshViewport(true)
}

func (m *Model) cancelGeneration() {
	if !m.busy || m.agentCancel == nil {
		m.status = "No active agent run."
		return
	}
	m.agentCancel()
	m.liveAgent.Phase = "cancelling"
	m.liveAgent.UpdatedAt = time.Now()
	m.status = "Cancelling agent run…"
}

func (m Model) liveStatusText() string {
	phase := firstNonEmpty(m.liveAgent.Phase, "working")
	parts := []string{fmt.Sprintf("round %d", max(1, m.liveAgent.Iteration)), phase}
	if m.liveAgent.Tool != "" {
		parts = append(parts, m.liveAgent.Tool)
	}
	if m.liveAgent.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("~%s out", formatTokenCount(m.liveAgent.OutputTokens)))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) upsertStreamEvent(event history.Event) {
	if event.ID != "" {
		for index := range m.session.Events {
			if m.session.Events[index].ID == event.ID {
				m.session.Events[index] = event
				return
			}
		}
	}
	m.session.AppendEvent(event)
}

func compactLiveError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 100 {
		return value[:97] + "..."
	}
	return value
}

func (m *Model) updateDecisionPreview() {
	raw := m.liveAgent.Partial
	if m.liveAgent.Goal == "" {
		if value := partialJSONStringField(raw, "goal"); value != "" {
			m.liveAgent.Goal = value
		}
	}
	if m.liveAgent.Summary == "" {
		if value := partialJSONStringField(raw, "summary"); value != "" {
			m.liveAgent.Summary = value
		}
	}
	if m.liveAgent.Plan == "" {
		if value := partialJSONArrayFirstString(raw, "plan"); value != "" {
			m.liveAgent.Plan = value
		} else if value := partialJSONArrayFirstString(raw, "approach"); value != "" {
			m.liveAgent.Plan = value
		}
	}
}

func partialJSONStringField(raw, key string) string {
	index := strings.Index(raw, `"`+key+`"`)
	if index < 0 {
		return ""
	}
	rest := raw[index+len(key)+2:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	value, ok := parsePartialJSONString(rest)
	if !ok {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func partialJSONArrayFirstString(raw, key string) string {
	index := strings.Index(raw, `"`+key+`"`)
	if index < 0 {
		return ""
	}
	rest := raw[index+len(key)+2:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if !strings.HasPrefix(rest, "[") {
		return ""
	}
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "["))
	value, ok := parsePartialJSONString(rest)
	if !ok {
		return ""
	}
	return strings.Join(strings.Fields(value), " ")
}

func parsePartialJSONString(raw string) (string, bool) {
	if !strings.HasPrefix(raw, `"`) {
		return "", false
	}
	escaped := false
	for index := 1; index < len(raw); index++ {
		char := raw[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char != '"' {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(raw[:index+1]), &value); err != nil {
			return "", false
		}
		return value, true
	}
	return "", false
}
