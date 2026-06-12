package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	agentmetrics "github.com/ephemera-ai/ephemera/internal/metrics"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
	agenttrace "github.com/ephemera-ai/ephemera/internal/trace"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

func (r Runner) run(ctx context.Context, session history.Session, emit StreamFunc) (finalResult RunResult) {
	defer r.Tools.Close()
	started := time.Now()
	configuredMode := r.Config.Mode
	r.Config.Mode = reasoning.AdaptiveMode(configuredMode, latestUserText(session), r.Config.AgentAdaptiveReasoning)
	var discoveryErrors []error
	if r.MCP != nil && r.MCP.Configured() {
		discoveryErrors = r.discoverMCPTools(ctx)
		defer r.MCP.Close()
	}
	state := r.initialState(session, started)
	providerName := ""
	if r.Provider != nil {
		providerName = r.Provider.Name()
	}
	runScope := debuglog.Scope{
		Session:   session.Name,
		RunID:     state.runID,
		Provider:  providerName,
		Model:     r.Config.Model(),
		Workspace: r.Tools.WorkspaceRoot,
	}
	ctx = debuglog.WithScope(ctx, runScope)
	debuglog.RegisterRunScope(runScope)
	defer debuglog.UnregisterRunScope(state.runID)
	currentIteration := 0
	runMetrics := agentmetrics.Default()
	runMetrics.Inc("agent_runs_total")
	_ = debuglog.WriteCtx(ctx, "info", "agent", "run started", "agent run started", map[string]any{
		"mode":      string(r.Config.Mode),
		"max_steps": r.Config.AgentMaxSteps,
	})
	defer func() {
		duration := time.Since(started)
		runMetrics.Observe("agent_run_duration_seconds", duration.Seconds())
		runMetrics.Add("agent_tokens_input_total", float64(finalResult.Usage.InputTokens))
		runMetrics.Add("agent_tokens_output_total", float64(finalResult.Usage.OutputTokens))
		runMetrics.Add("agent_tool_calls_total", float64(finalResult.Usage.ToolCalls))
		runMetrics.Set("agent_last_run_verified", boolMetric(state.verified))
		_ = runMetrics.WriteJSON(filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "metrics.json"))
		_, traceErr := agenttrace.Write(r.Tools.WorkspaceRoot, agenttrace.Run{
			ID:        state.runID,
			StartedAt: started,
			Duration:  duration,
			Provider:  providerName,
			Model:     r.Config.Model(),
			Mode:      r.Config.Mode,
			Verified:  state.verified,
			Usage: agenttrace.Usage{
				InputTokens: finalResult.Usage.InputTokens, OutputTokens: finalResult.Usage.OutputTokens, ToolCalls: finalResult.Usage.ToolCalls,
			},
			Reasoning: append([]reasoning.ReasoningStep(nil), state.reasoningTrace...),
			Events:    append([]history.Event(nil), finalResult.Events...),
			FinalText: finalResult.Text,
		})
		if traceErr != nil {
			debuglog.ErrorCtx(ctx, "trace", "write run trace", traceErr, map[string]any{"run_id": state.runID})
		}
		status := "stopped"
		if state.completed {
			status = "completed"
		} else if state.suspended {
			status = "suspended"
		} else if ctx.Err() != nil {
			status = "cancelled"
		}
		_ = debuglog.WriteCtx(ctx, "info", "agent", "run finished", "agent run finished", map[string]any{
			"status":        status,
			"duration_ms":   duration.Milliseconds(),
			"verified":      state.verified,
			"changed":       state.changed,
			"input_tokens":  finalResult.Usage.InputTokens,
			"output_tokens": finalResult.Usage.OutputTokens,
			"tool_calls":    finalResult.Usage.ToolCalls,
		})
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			stack := debug.Stack()
			crashPath, _ := debuglog.RecordCrash(ctx, "agent.run", recovered, stack, map[string]any{
				"mode": string(r.Config.Mode), "max_steps": r.Config.AgentMaxSteps, "elapsed_ms": time.Since(started).Milliseconds(),
			})
			message := fmt.Sprintf("Agent recovered from an internal error: %v", recovered)
			if strings.TrimSpace(crashPath) != "" {
				message += "\nCrash report: " + crashPath
			}
			state.suspended = true
			event := runEvent(state.runID, maxInt(1, currentIteration), "recovery", "Agent panic recovered", message, "", "error")
			event.Metadata["crash_report"] = crashPath
			finalResult.Text = message
			finalResult.Events = append(finalResult.Events, event)
			finalResult.Usage = state.usage
			if emit != nil {
				func() {
					defer func() { _ = recover() }()
					emit(StreamUpdate{Kind: StreamEvent, RunID: state.runID, Phase: "panic recovered", Iteration: maxInt(1, currentIteration), Event: &event, StartedAt: started, Verified: state.verified})
					emit(StreamUpdate{Kind: StreamDone, RunID: state.runID, Phase: "failed", Iteration: maxInt(1, currentIteration), Text: message, Err: fmt.Errorf("%v", recovered), StartedAt: started, Verified: state.verified})
				}()
			}
		}
	}()
	director, directorErr := newDirectorSession(r)
	if directorErr != nil {
		state.observations = append(state.observations, "[instrument unavailable]\nDirector mode will continue without instrument review: "+directorErr.Error())
	}
	if command := state.projectManifest.PrimaryTestCommand(); command != "" && (state.projectManifestSource == "file" || !r.verificationCommandApplicable()) {
		r.Config.AutoTestCommand = command
		r.Tools.AutoTestCommand = command
	}
	for _, err := range discoveryErrors {
		state.observations = append(state.observations, mcpDiscoveryObservation(err))
	}
	maxSteps := r.Config.AgentMaxSteps
	if r.Config.DirectorEnabled && r.delegationDepth == 0 {
		maxSteps = r.Config.DirectorMaxSteps
	}
	if maxSteps < 2 {
		maxSteps = 10
	}
	if r.delegationDepth > 0 && maxSteps > 8 {
		maxSteps = 8
	}

	emitUpdate := func(update StreamUpdate) {
		if emit == nil {
			return
		}
		update.RunID = state.runID
		if update.StartedAt.IsZero() {
			update.StartedAt = started
		}
		emit(update)
	}
	emitEvent := func(event history.Event, iteration int) {
		e := event
		emitUpdate(StreamUpdate{Kind: StreamEvent, Event: &e, Iteration: iteration, Verified: state.verified})
	}

	var events []history.Event
	guardRequiredToolEvidence := func(iteration int, proposed string) (bool, *RunResult) {
		pending := state.intent.pendingEvidence(state)
		if len(pending) == 0 {
			return false, nil
		}
		detail := "Missing required evidence: " + strings.Join(pending, "; ")
		if state.toolUseReprompts < 2 && iteration < maxSteps {
			state.toolUseReprompts++
			state.portableTools = true
			rememberPortableTools(r.Provider, r.Config.Model())
			state.observations = append(state.observations,
				"[execution contract]\nThe user requested workspace work, but the provider proposed completion before the required tools succeeded. Use the universal tool gateway now. "+detail+". Do not answer with prose-only completion.",
			)
			event := runEvent(state.runID, iteration, history.EventDecision, "Required tool action missing", detail, "", "recovered")
			event.Metadata["portable_tools"] = true
			event.Metadata["reprompt"] = state.toolUseReprompts
			events = append(events, event)
			emitEvent(event, iteration)
			debuglog.WarningCtx(ctx, "agent", "premature completion blocked", detail, map[string]any{
				"provider": r.Provider.Name(), "model": r.Config.Model(), "iteration": iteration,
				"proposed": compact(proposed, 500), "reprompt": state.toolUseReprompts,
			})
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "requiring workspace tools", Iteration: iteration, Delta: detail, Verified: state.verified})
			return true, nil
		}
		message := "Agent stopped without claiming completion because the provider did not execute the workspace actions required by the request. " + detail + "."
		event := runEvent(state.runID, iteration, "recovery", "Premature completion stopped", message, "", "error")
		events = append(events, event)
		emitEvent(event, iteration)
		result := &RunResult{Text: message, Events: events, Usage: state.usage, Completion: ptrCompletion(state.completionReport())}
		emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "missing required tool evidence", Iteration: iteration, Text: message, Verified: state.verified})
		return false, result
	}
	reviewDirectorFinal := func(iteration int, proposed string) bool {
		if director == nil || strings.TrimSpace(proposed) == "" {
			return false
		}
		review, reviewErr := director.review(ctx, state, iteration, "final", proposed, emitUpdate)
		reviewEvent := director.event(state.runID, iteration, "final", review, reviewErr)
		events = append(events, reviewEvent)
		emitEvent(reviewEvent, iteration)
		if reviewErr != nil || strings.TrimSpace(review.Text) == "" {
			return false
		}
		state.instrumentFinalReviews++
		state.usage.OutputTokens += estimateVisibleTokens(review.Text)
		if review.Incorporate && state.instrumentFinalReviews < 2 && iteration < maxSteps {
			state.observations = append(state.observations, "[instrument final review — revise]\n"+review.Text)
			return true
		}
		return false
	}
	if r.Config.DirectorEnabled && r.delegationDepth == 0 {
		status := "active"
		content := fmt.Sprintf("Director mode active · instrument weight %d%%", r.Config.InstrumentWeight)
		if directorErr != nil {
			status = "error"
			content += " · " + directorErr.Error()
		}
		event := runEvent(state.runID, 0, "director_status", "Director council", content, "instrument", status)
		event.Metadata["director"] = true
		event.Metadata["instrument_weight"] = r.Config.InstrumentWeight
		events = append(events, event)
		emitEvent(event, 0)
	}
	if state.contract != nil {
		contractEvent := runEvent(state.runID, 0, eventAcceptanceContract, "Definition of done", state.contract.Render(), "", "active")
		contractEvent.Metadata["source"] = state.contract.Source
		contractEvent.Metadata["contract"] = state.contract
		events = append(events, contractEvent)
		emitEvent(contractEvent, 0)
	}
	defer func() {
		if state.snapshot == nil {
			return
		}
		if state.completed {
			state.snapshot.Cleanup()
			return
		}
		if state.suspended {
			return
		}
		if r.Config.AgentAutoRollback && state.changed {
			report, err := state.snapshot.Restore()
			status := "done"
			text := fmt.Sprintf("Automatically rolled back the failed run (%d restored, %d removed, %d bytes).", report.RestoredFiles, report.RemovedFiles, report.Bytes)
			if err != nil {
				status = "error"
				text = "Automatic rollback failed: " + err.Error()
			} else {
				state.changed = false
				state.verified = false
			}
			event := runEvent(state.runID, 0, "recovery", "Workspace rollback", text, "", status)
			finalResult.Events = append(finalResult.Events, event)
			if strings.TrimSpace(finalResult.Text) != "" {
				finalResult.Text += "\n\n" + text
			} else {
				finalResult.Text = text
			}
			emitEvent(event, 0)
		} else if state.changed {
			text := "Workspace snapshot retained for manual rollback. Run `/rollback` before continuing if the changes should be reverted."
			event := runEvent(state.runID, 0, "recovery", "Rollback available", text, "", "available")
			event.Metadata["snapshot_path"] = state.snapshot.Directory
			finalResult.Events = append(finalResult.Events, event)
			if strings.TrimSpace(finalResult.Text) != "" {
				finalResult.Text += "\n\n" + text
			} else {
				finalResult.Text = text
			}
			emitEvent(event, 0)
			return
		}
		state.snapshot.Cleanup()
	}()
	for iteration := 1; iteration <= maxSteps; iteration++ {
		currentIteration = iteration
		ctx = debuglog.WithScope(ctx, debuglog.Scope{Iteration: iteration})
		if err := ctx.Err(); err != nil {
			result := RunResult{Text: "Agent run cancelled.", Events: events, Usage: state.usage}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "cancelled", Iteration: iteration, Text: result.Text, Verified: state.verified})
			return result
		}

		if budget := r.Config.AgentTaskTokenBudget; budget > 0 && state.usage.InputTokens+state.usage.OutputTokens >= budget {
			state.suspended = true
			text := fmt.Sprintf("Agent paused after reaching the configured task token budget (~%d tokens).", budget)
			event := runEvent(state.runID, iteration, history.EventFinal, "Token budget reached", text, "", "paused")
			event.Metadata["snapshot_path"] = snapshotPath(state.snapshot)
			events = append(events, event)
			emitEvent(event, iteration)
			result := RunResult{Text: text, Events: events, Usage: state.usage}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "budget reached", Iteration: iteration, Text: text, Verified: state.verified})
			return result
		}

		systemPrompt := r.systemPrompt(session, state)
		contextBudget := r.Config.ContextTokens
		if contextBudget <= 0 {
			contextBudget = 16_000
		}
		if maxContext := llm.Capabilities(r.Provider).MaxContextWindow; maxContext > 0 && contextBudget > maxContext {
			contextBudget = maxContext
		}
		window := ContextWindow{
			System:         systemPrompt,
			Budget:         contextBudget,
			SummaryTokens:  r.Config.AgentContextSummaryTok,
			RecallMessages: r.Config.AgentContextRecall,
			Provider:       r.Provider.Name(),
			Iteration:      iteration,
			MaxIterations:  maxSteps,
			Query:          latestUserText(session),
			WorkingMemory:  state.contextWorkingMemory(),
			Embedder:       r.embedder(),
			Messages:       conversationMessages(session.Messages),
			NativeTurns:    state.nativeTurns,
			Cache:          state.contextCache,
		}
		messages, selection := window.Fit()
		request := llm.Request{
			Model:            r.Config.Model(),
			System:           systemPrompt,
			Messages:         messages,
			MaxTokens:        r.Config.MaxTokens,
			Temperature:      r.Config.Mode.Temperature(),
			ReasoningSummary: r.Config.ShowThinking,
			ReasoningEffort:  r.Config.Mode.Effort(),
		}
		contextTokens := estimateRequestTokensForProvider(request, r.Provider.Name())
		emitUpdate(StreamUpdate{
			Kind:            StreamStatus,
			Phase:           "deliberating",
			Iteration:       iteration,
			ContextTokens:   contextTokens,
			SentMessages:    selection.Sent,
			TotalMessages:   selection.Total,
			DroppedMessages: selection.Dropped,
			Verified:        state.verified,
		})

		outputRunes := 0
		decision, err := r.generateToolDecisionWithRetry(ctx, request, r.toolSpecs(state), state.portableTools, func(delta llm.Delta) error {
			if delta.Text == "" {
				return ctx.Err()
			}
			kind := StreamDelta
			phase := "receiving decision"
			switch delta.Kind {
			case llm.DeltaReasoning:
				kind = StreamReasoning
				phase = "reasoning"
			case llm.DeltaActivity:
				kind = StreamActivity
				phase = "preparing action"
			default:
				outputRunes += len([]rune(delta.Text))
			}
			emitUpdate(StreamUpdate{
				Kind:          kind,
				Phase:         phase,
				Iteration:     iteration,
				Delta:         delta.Text,
				ContextTokens: contextTokens,
				OutputTokens:  (outputRunes + 3) / 4,
				Verified:      state.verified,
			})
			return ctx.Err()
		}, func(attempt int, class providerErrorClass, retryErr error) {
			emitUpdate(StreamUpdate{
				Kind:      StreamStatus,
				Phase:     "retrying provider",
				Iteration: iteration,
				Delta:     fmt.Sprintf("retry %d after %s: %s", attempt, class, compact(retryErr.Error(), 240)),
				Verified:  state.verified,
			})
		})
		text := strings.TrimSpace(decision.Text)
		if err == nil {
			state.usage.InputTokens += contextTokens
			state.usage.OutputTokens += estimateVisibleTokens(text)
			if decision.Transport == llm.ToolTransportPortable && !state.portableTools {
				state.portableTools = true
				event := runEvent(state.runID, iteration, history.EventDecision, "Universal tool mode", "The provider's native tool transport was unavailable or malformed. Ephemera switched this run to its provider-neutral tool gateway; all local and MCP tools remain available.", "", "recovered")
				events = append(events, event)
				emitEvent(event, iteration)
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				result := RunResult{Text: "Agent run cancelled.", Events: events, Usage: state.usage}
				emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "cancelled", Iteration: iteration, Text: result.Text, ContextTokens: contextTokens, OutputTokens: (outputRunes + 3) / 4, Verified: state.verified})
				return result
			}
			event := runEvent(state.runID, iteration, history.EventToolResult, "Agent request failed", err.Error(), "", "error")
			events = append(events, event)
			emitEvent(event, iteration)
			result := RunResult{Text: "Agent request failed: " + err.Error(), Events: events, Usage: state.usage}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "failed", Iteration: iteration, Text: result.Text, Err: err, ContextTokens: contextTokens, OutputTokens: (outputRunes + 3) / 4, Verified: state.verified})
			return result
		}
		if text == "" && len(decision.ToolCalls) > 0 {
			text = fmt.Sprintf("Provider requested %d tool call(s).", len(decision.ToolCalls))
		}
		_ = debuglog.WriteCtx(ctx, "info", "agent", "provider decision", "provider response received", map[string]any{
			"provider":      r.Provider.Name(),
			"model":         r.Config.Model(),
			"iteration":     iteration,
			"transport":     string(decision.Transport),
			"tool_calls":    len(decision.ToolCalls),
			"text_runes":    len([]rune(text)),
			"portable_mode": state.portableTools,
		})

		emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "parsing decision", Iteration: iteration, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Verified: state.verified})
		action, ok, repaired, parseErr := r.actionFromDecision(decision)
		if repaired {
			event := runEvent(state.runID, iteration, history.EventDecision, "Decision repaired", "Recovered a structured agent decision after repairing provider JSON.", "", "done")
			events = append(events, event)
			emitEvent(event, iteration)
		}
		if !ok {
			// A provider that can use native tools is allowed to answer directly
			// when no tool is needed. Text-only providers also commonly return a
			// perfectly valid direct answer instead of the optional JSON envelope.
			// Do not burn another model round merely to repackage that answer.
			if text != "" && !looksLikeAgentDecision(text) && !looksLikeUnfinishedActionNarration(text) {
				if retry, blocked := guardRequiredToolEvidence(iteration, text); retry {
					continue
				} else if blocked != nil {
					return *blocked
				}
				if reviewDirectorFinal(iteration, text) {
					continue
				}
				return r.finish(text, state, iteration, contextTokens, text, events, emitUpdate, emitEvent)
			}
			if state.parseFailures < 1 && iteration < maxSteps {
				state.parseFailures++
				detail := firstNonEmpty(parseErr, "provider output did not match the agent decision contract")
				event := runEvent(state.runID, iteration, history.EventDecision, "Decision parse failed", detail, "", "error")
				events = append(events, event)
				emitEvent(event, iteration)
				state.observations = append(state.observations, "[decision parse error]\nReturn exactly one valid JSON object matching the response contract. Do not wrap it in prose. If no tool is needed, put the user-facing answer in final. Error: "+detail+"\nPrevious response: "+compact(text, 900))
				continue
			}
			// Non-agent-capable providers can still return a useful normal answer,
			// but never let prose bypass an explicit workspace execution request.
			if retry, blocked := guardRequiredToolEvidence(iteration, text); retry {
				continue
			} else if blocked != nil {
				return *blocked
			}
			if reviewDirectorFinal(iteration, text) {
				continue
			}
			return r.finish(text, state, iteration, contextTokens, text, events, emitUpdate, emitEvent)
		}

		action = r.autoRouteSimpleActions(action)

		step := reasoningStepFromAction(iteration, action)
		if warnings := reasoning.ConsistencyWarnings(state.reasoningTrace, step); len(warnings) > 0 {
			observation := "[reasoning consistency check]\n" + strings.Join(warnings, "\n")
			state.observations = append(state.observations, observation)
			event := runEvent(state.runID, iteration, "reasoning_consistency", "Reasoning consistency warning", strings.Join(warnings, "\n"), "", "review")
			events = append(events, event)
			emitEvent(event, iteration)
		}
		state.reasoningTrace = append(state.reasoningTrace, step)
		if len(state.reasoningTrace) > 12 {
			state.reasoningTrace = append([]reasoning.ReasoningStep(nil), state.reasoningTrace[len(state.reasoningTrace)-12:]...)
		}
		if r.Config.AgentAdaptiveReasoning && configuredMode == reasoning.ModeNormal {
			nextMode := reasoning.AdaptiveModeWithTools(configuredMode, latestUserText(session), true, toolGraphFromAction(r, action))
			if reasoningModeRank(nextMode) > reasoningModeRank(r.Config.Mode) {
				r.Config.Mode = nextMode
				state.observations = append(state.observations, "[adaptive reasoning]\nTool dependencies increased task complexity; subsequent rounds use "+string(nextMode)+" mode.")
			}
		}

		planChanged := false
		descriptions := planDescriptions(action)
		goal := firstNonEmpty(strings.TrimSpace(string(action.Reasoning.Goal)), action.Summary, "Complete the current request.")
		if state.plan == nil && len(descriptions) > 0 {
			state.plan = newPlan(goal, descriptions)
			planChanged = true
		} else if state.plan != nil && len(descriptions) > 0 {
			planChanged = state.plan.sync(goal, descriptions)
		}
		if state.plan != nil {
			state.plan.applyDependencies(action.Actions)
		}
		if state.plan != nil && (planChanged || len(action.Actions) > 0) {
			planEvent := state.plan.event(state.runID, iteration, "active")
			events = append(events, planEvent)
			emitEvent(planEvent, iteration)
			state.lastPlan = planEvent.Content
			emitUpdate(StreamUpdate{Kind: StreamPlan, Phase: "plan updated", Iteration: iteration, Plan: state.plan, Verified: state.verified})
		}

		for _, event := range actionEvents(state.runID, iteration, action) {
			events = append(events, event)
			emitEvent(event, iteration)
			if event.Type == history.EventReasoningTrace || event.Type == history.EventReasoningSummary {
				state.lastReasoning = event.Content
			}
			if event.Type == history.EventPlanUpdate {
				state.lastPlan = event.Content
			}
		}

		decisionKey := modelActionFingerprint(action)
		if decisionKey != "" {
			state.decisionCounts[decisionKey]++
		}

		if strings.TrimSpace(action.Final) != "" && len(action.Actions) == 0 {
			if retry, blocked := guardRequiredToolEvidence(iteration, action.Final); retry {
				continue
			} else if blocked != nil {
				return *blocked
			}
			if r.shouldDeferFinalForVerification(state) {
				pending, verificationEvents := r.verifyWorkspace(ctx, state, iteration, emitUpdate, emitEvent)
				events = append(events, verificationEvents...)
				if pending != nil {
					state.suspended = true
					pending.SnapshotPath = snapshotPath(state.snapshot)
					result := RunResult{Text: approvalText(pending.Call, pending.Reason), Events: events, Pending: pending, Usage: state.usage}
					emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "awaiting verification approval", Iteration: iteration, Text: result.Text, Pending: pending, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Tool: pending.Call.Name, Verified: state.verified})
					return result
				}
				state.verificationDeferrals++
				if !state.verified && state.verificationDeferrals < 2 && iteration < maxSteps {
					state.observations = append(state.observations, "[verification gate]\nDo not finalize yet. Fix verification failures or explicitly gather evidence with git_status, git_diff, and the project test command.")
					continue
				}
			}
			if state.changed && state.verified && r.Config.AgentAutoReview && r.Config.SubagentEnabled && !r.Config.DirectorEnabled && !state.reviewed && r.delegationDepth == 0 && iteration < maxSteps {
				reviewEvents := r.reviewWorkspace(ctx, state, iteration, emitUpdate, emitEvent)
				events = append(events, reviewEvents...)
				continue
			}
			if r.Config.AgentSelfCritique && !state.critiqued && r.delegationDepth == 0 && iteration < maxSteps {
				clean, critiqueEvents := r.critiqueFinal(ctx, state, iteration, action.Final, emitUpdate, emitEvent)
				events = append(events, critiqueEvents...)
				if !clean {
					continue
				}
			}
			if reviewDirectorFinal(iteration, action.Final) {
				continue
			}
			return r.finish(action.Final, state, iteration, contextTokens, text, events, emitUpdate, emitEvent)
		}

		if len(action.Actions) == 0 {
			finalText := firstNonEmpty(action.Summary, "I need more direction before taking action.")
			if retry, blocked := guardRequiredToolEvidence(iteration, finalText); retry {
				continue
			} else if blocked != nil {
				return *blocked
			}
			if reviewDirectorFinal(iteration, finalText) {
				continue
			}
			return r.finish(finalText, state, iteration, contextTokens, text, events, emitUpdate, emitEvent)
		}

		batchMadeProgress := false
		batchResults := make([]tools.Result, 0, len(action.Actions))
		parallelBatch := r.canParallelActions(action.Actions)
		var parallelResults []dispatchedAction
		if parallelBatch {
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "running parallel tools", Iteration: iteration, Tool: fmt.Sprintf("%d independent reads", len(action.Actions)), ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Verified: state.verified})
			parallelResults = r.executeParallelActions(ctx, state, action, iteration, emitUpdate)
		}
		for actionIndex, item := range action.Actions {
			call := tools.Call{Name: item.Name, Arguments: item.Arguments}
			if parallelBatch {
				call = parallelResults[actionIndex].Call
			} else if normalized, err := r.normalizeToolCall(call); err == nil {
				call = normalized
			}
			purpose := firstNonEmpty(item.Purpose, item.ExpectedResult, action.Summary, "Advance the current plan.")
			fingerprint := toolFingerprint(call)
			if state.plan != nil {
				state.plan.markStarted(actionIndex, call.Name)
				planEvent := state.plan.event(state.runID, iteration, "active")
				events = append(events, planEvent)
				emitEvent(planEvent, iteration)
				emitUpdate(StreamUpdate{Kind: StreamPlan, Phase: "plan step running", Iteration: iteration, Plan: state.plan, Tool: call.Name, Verified: state.verified})
			}
			state.usage.ToolCalls++
			callEvent := runEvent(state.runID, iteration, "tool_call", call.Name, formatToolCall(item), call.Name, "running")
			callEvent.Metadata["call_fingerprint"] = fingerprint
			callEvent.Metadata["parallel"] = parallelBatch
			if parallelBatch {
				callEvent.Metadata["parallel_batch_size"] = len(action.Actions)
				callEvent.Metadata["parallel_batch_index"] = actionIndex
			}
			if item.ProviderCallID != "" {
				callEvent.Metadata["provider_call_id"] = item.ProviderCallID
				callEvent.Metadata["tool_arguments"] = cloneArguments(call.Arguments)
				state.recordNativeToolCall(item)
			}
			callIndex := len(events)
			events = append(events, callEvent)
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "running tool", Iteration: iteration, Tool: call.Name, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Verified: state.verified})
			emitEvent(callEvent, iteration)

			var result tools.Result
			var pending *PendingApproval
			if parallelBatch {
				result = parallelResults[actionIndex].Result
			} else {
				result, pending = r.executeAction(ctx, state, call, purpose, iteration, emitUpdate)
			}
			if pending != nil {
				state.suspended = true
				pending.SnapshotPath = snapshotPath(state.snapshot)
				callEvent.Status = "pending"
				events[callIndex] = callEvent
				emitEvent(callEvent, iteration)
				approval := runEvent(state.runID, iteration, "approval_request", "Approval required: "+call.Name, pending.Reason, call.Name, "pending")
				approval.Metadata["call_fingerprint"] = fingerprint
				approval.Metadata["call_event_id"] = callEvent.ID
				if item.ProviderCallID != "" {
					approval.Metadata["provider_call_id"] = item.ProviderCallID
				}
				pending.Fingerprint = fingerprint
				pending.ProviderCallID = item.ProviderCallID
				pending.CallEventID = callEvent.ID
				pending.ApprovalEventID = approval.ID
				events = append(events, approval)
				emitEvent(approval, iteration)
				resultRun := RunResult{Text: approvalText(call, pending.Reason), Events: events, Pending: pending, Usage: state.usage}
				emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "awaiting approval", Iteration: iteration, Text: resultRun.Text, Pending: pending, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Tool: call.Name, Verified: state.verified})
				return resultRun
			}

			attachCallMetadata(&result, fingerprint)
			if item.ProviderCallID != "" {
				result.Metadata["provider_call_id"] = item.ProviderCallID
			}
			callEvent.Status = "done"
			if !result.OK {
				callEvent.Status = "error"
			}
			events[callIndex] = callEvent
			emitEvent(callEvent, iteration)
			resultEvent := toolResultEvent(state.runID, iteration, result)
			events = append(events, resultEvent)
			emitEvent(resultEvent, iteration)
			state.observe(call, result)
			batchResults = append(batchResults, result)
			if state.plan != nil {
				state.plan.markResult(actionIndex, call, result)
				planStatus := "active"
				if !result.OK {
					planStatus = "revised"
				}
				planEvent := state.plan.event(state.runID, iteration, planStatus)
				events = append(events, planEvent)
				emitEvent(planEvent, iteration)
				state.lastPlan = planEvent.Content
				emitUpdate(StreamUpdate{Kind: StreamPlan, Phase: "plan step updated", Iteration: iteration, Plan: state.plan, Tool: call.Name, Verified: state.verified})
			}
			if item.ProviderCallID != "" {
				state.recordNativeToolResult(item.ProviderCallID, call.Name, result)
			}
			if result.OK && !metadataBool(result.Metadata, "deduplicated") {
				batchMadeProgress = true
			}
			if !result.OK {
				if parallelBatch {
					if metadataBool(result.Metadata, "atomic_batch") {
						state.observations = append(state.observations, "[atomic write batch rolled back]\nOne disjoint write failed, so every write in the batch was rolled back. Diagnose the failing target and re-plan without assuming any batch change remains.")
					} else {
						state.observations = append(state.observations, "[parallel partial failure]\nOne independent read failed; the remaining read results were still collected. Re-plan from all available evidence.")
					}
				} else {
					state.observations = append(state.observations, "[batch halted]\nA tool failed, so later actions from the same decision were not executed. Re-plan from the observed error before continuing.")
					break
				}
			}
		}
		if batchMadeProgress {
			state.noProgressRounds = 0
		} else {
			state.noProgressRounds++
		}
		if reflection := r.reflectOnBatch(state, action, batchResults, iteration); reflection.Replan {
			state.observations = append(state.observations, "[self-reflection]\n"+reflection.Observation)
			event := runEvent(state.runID, iteration, "reflection", "Tool outcome mismatch", reflection.Observation, "", "replan")
			events = append(events, event)
			emitEvent(event, iteration)
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "replanning after reflection", Iteration: iteration, Verified: state.verified})
			// Keep evaluating the bounded loop guards before starting the next round.
			// An immediate continue here would let repeated failed decisions evade
			// convergence detection indefinitely.
		}
		if director != nil && r.directorShouldReviewActions(action) {
			review, reviewErr := director.review(ctx, state, iteration, "action", "", emitUpdate)
			reviewEvent := director.event(state.runID, iteration, "action", review, reviewErr)
			events = append(events, reviewEvent)
			emitEvent(reviewEvent, iteration)
			if reviewErr == nil && strings.TrimSpace(review.Text) != "" {
				state.usage.OutputTokens += estimateVisibleTokens(review.Text)
				label := "noted"
				if review.Incorporate {
					label = "action required"
				}
				state.observations = append(state.observations, "[instrument review — "+label+"]\n"+review.Text)
			}
		}
		guardDecision := state.progressGuard.Record(progressSnapshot(state), batchMadeProgress)
		if !batchMadeProgress && guardDecision.Action == LoopReplan && iteration < maxSteps {
			runMetrics.Inc("agent_loop_stalls_total")
			state.noProgressRounds = 0
			state.observations = append(state.observations,
				"[progress guard]\nThe run repeated the same semantic state without new evidence. Abandon the current strategy, state a new hypothesis, and choose a materially different tool or rollback path. Reason: "+guardDecision.Reason,
			)
			event := runEvent(state.runID, iteration, "recovery", "Strategy change required", guardDecision.Reason, "", "replan")
			event.Metadata["progress_fingerprint"] = guardDecision.Fingerprint
			event.Metadata["repeat_count"] = guardDecision.Repeats
			events = append(events, event)
			emitEvent(event, iteration)
			emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "replanning after stall", Iteration: iteration, Verified: state.verified})
			continue
		}
		if !batchMadeProgress && guardDecision.Action == LoopStop {
			runMetrics.Inc("agent_loop_stalls_total")
			message := "Agent stopped safely because it remained in the same semantic state after a forced strategy change. " + guardDecision.Reason
			event := runEvent(state.runID, iteration, "recovery", "Semantic loop stopped", message, "", "error")
			event.Metadata["progress_fingerprint"] = guardDecision.Fingerprint
			event.Metadata["repeat_count"] = guardDecision.Repeats
			events = append(events, event)
			emitEvent(event, iteration)
			result := RunResult{Text: message, Events: events, Usage: state.usage, Completion: ptrCompletion(state.completionReport())}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "stalled safely", Iteration: iteration, Text: message, Verified: state.verified})
			return result
		}
		stalledDecision := decisionKey != "" && state.decisionCounts[decisionKey] > maxInt(1, r.Config.AgentLoopLimit)
		stalledRun := state.noProgressRounds > maxInt(2, r.Config.AgentLoopLimit)
		if !batchMadeProgress && (stalledDecision || stalledRun) {
			message := firstNonEmpty(action.Summary, "Agent stopped safely because the same unsuccessful action plan repeated without producing new evidence.")
			if !strings.Contains(strings.ToLower(message), "stopped") {
				message += "\n\nStopped safely because the same unsuccessful action plan repeated without producing new evidence."
			}
			event := runEvent(state.runID, iteration, "recovery", "Repeated action plan stopped", message, "", "error")
			events = append(events, event)
			emitEvent(event, iteration)
			result := RunResult{Text: message, Events: events, Usage: state.usage, Completion: ptrCompletion(state.completionReport())}
			emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "stalled safely", Iteration: iteration, Text: message, Verified: state.verified})
			return result
		}
		emitUpdate(StreamUpdate{Kind: StreamStatus, Phase: "reviewing results", Iteration: iteration, ContextTokens: contextTokens, OutputTokens: estimateVisibleTokens(text), Verified: state.verified})
	}

	state.suspended = true
	debuglog.WarningCtx(ctx, "agent", "step limit reached", "agent paused after reaching the configured step limit", map[string]any{
		"max_steps": maxSteps, "changed": state.changed, "verified": state.verified,
		"tool_sequence": append([]string(nil), state.toolSequence...), "changed_paths": changedArtifactPaths(state),
		"no_progress_rounds": state.noProgressRounds, "parse_failures": state.parseFailures,
	})
	text := "Agent paused after reaching the configured step limit. Review the timeline and run `/run` to continue."
	event := runEvent(state.runID, maxSteps, "final", "Paused", text, "", "paused")
	event.Metadata["verified"] = state.verified
	event.Metadata["snapshot_path"] = snapshotPath(state.snapshot)
	events = append(events, event)
	emitEvent(event, maxSteps)
	result := RunResult{Text: text, Events: events, Usage: state.usage}
	emitUpdate(StreamUpdate{Kind: StreamDone, Phase: "paused", Iteration: maxSteps, Text: text, Verified: state.verified})
	return result
}
