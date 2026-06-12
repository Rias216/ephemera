package agent

import (
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	workruntime "github.com/ephemera-ai/ephemera/internal/runtime"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"strings"
	"sync"
	"time"
)

type runState struct {
	mu                     sync.Mutex
	toolRegistry           tools.Registry
	runID                  string
	observations           []string
	nativeTurns            []llm.Message
	toolSequence           []string
	callCounts             map[string]int
	decisionCounts         map[string]int
	completedCalls         map[string]int
	successfulTools        map[string]int
	resultCache            map[string]cachedToolResult
	suppressedTools        map[string]bool
	rejectedCalls          map[string]bool
	failedApprovedCalls    map[string]bool
	workspaceRevision      int
	inspectedPaths         map[string]bool
	changedPaths           map[string]bool
	changed                bool
	verified               bool
	verificationAttempted  bool
	verificationDeferrals  int
	parseFailures          int
	noProgressRounds       int
	reviewed               bool
	lastReasoning          string
	lastPlan               string
	reasoningTrace         []reasoning.ReasoningStep
	reflectionCounts       map[int]int
	plan                   *Plan
	contextCache           *ContextFitCache
	projectManifest        workruntime.ProjectManifest
	projectManifestSource  string
	contract               *AcceptanceContract
	progressGuard          *ProgressGuard
	critiqued              bool
	instrumentFinalReviews int
	usage                  RunUsage
	snapshot               *workspaceSnapshot
	completed              bool
	suspended              bool
	portableTools          bool
	toolUseReprompts       int
	intent                 executionIntent
}

func (s *runState) contextWorkingMemory() string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.plan != nil {
		parts = append(parts, "Current plan:\n"+s.plan.Render())
	}
	if s.contract != nil {
		parts = append(parts, "Acceptance contract:\n"+s.contract.Render())
	}
	if history := reasoning.HistoryPrompt(s.reasoningTrace, 4); history != "" {
		parts = append(parts, "Structured reasoning history (decision summaries only):\n"+history)
	}
	if len(s.observations) > 0 {
		parts = append(parts, "Recent evidence:\n"+strings.Join(tailStrings(s.observations, 6), "\n"))
	}
	if len(s.changedPaths) > 0 {
		parts = append(parts, "Changed paths: "+strings.Join(sortedKeys(s.changedPaths), ", "))
	}
	if s.verified {
		parts = append(parts, "Verification state: verified")
	} else if s.changed {
		parts = append(parts, "Verification state: changes are not yet verified")
	}
	return compact(strings.Join(parts, "\n\n"), 3600)
}

type cachedToolResult struct {
	Revision  int
	Signature string
	Result    tools.Result
}

func (r Runner) initialState(session history.Session, started time.Time) *runState {
	events := eventsSinceLatestUser(session)
	state := &runState{
		runID:               fmt.Sprintf("run-%d", started.UnixNano()),
		toolRegistry:        r.Tools,
		observations:        recentToolObservations(events),
		nativeTurns:         reconstructNativeToolTurns(events),
		callCounts:          map[string]int{},
		decisionCounts:      map[string]int{},
		completedCalls:      map[string]int{},
		successfulTools:     map[string]int{},
		resultCache:         map[string]cachedToolResult{},
		suppressedTools:     map[string]bool{},
		rejectedCalls:       map[string]bool{},
		failedApprovedCalls: map[string]bool{},
		reflectionCounts:    map[int]int{},
		inspectedPaths:      map[string]bool{},
		changedPaths:        map[string]bool{},
		contextCache:        NewContextFitCache(),
		plan:                latestPlan(events),
		progressGuard:       NewProgressGuard(),
		portableTools:       prefersPortableTools(r.Provider, r.Config.Model()),
		intent:              classifyExecutionIntent(latestUserText(session)),
	}
	// Delegated specialists are synthetic, read-only advisory runs. The parent
	// agent owns workspace execution evidence and verification, so applying the
	// top-level completion guard here can trap a valid review in a nested loop.
	if r.delegationDepth > 0 {
		state.intent = executionIntent{}
	}
	manifest, source, manifestErr := workruntime.LoadOrDiscoverProjectManifest(r.Tools.WorkspaceRoot, r.Config.AutoTestCommand)
	if manifestErr != nil {
		state.observations = append(state.observations, "[project manifest]\nCould not load .ephemera/project.json: "+manifestErr.Error())
		manifest = workruntime.DiscoverProjectManifest(r.Tools.WorkspaceRoot, r.Config.AutoTestCommand)
		source = "discovered-after-error"
	}
	state.projectManifest = manifest
	state.projectManifestSource = source
	state.contract = newAcceptanceContract(latestUserText(session), manifest, source)
	for _, event := range events {
		if path := metadataString(event.Metadata, "snapshot_path"); path != "" {
			if snapshot, err := loadWorkspaceSnapshot(path); err == nil {
				state.snapshot = snapshot
			}
		}
		fingerprint := eventFingerprint(event)
		if event.Type == history.EventToolCall && fingerprint != "" {
			state.callCounts[fingerprint]++
		}
		if event.Type == history.EventApprovalRequest && event.Status == "rejected" && fingerprint != "" {
			state.rejectedCalls[fingerprint] = true
		}
		if event.Type != history.EventToolResult {
			continue
		}
		if state.contract != nil && !metadataBool(event.Metadata, "dry_run") && !metadataBool(event.Metadata, "deduplicated") {
			arguments := map[string]any{}
			if path := metadataString(event.Metadata, "path"); path != "" {
				arguments["path"] = path
			}
			state.contract.Observe(
				tools.Call{Name: event.Tool, Arguments: arguments},
				tools.Result{Tool: event.Tool, OK: event.Status != "error", Output: event.Content, Error: event.Content, Metadata: cloneMetadata(event.Metadata)},
			)
		}
		risk := r.eventRisk(event)
		if event.Status == "error" {
			if fingerprint != "" && metadataBool(event.Metadata, "approved") && (risk == tools.RiskWrite || risk == tools.RiskShell) {
				state.failedApprovedCalls[fingerprint] = true
			}
			continue
		}

		deduplicated := metadataBool(event.Metadata, "deduplicated")
		if !deduplicated && strings.TrimSpace(event.Tool) != "" {
			state.successfulTools[event.Tool]++
		}
		if !deduplicated && state.isWorkspaceMutation(event.Tool) {
			state.workspaceRevision++
		}
		if fingerprint != "" && (risk == tools.RiskWrite || risk == tools.RiskShell) {
			state.completedCalls[fingerprint] = state.workspaceRevision
		}
		if fingerprint != "" && risk == tools.RiskRead && event.Tool != "delegate" && !deduplicated {
			cacheKey := metadataString(event.Metadata, "semantic_cache_key")
			if cacheKey == "" {
				cacheKey = fingerprint
			}
			state.resultCache[cacheKey] = cachedToolResult{
				Revision:  state.workspaceRevision,
				Signature: metadataString(event.Metadata, "content_sha256"),
				Result:    tools.Result{Tool: event.Tool, OK: true, Output: event.Content, Metadata: cloneMetadata(event.Metadata)},
			}
		}
		if deduplicated {
			continue
		}

		path, _ := event.Metadata["path"].(string)
		switch event.Tool {
		case "read_file":
			if path != "" {
				state.inspectedPaths[normalizePath(path)] = true
			}
		case "apply_patch", "replace_in_file":
			state.changed = true
			if path != "" {
				state.changedPaths[normalizePath(path)] = true
			}
			state.verified = false
		case "apply_multi_patch":
			state.changed = true
			for _, changedPath := range metadataStringSlice(event.Metadata, "paths") {
				state.changedPaths[normalizePath(changedPath)] = true
			}
			state.verified = false
		case "go_test":
			state.verificationAttempted = true
			state.verified = true
		case "delegate":
			if role, _ := event.Metadata["role"].(string); role == "review" {
				state.reviewed = true
			} else if role == "critic" {
				state.critiqued = true
			}
		}
	}
	return state
}

func latestUserText(session history.Session) string {
	for index := len(session.Messages) - 1; index >= 0; index-- {
		if session.Messages[index].Role == "user" {
			return session.Messages[index].Content
		}
	}
	return ""
}

func eventsSinceLatestUser(session history.Session) []history.Event {
	if len(session.Messages) == 0 {
		return session.Events
	}
	latestUser := time.Time{}
	for index := len(session.Messages) - 1; index >= 0; index-- {
		if session.Messages[index].Role == "user" {
			latestUser = session.Messages[index].CreatedAt
			break
		}
	}
	if latestUser.IsZero() {
		return session.Events
	}
	start := len(session.Events)
	for index, event := range session.Events {
		if !event.CreatedAt.Before(latestUser) {
			start = index
			break
		}
	}
	if start >= len(session.Events) {
		return nil
	}
	return session.Events[start:]
}

func reconstructNativeToolTurns(events []history.Event) []llm.Message {
	var turns []llm.Message
	knownCalls := map[string]llm.ToolCall{}
	completed := map[string]bool{}
	rejected := map[string]bool{}
	for _, event := range events {
		callID := metadataString(event.Metadata, "provider_call_id")
		if callID == "" {
			continue
		}
		if event.Type == history.EventToolResult {
			completed[callID] = true
		}
		if event.Type == history.EventApprovalRequest && event.Status == "rejected" {
			rejected[callID] = true
		}
	}
	for _, event := range events {
		callID := metadataString(event.Metadata, "provider_call_id")
		if callID == "" {
			continue
		}
		switch event.Type {
		case history.EventToolCall:
			if !completed[callID] && !rejected[callID] {
				// Do not send an unresolved native tool call back to a provider. A
				// pending approval is control state, not a completed conversation turn.
				continue
			}
			call := llm.ToolCall{
				ID:        callID,
				Name:      event.Tool,
				Arguments: metadataArguments(event.Metadata, "tool_arguments"),
			}
			knownCalls[callID] = call
			turns = append(turns, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		case history.EventToolResult:
			call, ok := knownCalls[callID]
			if !ok {
				call = llm.ToolCall{ID: callID, Name: event.Tool, Arguments: map[string]any{}}
				turns = append(turns, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
				knownCalls[callID] = call
			}
			result := llm.ToolResult{
				ID:       callID,
				Name:     firstNonEmpty(event.Tool, call.Name),
				OK:       event.Status != "error",
				Metadata: cloneMetadata(event.Metadata),
			}
			if result.OK {
				result.Output = event.Content
			} else {
				result.Error = event.Content
			}
			turns = append(turns, llm.Message{Role: "tool", ToolResult: &result})
		case history.EventApprovalRequest:
			if event.Status != "rejected" || completed[callID] {
				continue
			}
			call, ok := knownCalls[callID]
			if !ok {
				continue
			}
			result := llm.ToolResult{
				ID:    callID,
				Name:  call.Name,
				OK:    false,
				Error: "User rejected this tool call. Choose another approach or explain the limitation.",
			}
			turns = append(turns, llm.Message{Role: "tool", ToolResult: &result})
		}
	}
	return turns
}

func (s *runState) recordNativeToolCall(action modelToolAction) {
	if strings.TrimSpace(action.ProviderCallID) == "" {
		return
	}
	call := llm.ToolCall{
		ID:        action.ProviderCallID,
		Name:      firstNonEmpty(action.Name, action.Tool),
		Arguments: cloneArguments(action.Arguments),
	}
	s.nativeTurns = append(s.nativeTurns, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
}

func (s *runState) recordNativeToolResult(callID, name string, result tools.Result) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	providerResult := llm.ToolResult{
		ID:       callID,
		Name:     name,
		OK:       result.OK,
		Output:   result.Output,
		Error:    result.Error,
		Metadata: cloneMetadata(result.Metadata),
	}
	s.nativeTurns = append(s.nativeTurns, llm.Message{Role: "tool", ToolResult: &providerResult})
}

func cloneArguments(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(arguments))
	for key, value := range arguments {
		out[key] = value
	}
	return out
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataStringSlice(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	switch values := metadata[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func metadataArguments(metadata map[string]any, key string) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	if values, ok := metadata[key].(map[string]any); ok {
		return cloneArguments(values)
	}
	return map[string]any{}
}
