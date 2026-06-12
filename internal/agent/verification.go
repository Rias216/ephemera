package agent

import (
	"context"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"os"
	"path/filepath"
	"strings"
)

func (r Runner) shouldDeferFinalForVerification(state *runState) bool {
	return r.Config.AgentAutoVerify && state.changed && !state.verified
}

func (r Runner) verifyWorkspace(ctx context.Context, state *runState, iteration int, emitUpdate func(StreamUpdate), emitEvent func(history.Event, int)) (*PendingApproval, []history.Event) {
	state.verificationAttempted = true
	var events []history.Event
	run := func(call tools.Call, purpose string) (*PendingApproval, tools.Result) {
		fingerprint := toolFingerprint(call)
		callEvent := runEvent(state.runID, iteration, "tool_call", call.Name, "automatic verification: "+purpose+"\n\n"+marshalArgs(call.Arguments), call.Name, "running")
		callEvent.Metadata["call_fingerprint"] = fingerprint
		events = append(events, callEvent)
		emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "verifying", Iteration: iteration, Tool: call.Name, Verified: state.verified})
		emitEvent(callEvent, iteration)
		result, pending := r.executeAction(ctx, state, call, purpose, iteration, emitUpdate)
		if pending != nil {
			callEvent.Status = "pending"
			events[len(events)-1] = callEvent
			emitEvent(callEvent, iteration)
			approval := runEvent(state.runID, iteration, history.EventApprovalRequest, "Approval required: "+call.Name, purpose, call.Name, "pending")
			approval.Metadata["call_fingerprint"] = fingerprint
			approval.Metadata["call_event_id"] = callEvent.ID
			events = append(events, approval)
			emitEvent(approval, iteration)
			pending.CallEventID = callEvent.ID
			pending.ApprovalEventID = approval.ID
			return pending, tools.Result{}
		}
		attachCallMetadata(&result, fingerprint)
		callEvent.Status = "done"
		if !result.OK {
			callEvent.Status = "error"
		}
		events[len(events)-1] = callEvent
		emitEvent(callEvent, iteration)
		resultEvent := toolResultEvent(state.runID, iteration, result)
		events = append(events, resultEvent)
		emitEvent(resultEvent, iteration)
		state.observe(call, result)
		return nil, result
	}

	_, statusResult := run(tools.Call{Name: "git_status", Arguments: map[string]any{}}, "capture the exact workspace state")
	_, diffResult := run(tools.Call{Name: "git_diff", Arguments: map[string]any{}}, "review the changes before claiming completion")

	filesReadable := true
	for _, path := range sortedKeys(state.changedPaths) {
		_, readResult := run(tools.Call{Name: "read_file", Arguments: map[string]any{"path": path, "start_line": 1, "end_line": 80}}, "confirm the changed file exists and contains readable content")
		if !readResult.OK {
			filesReadable = false
		}
	}

	testApplicable := r.verificationCommandApplicable()
	testPassed := true
	if testApplicable {
		pending, testResult := run(tools.Call{Name: "go_test", Arguments: map[string]any{}}, "run the configured verification command before finalizing")
		if pending != nil {
			return pending, events
		}
		testPassed = testResult.OK
	}
	// Git evidence is valuable but not mandatory for new/non-git workspaces.
	// Changed-file readback plus an applicable test command form the completion gate.
	state.verified = filesReadable && testPassed && (len(state.changedPaths) > 0 || statusResult.OK || diffResult.OK)
	verification := "Verification completed."
	status := "done"
	if !state.verified {
		verification = "Verification failed or remained incomplete; repair the failure before finalizing."
		status = "error"
	}
	event := runEvent(state.runID, iteration, "verification", "Verification gate", verification, "", status)
	event.Metadata["verified"] = state.verified
	events = append(events, event)
	emitEvent(event, iteration)
	state.observations = append(state.observations, "[verification gate]\n"+verification)
	return nil, events
}

func (r Runner) reviewWorkspace(ctx context.Context, state *runState, iteration int, emitUpdate func(StreamUpdate), emitEvent func(history.Event, int)) []history.Event {
	reviewCall := tools.Call{Name: "delegate", Arguments: map[string]any{
		"role": "review",
		"task": "Review the current workspace changes for correctness, regressions, incomplete requirements, and missing tests. Inspect git diff and relevant files. Return only evidence-backed findings and a clear clean/issues verdict.",
	}}
	reviewEvent := runEvent(state.runID, iteration, "tool_call", "delegate review", formatToolCall(modelToolAction{Name: "delegate", Arguments: reviewCall.Arguments, Purpose: "independent post-change review", ExpectedResult: "a concise clean/issues verdict with evidence"}), "delegate", "running")
	events := []history.Event{reviewEvent}
	emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "independent review", Iteration: iteration, Tool: "delegate", Verified: state.verified})
	emitEvent(reviewEvent, iteration)
	reviewResult := r.runDelegate(ctx, reviewCall)
	attachCallMetadata(&reviewResult, toolFingerprint(reviewCall))
	reviewEvent.Status = "done"
	if !reviewResult.OK {
		reviewEvent.Status = "error"
	}
	events[0] = reviewEvent
	emitEvent(reviewEvent, iteration)
	reviewResultEvent := toolResultEvent(state.runID, iteration, reviewResult)
	events = append(events, reviewResultEvent)
	emitEvent(reviewResultEvent, iteration)
	state.observe(reviewCall, reviewResult)
	state.reviewed = true
	state.observations = append(state.observations, "[review gate]\nUse the independent review result above. Fix any concrete issue before finalizing; otherwise produce the final answer with verification evidence.")
	return events
}

func (r Runner) verificationCommandApplicable() bool {
	command := strings.ToLower(strings.TrimSpace(r.Config.AutoTestCommand))
	if command == "" {
		return false
	}
	markers := []struct {
		prefix string
		file   string
	}{
		{"go test", "go.mod"},
		{"npm test", "package.json"},
		{"npm run", "package.json"},
		{"pnpm ", "package.json"},
		{"yarn ", "package.json"},
		{"cargo test", "Cargo.toml"},
		{"pytest", "pyproject.toml"},
	}
	for _, marker := range markers {
		if strings.HasPrefix(command, marker.prefix) {
			_, err := os.Stat(filepath.Join(r.Tools.WorkspaceRoot, marker.file))
			return err == nil
		}
	}
	return true
}

func (r Runner) finish(finalText string, state *runState, iteration, contextTokens int, raw string, events []history.Event, emitUpdate func(StreamUpdate), emitEvent func(history.Event, int)) RunResult {
	completion := state.completionReport()
	if state.changed && r.Config.AgentAutoVerify && !completion.Passed {
		state.suspended = true
		message := strings.TrimSpace(finalText)
		if message != "" {
			message += "\n\n"
		}
		message += completion.Summary()
		event := runEvent(state.runID, iteration, "verification", "Completion gate blocked", completion.Summary(), "", "blocked")
		event.Metadata["verified"] = state.verified
		event.Metadata["pending_checks"] = append([]string(nil), completion.PendingChecks...)
		event.Metadata["blockers"] = append([]string(nil), completion.Blockers...)
		event.Metadata["evidence"] = append([]string(nil), completion.Evidence...)
		event.Metadata["snapshot_path"] = snapshotPath(state.snapshot)
		events = append(events, event)
		emitEvent(event, iteration)
		result := RunResult{Text: message, Events: events, Usage: state.usage, Completion: &completion}
		emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "completion blocked", Iteration: iteration, Text: message, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(raw), Verified: false})
		return result
	}
	state.completed = true
	r.learnFromRun(finalText, state)
	status := completionStatus(state)
	event := runEvent(state.runID, iteration, "final", "Final", finalText, "", status)
	event.Metadata["verified"] = state.verified
	event.Metadata["changed"] = state.changed
	event.Metadata["changed_paths"] = sortedKeys(state.changedPaths)
	event.Metadata["completion_gate"] = completion
	events = append(events, event)
	emitEvent(event, iteration)
	result := RunResult{Text: finalText, Events: events, Usage: state.usage, Completion: &completion}
	emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "complete", Iteration: iteration, Text: result.Text, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(raw), Verified: state.verified})
	return result
}

func ptrCompletion(report CompletionGateReport) *CompletionGateReport { return &report }

func completionStatus(state *runState) string {
	if state.changed && !state.verified {
		return "unverified"
	}
	return "done"
}
