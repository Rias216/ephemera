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

// StreamKind identifies one live agent update sent to the TUI.
type StreamKind string

const (
	StreamStatus StreamKind = "status"
	StreamDelta  StreamKind = "delta"
	StreamEvent  StreamKind = "event"
	StreamDone   StreamKind = "done"
)

// StreamUpdate is a provider-neutral live agent update. Delta contains only
// visible model output; private thinking blocks are never forwarded.
type StreamUpdate struct {
	Kind            StreamKind
	Phase           string
	Iteration       int
	Delta           string
	Event           *history.Event
	Text            string
	Pending         *PendingApproval
	Err             error
	ContextTokens   int
	OutputTokens    int
	SentMessages    int
	TotalMessages   int
	DroppedMessages int
	Tool            string
	StartedAt       time.Time
}

// StreamFunc consumes live updates. It should return quickly.
type StreamFunc func(StreamUpdate)

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
	return r.run(ctx, session, nil)
}

// RunStream advances the agent while publishing provider deltas, structured
// reasoning summaries, plans, tool calls, tool results, and completion state.
func (r Runner) RunStream(ctx context.Context, session history.Session, emit StreamFunc) RunResult {
	return r.run(ctx, session, emit)
}

func (r Runner) run(ctx context.Context, session history.Session, emit StreamFunc) RunResult {
	started := time.Now()
	emitUpdate := func(update StreamUpdate) {
		if emit == nil {
			return
		}
		if update.StartedAt.IsZero() {
			update.StartedAt = started
		}
		emit(update)
	}
	emitEvent := func(event history.Event, iteration int) {
		e := event
		emitUpdate(StreamUpdate{Kind: StreamEvent, Event: &e, Iteration: iteration})
	}

	var events []history.Event
	observations := recentToolObservations(session.Events)
	for iteration := 0; iteration < maxAgentIterations; iteration++ {
		systemPrompt := r.systemPrompt(session, observations)
		messages, selection := selectAgentMessages(session.Messages, systemPrompt, r.Config.ContextTokens)
		request := llm.Request{
			Model:       r.Config.Model(),
			System:      systemPrompt,
			Messages:    messages,
			MaxTokens:   r.Config.MaxTokens,
			Temperature: r.Config.Mode.Temperature(),
		}
		contextTokens := estimateRequestTokens(request)
		emitUpdate(StreamUpdate{
			Kind:            StreamStatus,
			Phase:           "requesting model",
			Iteration:       iteration + 1,
			ContextTokens:   contextTokens,
			SentMessages:    selection.Sent,
			TotalMessages:   selection.Total,
			DroppedMessages: selection.Dropped,
		})

		outputRunes := 0
		text, err := llm.GenerateStreaming(ctx, r.Provider, request, func(delta string) error {
			outputRunes += len([]rune(delta))
			emitUpdate(StreamUpdate{
				Kind:          StreamDelta,
				Phase:         "receiving model",
				Iteration:     iteration + 1,
				Delta:         delta,
				ContextTokens: contextTokens,
				OutputTokens:  (outputRunes + 3) / 4,
			})
			return nil
		})
		if err != nil {
			event := newEvent("tool_result", "Agent request failed", err.Error(), "", "error")
			events = append(events, event)
			emitEvent(event, iteration+1)
			result := RunResult{Text: "Agent request failed: " + err.Error(), Events: events}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "failed", Iteration: iteration + 1, Text: result.Text, Err: err, ContextTokens: contextTokens, OutputTokens: (outputRunes + 3) / 4})
			return result
		}

		emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "parsing decision", Iteration: iteration + 1, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
		action, ok := parseModelAction(text)
		if !ok {
			event := newEvent("final", "Final", text, "", "done")
			events = append(events, event)
			emitEvent(event, iteration+1)
			result := RunResult{Text: text, Events: events}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "complete", Iteration: iteration + 1, Text: result.Text, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
			return result
		}

		for _, event := range actionEvents(action) {
			events = append(events, event)
			emitEvent(event, iteration+1)
		}
		if strings.TrimSpace(action.Final) != "" && len(action.Actions) == 0 {
			event := newEvent("final", "Final", action.Final, "", "done")
			events = append(events, event)
			emitEvent(event, iteration+1)
			result := RunResult{Text: action.Final, Events: events}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "complete", Iteration: iteration + 1, Text: result.Text, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
			return result
		}

		if len(action.Actions) == 0 {
			finalText := firstNonEmpty(action.Summary, "I need more direction before taking action.")
			event := newEvent("final", "Final", finalText, "", "done")
			events = append(events, event)
			emitEvent(event, iteration+1)
			result := RunResult{Text: finalText, Events: events}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "complete", Iteration: iteration + 1, Text: result.Text, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
			return result
		}

		for _, item := range action.Actions {
			call := tools.Call{Name: item.Name, Arguments: item.Arguments}
			callEvent := newEvent("tool_call", call.Name, marshalArgs(call.Arguments), call.Name, "running")
			callIndex := len(events)
			events = append(events, callEvent)
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "running tool", Iteration: iteration + 1, Tool: call.Name, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
			emitEvent(callEvent, iteration+1)
			if r.Tools.RequiresApproval(call.Name) {
				callEvent.Status = "pending"
				events[callIndex] = callEvent
				emitEvent(callEvent, iteration+1)
				reason := firstNonEmpty(action.Summary, "This action can change the workspace or run a command.")
				approval := newEvent("approval_request", "Approval required: "+call.Name, reason, call.Name, "pending")
				events = append(events, approval)
				emitEvent(approval, iteration+1)
				pending := &PendingApproval{Call: call, Reason: reason}
				result := RunResult{Text: approvalText(call, reason), Events: events, Pending: pending}
				emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "awaiting approval", Iteration: iteration + 1, Text: result.Text, Pending: pending, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Tool: call.Name})
				return result
			}
			result := r.Tools.Execute(ctx, call)
			callEvent.Status = "done"
			if !result.OK {
				callEvent.Status = "error"
			}
			events[callIndex] = callEvent
			emitEvent(callEvent, iteration+1)
			resultEvent := toolResultEvent(result)
			events = append(events, resultEvent)
			emitEvent(resultEvent, iteration+1)
			observations = append(observations, formatToolObservation(result))
		}
		emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "continuing agent", Iteration: iteration + 1, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text)})
	}

	text := "Agent paused after several tool rounds. Review the timeline and run `/run` to continue."
	event := newEvent("final", "Paused", text, "", "paused")
	events = append(events, event)
	emitEvent(event, maxAgentIterations)
	result := RunResult{Text: text, Events: events}
	emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "paused", Iteration: maxAgentIterations, Text: text})
	return result
}

type messageSelection struct {
	Sent    int
	Total   int
	Dropped int
}

func selectAgentMessages(messages []history.Message, system string, budget int) ([]llm.Message, messageSelection) {
	valid := conversationMessages(messages)
	if budget <= 0 {
		budget = 16_000
	}
	used := estimateVisibleTokens(system) + 4
	selected := make([]llm.Message, 0, len(valid))
	for index := len(valid) - 1; index >= 0; index-- {
		cost := estimateVisibleTokens(valid[index].Role) + estimateVisibleTokens(valid[index].Content) + 4
		if used+cost > budget && len(selected) > 0 {
			break
		}
		selected = append(selected, valid[index])
		used += cost
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	for len(selected) > 0 && selected[0].Role == "assistant" {
		selected = selected[1:]
	}
	return selected, messageSelection{
		Sent:    len(selected),
		Total:   len(valid),
		Dropped: len(valid) - len(selected),
	}
}

func estimateRequestTokens(req llm.Request) int {
	total := estimateVisibleTokens(req.System) + 4
	for _, message := range req.Messages {
		total += estimateVisibleTokens(message.Role) + estimateVisibleTokens(message.Content) + 4
	}
	return total
}

func estimateVisibleTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return maxInt(1, (len([]rune(text))+3)/4)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ExecuteApproved runs a previously approved tool call.
func (r Runner) ExecuteApproved(ctx context.Context, pending PendingApproval) history.Event {
	return toolResultEvent(r.Tools.Execute(ctx, pending.Call))
}

func (r Runner) systemPrompt(session history.Session, observations []string) string {
	var b strings.Builder
	b.WriteString(reasoning.SystemPrompt(r.Config.Mode))
	b.WriteString("\n\nYou are now operating as Ephemera's local project agent.\n")
	b.WriteString("Expose a concise visible decision trace for the UI, never private chain-of-thought. Include the goal, material assumptions, approach, why each tool is useful, and how the result will be verified.\n")
	b.WriteString("When action is useful, respond with only JSON in this shape:\n")
	b.WriteString(`{"reasoning":{"goal":"what success means","assumptions":["only material assumptions"],"approach":["short step"],"tool_rationale":"why these tools are needed","verification":"how success will be checked"},"summary":"brief decision summary","plan":["step 1","step 2"],"actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}],"final":""}`)
	b.WriteString("\nUse actions only from this catalog:\n")
	for _, tool := range tools.Builtins() {
		fmt.Fprintf(&b, "- %s [%s]: %s\n", tool.Name, tool.Risk, tool.Description)
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Inspect before editing unless the target file and change are already obvious.\n")
	b.WriteString("- For apply_patch, provide complete replacement file content in arguments.content.\n")
	b.WriteString("- Use shell/go_test only when verification is needed.\n")
	b.WriteString("- Always include reasoning, summary, and plan so the CLI can show Beneath the Surface. Keep it concise and outcome-focused.\n")
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
	Reasoning modelReasoning    `json:"reasoning"`
	Summary   string            `json:"summary"`
	Plan      []string          `json:"plan"`
	Actions   []modelToolAction `json:"actions"`
	Final     string            `json:"final"`
}

type modelReasoning struct {
	Goal          reasoningText  `json:"goal"`
	Assumptions   reasoningItems `json:"assumptions"`
	Approach      reasoningItems `json:"approach"`
	ToolRationale reasoningText  `json:"tool_rationale"`
	Verification  reasoningText  `json:"verification"`
}

type reasoningText string

type reasoningItems []string

func (value *reasoningText) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*value = reasoningText(strings.TrimSpace(text))
		return nil
	}
	var items []string
	if err := json.Unmarshal(data, &items); err == nil {
		*value = reasoningText(strings.Join(compactReasoningItems(reasoningItems(items)), "; "))
		return nil
	}
	if string(data) == "null" {
		*value = ""
		return nil
	}
	return fmt.Errorf("reasoning field must be text or a text list")
}

func (values *reasoningItems) UnmarshalJSON(data []byte) error {
	var items []string
	if err := json.Unmarshal(data, &items); err == nil {
		*values = reasoningItems(items)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			*values = nil
		} else {
			*values = reasoningItems{text}
		}
		return nil
	}
	if string(data) == "null" {
		*values = nil
		return nil
	}
	return fmt.Errorf("reasoning list must be text or a text list")
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
	if trace := formatReasoningTrace(action.Reasoning); trace != "" {
		events = append(events, newEvent("reasoning_trace", "Beneath the Surface", trace, "", "done"))
	} else if strings.TrimSpace(action.Summary) != "" {
		// Older or less capable providers may omit the structured reasoning object.
		// Keep the visible summary rather than hiding all decision context.
		events = append(events, newEvent("reasoning_summary", "Beneath the Surface", action.Summary, "", "done"))
	}
	if len(action.Plan) > 0 {
		events = append(events, newEvent("plan_update", "Plan", strings.Join(action.Plan, "\n"), "", "done"))
	}
	return events
}

func formatReasoningTrace(trace modelReasoning) string {
	var sections []string
	if value := strings.TrimSpace(string(trace.Goal)); value != "" {
		sections = append(sections, "**Goal**\n"+value)
	}
	if values := compactReasoningItems(trace.Assumptions); len(values) > 0 {
		sections = append(sections, "**Assumptions**\n- "+strings.Join(values, "\n- "))
	}
	if values := compactReasoningItems(trace.Approach); len(values) > 0 {
		var numbered []string
		for index, value := range values {
			numbered = append(numbered, fmt.Sprintf("%d. %s", index+1, value))
		}
		sections = append(sections, "**Approach**\n"+strings.Join(numbered, "\n"))
	}
	if value := strings.TrimSpace(string(trace.ToolRationale)); value != "" {
		sections = append(sections, "**Tool rationale**\n"+value)
	}
	if value := strings.TrimSpace(string(trace.Verification)); value != "" {
		sections = append(sections, "**Verification**\n"+value)
	}
	return strings.Join(sections, "\n\n")
}

func compactReasoningItems(values reasoningItems) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, compact(value, 360))
	}
	return out
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
