package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func (r Runner) critiqueFinal(ctx context.Context, state *runState, iteration int, finalText string, emitUpdate func(StreamUpdate), emitEvent func(history.Event, int)) (bool, []history.Event) {
	call := tools.Call{Name: "delegate", Arguments: map[string]any{
		"role": "critic",
		"task": fmt.Sprintf("Critique the proposed final answer against the user goal, reasoning summary, tool evidence, verification state, and remaining risks. Identify only material correctness or completeness issues. Start with VERDICT: CLEAN or VERDICT: ISSUES.\n\nPROPOSED FINAL:\n%s\n\nPLAN:\n%s\n\nRECENT EVIDENCE:\n%s", compact(finalText, 2400), compact(state.lastPlan, 1800), compact(strings.Join(tailStrings(state.observations, 8), "\n"), 3600)),
	}}
	callEvent := runEvent(state.runID, iteration, history.EventToolCall, "reasoning critique", "Independent critic reviews the proposed answer and evidence.", "delegate", "running")
	callEvent.Metadata["role"] = "critic"
	events := []history.Event{callEvent}
	emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "critiquing answer", Iteration: iteration, Tool: "delegate", Verified: state.verified})
	emitEvent(callEvent, iteration)
	result := r.runDelegate(ctx, call)
	attachCallMetadata(&result, toolFingerprint(call))
	callEvent.Status = "done"
	if !result.OK {
		callEvent.Status = "error"
	}
	events[0] = callEvent
	emitEvent(callEvent, iteration)
	resultEvent := toolResultEvent(state.runID, iteration, result)
	if resultEvent.Metadata == nil {
		resultEvent.Metadata = map[string]any{}
	}
	resultEvent.Metadata["role"] = "critic"
	events = append(events, resultEvent)
	emitEvent(resultEvent, iteration)
	state.observe(call, result)
	state.critiqued = true
	text := strings.ToUpper(result.Output)
	clean := result.OK && strings.Contains(text, "VERDICT: CLEAN") && !strings.Contains(text, "VERDICT: ISSUES")
	if !clean {
		state.observations = append(state.observations, "[reasoning critique]\nRevise the plan or final answer to address the critic's material findings. Do not repeat the same unsupported completion claim.")
	}
	return clean, events
}
