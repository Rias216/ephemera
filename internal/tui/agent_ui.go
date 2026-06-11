package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func (m Model) renderAgentTimeline() string {
	renderer := newCLIRenderer(m.styles, m.transcriptWidth())
	rows := []string{m.transcriptLine(m.styles.NoticeLabel, "agent")}
	for _, event := range m.session.Events {
		if (event.Type == "reasoning_summary" || event.Type == "reasoning_trace") && !m.cfg.ShowThinking {
			continue
		}
		rows = append(rows, m.renderAgentEvent(event, renderer)...)
	}
	if m.liveAgent.Active {
		rows = append(rows, m.renderLiveAgent(renderer)...)
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderLiveAgent(renderer cliRenderer) []string {
	phase := firstNonEmpty(m.liveAgent.Phase, "working")
	label := fmt.Sprintf("  ◆ live · round %d · %s", max(1, m.liveAgent.Iteration), phase)
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
	detail := fmt.Sprintf("  ctx ~%s/%s · output ~%s%s · received %d chars · elapsed %s",
		formatTokenCount(m.liveAgent.ContextTokens),
		formatTokenCount(m.cfg.ContextTokens),
		formatTokenCount(m.liveAgent.OutputTokens),
		messageState,
		m.liveAgent.ReceivedChars,
		elapsed,
	)
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
	return rows
}

func (m Model) renderAgentEvent(event history.Event, renderer cliRenderer) []string {
	titleColor := m.styles.Muted
	switch event.Type {
	case "approval_request":
		titleColor = m.styles.AccentBright
	case "tool_result", "test_result":
		if event.Status == "error" {
			titleColor = m.styles.Warning
		} else {
			titleColor = m.styles.Success
		}
	case "plan_update":
		titleColor = m.styles.Primary
	case "reasoning_summary", "reasoning_trace":
		titleColor = m.styles.AccentSoft
	case "final":
		titleColor = m.styles.Text
	}

	title := event.Title
	if event.Type == "reasoning_summary" || event.Type == "reasoning_trace" {
		title = "beneath the surface"
	}
	if event.Type == "tool_call" && event.Tool != "" {
		title = "tool " + event.Tool
	}
	status := event.Status
	if status == "" {
		status = "done"
	}

	prefix := "  " + agentGlyph(event.Type) + " "
	statusText := " · " + status
	available := max(1, renderer.width-lipgloss.Width(prefix)-lipgloss.Width(statusText))
	title = cliClipCells(title, available)
	header := renderer.paintRow(cliLine{
		{text: prefix + title, style: cliStyle{foreground: titleColor, bold: true}},
		{text: statusText, style: cliStyle{foreground: m.styles.Faint}},
	})
	rows := []string{header}

	showBody := strings.TrimSpace(event.Content) != "" &&
		(m.cfg.ToolDetails || (event.Type != "tool_call" && event.Type != "tool_result")) &&
		!m.agentBodyAlreadyShown(event.Content)
	if showBody {
		bodyRenderer := renderer
		bodyRenderer.body = cliStyle{foreground: m.styles.Muted}
		bodyRenderer.strong = cliStyle{foreground: m.styles.Text, bold: true}
		rows = append(rows, strings.Split(bodyRenderer.Render(event.Content), "\n")...)
	}
	return rows
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
	case "tool_call":
		return "›"
	case "tool_result", "test_result":
		return "✓"
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
	pending := *m.pendingApproval
	cfg := m.cfg
	m.busy = true
	m.status = "Running approved " + pending.Call.Name + "..."
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		event := agent.NewRunner(cfg, nil).ExecuteApproved(ctx, pending)
		return approvalResultMsg{event: event, continueAgent: !pending.LocalOnly}
	}
}

func (m *Model) resolvePendingToolEvent(result history.Event) {
	if result.Tool == "" {
		return
	}
	status := "done"
	if result.Status == "error" {
		status = "error"
	}
	for index := len(m.session.Events) - 1; index >= 0; index-- {
		event := &m.session.Events[index]
		if event.Type == "tool_call" && event.Tool == result.Tool && (event.Status == "pending" || event.Status == "running") {
			event.Status = status
			return
		}
	}
}

func (m *Model) rejectPending() {
	if m.pendingApproval == nil {
		m.status = "No pending approval."
		return
	}
	call := m.pendingApproval.Call
	m.session.AppendEvent(history.Event{
		Type:      "approval_request",
		Title:     "Rejected: " + call.Name,
		Content:   "User rejected the pending action.",
		Tool:      call.Name,
		Status:    "rejected",
		CreatedAt: time.Now(),
	})
	m.pendingApproval = nil
	_ = m.saveSession()
	m.status = "Rejected pending action."
}

func (m *Model) submitShellCommand(command string) tea.Cmd {
	if command == "" {
		m.status = "Usage: !<command>"
		return nil
	}
	call := tools.Call{Name: "shell", Arguments: map[string]any{"command": command}}
	registry := tools.NewRegistry(m.cfg)
	m.session.AppendEvent(history.Event{
		Type:      "tool_call",
		Title:     "shell",
		Content:   command,
		Tool:      "shell",
		Status:    "pending",
		CreatedAt: time.Now(),
	})
	if registry.RequiresApproval(call.Name) {
		reason := "Shell command requested from composer."
		m.pendingApproval = &agent.PendingApproval{Call: call, Reason: reason, LocalOnly: true}
		m.session.AppendEvent(history.Event{
			Type:      "approval_request",
			Title:     "Approval required: shell",
			Content:   reason,
			Tool:      "shell",
			Status:    "pending",
			CreatedAt: time.Now(),
		})
		_ = m.saveSession()
		m.status = "Shell approval needed · /approve or /reject"
		return nil
	}
	m.busy = true
	m.status = "Running shell command..."
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		event := agent.NewRunner(m.cfg, nil).ExecuteApproved(ctx, agent.PendingApproval{Call: call, Reason: "composer shell command", LocalOnly: true})
		return approvalResultMsg{event: event, continueAgent: false}
	}
}

func (m *Model) agentNotice() string {
	return fmt.Sprintf(`### Agent

- Enabled: %t
- Approval policy: %s
- Workspace: %s
- Auto test: %s
- Tool output budget: %s tokens
- Beneath the Surface: %t

Use /agent auto or /approval auto for automatic execution. Use /agent safe to restore confirmations. Use /thinking on to show concise goal, assumptions, approach, tool rationale, and verification. Other commands: /tools, /plan, /approve, /reject, /run, /diff, /compact, /config, and /memory.`,
		m.cfg.AgentEnabled,
		m.cfg.ApprovalPolicy,
		agent.NewRunner(m.cfg, nil).Tools.WorkspaceRoot,
		m.cfg.AutoTestCommand,
		formatTokenCount(m.cfg.MaxToolOutputTokens),
		m.cfg.ShowThinking,
	)
}

func (m *Model) toolsNotice() string {
	var b strings.Builder
	b.WriteString("### Built-in tools\n\n")
	for _, tool := range tools.Builtins() {
		fmt.Fprintf(&b, "- `%s` [%s] — %s\n", tool.Name, tool.Risk, tool.Description)
	}
	return b.String()
}

func (m Model) planNotice() string {
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
- Mode/theme: %s / %s
- Agent enabled: %t
- Approval policy: %s
- Workspace root: %s
- Auto test command: %s
- Context budget: %s
- Max output tokens: %s
- Max tool output tokens: %s
- Beneath the Surface: %t
- Theme density: %s`,
		m.providerName(),
		m.cfg.Model(),
		m.cfg.Mode,
		m.cfg.Theme,
		m.cfg.AgentEnabled,
		m.cfg.ApprovalPolicy,
		agent.NewRunner(m.cfg, nil).Tools.WorkspaceRoot,
		m.cfg.AutoTestCommand,
		formatTokenCount(m.cfg.ContextTokens),
		formatTokenCount(int(m.cfg.MaxTokens)),
		formatTokenCount(m.cfg.MaxToolOutputTokens),
		m.cfg.ShowThinking,
		m.cfg.ThemeDensity,
	)
}

func (m Model) memoryNotice() string {
	return "### Memory\n\nProject memory is loaded from `.ephemera/memory.json`, `.ephemera/instructions.md`, `CLAUDE.md`, and `AGENTS.md` in the workspace. Persistent memory editing will land after the V1 tool loop stabilizes."
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
