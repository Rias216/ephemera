# Modular agent architecture

Ephemera resolves providers and tools through registries. The agent loop depends only on those contracts, so provider adapters, built-ins, MCP servers, and subprocess plugins use the same execution path.

## Provider boundary

`internal/llm/provider_registry.go` owns `ProviderFactory` and `ProviderRegistry`:

```go
type ProviderFactory func(config.Config) (Provider, error)

type ProviderRegistry interface {
    Register(string, ProviderFactory) error
    New(config.Config) (Provider, error)
    ListModels(context.Context, config.Config) ([]string, error)
    Names() []string
}
```

Each adapter registers its factory from its own source file. Shared construction and model discovery contain no provider switch. Optional behavior is expressed through provider interfaces such as `ModelCatalogProvider`, `ErrorClassifier`, `NativeToolCompatibilityClassifier`, `HealthChecker`, and `CapableProvider`.

## Tool definition and registry

`tools.Tool` is the single provider-facing and executable definition. It contains identity, description, risk, JSON schema, version, provider hints, and a `Handler`. `llm.ToolSpec` aliases this type, so provider serialization and runtime execution cannot drift into separate catalogs.

Every `tools.Registry` clones the immutable process defaults and owns dynamic registrations for one harness run. Built-ins, plugins, and MCP definitions are all looked up from that runtime catalog. A tool call reaches its registered handler only after the shared middleware chain completes.

## Middleware execution

The default chain is:

1. debug lifecycle logging and result shaping;
2. call normalization and JSON-schema validation;
3. approval-policy enforcement;
4. dry-run simulation for mutating tools;
5. bounded execution timeout;
6. sandbox-route selection;
7. the registered tool handler.

Explicit approval is a context-scoped grant. It cannot leak to another tool call or session.

## Dynamic tools

### Subprocess plugins

Plugins are persistent child processes using line-delimited JSON-RPC 2.0 over stdin/stdout. Their manifests declare provider schemas and risks before process startup. Registration is runtime-local, collision-checked, rollback-safe, and cross-platform. Registry shutdown closes every child process.

### MCP

MCP discovery translates remote capabilities into `tools.Tool` definitions. Read-only annotations become read risk; all other remote capabilities conservatively become shell risk. Once registered, MCP tools use the same normalization, approval, dry-run, timeout, sandbox, logging, duplicate-call handling, and mutation tracking as local tools. The agent contains no separate MCP execution branch.

## Agent responsibility boundaries

- `agent.go`: public runner types, construction, entry points, and tool-catalog selection.
- `run_loop.go`: iteration orchestration and termination.
- `run_state.go`: run/session initialization, restoration, and mutable state.
- `model_action.go`: provider requests and model-decision decoding.
- `execution.go`: tool action execution, observations, cache handling, and delegation.
- `approval.go`: pending approval creation and approved resumption.
- `verification.go`: verification and completion evidence.
- `context_selection.go`: bounded history/context selection.
- `system_prompt.go`: system prompt assembly.
- `events.go`: event emission and persisted run diagnostics.

Focused policy components such as completion gates, director review, planning, snapshots, and provider retry remain independent files called by the loop.

## Configuration schema

The persisted schema is hierarchical:

```json
{
  "providers": {},
  "agent": {},
  "ui": {},
  "mcp": {},
  "plugins": {},
  "orchestration": {}
}
```

The Go struct embeds named settings groups to preserve source compatibility for existing `cfg.Provider`-style call sites. Custom unmarshalling accepts legacy flat files; a present nested group wins over flat fields. Runtime credentials are excluded from JSON.
