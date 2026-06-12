# Modular agent rework — execution report

Date: 2026-06-12

## Completed architecture

1. **Provider registry** — `internal/llm/provider_registry.go` owns registered factories, isolated registries, construction, names, and adapter-owned model catalogs. OpenAI, compatible, Anthropic, Ollama, and Codex register from their own files.
2. **Unified tool definition** — `tools.Tool` is both the provider schema and executable runtime contract. Every built-in has a registered handler; `ExecuteStream` performs lookup rather than name dispatch.
3. **Tool middleware** — normalization/schema validation, scoped approvals, dry-run, timeout, sandbox routing, and debug logging are composable middleware applied to every tool source.
4. **Cross-platform plugins** — manifests discover persistent subprocess tools that speak line-delimited JSON-RPC 2.0. Registration is session-local, collision-checked, rollback-safe, and process resources close with the registry.
5. **MCP unification** — discovered MCP capabilities become normal `tools.Tool` definitions with translated risk, full schema validation, and the same middleware/execution path as built-ins and plugins.
6. **Agent decomposition** — the former monolithic loop is split by orchestration, state, model action, execution, approval, verification, context selection, prompt construction, and events.
7. **Hierarchical config** — persisted configuration is grouped under `providers`, `agent`, `ui`, `mcp`, `plugins`, and `orchestration`; legacy flat files remain readable and nested groups take precedence.
8. **Provider compatibility recovery** — native-tool compatibility classification belongs to each adapter instead of shared provider-name/error substring routing.
9. **Session regression** — a representative harness test performs catalog delivery, list/read/edit/test iterations, completion, and verifies per-session debug/context logs.

## Verification performed

- `internal/config`, `internal/tools`, `internal/mcp`, provider-core `internal/llm`, and the full `internal/agent` test suites pass in an offline compatibility build.
- `TestAverageHarnessSessionAcrossToolIterations` passes independently.
- Windows amd64 test binaries compile for `internal/tools`, `internal/config`, `internal/agent`, and the earlier MCP/plugin foundations.
- All 161 Go source files parse successfully.
- `git diff --check` reports no whitespace errors.
- Static checks confirm provider construction uses factory lookup, `ExecuteStream` uses registered handlers, no `plugin_unsupported.go` remains, and the agent has no MCP execution bypass.

## Environment limitation

The repository declares Go 1.25.0. This environment has Go 1.23.2 and no outbound access, so the official toolchain and external SDK modules cannot be downloaded for a native `go test ./...`. Verification used an isolated Go 1.23 compatibility copy with provider/SQLite compile stubs; no compatibility files are included in the deliverable.


## Context and workspace follow-up

- The active workspace is now resolved from the launch directory on every process start, or from `--workspace <directory>`. It is runtime state and is never persisted to `config.json`, and the TUI test suite uses an isolated config home, so tests and temporary launches cannot poison later sessions.
- Relative tool paths use the active workspace as their base. Absolute paths and paths outside it are supported through the same call-aware approval flow as writes and shell actions; an approval prompt identifies every external target and warns that workspace snapshot rollback does not cover it.
- External reads and writes retain stable absolute path identities in tool metadata, state reconstruction, inspect-before-edit enforcement, debug logs, and completion evidence.
- Normal transcript view now shows the user conversation plus only pending approvals, failures, recovery, and non-duplicated final output. The full tool/reasoning history remains available in timeline focus with `Ctrl+T`.
- The Context inspector now prioritizes budget usage, message retention, active workspace, approval policy, sandbox mode, and verification state. Duplicate session/model counters and repeated tab hints were removed, and the header context meter is suppressed while the Context inspector is active.
- External-path registry and agent approval regressions pass under normal and race-enabled tests; transcript decluttering has focused unit coverage, but the full TUI package remains subject to the offline dependency limitation below.
