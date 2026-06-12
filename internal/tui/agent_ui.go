package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func (m Model) renderAgentTimeline() string {
	renderer := newCLIRenderer(m.styles, m.transcriptWidth())
	rows := []string{m.transcriptLine(m.styles.NoticeLabel, m.agentTimelineLabel())}
	events := m.visibleAgentEvents()
	selected := m.clampedSelectedEvent(len(events))
	for index, event := range events {
		rows = append(rows, m.renderAgentEvent(event, renderer, index == selected, m.eventExpanded(event, index), index)...)
	}
	if len(events) == 0 && !m.liveAgent.Active {
		rows = append(rows, renderer.paintRow(cliLine{{text: "  no timeline events match the current filter", style: cliStyle{foreground: m.styles.Faint}}}))
	}
	if m.liveAgent.Active {
		rows = append(rows, m.renderLiveAgent(renderer)...)
	} else if m.cfg.ShowThinking && m.timelineFilterAllows("reasoning") && strings.TrimSpace(m.session.Agent.Reasoning) != "" && !m.snapshotReasoningAlreadyRendered() {
		snapshot := history.Event{Type: "reasoning_trace", Title: "Beneath the Surface", Content: m.session.Agent.Reasoning, Status: m.session.Agent.Status, CreatedAt: m.session.Agent.UpdatedAt}
		rows = append(rows, m.renderAgentEvent(snapshot, renderer, false, false, len(events))...)
	}
	return strings.Join(rows, "\n")
}

func (m Model) agentTimelineLabel() string {
	label := "agent"
	if m.cfg.DirectorEnabled || m.liveAgent.DirectorMode {
		label += " · director"
	}
	if m.timelineFocus {
		label += " · timeline"
	}
	if m.timelineFilter != "" {
		label += " · " + m.timelineFilter
	}
	if !m.followLive {
		label += " · follow off"
	}
	return label
}

func (m Model) visibleAgentEvents() []history.Event {
	events := make([]history.Event, 0, len(m.session.Events))
	for _, event := range m.session.Events {
		if (event.Type == "reasoning_summary" || event.Type == "reasoning_trace") && !m.cfg.ShowThinking {
			continue
		}
		if m.timelineFilter == "errors" {
			if event.Status != "error" {
				continue
			}
		} else if !m.timelineFilterAllows(eventTimelineKind(event)) {
			continue
		}
		events = append(events, event)
	}
	return events
}

func eventTimelineKind(event history.Event) string {
	switch event.Type {
	case "tool_call", "tool_result", "approval_request", "verification", "test_result":
		return "tools"
	case "reasoning_summary", "reasoning_trace", "plan_update", "instrument_review", "director_status":
		return "reasoning"
	default:
		return event.Type
	}
}

func (m Model) timelineFilterAllows(kind string) bool {
	switch m.timelineFilter {
	case "", "all":
		return true
	case "tools":
		return kind == "tools"
	case "reasoning":
		return kind == "reasoning"
	case "errors":
		return true
	default:
		return true
	}
}

func (m Model) eventExpanded(event history.Event, index int) bool {
	if event.Type == "approval_request" && event.Status == "pending" {
		return true
	}
	return m.expandedEvents != nil && m.expandedEvents[timelineEventKey(event, index)]
}

func timelineEventKey(event history.Event, index int) string {
	if event.ID != "" {
		return event.ID
	}
	return fmt.Sprintf("%d:%s:%s:%s", index, event.Type, event.Tool, event.Title)
}

func (m Model) clampedSelectedEvent(count int) int {
	if count <= 0 {
		return 0
	}
	if m.selectedEvent < 0 {
		return 0
	}
	if m.selectedEvent >= count {
		return count - 1
	}
	return m.selectedEvent
}

func (m *Model) moveTimelineSelection(delta int) {
	count := len(m.visibleAgentEvents())
	if count == 0 {
		m.selectedEvent = 0
		m.status = "No timeline events."
		return
	}
	m.selectedEvent = m.clampedSelectedEvent(count) + delta
	if m.selectedEvent < 0 {
		m.selectedEvent = 0
	}
	if m.selectedEvent >= count {
		m.selectedEvent = count - 1
	}
	m.followLive = m.selectedEvent == count-1
}

func (m *Model) toggleSelectedEvent() {
	events := m.visibleAgentEvents()
	if len(events) == 0 {
		m.status = "No timeline event selected."
		return
	}
	index := m.clampedSelectedEvent(len(events))
	m.selectedEvent = index
	if m.expandedEvents == nil {
		m.expandedEvents = make(map[string]bool)
	}
	key := timelineEventKey(events[index], index)
	m.expandedEvents[key] = !m.expandedEvents[key]
	state := "collapsed"
	if m.expandedEvents[key] {
		state = "expanded"
	}
	m.status = firstNonEmpty(events[index].Title, events[index].Type) + " " + state
}

func (m *Model) cycleTimelineFilter() {
	filters := []string{"", "tools", "reasoning", "errors"}
	current := 0
	for index, value := range filters {
		if value == m.timelineFilter {
			current = index
			break
		}
	}
	m.timelineFilter = filters[(current+1)%len(filters)]
	m.selectedEvent = m.clampedSelectedEvent(len(m.visibleAgentEvents()))
	label := firstNonEmpty(m.timelineFilter, "all")
	m.status = "Timeline filter: " + label
}

func (m Model) snapshotReasoningAlreadyRendered() bool {
	content := strings.TrimSpace(m.session.Agent.Reasoning)
	if content == "" {
		return true
	}
	for index := len(m.session.Events) - 1; index >= 0; index-- {
		event := m.session.Events[index]
		if event.Type == "reasoning_trace" || event.Type == "reasoning_summary" {
			return strings.TrimSpace(event.Content) == content
		}
	}
	return false
}

func (m Model) renderLiveAgent(renderer cliRenderer) []string {
	phase := firstNonEmpty(m.liveAgent.Phase, "working")
	mode := "live"
	if m.liveAgent.DirectorMode {
		mode = "director"
	}
	label := fmt.Sprintf("  ◆ %s · round %d · %s", mode, max(1, m.liveAgent.Iteration), phase)
	if m.liveAgent.Tool != "" {
		label += " · " + m.liveAgent.Tool
	}
	header := renderer.paintRow(cliLine{{text: label, style: cliStyle{foreground: m.styles.Primary, bold: true}}})
	elapsed := time.Since(m.liveAgent.StartedAt).Round(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	messageState := ""
	if m.liveAgent.TotalMessages > 0 {
		messageState = fmt.Sprintf(" · sent %d/%d", m.liveAgent.SentMessages, m.liveAgent.TotalMessages)
		if m.liveAgent.DroppedMessages > 0 {
			messageState += fmt.Sprintf(" · trimmed %d", m.liveAgent.DroppedMessages)
		}
	}
	detail := fmt.Sprintf("  ctx ~%s/%s · output ~%s%s · received %d chars",
		formatTokenCount(m.liveAgent.ContextTokens),
		formatTokenCount(m.cfg.ContextTokens),
		formatTokenCount(m.liveAgent.OutputTokens),
		messageState,
		m.liveAgent.ReceivedChars,
	)
	if m.cfg.ShowThinking && m.liveAgent.ReasoningChars > 0 {
		detail += fmt.Sprintf(" · reasoning %d chars", m.liveAgent.ReasoningChars)
	}
	detail += " · elapsed " + elapsed.String()
	rows := []string{header, renderer.paintRow(cliLine{{text: detail, style: cliStyle{foreground: m.styles.Faint}}})}
	if goal := strings.TrimSpace(m.liveAgent.Goal); goal != "" {
		rows = append(rows, renderer.paintRow(cliLine{
			{text: "  goal · ", style: cliStyle{foreground: m.styles.AccentSoft, bold: true}},
			{text: firstLineCompact(goal, max(12, renderer.width-9)), style: cliStyle{foreground: m.styles.Muted}},
		}))
	}
	if plan := strings.TrimSpace(m.liveAgent.Plan); plan != "" {
		rows = append(rows, renderer.paintRow(cliLine{
			{text: "  next · ", style: cliStyle{foreground: m.styles.AccentSoft, bold: true}},
			{text: firstLineCompact(plan, max(12, renderer.width-9)), style: cliStyle{foreground: m.styles.Muted}},
		}))
	}
	if m.cfg.ShowThinking {
		thought := m.liveThoughtPreview()
		if thought != "" {
			rows = append(rows, renderer.paintRow(cliLine{
				{text: "  thinking · ", style: cliStyle{foreground: m.styles.AccentBright, bold: true}},
				{text: lastLineCompact(thought, max(12, renderer.width-13)), style: cliStyle{foreground: m.styles.Muted}},
			}))
		}
	}
	if review := strings.TrimSpace(m.liveAgent.InstrumentLast); review != "" {
		rows = append(rows, renderer.paintRow(cliLine{
			{text: "  instrument · ", style: cliStyle{foreground: m.styles.AccentSoft, bold: true}},
			{text: firstLineCompact(review, max(12, renderer.width-16)), style: cliStyle{foreground: m.styles.Muted}},
		}))
	}
	return rows
}

func (m Model) renderAgentEvent(event history.Event, renderer cliRenderer, selected, expanded bool, index int) []string {
	titleColor := m.eventTitleColor(event)
	if selected {
		titleColor = m.styles.AccentBright
	}
	title := m.eventDisplayTitle(event)
	if m.compactView && !expanded {
		if detail := m.eventCompactDetail(event, max(12, renderer.width/2)); detail != "" {
			title += " — " + detail
		}
	}
	status := fallbackStatus(event.Status)
	iteration := eventMetadataInt(event, "iteration")
	meta := status
	if iteration > 0 {
		meta = fmt.Sprintf("round %d · %s", iteration, status)
	}
	if selected {
		meta += " · selected"
	}

	fold := "▸"
	if expanded {
		fold = "▾"
	}
	if !eventHasExpandableBody(event) {
		fold = " "
	}
	prefix := "  " + fold + " " + agentGlyph(event.Type) + " "
	statusText := " · " + meta
	available := max(1, renderer.width-lipgloss.Width(prefix)-lipgloss.Width(statusText))
	header := renderer.paintRow(cliLine{
		{text: prefix + cliClipCells(title, available), style: cliStyle{foreground: titleColor, bold: true}},
		{text: statusText, style: cliStyle{foreground: m.styles.Faint}},
	})
	rows := []string{header}

	if compact := m.eventCompactDetail(event, renderer.width-4); compact != "" && !expanded && !m.compactView {
		rows = append(rows, renderer.paintRow(cliLine{
			{text: "    ", style: cliStyle{foreground: m.styles.Faint}},
			{text: compact, style: cliStyle{foreground: m.styles.Muted}},
		}))
	}

	showBody := strings.TrimSpace(event.Content) != "" && !m.agentBodyAlreadyShown(event.Content) &&
		(expanded || (!m.compactView && m.cfg.ToolDetails && eventIsToolish(event)) || (!m.compactView && eventShowsBodyByDefault(event)))
	if showBody {
		bodyRenderer := renderer
		bodyRenderer.body = cliStyle{foreground: m.styles.Muted}
		bodyRenderer.strong = cliStyle{foreground: m.styles.Text, bold: true}
		rows = append(rows, m.renderEventBody(event, bodyRenderer)...)
	}
	if selected && eventHasExpandableBody(event) {
		rows = append(rows, renderer.paintRow(cliLine{{text: "    Enter/Space expand · y copy event · c copy code · Ctrl+Shift+C copy surface · Ctrl+T return", style: cliStyle{foreground: m.styles.Faint}}}))
	}
	_ = index
	return rows
}

func (m Model) eventTitleColor(event history.Event) color.Color {
	switch event.Type {
	case "approval_request":
		return m.styles.AccentBright
	case "tool_result", "test_result", "verification":
		if event.Status == "error" {
			return m.styles.Warning
		}
		return m.styles.Success
	case "plan_update":
		return m.styles.Primary
	case "reasoning_summary", "reasoning_trace", "director_status":
		return m.styles.AccentSoft
	case "instrument_review":
		if event.Status == "error" || event.Status == "action-required" {
			return m.styles.Warning
		}
		if event.Status == "clean" {
			return m.styles.Success
		}
		return m.styles.Primary
	case "final":
		return m.styles.Text
	default:
		return m.styles.Muted
	}
}

func (m Model) eventDisplayTitle(event history.Event) string {
	if event.Type == "reasoning_summary" || event.Type == "reasoning_trace" {
		return "thinking surface"
	}
	if event.Type == "instrument_review" {
		stage := eventMetadataString(event, "stage")
		if stage != "" {
			return "instrument review · " + stage
		}
		return "instrument review"
	}
	if event.Type == "director_status" {
		return "director council"
	}
	if event.Type == "tool_call" && event.Tool != "" {
		return "tool " + event.Tool
	}
	if event.Type == "tool_result" && event.Tool != "" {
		return "result " + event.Tool
	}
	return firstNonEmpty(event.Title, event.Type)
}

func eventHasExpandableBody(event history.Event) bool {
	return strings.TrimSpace(event.Content) != ""
}

func eventIsToolish(event history.Event) bool {
	switch event.Type {
	case "tool_call", "tool_result", "test_result", "approval_request", "verification":
		return true
	default:
		return false
	}
}

func eventShowsBodyByDefault(event history.Event) bool {
	switch event.Type {
	case "tool_call", "tool_result", "test_result", "approval_request", "verification", "reasoning_trace", "reasoning_summary", "director_status":
		return false
	default:
		return true
	}
}

func (m Model) eventCompactDetail(event history.Event, width int) string {
	switch event.Type {
	case "reasoning_trace", "reasoning_summary":
		trace := eventAgentTrace(event)
		goal := trace.Goal
		if goal == "" {
			goal = firstLineCompact(event.Content, width)
		}
		next := firstNonEmpty(trace.NextStep, trace.Verification, parseReasoningSection(event.Content, "Next step"), parseReasoningSection(event.Content, "Verification"))
		if next != "" && lipgloss.Width(goal)+lipgloss.Width(next)+9 <= width {
			return "goal: " + goal + " · next: " + next
		}
		return "goal: " + firstLineCompact(goal, max(12, width-6))
	case "plan_update":
		return firstLineCompact(strings.ReplaceAll(event.Content, "- [ ]", "□"), width)
	case "instrument_review":
		severity := firstNonEmpty(eventMetadataString(event, "severity"), event.Status)
		provider := eventMetadataString(event, "provider")
		model := eventMetadataString(event, "model")
		prefix := severity
		if provider != "" || model != "" {
			prefix += " · " + strings.Trim(strings.TrimSpace(provider+"/"+model), "/")
		}
		return firstLineCompact(prefix+" · "+event.Content, width)
	case "director_status":
		return firstLineCompact(event.Content, width)
	case "tool_call", "tool_result", "test_result", "approval_request", "verification":
		prefix := ""
		if ms := eventMetadataInt(event, "duration_ms"); ms > 0 {
			prefix = fmt.Sprintf("%dms · ", ms)
		}
		if event.Type == "tool_result" || event.Type == "test_result" {
			path := eventMetadataString(event, "path")
			lines := 0
			if strings.TrimSpace(event.Content) != "" {
				lines = strings.Count(strings.TrimRight(event.Content, "\n"), "\n") + 1
			}
			size := len([]rune(event.Content))
			summary := prefix
			if path != "" {
				summary += path + " · "
			}
			summary += fmt.Sprintf("%d lines, %s chars", lines, formatTokenCount(size))
			return firstLineCompact(summary, width)
		}
		return firstLineCompact(prefix+event.Content, width)
	case "final":
		return firstLineCompact(event.Content, width)
	default:
		return firstLineCompact(event.Content, width)
	}
}

func (m Model) renderEventBody(event history.Event, renderer cliRenderer) []string {
	content := strings.TrimSpace(event.Content)
	if content == "" {
		return nil
	}
	if event.Type == history.EventReasoningTrace || event.Type == history.EventReasoningSummary {
		trace := eventAgentTrace(event)
		if !trace.Empty() {
			return m.renderStructuredReasoning(trace, renderer)
		}
	}
	if event.Type == "tool_call" || event.Type == "tool_result" || event.Type == "test_result" || event.Type == "approval_request" {
		content = "```text\n" + content + "\n```"
	}
	return strings.Split(renderer.Render(content), "\n")
}

func (m Model) renderStructuredReasoning(trace history.AgentTrace, renderer cliRenderer) []string {
	var sections []string
	appendText := func(glyph, label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		sections = append(sections, fmt.Sprintf("**%s %s**\n%s", glyph, label, value))
	}
	appendList := func(glyph, label string, values []string, numbered bool) {
		var clean []string
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				clean = append(clean, value)
			}
		}
		if len(clean) == 0 {
			return
		}
		var lines []string
		for index, value := range clean {
			if numbered {
				lines = append(lines, fmt.Sprintf("%d. %s", index+1, value))
			} else {
				lines = append(lines, "- "+value)
			}
		}
		sections = append(sections, fmt.Sprintf("**%s %s**\n%s", glyph, label, strings.Join(lines, "\n")))
	}
	appendText("◆", "Goal", trace.Goal)
	appendText("▸", "Current state", trace.CurrentState)
	appendList("▸", "Assumptions", trace.Assumptions, false)
	appendList("▾", "Approach", trace.Approach, true)
	appendList("▾", "Evidence", trace.Evidence, false)
	appendList("▸", "Risks", trace.Risks, false)
	appendText("▸", "Tool rationale", trace.ToolRationale)
	verificationGlyph := "✓"
	if strings.Contains(strings.ToLower(trace.Verification), "fail") || strings.Contains(strings.ToLower(trace.Verification), "unverified") {
		verificationGlyph = "!"
	}
	appendText(verificationGlyph, "Verification", trace.Verification)
	appendText("→", "Next step", trace.NextStep)
	return strings.Split(renderer.Render(strings.Join(sections, "\n\n")), "\n")
}

func eventMetadataInt(event history.Event, key string) int {
	if event.Metadata == nil {
		return 0
	}
	switch value := event.Metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func eventMetadataString(event history.Event, key string) string {
	if event.Metadata == nil {
		return ""
	}
	value, _ := event.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func (m Model) agentBodyAlreadyShown(content string) bool {
	content = strings.TrimSpace(content)
	answer := strings.TrimSpace(m.lastAssistant)
	if len(content) < 48 || answer == "" {
		return false
	}
	return content == answer || strings.Contains(answer, content)
}

func agentGlyph(kind string) string {
	switch kind {
	case "plan_update":
		return "◇"
	case "reasoning_summary", "reasoning_trace":
		return "◌"
	case "instrument_review":
		return "◈"
	case "director_status":
		return "♢"
	case "tool_call":
		return "›"
	case "tool_result", "test_result":
		return "✓"
	case "verification":
		return "◎"
	case "approval_request":
		return "◆"
	case "final":
		return "✦"
	default:
		return "·"
	}
}

func (m *Model) approvePending() tea.Cmd {
	if m.pendingApproval == nil {
		m.status = "No pending approval."
		return nil
	}
	if m.busy {
		m.status = "The approved action is already running."
		return nil
	}
	pending := *m.pendingApproval
	cfg := m.cfg
	m.busy = true
	m.status = "Running approved " + pending.Call.Name + "..."
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		event := agent.NewRunner(cfg, nil).ExecuteApproved(ctx, pending)
		return approvalResultMsg{event: event, pending: pending, continueAgent: !pending.LocalOnly}
	}
}

func (m *Model) resolvePendingApproval(pending agent.PendingApproval, result history.Event) {
	fingerprint := pending.Fingerprint
	if fingerprint == "" && result.Metadata != nil {
		fingerprint, _ = result.Metadata["call_fingerprint"].(string)
	}
	toolStatus := "done"
	approvalStatus := "approved"
	approvalContent := "User approved the action and it completed successfully."
	if result.Status == "error" {
		toolStatus = "error"
		approvalStatus = "error"
		approvalContent = "User approved the action, but execution failed."
	}

	toolResolved := false
	approvalResolved := false
	for index := len(m.session.Events) - 1; index >= 0; index-- {
		event := &m.session.Events[index]
		if !toolResolved && event.Type == history.EventToolCall && pendingEventMatches(*event, pending.CallEventID, fingerprint, pending.Call.Name) {
			event.Status = toolStatus
			setResolutionMetadata(event, result.ID)
			toolResolved = true
		}
		if !approvalResolved && event.Type == history.EventApprovalRequest && pendingEventMatches(*event, pending.ApprovalEventID, fingerprint, pending.Call.Name) {
			event.Status = approvalStatus
			event.Content = approvalContent
			setResolutionMetadata(event, result.ID)
			approvalResolved = true
		}
		if toolResolved && approvalResolved {
			break
		}
	}
}

func pendingEventMatches(event history.Event, eventID, fingerprint, tool string) bool {
	if eventID != "" {
		return event.ID == eventID
	}
	if event.Tool != tool || (event.Status != "pending" && event.Status != "running") {
		return false
	}
	if fingerprint == "" || event.Metadata == nil {
		return true
	}
	value, _ := event.Metadata["call_fingerprint"].(string)
	return value == fingerprint
}

func setResolutionMetadata(event *history.Event, resultEventID string) {
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	event.Metadata["resolved_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if resultEventID != "" {
		event.Metadata["result_event_id"] = resultEventID
	}
}

func (m *Model) rejectPending() {
	if m.pendingApproval == nil {
		m.status = "No pending approval."
		return
	}
	if m.busy {
		m.status = "The approved action is already running and can no longer be rejected."
		return
	}
	pending := *m.pendingApproval
	fingerprint := pending.Fingerprint
	resolvedTool := false
	resolvedApproval := false
	for index := len(m.session.Events) - 1; index >= 0; index-- {
		event := &m.session.Events[index]
		if !resolvedTool && event.Type == history.EventToolCall && pendingEventMatches(*event, pending.CallEventID, fingerprint, pending.Call.Name) {
			event.Status = "rejected"
			setResolutionMetadata(event, "")
			resolvedTool = true
		}
		if !resolvedApproval && event.Type == history.EventApprovalRequest && pendingEventMatches(*event, pending.ApprovalEventID, fingerprint, pending.Call.Name) {
			event.Status = "rejected"
			event.Content = "User rejected the pending action."
			setResolutionMetadata(event, "")
			resolvedApproval = true
		}
		if resolvedTool && resolvedApproval {
			break
		}
	}
	if !resolvedApproval {
		event := history.Event{
			Type:      history.EventApprovalRequest,
			Title:     "Rejected: " + pending.Call.Name,
			Content:   "User rejected the pending action.",
			Tool:      pending.Call.Name,
			Status:    "rejected",
			Metadata:  map[string]any{"call_fingerprint": fingerprint},
			CreatedAt: time.Now(),
		}
		m.session.AppendEvent(event)
	}
	m.pendingApproval = nil
	m.notice = ""
	_ = m.saveSession()
	m.status = "Rejected pending action."
}

func (m *Model) submitShellCommand(command string) tea.Cmd {
	if command == "" {
		m.status = "Usage: !<command>"
		return nil
	}
	call := tools.Call{Name: "shell", Arguments: map[string]any{"command": command}}
	fingerprint := callFingerprint(call)
	registry := tools.NewRegistry(m.cfg)
	callEvent := history.Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      history.EventToolCall,
		Title:     "shell",
		Content:   command,
		Tool:      "shell",
		Status:    "pending",
		Metadata:  map[string]any{"call_fingerprint": fingerprint},
		CreatedAt: time.Now(),
	}
	m.session.AppendEvent(callEvent)
	if registry.RequiresApproval(call.Name) {
		reason := "Shell command requested from composer."
		approvalEvent := history.Event{
			ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
			Type:      history.EventApprovalRequest,
			Title:     "Approval required: shell",
			Content:   reason,
			Tool:      "shell",
			Status:    "pending",
			Metadata:  map[string]any{"call_fingerprint": fingerprint, "call_event_id": callEvent.ID},
			CreatedAt: time.Now(),
		}
		m.pendingApproval = &agent.PendingApproval{
			Call:            call,
			Reason:          reason,
			LocalOnly:       true,
			Fingerprint:     fingerprint,
			CallEventID:     callEvent.ID,
			ApprovalEventID: approvalEvent.ID,
		}
		m.session.AppendEvent(approvalEvent)
		_ = m.saveSession()
		m.status = "Shell approval needed · /approve or /reject"
		return nil
	}
	m.busy = true
	m.status = "Running shell command..."
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		pending := agent.PendingApproval{Call: call, Reason: "composer shell command", LocalOnly: true, Fingerprint: fingerprint, CallEventID: callEvent.ID}
		event := agent.NewRunner(m.cfg, nil).ExecuteApproved(ctx, pending)
		return approvalResultMsg{event: event, pending: pending, continueAgent: false}
	}
}

func callFingerprint(call tools.Call) string {
	return tools.Fingerprint(call)
}

func (m *Model) roleModelLabel(route, model, inheritLabel string) string {
	if strings.TrimSpace(route) == "" && strings.TrimSpace(model) == "" {
		return inheritLabel
	}
	cfg, err := m.cfg.ConfigForRole(route, model)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	label := cfg.Provider + " / " + cfg.Model()
	if route != "" {
		if connection, ok := m.cfg.Connections[strings.ToLower(strings.TrimSpace(route))]; ok {
			label = connection.DisplayName() + " / " + cfg.Model()
		}
	}
	return label
}

func (m *Model) subagentNotice() string {
	return fmt.Sprintf(`### Lightweight subagent

- Enabled: %t
- Automatic routing: %t
- Model: %s
- Max steps: %d
- Token cap: %s
- Permissions: read-only

Use /subagent auto on only when you want eligible reads moved away from the main agent. Explicit delegate calls remain available whenever the subagent is enabled. Use /subagent model inherit to reuse the main model.`,
		m.cfg.SubagentEnabled,
		m.cfg.SubagentAutoRoute,
		m.roleModelLabel(m.cfg.SubagentProvider, m.cfg.SubagentModel, "inherit main model"),
		m.cfg.SubagentMaxSteps,
		formatTokenCount(int(m.cfg.SubagentMaxTokens)),
	)
}

func (m *Model) directorNotice() string {
	return fmt.Sprintf(`### Director mode

- Enabled: %t
- Director: %s
- Instrument: %s
- Instrument influence: %d%%
- Director max steps: %d
- Instrument review budget: %d
- Instrument permissions: advisory only; no tools

The director owns planning, tools, and final decisions. The instrument reviews major actions and final output. Reviews appear as distinct timeline events with their severity and whether they were incorporated.`,
		m.cfg.DirectorEnabled,
		m.roleModelLabel(m.cfg.DirectorProvider, m.cfg.DirectorModel, "inherit main model"),
		m.roleModelLabel(m.cfg.InstrumentProvider, m.cfg.InstrumentModel, "inherit director model"),
		m.cfg.InstrumentWeight,
		m.cfg.DirectorMaxSteps,
		m.cfg.InstrumentMaxSteps,
	)
}

func (m *Model) agentNotice() string {
	return fmt.Sprintf(`### Agent

- Enabled: %t
- Approval policy: %s
- Workspace: %s
- Auto test: %s
- Tool output budget: %s tokens
- Agent max steps: %d
- Repeated-call guard: %d
- Automatic verification: %t
- Automatic review specialist: %t
- Lightweight subagent: %t · %s
- Director mode: %t · instrument %s · influence %d%%
- Inspect before edit: %t
- Sandbox mode: %s
- Dry run: %t
- Automatic rollback: %t
- Semantic codebase index: %t
- TDD mode: %t
- Episodic learning: %t
- Beneath the Surface: %t

Use /agent auto or /approval auto for automatic execution. Use /agent safe to restore confirmations. Configure model routing with /subagent and /director. Safety controls: /sandbox, /dry-run, and /rollback. Intelligence controls: /index, /tdd, and /learn. Use /surface after a run to reopen the persisted goal, evidence, plan, and verification trace.`,
		m.cfg.AgentEnabled,
		m.cfg.ApprovalPolicy,
		agent.NewRunner(m.cfg, nil).Tools.WorkspaceRoot,
		m.cfg.AutoTestCommand,
		formatTokenCount(m.cfg.MaxToolOutputTokens),
		m.cfg.AgentMaxSteps,
		m.cfg.AgentLoopLimit,
		m.cfg.AgentAutoVerify,
		m.cfg.AgentAutoReview,
		m.cfg.SubagentEnabled,
		m.roleModelLabel(m.cfg.SubagentProvider, m.cfg.SubagentModel, "inherit main"),
		m.cfg.DirectorEnabled,
		m.roleModelLabel(m.cfg.InstrumentProvider, m.cfg.InstrumentModel, "inherit director"),
		m.cfg.InstrumentWeight,
		m.cfg.RequireReadBeforeEdit,
		m.cfg.SandboxMode,
		m.cfg.AgentDryRun,
		m.cfg.AgentAutoRollback,
		m.cfg.AgentSemanticIndex,
		m.cfg.AgentTDDMode,
		m.cfg.AgentLearnMemory,
		m.cfg.ShowThinking,
	)
}

func (m *Model) toolsNotice() string {
	var b strings.Builder
	b.WriteString("### Built-in tools\n\n")
	for _, tool := range tools.Builtins() {
		args := "no arguments"
		if len(tool.Arguments) > 0 {
			var parts []string
			for _, argument := range tool.Arguments {
				name := argument.Name
				if argument.Required {
					name += "*"
				}
				parts = append(parts, name+":"+argument.Type)
			}
			args = strings.Join(parts, ", ")
		}
		fmt.Fprintf(&b, "- `%s` [%s] — %s Args: %s\n", tool.Name, tool.Risk, tool.Description, args)
	}
	b.WriteString("\nRequired arguments are marked with `*`. Safe mode auto-runs read tools and asks before write/shell tools.")
	return b.String()
}

func (m Model) surfaceNotice() string {
	snapshot := m.session.Agent
	if !snapshot.Trace.Empty() {
		return m.structuredSurfaceNotice(snapshot)
	}
	if strings.TrimSpace(snapshot.Reasoning) == "" {
		if event, ok := m.latestReasoningEvent(); ok {
			if trace := eventAgentTrace(event); !trace.Empty() {
				return "### Beneath the Surface\n\n" + formatAgentTraceMarkdown(trace)
			}
			return "### Beneath the Surface\n\n" + strings.TrimSpace(event.Content)
		}
		return "### Beneath the Surface\n\nNo persisted reasoning summary is available yet."
	}
	var sections []string
	sections = append(sections, "### Beneath the Surface")
	sections = append(sections, strings.TrimSpace(snapshot.Reasoning))
	if strings.TrimSpace(snapshot.Plan) != "" {
		sections = append(sections, "### Persisted plan\n\n"+strings.TrimSpace(snapshot.Plan))
	}
	verification := strings.TrimSpace(snapshot.Verification)
	if verification == "" {
		verification = "No explicit verification evidence was captured."
	}
	state := "unverified"
	if snapshot.Verified {
		state = "verified"
	}
	sections = append(sections, "### Verification · "+state+"\n\n"+verification)
	if strings.TrimSpace(snapshot.Summary) != "" {
		sections = append(sections, "### Final summary\n\n"+strings.TrimSpace(snapshot.Summary))
	}
	return strings.Join(sections, "\n\n")
}

func (m Model) structuredSurfaceNotice(snapshot history.AgentSnapshot) string {
	var sections []string
	sections = append(sections, "### Beneath the Surface")
	sections = append(sections, formatAgentTraceMarkdown(snapshot.Trace))
	if strings.TrimSpace(snapshot.Plan) != "" {
		sections = append(sections, "### Persisted plan\n\n"+strings.TrimSpace(snapshot.Plan))
	}
	verification := strings.TrimSpace(firstNonEmpty(snapshot.Trace.Verification, snapshot.Verification))
	if verification == "" {
		verification = "No explicit verification evidence was captured."
	}
	state := "unverified"
	if snapshot.Verified {
		state = "verified"
	}
	sections = append(sections, "### Verification · "+state+"\n\n"+verification)
	if strings.TrimSpace(snapshot.Summary) != "" {
		sections = append(sections, "### Final summary\n\n"+strings.TrimSpace(snapshot.Summary))
	}
	return strings.Join(sections, "\n\n")
}

func formatAgentTraceMarkdown(trace history.AgentTrace) string {
	var sections []string
	appendText := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			sections = append(sections, "**"+label+"**\n"+strings.TrimSpace(value))
		}
	}
	appendList := func(label string, values []string) {
		var items []string
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				items = append(items, strings.TrimSpace(value))
			}
		}
		if len(items) > 0 {
			sections = append(sections, "**"+label+"**\n- "+strings.Join(items, "\n- "))
		}
	}
	appendText("Goal", trace.Goal)
	appendText("Current state", trace.CurrentState)
	appendList("Assumptions", trace.Assumptions)
	appendList("Approach", trace.Approach)
	appendList("Evidence", trace.Evidence)
	appendList("Risks", trace.Risks)
	appendText("Tool rationale", trace.ToolRationale)
	appendText("Verification", trace.Verification)
	appendText("Next step", trace.NextStep)
	if len(sections) == 0 {
		return "No structured trace fields were captured."
	}
	return strings.Join(sections, "\n\n")
}

func (m Model) planNotice() string {
	if strings.TrimSpace(m.session.Agent.Plan) != "" {
		return "### Plan\n\n" + strings.TrimSpace(m.session.Agent.Plan)
	}
	var plans []string
	for i := len(m.session.Events) - 1; i >= 0; i-- {
		if m.session.Events[i].Type == "plan_update" {
			plans = append(plans, m.session.Events[i].Content)
			break
		}
	}
	if len(plans) == 0 {
		return "### Plan\n\nNo active agent plan yet."
	}
	return "### Plan\n\n" + strings.TrimSpace(plans[0])
}

func (m Model) configNotice() string {
	return fmt.Sprintf(`### Config

- Provider/model: %s / %s
- Active route: %s
- Remembered routes: %d
- Mode/theme: %s / %s
- Agent enabled: %t
- Approval policy: %s
- Workspace root: %s
- Auto test command: %s
- Context budget: %s
- Max output tokens: %s
- Max tool output tokens: %s
- Agent max steps: %d
- Repeated-call guard: %d
- Automatic verification: %t
- Automatic review specialist: %t
- Lightweight subagent: %t · %s
- Director mode: %t · director %s
- Instrument: %s · influence %d%%
- Inspect before edit: %t
- Sandbox mode: %s
- Dry run: %t
- Automatic rollback: %t
- Snapshot limit: %d MiB
- Semantic codebase index: %t
- Context recall messages: %d
- TDD mode: %t
- Episodic learning: %t
- Beneath the Surface: %t
- Codex bridge response target: %s tokens
- Debug log: %s
- Theme density: %s`,
		m.providerName(),
		m.cfg.Model(),
		m.cfg.ActiveConnection,
		len(m.cfg.ConnectedConnections()),
		m.cfg.Mode,
		m.cfg.Theme,
		m.cfg.AgentEnabled,
		m.cfg.ApprovalPolicy,
		agent.NewRunner(m.cfg, nil).Tools.WorkspaceRoot,
		m.cfg.AutoTestCommand,
		formatTokenCount(m.cfg.ContextTokens),
		formatTokenCount(int(m.cfg.MaxTokens)),
		formatTokenCount(m.cfg.MaxToolOutputTokens),
		m.cfg.AgentMaxSteps,
		m.cfg.AgentLoopLimit,
		m.cfg.AgentAutoVerify,
		m.cfg.AgentAutoReview,
		m.cfg.SubagentEnabled,
		m.roleModelLabel(m.cfg.SubagentProvider, m.cfg.SubagentModel, "inherit main"),
		m.cfg.DirectorEnabled,
		m.roleModelLabel(m.cfg.DirectorProvider, m.cfg.DirectorModel, "inherit main"),
		m.roleModelLabel(m.cfg.InstrumentProvider, m.cfg.InstrumentModel, "inherit director"),
		m.cfg.InstrumentWeight,
		m.cfg.RequireReadBeforeEdit,
		m.cfg.SandboxMode,
		m.cfg.AgentDryRun,
		m.cfg.AgentAutoRollback,
		m.cfg.AgentSnapshotMaxMB,
		m.cfg.AgentSemanticIndex,
		m.cfg.AgentContextRecall,
		m.cfg.AgentTDDMode,
		m.cfg.AgentLearnMemory,
		m.cfg.ShowThinking,
		formatTokenCount(int(m.cfg.CodexBridgeMaxTokens)),
		debugLogPath(),
		m.cfg.ThemeDensity,
	)
}

func (m Model) codexNotice() string {
	workspaceAuthority := "Ephemera tools may read; writes follow " + string(m.cfg.ApprovalPolicy)
	if m.cfg.ApprovalPolicy == config.ApprovalReadOnly || m.cfg.ApprovalPolicy == config.ApprovalChat {
		workspaceAuthority = "Ephemera is currently read-only; use `/approval safe` to approve writes or `/approval workspace-write` for automatic workspace writes"
	}
	effort := "low"
	if m.cfg.Mode == reasoning.ModeDeep {
		effort = "high"
	} else if m.cfg.Mode == reasoning.ModeConcise {
		effort = "minimal/low"
	}
	return fmt.Sprintf(`### Codex model bridge

- Route: Codex CLI using the existing ChatGPT login
- Execution: isolated model-only bridge
- Bridge scratch sandbox: workspace-write in a disposable temp directory; project workspace is not mounted
- Codex-native shell, file edits, web, MCP, hooks, and subagents: disabled
- Workspace authority: %s
- Requested response target: %s tokens
- Bridge reasoning effort: %s
- Reasoning summaries: %s
- Compatibility fallback: enabled for older Codex CLI builds

The inner Codex process receives only a disposable isolated bridge directory, not the project workspace. It returns Ephemera tool requests; Ephemera then reads, writes, runs commands, records approvals, snapshots changes, and logs failures through its own tool layer. This removes the previous nested-agent read-only errors and reduces duplicated context/tool usage.`,
		workspaceAuthority,
		formatTokenCount(int(m.cfg.CodexBridgeMaxTokens)),
		effort,
		map[bool]string{true: "concise", false: "disabled"}[m.cfg.ShowThinking],
	)
}

func (m Model) memoryNotice() string {
	states := m.memorySourceStates()
	var found, tokens int
	for _, source := range states {
		if source.Found && source.Err == "" {
			found++
			tokens += source.Tokens
		}
	}

	var b strings.Builder
	b.WriteString("### Memory\n\n")
	fmt.Fprintf(&b, "- Workspace: `%s`\n", escapeMarkdown(m.workspaceRoot()))
	fmt.Fprintf(&b, "- Loaded sources: %d / %d\n", found, len(states))
	fmt.Fprintf(&b, "- Estimated memory tokens: ~%s\n\n", formatTokenCount(tokens))
	for _, source := range states {
		switch {
		case source.Found && source.Err == "":
			fmt.Fprintf(&b, "#### `%s`\n\n", source.Path)
			fmt.Fprintf(&b, "- Status: found · %s · ~%s tokens · updated %s\n", formatByteCount(source.Size), formatTokenCount(source.Tokens), source.Modified.Format("2006-01-02 15:04"))
			if strings.TrimSpace(source.Preview) == "" {
				b.WriteString("- Preview: empty file\n\n")
			} else {
				b.WriteString("\n```text\n")
				b.WriteString(source.Preview)
				b.WriteString("\n```\n\n")
			}
		case source.Err != "":
			fmt.Fprintf(&b, "- `%s` — unavailable: %s\n", source.Path, escapeMarkdown(source.Err))
		default:
			fmt.Fprintf(&b, "- `%s` — missing\n", source.Path)
		}
	}
	return strings.TrimSpace(b.String())
}

func (m Model) localToolNotice(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := tools.NewRegistry(m.cfg).Execute(ctx, tools.Call{Name: name, Arguments: map[string]any{}})
	title := "### " + name
	if result.OK {
		if strings.TrimSpace(result.Output) == "" {
			return title + "\n\nNo output."
		}
		return title + "\n\n```text\n" + result.Output + "\n```"
	}
	return title + "\n\n`" + escapeMarkdown(firstNonEmpty(result.Error, result.Output, "failed")) + "`"
}

func (m *Model) compactAgentEvents() {
	const keep = 40
	if len(m.session.Events) <= keep {
		m.status = "Timeline already compact."
		return
	}
	dropped := len(m.session.Events) - keep
	m.session.Events = append([]history.Event{{
		Type:      "reasoning_summary",
		Title:     "Compacted timeline",
		Content:   fmt.Sprintf("Compacted %d older agent event(s). Recent tool results and plans were kept.", dropped),
		Status:    "done",
		CreatedAt: time.Now(),
	}}, m.session.Events[dropped:]...)
	_ = m.saveSession()
	m.status = fmt.Sprintf("Compacted %d event(s).", dropped)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
