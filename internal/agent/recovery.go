package agent

import (
	"context"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/tools"
)

type errorClass string

const (
	errorNone      errorClass = "none"
	errorTransient errorClass = "transient"
	errorTimeout   errorClass = "timeout"
	errorDenied    errorClass = "denied"
	errorInvalid   errorClass = "invalid"
	errorPermanent errorClass = "permanent"
)

func classifyToolError(result tools.Result) errorClass {
	if result.OK {
		return errorNone
	}
	text := strings.ToLower(result.Error + " " + result.Output)
	switch {
	case strings.Contains(text, "deadline") || strings.Contains(text, "timed out") || strings.Contains(text, "timeout"):
		return errorTimeout
	case strings.Contains(text, "permission") || strings.Contains(text, "denied") || strings.Contains(text, "approval"):
		return errorDenied
	case strings.Contains(text, "requires") || strings.Contains(text, "does not accept") || strings.Contains(text, "must be") || strings.Contains(text, "unknown tool"):
		return errorInvalid
	case strings.Contains(text, "temporar") || strings.Contains(text, "connection reset") || strings.Contains(text, "eof") || strings.Contains(text, "503") || strings.Contains(text, "429"):
		return errorTransient
	default:
		return errorPermanent
	}
}

func (r Runner) executeWithRecovery(ctx context.Context, call tools.Call, iteration int, emit func(StreamUpdate)) tools.Result {
	attempts := 1
	if r.toolRisk(call.Name) == tools.RiskRead {
		attempts += maxInt(0, minInt(r.Config.AgentReadRetries, 3))
	}
	var result tools.Result
	for attempt := 1; attempt <= attempts; attempt++ {
		timeout := time.Duration(r.Config.AgentToolTimeoutSec) * time.Second
		if timeout < 5*time.Second {
			timeout = 120 * time.Second
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		if r.hasMCPTool(call.Name) {
			result = r.executeMCPTool(callCtx, call)
		} else {
			result = r.Tools.ExecuteStream(callCtx, call, func(chunk string) {
				if emit != nil && chunk != "" {
					emit(StreamUpdate{Kind: StreamToolProgress, Phase: "tool output", Iteration: iteration, Tool: call.Name, Delta: chunk})
				}
			})
		}
		cancel()
		class := classifyToolError(result)
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.Metadata["attempt"] = attempt
		result.Metadata["attempts_allowed"] = attempts
		result.Metadata["error_class"] = string(class)
		if result.OK {
			result.Metadata["recovered_after_retry"] = attempt > 1
			return result
		}
		if attempt >= attempts || (class != errorTransient && class != errorTimeout) || ctx.Err() != nil {
			return result
		}
		timer := time.NewTimer(time.Duration(attempt*75) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result
		case <-timer.C:
		}
	}
	return result
}
