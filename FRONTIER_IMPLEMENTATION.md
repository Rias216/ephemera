# Ephemera Frontier Implementation

This repository now contains the first two executable phases of the Frontier Plan. The upgrade focuses on provider-neutral execution, reliable agent loops, bounded context, codebase intelligence, recoverable workspace changes, and deterministic capability claims.

## Implemented

### Universal provider and tool layer

- Versioned provider-neutral `ToolContract` schemas.
- Canonical tool calls and tool results with provider call IDs preserved across turns.
- Capability negotiation for context size, vision, reasoning, native tools, parallel limits, tool-call format, and stream format.
- Normalized OpenAI, Anthropic, Ollama, compatible endpoint, and Codex transports.
- Provider-specific prompt profiles selected from negotiated capabilities rather than branches in the agent core.
- Bounded retries for transient, rate-limit, and context-length failures, including request compaction before context retries.

### Reliable plan-and-execute loop

- Persistent `Plan` and `PlanStep` state with dependencies, statuses, evidence, and live TUI updates.
- Dependency-aware parallel dispatch for independent reads and path-disjoint approval-free writes.
- Dedicated streamed tool progress for partial stdout/stderr.
- Duplicate-call caching and suppression, inspect-before-edit enforcement, approved-action reuse, rejected-action suppression, and no-progress loop guards.
- Adaptive reasoning depth, structured public reasoning summaries, and optional independent self-critique.
- Per-run token/tool usage estimates and task token budgets.
- Optional TDD contract.

### Context and memory

- `ContextWindow.Fit`, `Recall`, and hierarchical `Compact` behavior.
- Semantic recall of older file- and symbol-related messages.
- Native tool-result deduplication and compact run working memory.
- Persistent lazy codebase index in `.ephemera/codebase-index.json`.
- Go AST indexing plus bounded structural indexing for Python, JavaScript, TypeScript, and other common source files.
- Optional episodic learning in `.ephemera/memory.json`, with bounded history and atomic writes.

### Safety and recovery

- Dry-run previews for file writes, commands, Git operations, and external mutations.
- Snapshot sandbox mode with size limits, retained rollback points, manual `/rollback`, and optional automatic rollback after failed runs.
- Docker command sandbox mode using an already-installed local image, read-only container filesystem, temporary `/tmp`, and disabled networking.
- Snapshots preserve regular files, directories, symlinks, and modes while excluding generated dependency/build directories and `.git`.
- Failed snapshots block the risky action rather than continuing unprotected.

### Code, project, Git, and GitHub tools

Added provider-visible tools for:

- regex search, symbol lookup, reference lookup, file summaries, and source dependency graphs;
- project/build/test detection and dependency listing;
- project-aware linting, formatting, and dependency security auditing;
- Git log, blame, branch creation, checkout, commit, stash, and merge;
- bounded public web fetches with private-network blocking;
- GitHub issue get/list/create/update/comment operations;
- GitHub pull-request get/list/create/update/review/comment operations.

GitHub writes require `GITHUB_TOKEN`; credentials are used only in request headers and are never returned in tool output. GitHub operations validate repositories and action-specific fields and support dry-run previews without network access.

### TUI controls

Added commands:

- `/sandbox <none|snapshot|docker>`
- `/dry-run <on|off|toggle>`
- `/rollback [now|auto|manual|status]`
- `/index <on|off|rebuild|status>`
- `/tdd <on|off|toggle>`
- `/learn <on|off|toggle>`

Agent and config notices now show safety, indexing, TDD, and learning state.

## Configuration added

```json
{
  "agent_self_critique": false,
  "agent_adaptive_reasoning": true,
  "agent_tdd_mode": false,
  "agent_learn_memory": false,
  "agent_semantic_index": true,
  "agent_dry_run": false,
  "agent_auto_rollback": false,
  "sandbox_mode": "none",
  "agent_snapshot_max_mb": 128,
  "agent_context_recall_messages": 8,
  "provider_max_retries": 2,
  "provider_retry_backoff_ms": 350,
  "agent_task_token_budget": 100000
}
```

## Validation performed

- `gofmt` and `git diff --check` completed for modified Go sources.
- Configuration and reasoning tests pass in the Go 1.23 compatibility harness.
- The complete local tools test suite passes, including code intelligence, dry run, web safety, and GitHub request tests.
- Targeted agent tests pass for plan state, context recall/compaction, semantic indexing, and workspace snapshots.
- Provider prompt-profile tests pass.
- Deterministic agent capability evaluation: **32 / 32 passed**.

The repository declares Go 1.25.0. A native full `go test ./...` cannot run in this offline sandbox because Go 1.25 and external TUI/provider modules are not installed and cannot be downloaded. The original Go directive was preserved. Core packages were therefore validated in isolated Go 1.23 modules with local stubs only at unavailable external-provider boundaries. Full TUI compilation remains the principal environment-blocked validation item.

## Preserved repository state

The uploaded archive already contained modified, deleted, and untracked files. Those changes were preserved rather than reset. This upgrade was applied on top of that state.

## Remaining Frontier phases

- Git rebase, bisect, and richer conflict-resolution workflows.
- Provider-priced cost estimation and daily/session accounting beyond token budgets.
- Vision input routing and model-specific image capability tests.
- A real Docker-image matrix and integration tests on a host with Docker images installed.
- GitHub issue/PR pagination and richer review-diff operations.
- SWE-Bench-style real-repository evaluation, benchmark persistence, and CI regression gates.
- Full Go 1.25 and TUI integration validation in an online build environment.
