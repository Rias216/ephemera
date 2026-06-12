package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/history"
)

const (
	inspectorContext = iota
	inspectorAgent
	inspectorThinking
	inspectorKeys
)

func (m *Model) rotateInspector(delta int) {
	const total = 4
	m.inspectorTab = (m.inspectorTab + delta + total) % total
}

func (m Model) footerTabsLine(width int) string {
	bg := lipgloss.NewStyle().Background(m.styles.PanelDeep).ColorWhitespace(true)
	tabs := []string{
		m.renderFooterTab(inspectorContext, "context", "Alt+1"),
		m.renderFooterTab(inspectorAgent, "agent", "Alt+2"),
		m.renderFooterTab(inspectorThinking, "surface", "Alt+3"),
		m.renderFooterTab(inspectorKeys, "keys", "Alt+4"),
	}
	left := strings.Join(tabs, " ")
	_, glyph := m.statusPresentation()
	statusText := glyph + " live · Ctrl+T timeline · Ctrl+←/→ switch"
	if m.timelineFocus {
		statusText = glyph + " timeline · j/k · Enter · t filter"
	}
	if m.liveAgent.Active {
		statusText = glyph + " " + firstNonEmpty(m.liveAgent.Phase, "working") + " · Ctrl+X stop"
	}
	if lipgloss.Width(left)+len([]rune(statusText))+1 > width {
		tabs = []string{
			m.renderFooterTab(inspectorContext, "ctx", ""),
			m.renderFooterTab(inspectorAgent, "agent", ""),
			m.renderFooterTab(inspectorThinking, "surface", ""),
			m.renderFooterTab(inspectorKeys, "keys", ""),
		}
		left = strings.Join(tabs, " ")
	}
	statusWidth := max(0, width-lipgloss.Width(left)-1)
	statusText = cliClipCells(statusText, statusWidth)
	status := lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.PanelDeep).ColorWhitespace(true).Render(statusText)
	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(status))
	return left + bg.Render(strings.Repeat(" ", gap)) + status
}

func (m Model) renderFooterTab(index int, label, hint string) string {
	active := index == m.inspectorTab
	fg := m.styles.Muted
	accent := m.styles.Faint
	if active {
		fg = m.styles.Text
		accent = m.styles.Primary
	}
	style := lipgloss.NewStyle().Background(m.styles.PanelDeep).ColorWhitespace(true)
	left := style.Foreground(accent).Bold(active).Render("[")
	mid := style.Foreground(fg).Bold(active).Render(strings.ToUpper(label))
	right := style.Foreground(accent).Bold(active).Render("]")
	h := ""
	if hint != "" {
		h = style.Foreground(m.styles.Faint).Render(" " + hint)
	}
	return left + mid + right + h
}

func (m Model) footerPrimaryLine(width int) string {
	left, right := m.footerPaneSummary()
	return m.renderFooterPair(left, right, width, true)
}

func (m Model) footerSecondaryLine(width int) string {
	left, right := m.footerPaneDetail()
	return m.renderFooterPair(left, right, width, false)
}

func (m Model) renderFooterPair(left, right string, width int, primary bool) string {
	leftColor := m.styles.Muted
	if primary {
		leftColor = m.styles.Text
	}
	leftStyle := lipgloss.NewStyle().Foreground(leftColor).Background(m.styles.PanelDeep).ColorWhitespace(true)
	rightStyle := lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.PanelDeep).ColorWhitespace(true)
	fill := lipgloss.NewStyle().Background(m.styles.PanelDeep).ColorWhitespace(true)
	if width <= 0 {
		return ""
	}
	maxRight := max(0, width/2)
	if lipgloss.Width(right) > maxRight {
		right = cliClipCells(right, maxRight)
	}
	rightRendered := rightStyle.Render(right)
	leftBudget := max(0, width-lipgloss.Width(rightRendered))
	if right != "" && leftBudget > 0 {
		leftBudget--
	}
	left = cliClipCells(left, leftBudget)
	leftRendered := leftStyle.Render(left)
	gap := max(0, width-lipgloss.Width(leftRendered)-lipgloss.Width(rightRendered))
	return leftRendered + fill.Render(strings.Repeat(" ", gap)) + rightRendered
}

func (m Model) footerPaneSummary() (string, string) {
	stats := m.currentContextStats()
	switch m.inspectorTab {
	case inspectorAgent:
		if m.liveAgent.Active {
			tool := ""
			if m.liveAgent.Tool != "" {
				tool = " · " + m.liveAgent.Tool
			}
			return fmt.Sprintf("agent LIVE · round %d · %s%s", max(1, m.liveAgent.Iteration), firstNonEmpty(m.liveAgent.Phase, "working"), tool),
				fmt.Sprintf("~%s out · %d chars", formatTokenCount(m.liveAgent.OutputTokens), m.liveAgent.ReceivedChars)
		}
		pending := "none"
		if m.pendingApproval != nil {
			pending = m.pendingApproval.Call.Name
		}
		if snapshot := m.session.Agent; snapshot.Status != "" {
			verified := "unverified"
			if snapshot.Verified {
				verified = "verified"
			}
			return fmt.Sprintf("agent %s · round %d · %s · %s", snapshot.Status, max(1, snapshot.Iteration), verified, m.cfg.ApprovalPolicy),
				fmt.Sprintf("ctx ~%s · out ~%s", formatTokenCount(snapshot.ContextTokens), formatTokenCount(snapshot.OutputTokens))
		}
		return fmt.Sprintf("agent %s · policy %s · pending %s", onOff(m.cfg.AgentEnabled), m.cfg.ApprovalPolicy, pending),
			fmt.Sprintf("events %d · msgs %d", len(m.session.Events), len(m.session.Messages))
	case inspectorThinking:
		if m.liveAgent.Active {
			preview := firstNonEmpty(m.liveThoughtPreview(), m.liveGoalPreview())
			return "beneath the surface · " + lastLineCompact(preview, 96), fmt.Sprintf("round %d · %s", max(1, m.liveAgent.Iteration), firstNonEmpty(m.liveAgent.Phase, "working"))
		}
		if snapshot := m.session.Agent; strings.TrimSpace(snapshot.Reasoning) != "" || strings.TrimSpace(snapshot.Goal) != "" || !snapshot.Trace.Empty() {
			preview := firstNonEmpty(snapshot.Trace.Goal, snapshot.Goal, parseReasoningSection(snapshot.Reasoning, "Goal"), snapshot.Reasoning)
			state := snapshot.Status
			if snapshot.Verified {
				state += " · verified"
			} else if snapshot.Completed {
				state += " · unverified"
			}
			return "beneath the surface · " + firstLineCompact(preview, 96), state
		}
		if event, ok := m.latestReasoningEvent(); ok {
			return "beneath the surface · " + firstLineCompact(event.Content, 96), fmt.Sprintf("updated %s", event.CreatedAt.Format("15:04:05"))
		}
		return "beneath the surface is waiting for the next agent step", fmt.Sprintf("thinking %s", onOff(m.cfg.ShowThinking))
	case inspectorKeys:
		return "Enter send · Ctrl+T timeline · j/k select · Enter expand · f follow · t filter", "Alt+1..4 inspector"
	default:
		trim := "clean"
		if stats.DroppedMessages > 0 {
			trim = fmt.Sprintf("trimmed %d", stats.DroppedMessages)
		}
		right := fmt.Sprintf("%s / %s", m.providerName(), m.cfg.Model())
		if m.liveAgent.Active {
			right = fmt.Sprintf("round %d · %s", max(1, m.liveAgent.Iteration), firstNonEmpty(m.liveAgent.Phase, "working"))
		}
		return fmt.Sprintf("ctx %s/%s · sent %d/%d · out %s · %s", formatTokenCount(stats.EstimatedTokens), formatTokenCount(stats.Budget), stats.SentMessages, stats.TotalMessages, formatTokenCount(int(m.cfg.MaxTokens)), trim), right
	}
}

func (m Model) footerPaneDetail() (string, string) {
	switch m.inspectorTab {
	case inspectorAgent:
		if m.liveAgent.Active {
			elapsed := time.Since(m.liveAgent.StartedAt).Round(time.Second)
			if elapsed < 0 {
				elapsed = 0
			}
			return fmt.Sprintf("streaming · ctx ~%s · output ~%s · elapsed %s", formatTokenCount(m.liveAgent.ContextTokens), formatTokenCount(m.liveAgent.OutputTokens), elapsed),
				fmt.Sprintf("policy %s", m.cfg.ApprovalPolicy)
		}
		last := "no agent activity yet"
		if snapshot := m.session.Agent; snapshot.Status != "" {
			last = firstNonEmpty(snapshot.Summary, snapshot.Goal, snapshot.Phase, last)
			last = fmt.Sprintf("last %s: %s", snapshot.Status, firstLineCompact(last, 96))
		} else if event, ok := m.latestEvent(); ok {
			last = fmt.Sprintf("last %s/%s: %s", event.Type, fallbackStatus(event.Status), firstLineCompact(firstNonEmpty(event.Title, event.Content), 96))
		}
		workspace := strings.TrimSpace(m.cfg.WorkspaceRoot)
		if workspace == "" {
			workspace = "(current directory)"
		}
		return last, cliClipCells("workspace "+workspace, 42)
	case inspectorThinking:
		goal, plan := m.latestThinkingSummary()
		phase := fmt.Sprintf("thinking %s", onOff(m.cfg.ShowThinking))
		if snapshot := m.session.Agent; snapshot.Status != "" && !m.liveAgent.Active {
			phase = snapshot.Status
			if verification := firstNonEmpty(snapshot.Trace.Verification, snapshot.Verification); strings.TrimSpace(verification) != "" {
				plan = "verify · " + firstLineCompact(verification, 96)
			}
		}
		if m.liveAgent.Active {
			goal = firstNonEmpty(m.liveThoughtPreview(), m.liveGoalPreview())
			phase = fmt.Sprintf("%s · ~%s generated", firstNonEmpty(m.liveAgent.Phase, "working"), formatTokenCount(m.liveAgent.OutputTokens))
			if strings.TrimSpace(m.liveAgent.Plan) != "" {
				plan = "plan · " + firstLineCompact(m.liveAgent.Plan, 96)
			} else {
				plan = ""
			}
		}
		if m.liveAgent.Active {
			goal = lastLineCompact(goal, 96)
		}
		return firstNonEmpty(goal, "goal not available yet"), firstNonEmpty(plan, phase)
	case inspectorKeys:
		return "Ctrl+Y copy · Ctrl+L clear · Ctrl+X stop · Ctrl+C quit · /agent auto enables full auto-approve", "timeline focus keeps composer safe"
	default:
		if m.liveAgent.Active && m.cfg.ShowThinking {
			thought := m.liveThoughtPreview()
			if strings.TrimSpace(thought) != "" {
				return "thinking · " + lastLineCompact(thought, 110), fmt.Sprintf("reasoning %d chars", m.liveAgent.ReasoningChars)
			}
		}
		return fmt.Sprintf("session %s · mode %s · viewport %3.0f%%", m.session.Name, m.cfg.Mode, m.viewport.ScrollPercent()*100),
			fmt.Sprintf("messages %d · agent %s", len(m.session.Messages), onOff(m.cfg.AgentEnabled))
	}
}

func (m Model) liveGoalPreview() string {
	if strings.TrimSpace(m.liveAgent.Goal) != "" {
		return firstLineCompact(m.liveAgent.Goal, 96)
	}
	if strings.TrimSpace(m.liveAgent.Summary) != "" {
		return firstLineCompact(m.liveAgent.Summary, 96)
	}
	for index := len(m.session.Messages) - 1; index >= 0; index-- {
		if m.session.Messages[index].Role == "user" {
			return firstLineCompact(m.session.Messages[index].Content, 96)
		}
	}
	return "understand the current request and choose the next safe action"
}

func (m Model) latestEvent() (history.Event, bool) {
	if len(m.session.Events) == 0 {
		return history.Event{}, false
	}
	return m.session.Events[len(m.session.Events)-1], true
}

func (m Model) latestReasoningEvent() (history.Event, bool) {
	for i := len(m.session.Events) - 1; i >= 0; i-- {
		event := m.session.Events[i]
		if event.Type == "reasoning_trace" || event.Type == "reasoning_summary" {
			return event, true
		}
	}
	return history.Event{}, false
}

func (m Model) latestPlanEvent() (history.Event, bool) {
	for i := len(m.session.Events) - 1; i >= 0; i-- {
		event := m.session.Events[i]
		if event.Type == "plan_update" {
			return event, true
		}
	}
	return history.Event{}, false
}

func (m Model) latestThinkingSummary() (string, string) {
	if snapshot := m.session.Agent; strings.TrimSpace(snapshot.Reasoning) != "" || strings.TrimSpace(snapshot.Goal) != "" || strings.TrimSpace(snapshot.Plan) != "" || !snapshot.Trace.Empty() {
		goal := firstNonEmpty(snapshot.Trace.Goal, snapshot.Goal, parseReasoningSection(snapshot.Reasoning, "Goal"), snapshot.Reasoning)
		plan := ""
		if strings.TrimSpace(snapshot.Trace.NextStep) != "" {
			plan = "next · " + firstLineCompact(snapshot.Trace.NextStep, 96)
		} else if strings.TrimSpace(snapshot.Plan) != "" {
			plan = "plan · " + firstLineCompact(snapshot.Plan, 96)
		}
		return firstLineCompact(goal, 96), plan
	}
	goal := ""
	if event, ok := m.latestReasoningEvent(); ok {
		goal = parseReasoningSection(event.Content, "Goal")
		if goal == "" {
			goal = firstLineCompact(event.Content, 96)
		}
	}
	plan := ""
	if event, ok := m.latestPlanEvent(); ok {
		plan = "plan · " + firstLineCompact(event.Content, 96)
	}
	return goal, plan
}

func parseReasoningSection(source, heading string) string {
	needle := "**" + heading + "**"
	index := strings.Index(source, needle)
	if index < 0 {
		return ""
	}
	rest := strings.TrimSpace(source[index+len(needle):])
	if strings.HasPrefix(rest, "\n") {
		rest = strings.TrimSpace(rest)
	}
	if rest == "" {
		return ""
	}
	sections := strings.Split(rest, "\n\n")
	return firstLineCompact(sections[0], 96)
}

func firstLineCompact(source string, limit int) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	source = strings.ReplaceAll(source, "\n", " · ")
	source = strings.Join(strings.Fields(source), " ")
	if lipgloss.Width(source) <= limit {
		return source
	}
	return cliClipCells(source, max(1, limit-1)) + "…"
}

func fallbackStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "done"
	}
	return value
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}
