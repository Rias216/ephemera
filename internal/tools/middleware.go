package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
)

// Middleware wraps one tool execution stage. Chains are composed in reverse so
// the first middleware is the outermost lifecycle boundary.
type Middleware func(Handler) Handler

type approvalGrantKey struct{}

// WithApproval marks one execution context as explicitly approved by the user.
// The grant is scoped to the current call chain and is not persisted globally.
func WithApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalGrantKey{}, true)
}

func approvalGranted(ctx context.Context) bool {
	granted, _ := ctx.Value(approvalGrantKey{}).(bool)
	return granted
}

func defaultMiddlewareChain() []Middleware {
	return []Middleware{
		panicRecoveryMiddleware(),
		normalizationMiddleware(),
		debugMiddleware(),
		workspaceScopeMiddleware(),
		approvalMiddleware(),
		dryRunMiddleware(),
		timeoutMiddleware(),
		sandboxMiddleware(),
	}
}

func panicRecoveryMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) (result Result) {
			ctx = debuglog.WithScope(ctx, debuglog.Scope{Tool: call.Name, Workspace: r.WorkspaceRoot})
			defer func() {
				if recovered := recover(); recovered != nil {
					stack := debug.Stack()
					message := fmt.Sprintf("tool panic recovered: %v", recovered)
					crashPath, _ := debuglog.RecordCrash(ctx, "tool", recovered, stack, map[string]any{
						"tool": call.Name, "workspace": r.WorkspaceRoot, "fingerprint": Fingerprint(call),
					})
					failed := false
					_ = debuglog.AppendTool(ctx, debuglog.ToolRecord{
						Stage: "panic", Tool: call.Name, Fingerprint: Fingerprint(call), Arguments: auditToolArguments(call.Arguments),
						OK: &failed, Error: message, Metadata: map[string]any{"crash_report": crashPath},
					})
					result = Result{
						Tool: call.Name, OK: false, Error: message,
						Metadata: map[string]any{"panic_recovered": true, "debug_logged": true, "crash_report": crashPath},
					}
				}
			}()
			return next(ctx, r, call, emit)
		}
	}
}

func normalizationMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			normalized, err := r.Normalize(call)
			if err != nil {
				ctx = debuglog.WithScope(ctx, debuglog.Scope{Tool: call.Name, Workspace: r.WorkspaceRoot})
				hint := RepairHint(call, err)
				message := err.Error()
				if hint != "" {
					message += ". Recovery: " + hint
				}
				args := auditToolArguments(call.Arguments)
				failed := false
				_ = debuglog.AppendTool(ctx, debuglog.ToolRecord{
					Stage: "normalization_failed", Tool: call.Name, Fingerprint: Fingerprint(call), Arguments: args,
					OK: &failed, Error: message, Metadata: map[string]any{"workspace": r.WorkspaceRoot, "repair_hint": hint},
				})
				debuglog.FailureCtx(ctx, "tool", "tool call normalization failed", message, map[string]any{
					"fingerprint": Fingerprint(call), "workspace": r.WorkspaceRoot, "arguments": args, "repair_hint": hint,
				})
				return Result{Tool: call.Name, OK: false, Error: message, Metadata: map[string]any{"debug_logged": true, "error_class": "normalization", "repair_hint": hint}}
			}
			return next(ctx, r, normalized, emit)
		}
	}
}

func workspaceScopeMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			targets, err := r.externalTargets(call)
			if err != nil {
				return fail(call.Name, err.Error())
			}
			approved := approvalGranted(ctx) || r.ApprovalPolicy == config.ApprovalAutoApprove
			if len(targets) > 0 && !approved {
				return Result{Tool: call.Name, OK: false, Error: "access outside the active workspace requires explicit approval: " + strings.Join(targets, ", "), Metadata: map[string]any{
					"approval_required": true, "external_targets": targets, "workspace": r.WorkspaceRoot,
				}}
			}
			if approved {
				r.allowExternalPaths = true
			}
			return next(ctx, r, call, emit)
		}
	}
}

func approvalMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			tool, _ := r.Lookup(call.Name)
			switch r.ApprovalPolicy {
			case config.ApprovalChat:
				return Result{Tool: call.Name, OK: false, Error: "agent tools are disabled by chat approval policy", Metadata: map[string]any{"approval_policy": string(r.ApprovalPolicy), "approval_required": true}}
			case config.ApprovalReadOnly:
				if tool.Risk != RiskRead {
					return Result{Tool: call.Name, OK: false, Error: "write and shell tools are disabled by read-only policy", Metadata: map[string]any{"approval_policy": string(r.ApprovalPolicy), "risk": string(tool.Risk)}}
				}
			case config.ApprovalApproveWrites:
				if tool.Risk != RiskRead && !approvalGranted(ctx) {
					return Result{Tool: call.Name, OK: false, Error: "tool requires explicit user approval", Metadata: map[string]any{"approval_policy": string(r.ApprovalPolicy), "approval_required": true, "risk": string(tool.Risk)}}
				}
			}
			return next(ctx, r, call, emit)
		}
	}
}

func dryRunMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			tool, _ := r.Lookup(call.Name)
			if r.DryRun && tool.Risk != RiskRead {
				return r.preview(call)
			}
			return next(ctx, r, call, emit)
		}
	}
}

func timeoutMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			timeout := r.CommandTimeout
			if timeout <= 0 {
				timeout = 2 * time.Minute
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			result := next(callCtx, r, call, emit)
			if callCtx.Err() == context.DeadlineExceeded {
				result.OK = false
				result.Tool = call.Name
				result.Error = "tool execution timed out"
			}
			return result
		}
	}
}

type sandboxRoute struct {
	mode       string
	dockerPath string
	image      string
}

type sandboxRouteKey struct{}

func sandboxMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			tool, _ := r.Lookup(call.Name)
			route := sandboxRoute{mode: "host"}
			if tool.Risk == RiskShell && r.SandboxMode == config.SandboxDocker {
				dockerPath, err := exec.LookPath("docker")
				if err != nil {
					return Result{Tool: call.Name, OK: false, Error: "docker sandbox requested, but docker is not installed or not on PATH", Metadata: map[string]any{"sandbox": "docker"}}
				}
				image := r.dockerSandboxImage()
				inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				inspect := exec.CommandContext(inspectCtx, dockerPath, "image", "inspect", image)
				err = inspect.Run()
				cancel()
				if err != nil {
					return Result{Tool: call.Name, OK: false, Error: "docker sandbox image is unavailable locally: " + image + " (pull it before running with network isolation)", Metadata: map[string]any{"sandbox": "docker", "image": image}}
				}
				route = sandboxRoute{mode: "docker", dockerPath: dockerPath, image: image}
			}
			return next(context.WithValue(ctx, sandboxRouteKey{}, route), r, call, emit)
		}
	}
}

func debugMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			ctx = debuglog.WithScope(ctx, debuglog.Scope{Tool: call.Name, Workspace: r.WorkspaceRoot})
			started := time.Now()
			fingerprint := Fingerprint(call)
			tool, _ := r.Lookup(call.Name)
			approval := "not_required"
			if tool.Risk != RiskRead {
				approval = "policy_auto"
				if approvalGranted(ctx) {
					approval = "granted"
				} else if r.ApprovalPolicy != config.ApprovalAutoApprove {
					approval = "not_granted"
				}
			}
			auditArgs := auditToolArguments(call.Arguments)
			_ = debuglog.AppendTool(ctx, debuglog.ToolRecord{
				Stage: "started", Tool: call.Name, Fingerprint: fingerprint, Risk: string(tool.Risk), Approval: approval,
				Arguments: auditArgs, Metadata: map[string]any{"dry_run": r.DryRun, "sandbox": string(r.SandboxMode)},
			})

			result := next(ctx, r, call, emit)
			result.Tool = call.Name
			result.Duration = time.Since(started)
			if result.OK && strings.TrimSpace(result.Output) == "" {
				result.Output = emptySuccessOutput(call.Name)
			}
			result.Output, result.Summary = truncateApproxTokensWithSummary(result.Output, r.MaxOutputTokens)
			result.Error, _ = truncateApproxTokensWithSummary(result.Error, r.MaxOutputTokens)
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["ok"] = result.OK
			result.Metadata["risk"] = string(tool.Risk)
			result.Metadata["duration_ms"] = result.Duration.Milliseconds()
			result.Metadata["fingerprint"] = fingerprint
			if result.Summary != "" {
				result.Metadata["summary"] = result.Summary
			}
			outputSummary := strings.TrimSpace(result.Summary)
			if outputSummary == "" {
				outputSummary = compactAuditText(result.Output, 1200)
			}
			okValue := result.OK
			_ = debuglog.AppendTool(ctx, debuglog.ToolRecord{
				Stage: "completed", Tool: call.Name, Fingerprint: fingerprint, Risk: string(tool.Risk), Approval: approval,
				Arguments: auditArgs, OK: &okValue, DurationMS: result.Duration.Milliseconds(), Error: result.Error,
				Output: outputSummary, Metadata: result.Metadata,
			})
			fields := map[string]any{
				"risk": string(tool.Risk), "workspace": r.WorkspaceRoot, "duration_ms": result.Duration.Milliseconds(),
				"fingerprint": fingerprint, "ok": result.OK, "approval": approval, "arguments": auditArgs,
			}
			if outputSummary != "" {
				fields["output_summary"] = compactAuditText(outputSummary, 1200)
			}
			if len(result.Metadata) > 0 {
				fields["result_metadata"] = result.Metadata
			}
			if result.OK {
				_ = debuglog.WriteCtx(ctx, "info", "tool", "tool execution completed", "tool call completed", fields)
			} else {
				message := strings.TrimSpace(result.Error)
				if message == "" {
					message = strings.TrimSpace(result.Output)
				}
				fields["error"] = compactAuditText(message, 1200)
				debuglog.FailureCtx(ctx, "tool", "tool execution failed", message, fields)
				result.Metadata["debug_logged"] = true
			}
			return result
		}
	}
}

// AuditArguments returns the bounded, redacted argument representation used in
// session diagnostics. Large file bodies are stored as hashes rather than copied.
func AuditArguments(arguments map[string]any) map[string]any {
	return auditToolArguments(arguments)
}

func auditToolArguments(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return nil
	}
	out := make(map[string]any, len(arguments))
	for key, value := range arguments {
		out[key] = auditArgumentValue(strings.ToLower(strings.TrimSpace(key)), value, 0)
	}
	return out
}

func auditArgumentValue(key string, value any, depth int) any {
	if depth > 5 {
		return "[TRUNCATED]"
	}
	if auditPayloadKey(key) {
		return auditPayloadSummary(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = auditArgumentValue(strings.ToLower(strings.TrimSpace(childKey)), childValue, depth+1)
		}
		return out
	case []any:
		limit := min(len(typed), 32)
		out := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			out = append(out, auditArgumentValue("", item, depth+1))
		}
		if len(typed) > limit {
			out = append(out, fmt.Sprintf("[%d more items]", len(typed)-limit))
		}
		return out
	case string:
		return compactAuditText(typed, 4000)
	default:
		return typed
	}
}

func auditPayloadKey(key string) bool {
	switch key {
	case "content", "old", "new", "body", "data", "diff", "patch":
		return true
	default:
		return false
	}
}

func auditPayloadSummary(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		data = []byte(fmt.Sprint(value))
	}
	sum := sha256.Sum256(data)
	return map[string]any{
		"bytes":  len(data),
		"sha256": fmt.Sprintf("%x", sum[:]),
	}
}

func compactAuditText(value string, limit int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…[TRUNCATED]"
}
