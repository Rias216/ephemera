# Provider Tool-Calling Reliability Plan

## Goal

Make file reads, file writes, edits, git operations, and verification behave consistently across NVIDIA, OpenAI-compatible, OpenAI, Anthropic, Ollama, and future providers without weakening approvals or provider portability.

## Findings

1. **Premature completion:** prose such as “done” was accepted even when the user explicitly requested workspace changes and no tool had run.
2. **Main-agent control loss:** automatic routing replaced eligible core reads with `delegate`, so the main agent sometimes never received the evidence needed to edit or verify.
3. **Streaming corruption:** several OpenAI-compatible endpoints replay cumulative tool-name and argument fragments; blind concatenation produced invalid names and duplicated JSON.
4. **Context starvation:** native tool call/result turns were appended only after conversation fitting, allowing required provider tool history to disappear under pressure.
5. **Over-broad compatibility cache:** one model’s malformed tool protocol could force every model on the same provider into portable mode.
6. **Weak diagnostics:** successful, malformed, and prose-only provider decisions were difficult to compare in the persistent debug log.

## Executed Changes

### Phase 1 — Universal execution contract

- Classify explicit workspace, write, git, and verification requests.
- Block prose-only or empty structured completion until required successful tool evidence exists.
- Retry through the provider-neutral universal tool gateway twice, then stop honestly instead of claiming success.
- Remember silent native-tool refusal per provider and model.
- Exempt advisory questions, previews, and dry runs from real-mutation requirements.
- Keep synthetic read-only subagent runs outside the top-level mutation completion guard.

### Phase 2 — Provider transport normalization

- Merge true deltas, cumulative fragments, and replayed fragments safely.
- Apply normalization to OpenAI chat-completions tool names/arguments, OpenAI Responses arguments, and Anthropic partial JSON.
- Preserve existing structural truncation detection and portable fallback.

### Phase 3 — Main/subagent routing

- Make automatic read delegation opt-in (`subagent_auto_route`, default `false`).
- Keep explicit `delegate` available for bounded exploration and review.
- Add `/subagent auto on|off` and expose the state in the UI.

### Phase 4 — Context and recovery

- Reserve complete recent native tool-call/result groups before filling ordinary conversation context.
- Restore successful-tool evidence when resuming a run after approval.
- Cache native-tool incompatibility by provider **and model**.

### Phase 5 — Reasoning and observability

- Add an execution-specific prompt contract: inspect → mutate → read/diff → verify → summarize evidence.
- Record provider decision transport, model, tool-call count, text size, and portable-mode state in the persistent debug log.
- Preserve deterministic acceptance, loop, approval, verification, and review gates.

## Acceptance Criteria

- A provider cannot claim a requested file/git change succeeded without a successful matching tool result.
- Malformed native tool calls fall back without consuming the normal transient retry budget.
- Cumulative streamed fragments produce exactly one valid tool name and argument object.
- The main agent directly owns core workspace reads/writes unless automatic routing is explicitly enabled.
- Dry-run and preview tools complete without being mistaken for real mutations.
- Automatic verification executes before a final completion claim.
- Tool history remains provider-valid under constrained context.
- Compatibility learned for one model does not poison another model.

## Validation

- Full `internal/agent` regression suite passed in a Go 1.23 compatibility build with SDK/database boundaries stubbed only because this environment cannot download the repository’s Go 1.25 toolchain or modules.
- `internal/config`, `internal/tools`, `internal/reasoning`, `internal/eval`, `internal/metrics`, and `internal/trace` tests passed in the same compatibility build.
- Provider-neutral tool-protocol tests passed in an isolated build using the production `tool_protocol.go`.
- OpenAI cumulative-stream regression passed using the production stream accumulator.
- All Go files parse successfully; all eval JSON files validate; `git diff --check` passes.
