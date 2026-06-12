package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

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
// batch of approval-free writes to disjoint files. Mixed read/write batches are
// kept sequential because an undeclared dependency is too easy to miss.
func (r Runner) canParallelActions(actions []modelToolAction) bool {
	if len(actions) < 2 {
		return false
	}
	caps := llm.Capabilities(r.Provider)
	if caps.MaxParallelTools == 1 {
		return false
	}
	seenCalls := map[string]bool{}
	seenPaths := []string{}
	var batchRisk tools.Risk
	for _, item := range actions {
		if len(item.DependsOn) > 0 {
			return false
		}
		call, err := r.normalizeToolCall(tools.Call{Name: firstNonEmpty(item.Name, item.Tool), Arguments: cloneArguments(item.Arguments)})
		if err != nil || call.Name == "delegate" || r.requiresApproval(call.Name) {
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
	limit := r.Config.AgentMaxParallelTools
	if limit < 1 {
		limit = 4
	}
	if providerLimit := llm.Capabilities(r.Provider).MaxParallelTools; providerLimit > 0 && limit > providerLimit {
		limit = providerLimit
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
	for index := range out {
		if out[index].Pending != nil {
			out[index].Result = tools.Result{Tool: out[index].Call.Name, OK: false, Error: fmt.Sprintf("parallel dispatcher unexpectedly received approval requirement for %s", out[index].Call.Name)}
			out[index].Pending = nil
		}
	}
	return out
}
