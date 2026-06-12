# Frontier Harness Plan — Execution Report

Date: 2026-06-12

## Result

The high-impact, locally verifiable parts of the frontier plan are implemented. Existing uncommitted work from the supplied archive was preserved; no reset or destructive cleanup was performed.

## Implemented

### Architecture cleanup

- Added shared generic slice utilities in `internal/util/slice.go`.
- Removed duplicated `contains`, deduplication, and sorted-unique helpers from runtime and agent call sites.
- Preserved all graph artifacts and documented the graph rebuild limitation instead of deleting alleged isolated nodes without evidence.

### Reasoning and orchestration

- Added bounded structured `ReasoningStep` history to each run. It stores concise goals, evidence, risks, verification, and next actions—not hidden chain-of-thought.
- Added consistency warnings across iterations.
- Added tool-graph-aware adaptive depth using call count, dependency depth, risk, and cross-file scope.
- Added a bounded post-tool reflection/replan loop.
- Added mode-specific tool catalogs while retaining extension/MCP compatibility.
- Prevented same-model “subagent” delegation, eliminating an unnecessary token-cost path.
- Kept independent local reads parallel even when a provider does not advertise concurrency metadata.

### Memory and codebase learning

- Added a provider-neutral `Embedder` interface.
- Added a zero-network local semantic hash embedder and an opt-in OpenAI-compatible remote embedding adapter.
- Added cosine-based recall with lexical fallback for memory and context windows.
- Added embedding-backed incremental codebase indexing.
- Added reinforcement, semantic merge, consolidation, and age-aware forgetting for episodic memory.
- Added merged workspace and global memory plus the `prefer` tool and `/memory add|project` commands.

### Evaluation and regression tracking

- Added 10 deterministic subsystem eval scenarios and 6 real-provider frontier tasks.
- Added a real-LLM eval runner with pass/fail, latency, token, tool-call, and structured-reasoning quality fields.
- Added append-only `evals/history.json` support.
- Added CLI flags:
  - `--llm-eval`
  - `--eval-history`
  - `--eval-diff`

### Recovery, health, metrics, and traces

- Replaced free-form recovery output with a structured error taxonomy.
- Added provider-specific classification for OpenAI, Anthropic, Ollama, and Codex.
- Added cached 60-second provider health checks and fail-fast routing.
- Added dependency-free counters, gauges, histograms, JSON export, and Prometheus text rendering.
- Added structured per-run JSON traces and tree/Mermaid renderers.
- Added CLI flags:
  - `--metrics`
  - `--trace <run-id>`
  - `--trace-format tree|mermaid`

### Tool system

- Added progressive streaming for file reads, directory traversal, text search, and regex search.
- Added informative head/tail truncation with total-size summaries.
- Added `Result.Summary` metadata.
- Added a unified dynamic tool registry used by built-ins and custom handlers.
- Added repeatable `--tool <plugin.so>` loading on platforms supported by Go plugins.
- Added explicit unsupported-platform reporting on Windows rather than silently failing.

### Diagnostics

- Preserved and extended bounded debug logging.
- All encountered execution and verification failures are recorded in `.ephemera/execution-debug.log`.

## Verification

Passed:

- Focused compatibility tests for `internal/agent`, `internal/reasoning`, `internal/tools`, `internal/eval`, `internal/metrics`, and `internal/trace`.
- New regression tests for local/remote embeddings, adaptive tool-depth, metrics export, trace round-trip/rendering, eval history/diff, dynamic registration/streaming, and truncation summaries.
- Existing agent capability suite in the compatibility build.
- All 17 new eval JSON files parse successfully.
- Every Go source file parses successfully with the standalone Go parser.
- `git diff --check` reports no whitespace errors.

Native `go test ./...` could not run in this environment because the repository requires Go 1.25.0, the installed toolchain is Go 1.23.2, and outbound toolchain downloads are blocked. A temporary Go 1.23 compatibility copy with local dependency stubs was used to type-check and test the core changed packages. That copy is not part of the deliverable.

## Intentionally deferred or adapted

- **Graphify thresholds:** `graphify` is not installed in this environment. The supplied report is a pre-change snapshot, so claims such as “below 100 isolated nodes” or “below 15% inferred edges” were not fabricated. Rebuild commands are documented in `GRAPHIFY_AUDIT.md`.
- **Automatic isolated-node deletion:** deferred because graph isolation does not prove dead code.
- **ONNX dependency:** adapted to a local zero-network semantic embedder plus an opt-in remote protocol to avoid adding a large native runtime.
- **OpenTelemetry SDK:** adapted to dependency-free JSON and Prometheus export. The registry is ready for a small OTLP adapter later without coupling agent logic to a vendor SDK.
- **Go plugins on Windows:** Go’s standard `plugin` package is unsupported on Windows; the CLI returns a clear error there. MCP and registered built-ins remain portable.

## New environment variables

```text
EPHEMERA_EMBEDDING_URL
EPHEMERA_EMBEDDING_API_KEY
EPHEMERA_EMBEDDING_MODEL
EPHEMERA_METRICS
```

When no embedding endpoint is configured, semantic recall remains local and creates no API cost.
