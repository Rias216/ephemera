package tools

import (
	"context"
	"os/exec"
	"strings"
	"time"

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
		debugMiddleware(),
		normalizationMiddleware(),
		workspaceScopeMiddleware(),
		approvalMiddleware(),
		dryRunMiddleware(),
		timeoutMiddleware(),
		sandboxMiddleware(),
	}
}

func normalizationMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
			normalized, err := r.Normalize(call)
			if err != nil {
				return fail(call.Name, err.Error())
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
				return fail(call.Name, "access outside the active workspace requires explicit approval: "+strings.Join(targets, ", "))
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
				return fail(call.Name, "agent tools are disabled by chat approval policy")
			case config.ApprovalReadOnly:
				if tool.Risk != RiskRead {
					return fail(call.Name, "write and shell tools are disabled by read-only policy")
				}
			case config.ApprovalApproveWrites:
				if tool.Risk != RiskRead && !approvalGranted(ctx) {
					return fail(call.Name, "tool requires explicit user approval")
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
			_ = debuglog.WriteCtx(ctx, "info", "tool", "tool execution started", "tool call accepted by registry", map[string]any{"fingerprint": Fingerprint(call)})
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
			tool, _ := r.Lookup(call.Name)
			result.Metadata["ok"] = result.OK
			result.Metadata["risk"] = string(tool.Risk)
			result.Metadata["duration_ms"] = result.Duration.Milliseconds()
			if result.Summary != "" {
				result.Metadata["summary"] = result.Summary
			}
			_ = debuglog.WriteCtx(ctx, "info", "tool", "tool execution completed", "tool call completed", map[string]any{
				"tool": call.Name, "risk": string(tool.Risk), "workspace": r.WorkspaceRoot,
				"duration_ms": result.Duration.Milliseconds(), "fingerprint": Fingerprint(call), "ok": result.OK,
			})
			if !result.OK {
				message := strings.TrimSpace(result.Error)
				if message == "" {
					message = strings.TrimSpace(result.Output)
				}
				debuglog.FailureCtx(ctx, "tool", "tool execution failed", message, map[string]any{
					"tool": call.Name, "risk": string(tool.Risk), "workspace": r.WorkspaceRoot,
					"duration_ms": result.Duration.Milliseconds(), "fingerprint": Fingerprint(call),
				})
				result.Metadata["debug_logged"] = true
			}
			return result
		}
	}
}
