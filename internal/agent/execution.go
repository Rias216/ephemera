package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/tools"
	"os"
	"path/filepath"
	"strings"
)

func (r Runner) executeAction(ctx context.Context, state *runState, call tools.Call, purpose string, iteration int, emit func(StreamUpdate)) (tools.Result, *PendingApproval) {
	state.mu.Lock()
	locked := true
	unlock := func() {
		if locked {
			state.mu.Unlock()
			locked = false
		}
	}
	defer unlock()

	originalFingerprint := toolFingerprint(call)
	state.callCounts[originalFingerprint]++
	loopLimit := r.Config.AgentLoopLimit
	if loopLimit < 1 {
		loopLimit = 2
	}
	normalized, err := r.normalizeToolCall(call)
	if err != nil {
		hint := tools.RepairHint(call, err)
		message := err.Error()
		if hint != "" {
			message += ". Recovery: " + hint
		}
		if state.callCounts[originalFingerprint] > loopLimit {
			message = "doom-loop guard: identical invalid tool call repeated; change the tool or arguments before retrying. Last validation error: " + message
		}
		logCtx := debuglog.WithScope(ctx, debuglog.Scope{Tool: call.Name, Workspace: r.Tools.WorkspaceRoot, Iteration: iteration})
		failed := false
		args := tools.AuditArguments(call.Arguments)
		_ = debuglog.AppendTool(logCtx, debuglog.ToolRecord{
			Stage: "normalization_failed", Tool: call.Name, Fingerprint: originalFingerprint, Risk: string(r.toolRisk(call.Name)),
			Arguments: args, OK: &failed, Error: message, Metadata: map[string]any{"repair_hint": hint},
		})
		debuglog.FailureCtx(logCtx, "agent", "tool call rejected before execution", message, map[string]any{
			"fingerprint": originalFingerprint, "arguments": args, "repair_hint": hint,
		})
		return tools.Result{Tool: call.Name, OK: false, Error: message, Metadata: map[string]any{
			"risk": string(r.toolRisk(call.Name)), "error_class": string(errorInvalid), "repair_hint": hint, "debug_logged": true,
		}}, nil
	}
	call = normalized
	fingerprint := toolFingerprint(call)
	if fingerprint != originalFingerprint {
		state.callCounts[fingerprint]++
	}
	callCount := state.callCounts[fingerprint]
	if state.rejectedCalls[fingerprint] {
		return tools.Result{Tool: call.Name, OK: false, Error: "user rejected this exact action during the current request; do not request it again unless the user changes the instruction"}, nil
	}
	if state.failedApprovedCalls[fingerprint] {
		return tools.Result{Tool: call.Name, OK: false, Error: "this exact approved action already failed during the current request; change the arguments or approach instead of requesting the same approval again"}, nil
	}
	if state.completedAtCurrentRevisionWithRisk(call, r.toolRisk(call.Name)) {
		return tools.Result{
			Tool:   call.Name,
			OK:     true,
			Output: "Skipped duplicate execution: this exact risky action already completed successfully at the current workspace revision. Reuse the existing result and continue.",
			Metadata: map[string]any{
				"call_fingerprint": fingerprint, "deduplicated": true, "previously_completed": true,
				"workspace_revision": state.workspaceRevision, "risk": string(r.toolRisk(call.Name)),
			},
		}, nil
	}
	if cached, ok := r.cachedReadResultWithRisk(state, call, r.toolRisk(call.Name)); ok {
		if callCount > loopLimit {
			return tools.Result{Tool: call.Name, OK: false, Error: "duplicate read suppressed: this exact call already succeeded and its result was returned again; use that evidence, choose a narrower/different tool, or finalize"}, nil
		}
		if shouldSuppressRepeatedTool(call.Name) {
			state.suppressedTools[call.Name] = true
		}
		if cached.Metadata == nil {
			cached.Metadata = map[string]any{}
		}
		cached.Metadata["call_fingerprint"] = fingerprint
		cached.Metadata["deduplicated"] = true
		cached.Metadata["cache_hit"] = true
		cached.Metadata["workspace_revision"] = state.workspaceRevision
		return cached, nil
	}
	if callCount > loopLimit {
		return tools.Result{Tool: call.Name, OK: false, Error: "doom-loop guard: identical tool call repeated without enough new evidence; change the query, arguments, or approach"}, nil
	}
	if err := r.enforceInspectBeforeEdit(state, call); err != nil {
		return tools.Result{Tool: call.Name, OK: false, Error: err.Error()}, nil
	}
	if call.Name == "delegate" {
		if r.delegationDepth > 0 {
			return tools.Result{Tool: call.Name, OK: false, Error: "nested delegation is disabled; complete the assigned specialist task directly"}, nil
		}
		unlock()
		if emit != nil {
			emit(StreamUpdate{Kind: StreamStatus, Phase: "delegating specialist", Iteration: iteration, Tool: call.Name, Verified: state.verified})
		}
		return r.runDelegate(ctx, call), nil
	}
	if r.requiresApproval(call) {
		reason := strings.TrimSpace(purpose)
		if scope := r.Tools.ApprovalReason(call); scope != "" {
			if reason != "" {
				reason += "\n\n"
			}
			reason += scope
		}
		return tools.Result{}, &PendingApproval{Call: call, Reason: reason, RunID: state.runID, Purpose: purpose, Fingerprint: fingerprint}
	}
	if !r.Config.AgentDryRun && r.Config.SandboxMode == config.SandboxSnapshot && r.toolRisk(call.Name) != tools.RiskRead && state.snapshot == nil {
		maxBytes := int64(r.Config.AgentSnapshotMaxMB) * 1024 * 1024
		snapshot, snapshotErr := createWorkspaceSnapshot(r.Tools.WorkspaceRoot, maxBytes)
		if snapshotErr != nil {
			return tools.Result{Tool: call.Name, OK: false, Error: "workspace snapshot failed; action was not executed: " + snapshotErr.Error(), Metadata: map[string]any{"risk": string(r.toolRisk(call.Name)), "snapshot_failed": true}}, nil
		}
		state.snapshot = snapshot
	}
	snapshotDir := snapshotPath(state.snapshot)
	unlock()
	result := r.executeWithRecovery(ctx, call, iteration, emit)
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["semantic_cache_key"] = semanticToolFingerprint(call)
	if snapshotDir != "" {
		result.Metadata["snapshot_path"] = snapshotDir
	}
	return result, nil
}

func (r Runner) enforceInspectBeforeEdit(state *runState, call tools.Call) error {
	if !r.Config.RequireReadBeforeEdit || (call.Name != "apply_patch" && call.Name != "replace_in_file" && call.Name != "apply_multi_patch") {
		return nil
	}
	paths := []string{strings.TrimSpace(fmt.Sprint(call.Arguments["path"]))}
	if call.Name == "apply_multi_patch" {
		paths = multiPatchCallPaths(call)
	}
	for _, path := range paths {
		if path == "" || path == "<nil>" {
			continue
		}
		identity, err := r.Tools.PathIdentity(path)
		if err != nil {
			return err
		}
		resolved := path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(r.Tools.WorkspaceRoot, resolved)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return err
		}
		if _, err := os.Stat(resolved); os.IsNotExist(err) {
			continue
		}
		key := normalizePath(identity)
		if !state.inspectedPaths[key] {
			return fmt.Errorf("inspect-before-edit guard: read_file %q before modifying the existing file", identity)
		}
	}
	return nil
}

func multiPatchCallPaths(call tools.Call) []string {
	data, err := json.Marshal(call.Arguments["patches"])
	if err != nil {
		return nil
	}
	var specs []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(data, &specs) != nil {
		return nil
	}
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		if path := strings.TrimSpace(spec.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func (r Runner) autoRouteSimpleActions(action modelAction) modelAction {
	if !r.Config.SubagentEnabled || !r.Config.SubagentAutoRoute || !r.hasDistinctSubagentRoute() || r.delegationDepth > 0 || len(action.Actions) != 1 {
		return action
	}
	item := action.Actions[0]
	name := firstNonEmpty(item.Name, item.Tool)
	if name == "" || name == "delegate" || r.toolRisk(name) != tools.RiskRead {
		return action
	}
	eligible := map[string]bool{
		"dependency_graph":  true,
		"file_summary":      true,
		"find_refs":         true,
		"find_symbol":       true,
		"grep_regex":        true,
		"list_dependencies": true,
		"list_files":        true,
		"read_file":         true,
		"search":            true,
		"security_audit":    true,
		"tree":              true,
	}
	if !eligible[name] {
		return action
	}
	// modelAction contains a slice. Clone it before replacement so routing does
	// not mutate a cached/native decision that may be retried or inspected.
	action.Actions = append([]modelToolAction(nil), action.Actions...)
	role := "explore"
	if name == "security_audit" {
		role = "review"
	}
	task := fmt.Sprintf("Perform this read-only %s task with the available tools, then return concise findings with exact paths and line references. Requested tool: %s\nArguments:\n%s", role, name, marshalArgs(item.Arguments))
	action.Actions[0] = modelToolAction{
		ID:             item.ID,
		Tool:           "delegate",
		Name:           "delegate",
		Arguments:      map[string]any{"task": task, "role": role},
		Purpose:        "Auto-routed lightweight " + role + " task from " + name,
		ExpectedResult: firstNonEmpty(item.ExpectedResult, "A compact evidence summary for the main agent."),
		DependsOn:      append([]string(nil), item.DependsOn...),
	}
	return action
}

func (r Runner) hasDistinctSubagentRoute() bool {
	route := strings.TrimSpace(r.Config.SubagentProvider)
	model := strings.TrimSpace(r.Config.SubagentModel)
	if route == "" && model == "" {
		return false
	}
	activeRoute := strings.TrimSpace(r.Config.ActiveConnection)
	sameRoute := route == "" || strings.EqualFold(route, activeRoute) || strings.EqualFold(route, r.Config.Provider)
	sameModel := model == "" || strings.EqualFold(model, r.Config.Model())
	return !(sameRoute && sameModel)
}

func (r Runner) runDelegate(ctx context.Context, call tools.Call) tools.Result {
	if !r.Config.SubagentEnabled {
		return tools.Result{Tool: "delegate", OK: false, Error: "subagent system is disabled; enable it with /subagent on"}
	}
	task := strings.TrimSpace(fmt.Sprint(call.Arguments["task"]))
	role := strings.ToLower(strings.TrimSpace(fmt.Sprint(call.Arguments["role"])))
	if role == "" || role == "<nil>" {
		role = "explore"
	}

	cfg, cfgErr := r.Config.ConfigForRole(r.Config.SubagentProvider, r.Config.SubagentModel)
	if cfgErr != nil {
		return tools.Result{Tool: "delegate", OK: false, Error: "subagent route setup failed: " + cfgErr.Error()}
	}
	subProvider, err := llm.NewSubagentProvider(r.Config)
	if err != nil {
		return tools.Result{Tool: "delegate", OK: false, Error: "subagent provider setup failed: " + err.Error()}
	}
	if subProvider == nil {
		subProvider = r.Provider
		cfg = r.Config
	}
	cfg.ApprovalPolicy = config.ApprovalReadOnly
	cfg.AgentMaxSteps = minInt(maxInt(1, cfg.SubagentMaxSteps), 8)
	cfg.MaxTokens = cfg.SubagentMaxTokens
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	cfg.AgentSelfCritique = false
	cfg.DirectorEnabled = false
	session := history.New("delegate-"+role, cfg.Provider, cfg.Model(), cfg.Mode)
	session.Append("user", task)
	sub := NewRunner(cfg, subProvider)
	sub.delegationDepth = r.delegationDepth + 1
	sub.delegateRole = role
	result := sub.run(ctx, session, nil)
	if strings.TrimSpace(result.Text) == "" {
		return tools.Result{Tool: "delegate", OK: false, Error: "specialist returned no result"}
	}
	return tools.Result{
		Tool:   "delegate",
		OK:     result.Pending == nil,
		Output: fmt.Sprintf("specialist=%s model=%s/%s\n%s", role, subProvider.Name(), cfg.Model(), compact(result.Text, 2400)),
		Metadata: map[string]any{
			"role":     role,
			"task":     compact(task, 300),
			"provider": subProvider.Name(),
			"model":    cfg.Model(),
		},
	}
}

func (s *runState) observe(call tools.Call, result tools.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, formatToolObservation(result))
	if result.OK && !metadataBool(result.Metadata, "deduplicated") {
		if s.successfulTools == nil {
			s.successfulTools = map[string]int{}
		}
		s.successfulTools[call.Name]++
	}
	if metadataBool(result.Metadata, "dry_run") {
		return
	}
	if metadataBool(result.Metadata, "deduplicated") {
		return
	}
	if s.contract != nil {
		s.contract.Observe(call, result)
	}
	if call.Name != "" && (len(s.toolSequence) == 0 || s.toolSequence[len(s.toolSequence)-1] != call.Name) {
		s.toolSequence = append(s.toolSequence, call.Name)
	}
	risk := tools.Risk(metadataString(result.Metadata, "risk"))
	if risk == "" {
		risk = s.toolRisk(call.Name)
	}
	if result.OK && risk == tools.RiskRead && call.Name != "delegate" {
		key := metadataString(result.Metadata, "semantic_cache_key")
		if key == "" {
			key = semanticToolFingerprint(call)
		}
		s.resultCache[key] = cachedToolResult{
			Revision:  s.workspaceRevision,
			Signature: metadataString(result.Metadata, "content_sha256"),
			Result:    cloneToolResult(result),
		}
	}
	if result.OK {
		if s.isWorkspaceMutation(call.Name) {
			s.workspaceRevision++
			for name := range s.suppressedTools {
				delete(s.suppressedTools, name)
			}
		}
		if risk == tools.RiskWrite || risk == tools.RiskShell {
			s.completedCalls[toolFingerprint(call)] = s.workspaceRevision
		}
	}
	path := ""
	if result.Metadata != nil {
		path, _ = result.Metadata["path"].(string)
	}
	if path == "" && call.Arguments != nil {
		path = strings.TrimSpace(fmt.Sprint(call.Arguments["path"]))
	}
	switch call.Name {
	case "read_file":
		if result.OK && path != "" {
			s.inspectedPaths[normalizePath(path)] = true
		}
	case "create_directory":
		if result.OK && metadataBool(result.Metadata, "changed") {
			s.recordChangedArtifact(path, true)
		}
	case "apply_patch", "replace_in_file":
		if result.OK {
			s.recordChangedArtifact(path, false)
		}
	case "apply_multi_patch":
		if result.OK {
			for _, changedPath := range metadataStringSlice(result.Metadata, "paths") {
				s.recordChangedArtifact(changedPath, false)
			}
		}
	case "run_formatter", "git_merge", "git_checkout":
		if result.OK {
			s.changed = true
			s.verified = false
		}
	case "go_test":
		s.verificationAttempted = true
		s.verified = result.OK
	}
}

func (s *runState) completedAtCurrentRevisionWithRisk(call tools.Call, risk tools.Risk) bool {
	if risk != tools.RiskWrite && risk != tools.RiskShell {
		return false
	}
	revision, ok := s.completedCalls[toolFingerprint(call)]
	if !ok {
		return false
	}
	if call.Name == "go_test" {
		return revision == s.workspaceRevision
	}
	return true
}

func (s *runState) completedAtCurrentRevision(call tools.Call) bool {
	return s.completedAtCurrentRevisionWithRisk(call, s.toolRisk(call.Name))
}

func (r Runner) cachedReadResultWithRisk(s *runState, call tools.Call, risk tools.Risk) (tools.Result, bool) {
	if risk != tools.RiskRead || call.Name == "delegate" {
		return tools.Result{}, false
	}
	cacheKey := semanticToolFingerprint(call)
	cached, ok := s.resultCache[cacheKey]
	if !ok {
		return tools.Result{}, false
	}
	if cached.Revision != s.workspaceRevision {
		if call.Name != "read_file" || cached.Signature == "" || cached.Signature != r.readFileSignature(call) {
			return tools.Result{}, false
		}
	}
	return cloneToolResult(cached.Result), true
}

func (r Runner) cachedReadResult(s *runState, call tools.Call) (tools.Result, bool) {
	return r.cachedReadResultWithRisk(s, call, r.toolRisk(call.Name))
}

func cloneToolResult(result tools.Result) tools.Result {
	result.Metadata = cloneMetadata(result.Metadata)
	return result
}

// semanticToolFingerprint canonicalizes optional defaults before hashing. This
// treats calls such as read_file(path) and read_file(path,start_line=1) as the
// same observation without conflating materially different ranges or queries.
func semanticToolFingerprint(call tools.Call) string {
	canonical := tools.Call{Name: strings.TrimSpace(call.Name), Arguments: cloneArguments(call.Arguments)}
	if canonical.Arguments == nil {
		canonical.Arguments = map[string]any{}
	}
	normalizePathArgument := func(key, fallback string) {
		value := strings.TrimSpace(fmt.Sprint(canonical.Arguments[key]))
		if value == "" || value == "<nil>" {
			value = fallback
		}
		canonical.Arguments[key] = filepath.ToSlash(filepath.Clean(value))
	}
	setDefault := func(key string, value any) {
		if current, ok := canonical.Arguments[key]; !ok || current == nil || strings.TrimSpace(fmt.Sprint(current)) == "" {
			canonical.Arguments[key] = value
		}
	}
	switch canonical.Name {
	case "list_files":
		normalizePathArgument("path", ".")
		setDefault("max", 200)
	case "tree":
		normalizePathArgument("path", ".")
		setDefault("depth", 2)
	case "read_file":
		normalizePathArgument("path", ".")
		setDefault("start_line", 1)
		setDefault("end_line", 0)
	case "search":
		normalizePathArgument("path", ".")
		setDefault("max", 80)
	case "grep_regex":
		normalizePathArgument("path", ".")
		setDefault("max", 80)
		setDefault("case_sensitive", false)
	case "find_symbol", "find_refs":
		normalizePathArgument("path", ".")
		setDefault("max", 80)
	case "dependency_graph":
		normalizePathArgument("path", ".")
		setDefault("max", 200)
	case "git_diff", "git_log":
		if _, ok := canonical.Arguments["path"]; ok {
			normalizePathArgument("path", "")
		}
	}
	return tools.Fingerprint(canonical)
}

func (r Runner) readFileSignature(call tools.Call) string {
	path := strings.TrimSpace(fmt.Sprint(call.Arguments["path"]))
	if path == "" || path == "<nil>" {
		return ""
	}
	resolved, err := r.Tools.ResolvePath(path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func isCacheableReadTool(name string) bool {
	switch name {
	case "list_files", "tree", "read_file", "search", "grep_regex", "find_symbol", "find_refs", "file_summary", "dependency_graph", "detect_project_type", "list_dependencies", "web_fetch", "git_status", "git_diff", "git_log", "git_blame":
		return true
	default:
		return false
	}
}

func shouldSuppressRepeatedTool(name string) bool {
	switch name {
	case "list_files", "tree":
		return true
	default:
		return false
	}
}
