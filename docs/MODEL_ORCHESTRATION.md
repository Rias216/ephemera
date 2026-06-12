# Model orchestration

Ephemera can route bounded work to separate remembered model connections without changing the active main model.

## Lightweight subagent

The subagent is read-only and intended for exploration, search, isolated debugging, and review. A single eligible read action may be auto-routed so large tool output is summarized before it reaches the main model.

```text
/subagent status
/subagent on
/subagent model
/subagent model <route>::<model>
/subagent model inherit
/subagent steps 4
/subagent tokens 2000
/subagent off
```

`/subagent model` opens model autocomplete across every remembered connection. Disabling the feature removes the delegate tool and disables automatic routing.

## Director mode

Director mode uses a primary model for planning and tools plus a second, tool-free instrument model for review. Instrument feedback is visible in the reasoning timeline and is advisory unless it identifies a concrete issue at a severity enabled by the configured weight.

```text
/director status
/director on
/director model
/director model <route>::<model>
/director instrument
/director instrument <route>::<model>
/director weight 20
/director steps 12 2
/director off
```

The incorporation policy is deterministic:

- HIGH feedback is always incorporated.
- MEDIUM feedback is incorporated at weight 20 or higher.
- LOW feedback is incorporated at weight 60 or higher.
- CLEAN feedback never blocks completion.

The instrument receives no tool schemas and cannot modify the workspace or request approval.

## Native tool recovery

When a provider ends a streamed tool call with truncated JSON, Ephemera requests one fresh native tool decision without consuming the normal provider retry budget. A second truncation switches that provider route to the universal text tool gateway. Structural repair only closes braces or brackets; unterminated string content is never invented.
