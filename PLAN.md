# Plan: Tool Calling Compatibility + Subagent System

## Problem 1: Provider Tool Calling Failures (NVIDIA EOF)

**Root cause:** Providers like NVIDIA stream tool call arguments incrementally but sometimes
truncate mid-string or mid-object, producing `unexpected EOF` during JSON decode. The current
`closeJSONStructures()` in `internal/llm/tool_protocol.go:234-277` already refuses to repair
truncated strings (safety feature), but the caller gets a hard error with no retry.

**Current flow:**
1. Provider streams partial tool arguments
2. `decodeToolArgumentsString()` accumulates into a string
3. `json.Decoder.Decode()` fails with `io.ErrUnexpectedEOF`
4. `generateToolDecisionWithRetry()` in `internal/agent/provider_retry.go` classifies the error
5. If the error is `providerErrorToolProtocol`, it switches to universal (text) mode
6. But it does NOT retry the same native call with the same provider — it gives up on native tools

**Fix strategy:** Add provider-specific retry with argument re-accumulation before falling back.

---

## Problem 2: Subagent with Lighter Model

**Current state:** The `delegate` tool exists (`internal/agent/agent.go:1177-1205`) but uses
the **same provider** as the parent. For simple tasks (explore, review, debug), using a
large/expensive model wastes tokens.

**Goal:** Allow the main agent to delegate simple tasks to a lighter model (e.g., Qwen 8B
for exploration, GPT-4o-mini for review) while keeping the heavy model for complex work.

---

## Problem 3: Feature Toggle

Need a config toggle and TUI command to enable/disable the subagent system and control
which model it uses.

---

## Implementation Plan

### Phase 1: Provider Tool Calling Resilience

**Files to modify:**
- `internal/llm/tool_protocol.go`
- `internal/agent/provider_retry.go`
- `internal/llm/openai_chat_stream.go`
- `internal/llm/anthropic_stream.go`
- `internal/llm/openai_responses.go`

#### 1a. Add argument re-accumulation retry to tool protocol

**`internal/llm/tool_protocol.go`:**

Add a new exported function:
```go
// RepairTruncatedToolCall attempts to salvage a tool call whose arguments were
// truncated mid-stream. It returns the repaired JSON and true if the truncation
// is safe to repair (not inside a string literal), or empty string and false if
// the truncation is unsafe.
func RepairTruncatedToolCall(raw string) (string, bool)
```

This function:
1. Calls `firstJSONObject()` to find the JSON boundary
2. Calls `closeJSONStructures()` to attempt structural closure
3. If `closeJSONStructures` returns `inString=true`, returns `("", false)` — unsafe
4. If safe, attempts `decodeJSONObject()` on the repaired string
5. Returns the repaired JSON and success status

This is essentially a clean API wrapper around the existing `closeJSONStructures` + decode flow.

#### 1b. Add tool protocol error classification

**`internal/llm/provider_retry.go`:**

Add a new error class:
```go
const providerErrorToolTruncated providerErrorClass = "tool_truncated"
```

Modify `classifyProviderError()` to detect `unexpected EOF` / `io.ErrUnexpectedEOF` in tool
decision results specifically (not general generation errors). This distinguishes "provider
returned a tool call but it was truncated" from "provider returned garbage."

#### 1c. Add native-tool retry with fresh generation

**`internal/agent/provider_retry.go`:**

Modify `generateToolDecisionWithRetry()` to add a new retry path for `providerErrorToolTruncated`:

```
Attempt 1: Normal GenerateWithTools
  → If tool_truncated and attempts < 2:
    - Log the truncation as a recovery event
    - Retry GenerateWithTools with the same request (provider may return complete args)
    - Do NOT consume the general retry budget (this is a transport-level recovery)
  → If still truncated on attempt 2:
    - Fall through to universal tool gateway (existing behavior)
```

Key: This retry is **free** — it does not count against `ProviderMaxRetries` because it's a
transport-level fix, not a provider error.

#### 1d. Stream-level argument validation

**`internal/llm/openai_chat_stream.go`, `internal/llm/anthropic_stream.go`, `internal/llm/openai_responses.go`:**

In each streaming accumulator, add a **flush-time validation** step:

When the stream signals completion (end-of-stream marker), before returning the accumulated
tool calls, validate each argument string:
1. Call `decodeToolArgumentsString()` on each accumulated argument
2. If it fails with `unexpected EOF`, set a `Truncated` flag on the `ToolCall`
3. Return the truncated flag in the `ToolDecision` metadata

This lets the caller know which specific tool calls are truncated, enabling targeted retry.

**New field in `internal/llm/provider.go`:**
```go
type ToolCall struct {
    ID        string
    Name      string
    Arguments map[string]any
    Truncated bool  // true if arguments were truncated during streaming
}
```

#### 1e. Add provider-specific truncation workarounds

**`internal/llm/tool_protocol.go`:**

Add a "lenient" decode mode for known-troublesome providers:

```go
// DecodeToolArgumentsLenient attempts strict decode first, then falls back to
// structural repair for providers known to truncate tool arguments.
func DecodeToolArgumentsLenient(raw string) (map[string]any, error)
```

This function:
1. Tries `decodeToolArgumentsString()` first (strict)
2. If that fails with EOF, tries `RepairTruncatedToolCall()`
3. If repair succeeds but the result is missing required fields, returns the error
4. If repair fails (unsafe truncation in string), returns the original error

---

### Phase 2: Subagent with Lighter Model

**Files to modify:**
- `internal/agent/agent.go` (modify `runDelegate`)
- `internal/config/config.go` (add subagent config)
- `internal/llm/provider.go` (add lightweight provider factory)
- `internal/tui/commands.go` (add `/subagent` command)

#### 2a. Add subagent config fields

**`internal/config/config.go`:**

Add to `Config` struct:
```go
// Subagent configuration
SubagentEnabled    bool   `json:"subagent_enabled,omitempty"`     // master toggle
SubagentProvider   string `json:"subagent_provider,omitempty"`    // e.g., "ollama"
SubagentModel      string `json:"subagent_model,omitempty"`       // e.g., "qwen3:8b"
SubagentMaxSteps   int    `json:"subagent_max_steps,omitempty"`   // default: 4
SubagentMaxTokens  int64  `json:"subagent_max_tokens,omitempty"`  // default: 2000
```

Update `Default()` to set:
```go
SubagentEnabled:  true,
SubagentProvider: "",     // empty = inherit from parent
SubagentModel:    "",     // empty = inherit from parent
SubagentMaxSteps: 4,
SubagentMaxTokens: 2000,
```

Update `normalize()` to apply bounds:
```go
if cfg.SubagentMaxSteps < 1 { cfg.SubagentMaxSteps = 1 }
if cfg.SubagentMaxSteps > 8 { cfg.SubagentMaxSteps = 8 }
if cfg.SubagentMaxTokens < 500 { cfg.SubagentMaxTokens = 500 }
if cfg.SubagentMaxTokens > 8000 { cfg.SubagentMaxTokens = 8000 }
```

#### 2b. Add subagent provider factory

**`internal/llm/provider.go`:**

Add a new constructor:
```go
// NewSubagentProvider creates a lightweight provider for subagent tasks.
// If subagentProvider/subagentModel are empty, falls back to the parent provider.
func NewSubagentProvider(cfg config.Config) Provider
```

This function:
1. If `cfg.SubagentProvider != "" && cfg.SubagentModel != ""`:
   - Build a temporary config with the subagent provider/model
   - Call `New(tempCfg)` to create the provider
2. Otherwise, return `nil` (caller uses parent provider)

#### 2c. Modify `runDelegate` to use lighter provider

**`internal/agent/agent.go`:**

Modify `runDelegate` (line 1177-1205):
```go
func (r Runner) runDelegate(ctx context.Context, call tools.Call) tools.Result {
    // ... existing task/role extraction ...

    // Determine provider for subagent
    var subProvider llm.Provider
    if r.Config.SubagentEnabled {
        subProvider = llm.NewSubagentProvider(r.Config)
    }
    if subProvider == nil {
        subProvider = r.Provider  // fallback to parent provider
    }

    cfg := r.Config
    cfg.ApprovalPolicy = config.ApprovalReadOnly
    cfg.AgentMaxSteps = minInt(maxInt(2, cfg.SubagentMaxSteps), cfg.SubagentMaxSteps)
    cfg.AgentAutoVerify = false

    session := history.New("delegate-"+role, subProvider.Name(), cfg.Model(), cfg.Mode)
    session.Append("user", task)

    sub := NewRunner(cfg, subProvider)
    sub.delegationDepth = r.delegationDepth + 1
    sub.delegateRole = role

    result := sub.run(ctx, session, nil)

    // ... rest unchanged ...
}
```

#### 2d. Add auto-routing heuristic

**`internal/agent/agent.go`:**

Add a new helper method to the Runner:
```go
// shouldDelegateToSubagent returns true if the current task is simple enough
// to benefit from a lighter model. This is used for auto-delegation when the
// main agent detects a task that doesn't need its full capability.
func (r Runner) shouldDelegateToSubagent(task string) bool
```

Heuristics:
- Read-only exploration tasks (file listing, grep, simple questions)
- Review/critique tasks (already delegated via `AgentAutoReview`)
- Tasks with low complexity score (reuse `reasoning.ClassifyComplexity`)
- The task doesn't involve file writes or complex reasoning

This method is used by the main agent to **automatically** delegate simple tasks without
the model explicitly calling the `delegate` tool. This saves tokens because the lighter
model processes fewer input tokens.

#### 2e. Add auto-delegation in the agent loop

**`internal/agent/agent.go`:**

In the `run()` method, after the model generates a decision but before executing tools:

```go
// Auto-delegate simple tasks to subagent if enabled
if r.Config.SubagentEnabled && r.delegationDepth == 0 && len(action.Actions) > 0 {
    for i, act := range action.Actions {
        if r.shouldDelegateSimpleToolCall(act) {
            // Replace with a delegate call to the subagent
            action.Actions[i] = modelToolAction{
                Call: tools.Call{
                    Name: "delegate",
                    Arguments: map[string]any{
                        "task": fmt.Sprintf("Execute tool %s: %s", act.Call.Name, marshalArgs(act.Call.Arguments)),
                        "role": "explore",
                    },
                },
            }
        }
    }
}
```

The `shouldDelegateSimpleToolCall` method checks:
- The tool is read-only (`tools.RiskRead`)
- The tool doesn't have side effects
- The subagent is enabled

---

### Phase 3: Feature Toggle and TUI Integration

**Files to modify:**
- `internal/config/config.go` (already covered in 2a)
- `internal/tui/commands.go` (add slash commands)
- `internal/tui/features.go` (add feature flags)
- `internal/agent/agent.go` (update system prompt)

#### 3a. Add TUI slash commands

**`internal/tui/commands.go`:**

Add new command handlers:
```go
// /subagent on|off — toggle subagent system
// /subagent model <provider>/<model> — set the subagent model
// /subagent status — show current subagent configuration
```

These map to `cfg.SubagentEnabled`, `cfg.SubagentProvider`, `cfg.SubagentModel`.

#### 3b. Update system prompt

**`internal/agent/agent.go`:**

In `systemPrompt()`, add a section when subagent is enabled:
```
You have access to a lightweight subagent specialist for read-only exploration,
review, and debugging tasks. Use the `delegate` tool to offload simple tasks:
- File exploration and search
- Code review and critique
- Debugging isolated issues
- Answering factual questions about the codebase

The subagent uses a smaller, faster model to save tokens. Delegate when the task
doesn't require file modifications or complex multi-step reasoning.
```

#### 3c. Add feature flag display

**`internal/tui/features.go`:**

Add the subagent to the feature registry so it shows in `/status` and the TUI header:
```go
{
    Name:    "subagent",
    Enabled: cfg.SubagentEnabled,
    Detail:  fmt.Sprintf("%s/%s", cfg.SubagentProvider, cfg.SubagentModel),
}
```

---

## File Change Summary

| File | Changes |
|------|---------|
| `internal/llm/tool_protocol.go` | Add `RepairTruncatedToolCall()`, `DecodeToolArgumentsLenient()` |
| `internal/llm/provider.go` | Add `ToolCall.Truncated` field, `NewSubagentProvider()` |
| `internal/llm/openai_chat_stream.go` | Add flush-time validation, set `Truncated` flag |
| `internal/llm/anthropic_stream.go` | Add flush-time validation, set `Truncated` flag |
| `internal/llm/openai_responses.go` | Add flush-time validation, set `Truncated` flag |
| `internal/agent/provider_retry.go` | Add `providerErrorToolTruncated`, native-tool retry path |
| `internal/agent/agent.go` | Modify `runDelegate`, add `shouldDelegateToSubagent`, update system prompt |
| `internal/config/config.go` | Add subagent config fields, defaults, normalization |
| `internal/tui/commands.go` | Add `/subagent` command handlers |
| `internal/tui/features.go` | Add subagent feature flag |

## Verification

1. Run `go vet ./...` — must pass
2. Run `go test ./internal/llm/...` — tool protocol tests
3. Run `go test ./internal/agent/...` — agent delegation tests
4. Run `go test ./internal/config/...` — config normalization tests
5. Manual test: Connect to NVIDIA provider, trigger tool calls, verify retry works
6. Manual test: Enable subagent, verify delegation to lighter model
7. Manual test: Toggle `/subagent off`, verify delegation is disabled
