package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

type dispatchedAction struct {
	Call    tools.Call
	Purpose string
	Result  tools.Result
	Pending *PendingApproval
}

// canParallelActions accepts either an independent read batch or an explicit
// batch of approval-free writes to disjoint files. Write batches are executed
// atomically: every target is snapshotted and the complete batch is rolled back
// if any member fails.
func (r Runner) canParallelActions(actions []modelToolAction) bool {
	if len(actions) < 2 {
		return false
	}
	if capable, ok := r.Provider.(llm.CapableProvider); ok {
		caps := capable.Capabilities()
		if caps.MaxParallelTools == 1 {
			return false
		}
	}
	seenCalls := map[string]bool{}
	seenPaths := []string{}
	var batchRisk tools.Risk
	for _, item := range actions {
		if len(item.DependsOn) > 0 {
			return false
		}
		call, err := r.normalizeToolCall(tools.Call{Name: firstNonEmpty(item.Name, item.Tool), Arguments: cloneArguments(item.Arguments)})
		if err != nil || call.Name == "delegate" || r.requiresApproval(call) {
			return false
		}
		risk := r.toolRisk(call.Name)
		if risk != tools.RiskRead && risk != tools.RiskWrite {
			return false
		}
		if batchRisk == "" {
			batchRisk = risk
		} else if batchRisk != risk {
			return false
		}
		fingerprint := toolFingerprint(call)
		if seenCalls[fingerprint] {
			return false
		}
		seenCalls[fingerprint] = true
		if risk == tools.RiskWrite {
			if call.Name != "apply_patch" && call.Name != "replace_in_file" {
				return false
			}
			path := normalizePath(fmt.Sprint(call.Arguments["path"]))
			if path == "" {
				return false
			}
			for _, existing := range seenPaths {
				if pathsOverlap(existing, path) {
					return false
				}
			}
			seenPaths = append(seenPaths, path)
		}
	}
	return true
}

func pathsOverlap(left, right string) bool {
	left = strings.Trim(filepath.ToSlash(filepath.Clean(left)), "/")
	right = strings.Trim(filepath.ToSlash(filepath.Clean(right)), "/")
	if left == "" || right == "" {
		return true
	}
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func (r Runner) executeParallelActions(ctx context.Context, state *runState, action modelAction, iteration int, emit func(StreamUpdate)) []dispatchedAction {
	out := make([]dispatchedAction, len(action.Actions))
	writeBatch := r.parallelActionRisk(action.Actions) == tools.RiskWrite && !r.Config.AgentDryRun
	var snapshots []atomicFileSnapshot
	if writeBatch {
		var err error
		snapshots, err = r.snapshotAtomicWriteTargets(action.Actions)
		if err != nil {
			for index, item := range action.Actions {
				name := firstNonEmpty(item.Name, item.Tool, "write")
				out[index] = dispatchedAction{
					Call:   tools.Call{Name: name, Arguments: cloneArguments(item.Arguments)},
					Result: tools.Result{Tool: name, OK: false, Error: "atomic write batch was not started: " + err.Error(), Metadata: map[string]any{"atomic_batch": true, "snapshot_failed": true}},
				}
			}
			return out
		}
	}
	limit := r.Config.AgentMaxParallelTools
	if limit < 1 {
		limit = 4
	}
	if capable, ok := r.Provider.(llm.CapableProvider); ok {
		if providerLimit := capable.Capabilities().MaxParallelTools; providerLimit > 0 && limit > providerLimit {
			limit = providerLimit
		}
	}
	if limit > 8 {
		limit = 8
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for index, item := range action.Actions {
		index, item := index, item
		call := tools.Call{Name: firstNonEmpty(item.Name, item.Tool), Arguments: cloneArguments(item.Arguments)}
		if normalized, err := r.normalizeToolCall(call); err == nil {
			call = normalized
		}
		purpose := firstNonEmpty(item.Purpose, item.ExpectedResult, action.Summary, "Gather independent evidence.")
		out[index] = dispatchedAction{Call: call, Purpose: purpose}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					panicCtx := debuglog.WithScope(ctx, debuglog.Scope{Tool: call.Name, Workspace: r.Tools.WorkspaceRoot, Iteration: iteration})
					crashPath, _ := debuglog.RecordCrash(panicCtx, "agent.parallel_tool", recovered, debug.Stack(), map[string]any{
						"batch_size": len(action.Actions), "batch_index": index, "fingerprint": toolFingerprint(call),
					})
					out[index].Result = tools.Result{Tool: call.Name, OK: false, Error: fmt.Sprintf("parallel tool worker panic recovered: %v", recovered), Metadata: map[string]any{
						"parallel": true, "panic_recovered": true, "debug_logged": true, "crash_report": crashPath,
					}}
				}
			}()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				out[index].Result = tools.Result{Tool: call.Name, OK: false, Error: ctx.Err().Error(), Metadata: map[string]any{"parallel": true}}
				return
			}
			result, pending := r.executeAction(ctx, state, call, purpose, iteration, emit)
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["parallel"] = true
			result.Metadata["parallel_batch_size"] = len(action.Actions)
			result.Metadata["parallel_batch_index"] = index
			out[index].Result, out[index].Pending = result, pending
		}()
	}
	wg.Wait()
	failure := ""
	for index := range out {
		if out[index].Pending != nil {
			out[index].Result = tools.Result{Tool: out[index].Call.Name, OK: false, Error: fmt.Sprintf("parallel dispatcher unexpectedly received approval requirement for %s", out[index].Call.Name)}
			out[index].Pending = nil
		}
		if !out[index].Result.OK && failure == "" {
			failure = firstNonEmpty(out[index].Result.Error, out[index].Result.Output, out[index].Call.Name+" failed")
		}
	}
	if writeBatch && failure != "" {
		rollbackErr := restoreAtomicWriteTargets(r.Tools.WorkspaceRoot, snapshots)
		for index := range out {
			if out[index].Result.Metadata == nil {
				out[index].Result.Metadata = map[string]any{}
			}
			out[index].Result.Metadata["atomic_batch"] = true
			out[index].Result.Metadata["rolled_back"] = rollbackErr == nil
			if out[index].Result.OK {
				out[index].Result.OK = false
				out[index].Result.Output = ""
				out[index].Result.Error = "atomic write batch rolled back because another write failed: " + failure
			}
			if rollbackErr != nil {
				out[index].Result.Error = firstNonEmpty(out[index].Result.Error, failure) + "; rollback error: " + rollbackErr.Error()
			}
		}
	} else if writeBatch {
		for index := range out {
			if out[index].Result.Metadata == nil {
				out[index].Result.Metadata = map[string]any{}
			}
			out[index].Result.Metadata["atomic_batch"] = true
		}
	}
	return out
}

func (r Runner) parallelActionRisk(actions []modelToolAction) tools.Risk {
	var risk tools.Risk
	for _, item := range actions {
		call, err := r.normalizeToolCall(tools.Call{Name: firstNonEmpty(item.Name, item.Tool), Arguments: cloneArguments(item.Arguments)})
		if err != nil {
			return ""
		}
		current := r.toolRisk(call.Name)
		if risk == "" {
			risk = current
		} else if risk != current {
			return ""
		}
	}
	return risk
}

type atomicFileSnapshot struct {
	Path    string
	Existed bool
	Mode    os.FileMode
	Data    []byte
}

func (r Runner) snapshotAtomicWriteTargets(actions []modelToolAction) ([]atomicFileSnapshot, error) {
	snapshots := make([]atomicFileSnapshot, 0, len(actions))
	for _, item := range actions {
		call, err := r.normalizeToolCall(tools.Call{Name: firstNonEmpty(item.Name, item.Tool), Arguments: cloneArguments(item.Arguments)})
		if err != nil {
			return nil, err
		}
		path, err := r.Tools.ResolvePath(strings.TrimSpace(fmt.Sprint(call.Arguments["path"])))
		if err != nil {
			return nil, err
		}
		snapshot := atomicFileSnapshot{Path: path}
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return nil, fmt.Errorf("atomic write target is a directory: %s", path)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, readErr
			}
			snapshot.Existed = true
			snapshot.Mode = info.Mode()
			snapshot.Data = data
		case os.IsNotExist(statErr):
			// New files are removed during rollback.
		default:
			return nil, statErr
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func restoreAtomicWriteTargets(root string, snapshots []atomicFileSnapshot) error {
	var failures []string
	for _, snapshot := range snapshots {
		if !snapshot.Existed {
			if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
			removeEmptyParents(filepath.Dir(snapshot.Path), root)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(snapshot.Path), 0o700); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := os.WriteFile(snapshot.Path, snapshot.Data, snapshot.Mode.Perm()); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := os.Chmod(snapshot.Path, snapshot.Mode.Perm()); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func removeEmptyParents(directory, root string) {
	root = filepath.Clean(root)
	for directory = filepath.Clean(directory); directory != root && strings.HasPrefix(directory, root+string(os.PathSeparator)); directory = filepath.Dir(directory) {
		if err := os.Remove(directory); err != nil {
			return
		}
	}
}
