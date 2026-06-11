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
	width := m.transcriptWidth()
	var out strings.Builder
	out.WriteString(m.transcriptLine(m.styles.NoticeLabel, "agent"))
	out.WriteString("\n")
	for _, event := range m.session.Events {
		if event.Type == "reasoning_summary" && !m.cfg.ShowThinking {
			continue
		}
		out.WriteString(m.renderAgentEvent(event, width))
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}

func (m Model) renderAgentEvent(event history.Event, width int) string {
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
	case "reasoning_summary":
		titleColor = m.styles.AccentSoft
	case "final":
		titleColor = m.styles.Text
	}
	title := event.Title
	if event.Type == "reasoning_summary" {
		title = "thinking"
	}
	if event.Type == "tool_call" && event.Tool != "" {
		title = "tool " + event.Tool
	}

	status := event.Status
	if status == "" {
		status = "done"
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(titleColor).Background(m.styles.Panel).Render("  "+agentGlyph(event.Type)+" "+title) +
		lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.Panel).Render(" · "+status)

	bodyStyle := lipgloss.NewStyle().Foreground(m.styles.Muted).Background(m.styles.Panel).Width(width)
	body := ""
	showBody := strings.TrimSpace(event.Content) != "" && (m.cfg.ToolDetails || (event.Type != "tool_call" && event.Type != "tool_result"))
	if showBody {
		for _, line := range strings.Split(event.Content, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			body += bodyStyle.Render("    "+line) + "\n"
		}
	}
	return strings.TrimRight(header+"\n"+body, "\n")
}

func agentGlyph(kind string) string {
	switch kind {
	case "plan_update":
		return "◇"
	case "reasoning_summary":
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

Use /agent on, /agent off, /tools, /plan, /approve, /reject, /run, /diff, /compact, /config, and /memory.`,
		m.cfg.AgentEnabled,
		m.cfg.ApprovalPolicy,
		agent.NewRunner(m.cfg, nil).Tools.WorkspaceRoot,
		m.cfg.AutoTestCommand,
		formatTokenCount(m.cfg.MaxToolOutputTokens),
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
