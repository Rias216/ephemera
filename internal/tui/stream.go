package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type agentStreamMsg struct {
	update agent.StreamUpdate
	ok     bool
}

type liveAgentState struct {
	Active            bool
	RunID             string
	Phase             string
	Iteration         int
	Tool              string
	Partial           string
	ReceivedChars     int
	ContextTokens     int
	OutputTokens      int
	SentMessages      int
	TotalMessages     int
	DroppedMessages   int
	StartedAt         time.Time
	UpdatedAt         time.Time
	LastPaint         time.Time
	Err               string
	Goal              string
	Summary           string
	Thought           string
	Activity          string
	ThoughtUpdatedAt  time.Time
	ActivityUpdatedAt time.Time
	Reasoning         string
	ReasoningChars    int
	Trace             history.AgentTrace
	Plan              string
	Verification      string
	Verified          bool
	DirectorMode      bool
	InstrumentLast    string
	RunProvider       string
	RunModel          string
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
	runCfg := cfg
	var roleErr error
	if cfg.DirectorEnabled {
		resolved, err := cfg.ConfigForRole(cfg.DirectorProvider, cfg.DirectorModel)
		roleErr = err
		if err == nil {
			runCfg = resolved
		}
	}
	session := m.session
	stream := make(chan agent.StreamUpdate, 128)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = debuglog.WithScope(ctx, debuglog.Scope{
		Session:   session.Name,
		Provider:  runCfg.Provider,
		Model:     runCfg.Model(),
		Workspace: runCfg.WorkspaceRoot,
	})
	m.agentStream = stream
	m.agentCancel = cancel
	now := time.Now()
	m.liveAgent = liveAgentState{
		Active:            true,
		Phase:             "starting",
		Iteration:         1,
		StartedAt:         now,
		UpdatedAt:         now,
		Activity:          "Starting the model stream…",
		ActivityUpdatedAt: now,
		DirectorMode:      cfg.DirectorEnabled,
		RunProvider:       runCfg.Provider,
		RunModel:          runCfg.Model(),
	}
	m.session.Agent = history.AgentSnapshot{
		Status:    "running",
		Phase:     "starting",
		Iteration: 1,
		UpdatedAt: now,
	}

	go func() {
		defer close(stream)
		emit := func(update agent.StreamUpdate) {
			select {
			case stream <- update:
			case <-ctx.Done():
			}
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("agent worker panic: %v", recovered)
				debuglog.Failure("tui", "agent worker panic", err.Error(), map[string]any{
					"session":  session.Name,
					"provider": runCfg.Provider,
					"model":    runCfg.Model(),
					"stack":    string(debug.Stack()),
				})
				emit(agent.StreamUpdate{Kind: agent.StreamDone, Phase: "failed", Err: err, Text: "Agent worker failed: " + err.Error()})
			}
		}()
		if roleErr != nil {
			emit(agent.StreamUpdate{Kind: agent.StreamDone, Phase: "failed", Err: roleErr, Text: "Director model setup failed: " + roleErr.Error()})
			return
		}
		provider, err := llm.New(runCfg)
		if err != nil {
			emit(agent.StreamUpdate{Kind: agent.StreamDone, Phase: "failed", Err: err, Text: "Provider setup failed: " + err.Error()})
			return
		}
		if runCfg.AgentEnabled {
			runner := agent.NewRunner(runCfg, provider)
			if runCfg.DirectorEnabled {
				runner.RunDirector(ctx, session, emit)
			} else {
				runner.RunStream(ctx, session, emit)
			}
			return
		}

		system := reasoning.SystemPrompt(runCfg.Mode)
		messages, stats := buildRequestMessages(session.Messages, system, runCfg.ContextTokens)
		req := llm.Request{
			Model:            runCfg.Model(),
			System:           system,
			Messages:         messages,
			MaxTokens:        runCfg.MaxTokens,
			Temperature:      runCfg.Mode.Temperature(),
			ReasoningSummary: runCfg.ShowThinking,
			ReasoningEffort:  runCfg.Mode.Effort(),
		}
		req, replacements := llm.NormalizeRequestUTF8(req)
		if replacements > 0 {
			debuglog.WarningCtx(ctx, "tui", "invalid utf-8 normalized", "chat request contained invalid UTF-8 and was normalized before transport", map[string]any{
				"replacement_fields": replacements,
			})
		}
		_ = debuglog.AppendContext(ctx, "provider_request", 1, "text", map[string]any{
			"request": req,
			"selection": map[string]any{
				"estimated_tokens": stats.EstimatedTokens,
				"sent_messages":    stats.SentMessages,
				"total_messages":   stats.TotalMessages,
				"dropped_messages": stats.DroppedMessages,
			},
		})
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
		text, err := llm.GenerateStreaming(ctx, provider, req, func(delta llm.Delta) error {
			if delta.Text == "" {
				return nil
			}
			kind := agent.StreamDelta
			phase := "receiving model"
			switch delta.Kind {
			case llm.DeltaReasoning:
				kind = agent.StreamReasoning
				phase = "reasoning"
			case llm.DeltaActivity:
				kind = agent.StreamActivity
				phase = "preparing response"
			default:
				outputRunes += len([]rune(delta.Text))
			}
			emit(agent.StreamUpdate{
				Kind:          kind,
				Phase:         phase,
				Iteration:     1,
				Delta:         delta.Text,
				ContextTokens: stats.EstimatedTokens,
				OutputTokens:  (outputRunes + 3) / 4,
			})
			return nil
		})
		responsePayload := map[string]any{
			"text":          text,
			"output_tokens": (outputRunes + 3) / 4,
		}
		if err != nil {
			responsePayload["error"] = err.Error()
		}
		_ = debuglog.AppendContext(ctx, "provider_response", 1, "text", responsePayload)
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
	if update.RunID != "" {
		m.liveAgent.RunID = update.RunID
	}
	m.liveAgent.Verified = update.Verified
	if update.Kind == agent.StreamStatus && (update.Phase == "requesting model" || update.Phase == "deliberating") &&
		(update.Iteration != m.liveAgent.Iteration || (m.liveAgent.Phase != "requesting model" && m.liveAgent.Phase != "deliberating")) {
		m.liveAgent.Partial = ""
		m.liveAgent.ReceivedChars = 0
		m.liveAgent.OutputTokens = 0
		m.liveAgent.Goal = ""
		m.liveAgent.Summary = ""
		m.liveAgent.Thought = ""
		m.liveAgent.Activity = "Analyzing the request…"
		m.liveAgent.ThoughtUpdatedAt = time.Time{}
		m.liveAgent.ActivityUpdatedAt = now
		m.liveAgent.Reasoning = ""
		m.liveAgent.ReasoningChars = 0
		m.liveAgent.Trace = history.AgentTrace{}
		m.liveAgent.Plan = ""
		m.liveAgent.Verification = ""
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
		if activity := phaseActivity(update.Phase, update.Tool); activity != "" {
			if activity != m.liveAgent.Activity {
				m.liveAgent.Activity = activity
				m.liveAgent.ActivityUpdatedAt = now
			}
		}
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
	if update.Plan != nil {
		m.liveAgent.Plan = update.Plan.Render()
	}
	if update.Delta != "" && update.Kind == agent.StreamDelta {
		m.liveAgent.ReceivedChars += len([]rune(update.Delta))
		m.liveAgent.Partial += update.Delta
		const maxPartial = 96 * 1024
		if len(m.liveAgent.Partial) > maxPartial {
			m.liveAgent.Partial = m.liveAgent.Partial[len(m.liveAgent.Partial)-maxPartial:]
		}
		previousThought := m.liveAgent.Thought
		m.updateDecisionPreview()
		if m.liveAgent.Thought != previousThought {
			m.liveAgent.ThoughtUpdatedAt = now
		} else if strings.TrimSpace(m.liveAgent.Thought) == "" {
			m.liveAgent.Activity = fmt.Sprintf("Receiving the model response · %d chars", m.liveAgent.ReceivedChars)
			m.liveAgent.ActivityUpdatedAt = now
		}
	}
	if update.Delta != "" && update.Kind == agent.StreamReasoning {
		m.liveAgent.ReasoningChars += len([]rune(update.Delta))
		m.liveAgent.Reasoning += update.Delta
		const maxReasoning = 48 * 1024
		if len(m.liveAgent.Reasoning) > maxReasoning {
			m.liveAgent.Reasoning = m.liveAgent.Reasoning[len(m.liveAgent.Reasoning)-maxReasoning:]
		}
		m.liveAgent.Thought = latestReasoningPreview(m.liveAgent.Reasoning)
		m.liveAgent.ThoughtUpdatedAt = now
	}
	if update.Delta != "" && (update.Kind == agent.StreamActivity || update.Kind == agent.StreamToolProgress) {
		m.liveAgent.Activity = lastLineCompact(update.Delta, 180)
		m.liveAgent.ActivityUpdatedAt = now
	}

	atBottom := m.activeViewportAtBottom()
	switch update.Kind {
	case agent.StreamStatus:
		m.status = m.liveStatusText()
		m.refreshViewport(atBottom)
	case agent.StreamDelta, agent.StreamReasoning, agent.StreamActivity, agent.StreamToolProgress:
		m.status = m.liveStatusText()
		cadence := 40 * time.Millisecond
		switch update.Kind {
		case agent.StreamDelta:
			cadence = 16 * time.Millisecond
		case agent.StreamReasoning:
			cadence = 50 * time.Millisecond
		case agent.StreamActivity, agent.StreamToolProgress:
			cadence = 100 * time.Millisecond
		}
		// Match repaint frequency to the kind of stream: final text feels fluid,
		// reasoning remains readable, and tool-heavy runs avoid needless CPU work.
		if now.Sub(m.liveAgent.LastPaint) >= cadence {
			m.liveAgent.LastPaint = now
			m.refreshViewport(atBottom)
		}
	case agent.StreamPlan:
		m.status = m.liveStatusText()
		m.refreshViewport(atBottom)
	case agent.StreamEvent:
		if update.Event != nil {
			if update.Event.Metadata != nil {
				if instrument, _ := update.Event.Metadata["instrument"].(bool); instrument {
					m.liveAgent.DirectorMode = true
					m.liveAgent.InstrumentLast = strings.TrimSpace(update.Event.Content)
				}
			}
			m.upsertStreamEvent(*update.Event)
			m.captureAgentEvent(*update.Event)
			if m.followLive {
				m.selectedEvent = max(0, len(m.visibleAgentEvents())-1)
			}
			m.persistAgentSnapshot(false)
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
	m.liveAgent.Verified = update.Verified
	if update.RunID != "" {
		m.liveAgent.RunID = update.RunID
	}
	m.liveAgent.UpdatedAt = time.Now()
	if update.Err != nil {
		m.recordError("agent stream failed", update.Err, map[string]any{
			"phase": update.Phase,
			"tool":  update.Tool,
		})
		m.liveAgent.Err = update.Err.Error()
		m.status = "The signal broke · " + compactLiveError(update.Err.Error())
		m.notice = "**Request failed:** " + escapeMarkdown(update.Err.Error()) + "\n\nDebug log: `" + escapeMarkdown(debugLogPath()) + "`"
	} else {
		text := strings.TrimSpace(update.Text)
		if text != "" {
			if m.pendingApproval == nil {
				m.session.Append("assistant", text)
				m.lastAssistant = text
			} else {
				// Approval prompts belong to the control timeline. Keeping them out
				// of chat prevents the resumed model from treating a stale request
				// as an unresolved assistant instruction.
				m.notice = text
			}
		}
		m.session.Provider = firstNonEmpty(m.liveAgent.RunProvider, m.cfg.Provider)
		m.session.Model = firstNonEmpty(m.liveAgent.RunModel, m.cfg.Model())
		m.session.Mode = m.cfg.Mode
		if m.pendingApproval != nil {
			m.status = "Approval needed · /approve or /reject"
		} else {
			m.status = "Saved · " + contextSummary(m.currentContextStats())
		}
	}
	m.persistAgentSnapshot(m.pendingApproval == nil)
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
	if m.liveAgent.DirectorMode {
		parts = append([]string{"director"}, parts...)
	}
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

func phaseActivity(phase, tool string) string {
	tool = strings.TrimSpace(tool)
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "starting":
		return "Starting the model stream…"
	case "deliberating", "requesting model":
		return "Analyzing the request…"
	case "reasoning":
		return "Receiving a reasoning summary…"
	case "receiving decision", "receiving model":
		return "Receiving the model response…"
	case "preparing action":
		if tool != "" {
			return "Preparing " + tool + "…"
		}
		return "Preparing the next action…"
	case "parsing decision":
		return "Parsing the next action…"
	case "reviewing results":
		return "Reviewing the latest tool result…"
	case "answering directly":
		return "Responding without starting the agent loop…"
	case "verifying":
		return "Verifying the result…"
	case "tool output":
		if tool != "" {
			return "Streaming " + tool + " output…"
		}
		return "Streaming tool output…"
	case "plan updated", "plan step started", "plan step completed", "plan step running", "plan step updated":
		return "Updating the execution plan…"
	case "retrying provider":
		return "Retrying the provider request…"
	case "budget reached":
		return "Task token budget reached."
	case "self critique":
		return "Reviewing the completed answer…"
	case "instrument review":
		return "Instrument model is reviewing the director…"
	}
	return ""
}

func (m Model) liveThoughtPreview() string {
	thought := strings.TrimSpace(m.liveAgent.Thought)
	activity := strings.TrimSpace(m.liveAgent.Activity)
	if activity != "" && (thought == "" || m.liveAgent.ActivityUpdatedAt.After(m.liveAgent.ThoughtUpdatedAt)) {
		return activity
	}
	if thought != "" {
		return thought
	}
	if activity != "" {
		return activity
	}
	return phaseActivity(m.liveAgent.Phase, m.liveAgent.Tool)
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
	if m.liveAgent.Verification == "" {
		if value := partialJSONStringField(raw, "verification"); value != "" {
			m.liveAgent.Verification = value
		}
	}
	if thought := latestDecisionThought(raw); thought != "" {
		m.liveAgent.Thought = thought
	}
}

func latestDecisionThought(raw string) string {
	type candidate struct {
		index int
		text  string
	}
	best := candidate{index: -1}
	for _, key := range []string{"goal", "current_state", "tool_rationale", "verification", "next_step", "summary"} {
		index := strings.LastIndex(raw, `"`+key+`"`)
		if index < best.index {
			continue
		}
		if value := partialJSONStringField(raw, key); value != "" {
			best = candidate{index: index, text: value}
		}
	}
	for _, key := range []string{"assumptions", "approach", "evidence", "risks", "plan"} {
		index := strings.LastIndex(raw, `"`+key+`"`)
		if index < best.index {
			continue
		}
		if value := partialJSONArrayFirstString(raw, key); value != "" {
			best = candidate{index: index, text: value}
		}
	}
	return lastLineCompact(best.text, 180)
}

func latestReasoningPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" {
			return lastLineCompact(line, 180)
		}
	}
	return lastLineCompact(raw, 180)
}

func lastLineCompact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-limit+1:])
}

func (m *Model) captureAgentEvent(event history.Event) {
	if event.Metadata != nil {
		if instrument, _ := event.Metadata["instrument"].(bool); instrument {
			m.liveAgent.DirectorMode = true
			m.liveAgent.InstrumentLast = strings.TrimSpace(event.Content)
		}
	}
	switch event.Type {
	case "reasoning_trace", "reasoning_summary":
		m.liveAgent.Reasoning = event.Content
		if trace := eventAgentTrace(event); !trace.Empty() {
			m.liveAgent.Trace = trace
			if strings.TrimSpace(trace.Goal) != "" {
				m.liveAgent.Goal = trace.Goal
			}
			if strings.TrimSpace(trace.Verification) != "" {
				m.liveAgent.Verification = trace.Verification
			}
			return
		}
		if goal := parseReasoningSection(event.Content, "Goal"); goal != "" {
			m.liveAgent.Goal = goal
		}
		if verification := parseReasoningSection(event.Content, "Verification"); verification != "" {
			m.liveAgent.Verification = verification
		}
	case "plan_update":
		m.liveAgent.Plan = event.Content
	case "verification":
		m.liveAgent.Verification = event.Content
		if value, ok := event.Metadata["verified"].(bool); ok {
			m.liveAgent.Verified = value
		}
	case "final":
		m.liveAgent.Summary = event.Content
		if value, ok := event.Metadata["verified"].(bool); ok {
			m.liveAgent.Verified = value
		}
	}
}

func eventAgentTrace(event history.Event) history.AgentTrace {
	if event.Metadata == nil {
		return history.AgentTrace{}
	}
	value, ok := event.Metadata["trace"]
	if !ok || value == nil {
		return history.AgentTrace{}
	}
	if trace, ok := value.(history.AgentTrace); ok {
		return trace
	}
	data, err := json.Marshal(value)
	if err != nil {
		return history.AgentTrace{}
	}
	var trace history.AgentTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		return history.AgentTrace{}
	}
	return trace
}

func (m *Model) persistAgentSnapshot(completed bool) {
	status := "running"
	if completed {
		status = firstNonEmpty(m.liveAgent.Phase, "complete")
	}
	if m.pendingApproval != nil {
		status = "awaiting approval"
	}
	m.session.Agent = history.AgentSnapshot{
		RunID:         m.liveAgent.RunID,
		Status:        status,
		Phase:         m.liveAgent.Phase,
		Iteration:     m.liveAgent.Iteration,
		Goal:          m.liveAgent.Goal,
		Summary:       m.liveAgent.Summary,
		Reasoning:     m.liveAgent.Reasoning,
		Trace:         m.liveAgent.Trace,
		Plan:          m.liveAgent.Plan,
		Verification:  m.liveAgent.Verification,
		LastTool:      m.liveAgent.Tool,
		ContextTokens: m.liveAgent.ContextTokens,
		OutputTokens:  m.liveAgent.OutputTokens,
		Verified:      m.liveAgent.Verified,
		Completed:     completed,
		UpdatedAt:     m.liveAgent.UpdatedAt,
	}
}

func partialJSONStringField(raw, key string) string {
	index := strings.LastIndex(raw, `"`+key+`"`)
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
	index := strings.LastIndex(raw, `"`+key+`"`)
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
	var value strings.Builder
	for index := 1; index < len(raw); index++ {
		char := raw[index]
		if char == '"' {
			var complete string
			if err := json.Unmarshal([]byte(raw[:index+1]), &complete); err == nil {
				return complete, true
			}
			return value.String(), true
		}
		if char != '\\' {
			value.WriteByte(char)
			continue
		}
		if index+1 >= len(raw) {
			break
		}
		index++
		switch escaped := raw[index]; escaped {
		case '"', '\\', '/':
			value.WriteByte(escaped)
		case 'b':
			value.WriteByte('\b')
		case 'f':
			value.WriteByte('\f')
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case 'u':
			if index+4 >= len(raw) {
				return value.String(), true
			}
			encoded := raw[index-1 : index+5]
			var decoded string
			if err := json.Unmarshal([]byte(`"`+encoded+`"`), &decoded); err != nil {
				return value.String(), true
			}
			value.WriteString(decoded)
			index += 4
		default:
			// Preserve a malformed or provider-specific escape in the preview;
			// final JSON validation still happens in the agent parser.
			value.WriteByte(escaped)
		}
	}
	return value.String(), true
}
