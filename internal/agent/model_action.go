package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"regexp"
	"strings"
)

var trailingJSONComma = regexp.MustCompile(`,\s*([}\]])`)

type modelAction struct {
	Reasoning  modelReasoning    `json:"reasoning"`
	Summary    string            `json:"summary"`
	Plan       []string          `json:"plan"`
	Actions    []modelToolAction `json:"actions"`
	Completion modelCompletion   `json:"completion"`
	Final      string            `json:"final"`
}

type modelReasoning struct {
	Goal          reasoningText  `json:"goal"`
	CurrentState  reasoningText  `json:"current_state"`
	Assumptions   reasoningItems `json:"assumptions"`
	Approach      reasoningItems `json:"approach"`
	Evidence      reasoningItems `json:"evidence"`
	Risks         reasoningItems `json:"risks"`
	ToolRationale reasoningText  `json:"tool_rationale"`
	Verification  reasoningText  `json:"verification"`
	NextStep      reasoningText  `json:"next_step"`
}

type modelCompletion struct {
	Verified       bool           `json:"verified"`
	Evidence       reasoningItems `json:"evidence"`
	RemainingRisks reasoningItems `json:"remaining_risks"`
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
	ID             string         `json:"id,omitempty"`
	Tool           string         `json:"tool"`
	Name           string         `json:"name"`
	Arguments      map[string]any `json:"arguments"`
	Purpose        string         `json:"purpose"`
	ExpectedResult string         `json:"expected_result"`
	DependsOn      []string       `json:"depends_on,omitempty"`
	ProviderCallID string         `json:"-"`
}

func (r Runner) actionFromDecision(decision llm.ToolDecision) (modelAction, bool, bool, string) {
	if len(decision.ToolCalls) > 0 {
		action := actionFromNativeToolCalls(decision)
		if len(action.Actions) > 8 {
			return modelAction{}, false, false, fmt.Sprintf("provider requested %d tool calls; maximum is 8", len(action.Actions))
		}
		seen := map[string]bool{}
		seenIDs := map[string]bool{}
		for _, item := range action.Actions {
			fingerprint := toolFingerprint(tools.Call{Name: item.Name, Arguments: item.Arguments})
			if seen[fingerprint] {
				return modelAction{}, false, false, "provider repeated an identical tool call in one batch"
			}
			seen[fingerprint] = true
			if item.ProviderCallID != "" {
				if seenIDs[item.ProviderCallID] {
					return modelAction{}, false, false, "provider repeated a tool call id"
				}
				seenIDs[item.ProviderCallID] = true
			}
		}
		return action, true, false, ""
	}
	action, ok, repaired, parseErr := parseModelActionDetailed(decision.Text)
	return action, ok, repaired, parseErr
}

func actionFromNativeToolCalls(decision llm.ToolDecision) modelAction {
	portable := decision.Transport == llm.ToolTransportPortable
	transportLabel := "provider-native"
	toolRationale := "The provider emitted typed tool calls through the native tool interface."
	if portable {
		transportLabel = "universal gateway"
		toolRationale = "Ephemera recovered the requested capability through its provider-neutral tool gateway."
	}
	summary := firstNonEmpty(decision.Text, fmt.Sprintf("%s requested %d tool call(s).", transportLabel, len(decision.ToolCalls)))
	action := modelAction{
		Reasoning: modelReasoning{
			Goal:          reasoningText("Execute validated tool calls and feed the observed evidence back into the agent loop."),
			CurrentState:  reasoningText(summary),
			ToolRationale: reasoningText(toolRationale),
			Verification:  reasoningText("Validate each tool call locally, apply the configured approval policy, and observe the normalized result."),
			NextStep:      reasoningText("Run the requested tool calls."),
		},
		Summary: summary,
		Plan:    []string{"Run requested tool call(s)", "Observe results", "Continue or finalize with evidence"},
		Actions: make([]modelToolAction, 0, len(decision.ToolCalls)),
	}
	for index, call := range decision.ToolCalls {
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			callID = fmt.Sprintf("ephemera_call_%d_%s", index+1, strings.ReplaceAll(call.Name, "-", "_"))
		}
		providerCallID := callID
		if portable {
			providerCallID = ""
		}
		action.Actions = append(action.Actions, modelToolAction{
			ID:             callID,
			Tool:           call.Name,
			Name:           call.Name,
			Arguments:      args,
			Purpose:        "Validated tool call",
			ExpectedResult: "Normalized local tool result",
			ProviderCallID: providerCallID,
		})
	}
	return action
}

func parseModelAction(text string) (modelAction, bool) {
	action, ok, _, _ := parseModelActionDetailed(text)
	return action, ok
}

func parseModelActionDetailed(text string) (modelAction, bool, bool, string) {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return modelAction{}, false, false, "no JSON object found"
	}
	candidate := raw[start : end+1]
	action, err := decodeModelAction(candidate)
	if err == nil {
		return action, true, false, ""
	}
	repaired := trailingJSONComma.ReplaceAllString(candidate, "$1")
	if repaired != candidate {
		action, repairedErr := decodeModelAction(repaired)
		if repairedErr == nil {
			return action, true, true, ""
		}
		err = repairedErr
	}
	return modelAction{}, false, false, err.Error()
}

func decodeModelAction(raw string) (modelAction, error) {
	var action modelAction
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&action); err != nil {
		return modelAction{}, err
	}
	if len(action.Actions) > 8 {
		return modelAction{}, fmt.Errorf("agent decision contains %d actions; maximum is 8", len(action.Actions))
	}
	if len(action.Actions) > 0 && strings.TrimSpace(action.Final) != "" {
		return modelAction{}, fmt.Errorf("agent decision cannot contain both actions and final")
	}
	seen := map[string]bool{}
	seenProviderIDs := map[string]bool{}
	for i := range action.Actions {
		action.Actions[i].ID = strings.TrimSpace(action.Actions[i].ID)
		if action.Actions[i].ID == "" {
			action.Actions[i].ID = fmt.Sprintf("step-%d", i+1)
		}
		action.Actions[i].Tool = strings.TrimSpace(action.Actions[i].Tool)
		action.Actions[i].Name = strings.TrimSpace(action.Actions[i].Name)
		if action.Actions[i].Name == "" {
			action.Actions[i].Name = action.Actions[i].Tool
		}
		if action.Actions[i].Tool == "" {
			action.Actions[i].Tool = action.Actions[i].Name
		}
		if action.Actions[i].Name == "" {
			return modelAction{}, fmt.Errorf("action %d has no tool name", i+1)
		}
		if action.Actions[i].Arguments == nil {
			action.Actions[i].Arguments = map[string]any{}
		}
		fingerprint := toolFingerprint(tools.Call{Name: action.Actions[i].Name, Arguments: action.Actions[i].Arguments})
		if seen[fingerprint] {
			return modelAction{}, fmt.Errorf("agent decision repeats the same tool call")
		}
		seen[fingerprint] = true
		for _, dependency := range action.Actions[i].DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" || dependency == action.Actions[i].ID {
				return modelAction{}, fmt.Errorf("action %d has an invalid dependency", i+1)
			}
		}
		if id := strings.TrimSpace(action.Actions[i].ProviderCallID); id != "" {
			if seenProviderIDs[id] {
				return modelAction{}, fmt.Errorf("agent decision repeats provider call id %q", id)
			}
			seenProviderIDs[id] = true
		}
	}
	if len(action.Actions) == 0 && strings.TrimSpace(action.Final) == "" && strings.TrimSpace(action.Summary) == "" {
		return modelAction{}, fmt.Errorf("agent decision contains no action, summary, or final answer")
	}
	return action, nil
}

func looksLikeAgentDecision(text string) bool {
	raw := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(raw, "{") ||
		strings.HasPrefix(raw, "```json") ||
		strings.Contains(raw, `"actions"`) ||
		strings.Contains(raw, `"reasoning"`) ||
		strings.Contains(raw, `"final"`)
}

var unfinishedActionLeadPattern = regexp.MustCompile(`(?i)\b(?:i(?:'ll| will| am going to| need to| should)|let me|first,?\s+i(?:'ll| will)|next,?\s+i(?:'ll| will))\b`)
var unfinishedActionVerbPattern = regexp.MustCompile(`(?i)\b(?:inspect|read|open|check|search|find|run|execute|edit|modify|patch|create|write|delete|remove|test|build|compile|browse|look\s+at)\b`)

func looksLikeUnfinishedActionNarration(text string) bool {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return false
	}
	return unfinishedActionLeadPattern.MatchString(raw) && unfinishedActionVerbPattern.MatchString(raw)
}

func modelActionFingerprint(action modelAction) string {
	if len(action.Actions) == 0 {
		return ""
	}
	data, err := json.Marshal(action.Actions)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:12])
}

func actionEvents(runID string, iteration int, action modelAction) []history.Event {
	var events []history.Event
	if structured := action.toTrace(); !structured.Empty() {
		event := runEvent(runID, iteration, history.EventReasoningTrace, "Beneath the Surface", formatAgentTrace(structured), "", "done")
		event.Metadata["trace"] = structured
		events = append(events, event)
	} else if strings.TrimSpace(action.Summary) != "" {
		events = append(events, runEvent(runID, iteration, history.EventReasoningSummary, "Beneath the Surface", action.Summary, "", "done"))
	}
	return events
}

func formatReasoningTrace(trace modelReasoning) string {
	return formatAgentTrace(trace.toTrace())
}

func formatAgentTrace(trace history.AgentTrace) string {
	var sections []string
	appendText := func(label, value string) {
		if text := strings.TrimSpace(value); text != "" {
			sections = append(sections, "**"+label+"**\n"+compact(text, 700))
		}
	}
	appendList := func(label string, values []string) {
		if items := compactTraceItems(values); len(items) > 0 {
			sections = append(sections, "**"+label+"**\n- "+strings.Join(items, "\n- "))
		}
	}
	appendText("Goal", trace.Goal)
	appendText("Current state", trace.CurrentState)
	appendList("Assumptions", trace.Assumptions)
	if values := compactTraceItems(trace.Approach); len(values) > 0 {
		var numbered []string
		for index, value := range values {
			numbered = append(numbered, fmt.Sprintf("%d. %s", index+1, value))
		}
		sections = append(sections, "**Approach**\n"+strings.Join(numbered, "\n"))
	}
	appendList("Evidence", trace.Evidence)
	appendList("Risks", trace.Risks)
	appendText("Tool rationale", trace.ToolRationale)
	appendText("Verification", trace.Verification)
	appendText("Next step", trace.NextStep)
	return strings.Join(sections, "\n\n")
}

func (action modelAction) toTrace() history.AgentTrace {
	trace := action.Reasoning.toTrace()
	if trace.Goal == "" {
		trace.Goal = firstNonEmpty(action.Summary, action.Final, "Complete the current user request.")
	}
	if trace.CurrentState == "" && strings.TrimSpace(action.Summary) != "" {
		trace.CurrentState = action.Summary
	}
	if len(trace.Approach) == 0 {
		trace.Approach = compactTraceItems(action.Plan)
	}
	if len(trace.Approach) == 0 && len(action.Actions) > 0 {
		for _, item := range action.Actions {
			trace.Approach = append(trace.Approach, compact(firstNonEmpty(item.Purpose, item.ExpectedResult, "Run "+firstNonEmpty(item.Name, item.Tool)), 420))
		}
	}
	if len(trace.Evidence) == 0 {
		trace.Evidence = compactReasoningItems(action.Completion.Evidence)
	}
	if len(trace.Risks) == 0 {
		trace.Risks = compactReasoningItems(action.Completion.RemainingRisks)
	}
	if trace.ToolRationale == "" && len(action.Actions) > 0 {
		trace.ToolRationale = actionToolRationale(action.Actions)
	}
	if trace.Verification == "" {
		trace.Verification = actionVerification(action)
	}
	if trace.NextStep == "" {
		trace.NextStep = actionNextStep(action)
	}
	return trace
}

func actionToolRationale(actions []modelToolAction) string {
	var parts []string
	for _, item := range actions {
		name := firstNonEmpty(item.Name, item.Tool, "tool")
		purpose := firstNonEmpty(item.Purpose, item.ExpectedResult)
		if purpose == "" {
			parts = append(parts, name)
		} else {
			parts = append(parts, name+": "+purpose)
		}
	}
	return compact(strings.Join(parts, "; "), 700)
}

func actionVerification(action modelAction) string {
	if action.Completion.Verified {
		return "Model marked completion verified; local gates still require tool evidence for workspace changes."
	}
	if len(action.Completion.Evidence) > 0 {
		return "Check completion evidence: " + strings.Join(compactReasoningItems(action.Completion.Evidence), "; ")
	}
	for _, item := range action.Actions {
		if strings.TrimSpace(item.ExpectedResult) != "" {
			return item.ExpectedResult
		}
	}
	if strings.TrimSpace(action.Final) != "" {
		return "Final answer only; no tool action requested."
	}
	return ""
}

func actionNextStep(action modelAction) string {
	if len(action.Actions) > 0 {
		var names []string
		for _, item := range action.Actions {
			names = append(names, firstNonEmpty(item.Name, item.Tool, "tool"))
		}
		return "Run " + strings.Join(names, ", ")
	}
	if strings.TrimSpace(action.Final) != "" {
		return "Return final answer."
	}
	return ""
}

func (trace modelReasoning) toTrace() history.AgentTrace {
	return history.AgentTrace{
		Goal:          strings.TrimSpace(string(trace.Goal)),
		CurrentState:  strings.TrimSpace(string(trace.CurrentState)),
		Assumptions:   compactReasoningItems(trace.Assumptions),
		Approach:      compactReasoningItems(trace.Approach),
		Evidence:      compactReasoningItems(trace.Evidence),
		Risks:         compactReasoningItems(trace.Risks),
		ToolRationale: strings.TrimSpace(string(trace.ToolRationale)),
		Verification:  strings.TrimSpace(string(trace.Verification)),
		NextStep:      strings.TrimSpace(string(trace.NextStep)),
	}
}

func reasoningStepFromAction(iteration int, action modelAction) reasoning.ReasoningStep {
	trace := action.toTrace()
	return reasoning.ReasoningStep{
		Iteration:     iteration,
		Goal:          trace.Goal,
		CurrentState:  trace.CurrentState,
		Assumptions:   append([]string(nil), trace.Assumptions...),
		Approach:      append([]string(nil), trace.Approach...),
		Evidence:      append([]string(nil), trace.Evidence...),
		Risks:         append([]string(nil), trace.Risks...),
		ToolRationale: trace.ToolRationale,
		Verification:  trace.Verification,
		NextStep:      trace.NextStep,
	}
}

func toolGraphFromAction(r Runner, action modelAction) reasoning.ToolGraph {
	graph := reasoning.ToolGraph{Calls: make([]reasoning.ToolNode, 0, len(action.Actions))}
	paths := map[string]bool{}
	for _, item := range action.Actions {
		name := firstNonEmpty(item.Name, item.Tool)
		path := ""
		if item.Arguments != nil {
			if value, ok := item.Arguments["path"].(string); ok {
				path = normalizePath(value)
				if path != "" {
					paths[path] = true
				}
			}
		}
		graph.Calls = append(graph.Calls, reasoning.ToolNode{
			Name:      firstNonEmpty(item.ID, name),
			DependsOn: append([]string(nil), item.DependsOn...),
			Risk:      string(r.toolRisk(name)),
			Path:      path,
		})
	}
	graph.CrossFileScope = len(paths)
	return graph
}

func reasoningModeRank(mode reasoning.Mode) int {
	switch mode {
	case reasoning.ModeDeep:
		return 3
	case reasoning.ModeNormal, reasoning.ModeCreative:
		return 2
	case reasoning.ModeConcise:
		return 1
	default:
		return 0
	}
}

func compactReasoningItems(values reasoningItems) []string {
	return compactTraceItems([]string(values))
}

func compactTraceItems(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, compact(value, 420))
	}
	return out
}
