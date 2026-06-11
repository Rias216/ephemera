// Package agent implements Ephemera's provider-neutral project agent loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

const maxAgentIterations = 4

// PendingApproval is a tool call that must be approved by the user.
type PendingApproval struct {
	Call      tools.Call
	Reason    string
	LocalOnly bool
}

// RunResult contains the visible output and structured timeline deltas.
type RunResult struct {
	Text    string
	Events  []history.Event
	Pending *PendingApproval
}

// Runner executes agent turns with the configured provider and tools.
type Runner struct {
	Config   config.Config
	Provider llm.Provider
	Tools    tools.Registry
}

// NewRunner creates an agent runner.
func NewRunner(cfg config.Config, provider llm.Provider) Runner {
	return Runner{
		Config:   cfg,
		Provider: provider,
		Tools:    tools.NewRegistry(cfg),
	}
}

// Run advances the agent until it produces a final answer or needs approval.
func (r Runner) Run(ctx context.Context, session history.Session) RunResult {
	var events []history.Event
	observations := recentToolObservations(session.Events)
	for iteration := 0; iteration < maxAgentIterations; iteration++ {
		text, err := r.Provider.Generate(ctx, llm.Request{
			Model:       r.Config.Model(),
			System:      r.systemPrompt(session, observations),
			Messages:    conversationMessages(session.Messages),
			MaxTokens:   r.Config.MaxTokens,
			Temperature: r.Config.Mode.Temperature(),
		})
		if err != nil {
			events = append(events, newEvent("tool_result", "Agent request failed", err.Error(), "", "error"))
			return RunResult{Text: "Agent request failed: " + err.Error(), Events: events}
		}

		action, ok := parseModelAction(text)
		if !ok {
			events = append(events, newEvent("final", "Final", text, "", "done"))
			return RunResult{Text: text, Events: events}
		}

		events = append(events, actionEvents(action)...)
		if strings.TrimSpace(action.Final) != "" && len(action.Actions) == 0 {
			events = append(events, newEvent("final", "Final", action.Final, "", "done"))
			return RunResult{Text: action.Final, Events: events}
		}

		if len(action.Actions) == 0 {
			text := firstNonEmpty(action.Summary, "I need more direction before taking action.")
			events = append(events, newEvent("final", "Final", text, "", "done"))
			return RunResult{Text: text, Events: events}
		}

		for _, item := range action.Actions {
			call := tools.Call{Name: item.Name, Arguments: item.Arguments}
			events = append(events, newEvent("tool_call", call.Name, marshalArgs(call.Arguments), call.Name, "pending"))
			if r.Tools.RequiresApproval(call.Name) {
				reason := firstNonEmpty(action.Summary, "This action can change the workspace or run a command.")
				events = append(events, newEvent("approval_request", "Approval required: "+call.Name, reason, call.Name, "pending"))
				return RunResult{Text: approvalText(call, reason), Events: events, Pending: &PendingApproval{Call: call, Reason: reason}}
			}
			result := r.Tools.Execute(ctx, call)
			events = append(events, toolResultEvent(result))
			observations = append(observations, formatToolObservation(result))
		}
	}

	text := "Agent paused after several tool rounds. Review the timeline and run `/run` to continue."
	events = append(events, newEvent("final", "Paused", text, "", "paused"))
	return RunResult{Text: text, Events: events}
}

// ExecuteApproved runs a previously approved tool call.
func (r Runner) ExecuteApproved(ctx context.Context, pending PendingApproval) history.Event {
	return toolResultEvent(r.Tools.Execute(ctx, pending.Call))
}

func (r Runner) systemPrompt(session history.Session, observations []string) string {
	var b strings.Builder
	b.WriteString(reasoning.SystemPrompt(r.Config.Mode))
	b.WriteString("\n\nYou are now operating as Ephemera's local project agent.\n")
	b.WriteString("Expose a concise visible reasoning trace: goal, assumptions, plan, tool rationale, and verification summary. Keep private chain-of-thought private.\n")
	b.WriteString("When action is useful, respond with only JSON in this shape:\n")
	b.WriteString(`{"summary":"brief reason","plan":["step 1","step 2"],"actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}],"final":""}`)
	b.WriteString("\nUse actions only from this catalog:\n")
	for _, tool := range tools.Builtins() {
		fmt.Fprintf(&b, "- %s [%s]: %s\n", tool.Name, tool.Risk, tool.Description)
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Inspect before editing unless the target file and change are already obvious.\n")
	b.WriteString("- For apply_patch, provide complete replacement file content in arguments.content.\n")
	b.WriteString("- Use shell/go_test only when verification is needed.\n")
	b.WriteString("- Always include summary and plan so the CLI can show your reasoning trace.\n")
	b.WriteString("- If done, return JSON with summary, plan, final, and no actions.\n")
	b.WriteString("- If the user asks for normal explanation only, still prefer JSON with final so the reasoning trace is visible.\n")
	fmt.Fprintf(&b, "\nWorkspace root: %s\n", r.Tools.WorkspaceRoot)
	fmt.Fprintf(&b, "Approval policy: %s\n", r.Config.ApprovalPolicy)
	if strings.TrimSpace(r.Config.AutoTestCommand) != "" {
		fmt.Fprintf(&b, "Default test command: %s\n", r.Config.AutoTestCommand)
	}
	if memory := r.projectMemory(); strings.TrimSpace(memory) != "" {
		b.WriteString("\nProject memory and instructions:\n")
		b.WriteString(memory)
		b.WriteString("\n")
	}
	if len(session.Events) > 0 {
		b.WriteString("\nRecent timeline:\n")
		for _, event := range tailEvents(session.Events, 10) {
			fmt.Fprintf(&b, "- %s/%s: %s %s\n", event.Type, event.Status, event.Title, compact(event.Content, 260))
		}
	}
	if len(observations) > 0 {
		b.WriteString("\nTool observations:\n")
		for _, observation := range observations {
			b.WriteString(observation)
			b.WriteString("\n")
		}
	}
	return b.String()
}

type modelAction struct {
	Summary string            `json:"summary"`
	Plan    []string          `json:"plan"`
	Actions []modelToolAction `json:"actions"`
	Final   string            `json:"final"`
}

type modelToolAction struct {
	Tool      string         `json:"tool"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func parseModelAction(text string) (modelAction, bool) {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return modelAction{}, false
	}
	var action modelAction
	dec := json.NewDecoder(strings.NewReader(raw[start : end+1]))
	dec.UseNumber()
	if err := dec.Decode(&action); err != nil {
		return modelAction{}, false
	}
	for i := range action.Actions {
		if action.Actions[i].Name == "" {
			action.Actions[i].Name = action.Actions[i].Tool
		}
		if action.Actions[i].Arguments == nil {
			action.Actions[i].Arguments = map[string]any{}
		}
	}
	return action, true
}

func actionEvents(action modelAction) []history.Event {
	var events []history.Event
	if strings.TrimSpace(action.Summary) != "" {
		events = append(events, newEvent("reasoning_summary", "Reasoning", action.Summary, "", "done"))
	}
	if len(action.Plan) > 0 {
		events = append(events, newEvent("plan_update", "Plan", strings.Join(action.Plan, "\n"), "", "done"))
	}
	return events
}

func conversationMessages(messages []history.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" || message.Role == "assistant" {
			out = append(out, llm.Message{Role: message.Role, Content: message.Content})
		}
	}
	return out
}

func toolResultEvent(result tools.Result) history.Event {
	status := "done"
	content := result.Output
	if !result.OK {
		status = "error"
		content = firstNonEmpty(result.Error, result.Output)
	}
	return newEvent("tool_result", result.Tool, content, result.Tool, status)
}

func newEvent(kind, title, content, tool, status string) history.Event {
	return history.Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Type:      kind,
		Title:     title,
		Content:   strings.TrimSpace(content),
		Tool:      tool,
		Status:    status,
		CreatedAt: time.Now(),
	}
}

func recentToolObservations(events []history.Event) []string {
	var out []string
	for _, event := range tailEvents(events, 8) {
		if event.Type == "tool_result" {
			out = append(out, fmt.Sprintf("%s: %s", event.Title, compact(event.Content, 1200)))
		}
	}
	return out
}

func tailEvents(events []history.Event, max int) []history.Event {
	if len(events) <= max {
		return events
	}
	return events[len(events)-max:]
}

func formatToolObservation(result tools.Result) string {
	status := "ok"
	content := result.Output
	if !result.OK {
		status = "error"
		content = firstNonEmpty(result.Error, result.Output)
	}
	return fmt.Sprintf("[%s %s]\n%s", result.Tool, status, compact(content, 1600))
}

func approvalText(call tools.Call, reason string) string {
	return fmt.Sprintf("Approval required for `%s`: %s\n\nRun `/approve` to execute it or `/reject` to skip it.", call.Name, reason)
}

func marshalArgs(args map[string]any) string {
	data, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return fmt.Sprint(args)
	}
	return string(data)
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit-3] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
