# Codex model bridge

Ephemera uses the locally authenticated Codex CLI as a model transport, not as a second autonomous coding agent.

## Why

Running a complete Codex agent inside Ephemera duplicated planning, workspace discovery, tool execution, approval checks, and context. It also forced the inner process into a read-only sandbox while the outer Ephemera agent expected it to edit files. The result was slower startup, permission complaints, and unnecessary token use.

## Current behavior

- Codex starts in an isolated disposable temporary directory with workspace-write limited to that bridge directory, never the project workspace.
- User rules, project instructions, history persistence, shell tools, file tools, web search, MCP, hooks, memories, image tools, and nested subagents are disabled for the inner process.
- Codex returns only the next model response or Ephemera universal-tool request.
- Ephemera remains responsible for workspace reads and writes, approvals, command execution, snapshots, retries, verification, and debug logging.
- Normal mode maps to low bridge reasoning effort. Deep mode maps to high effort.
- Visible output has a configurable target, defaulting to 2,048 tokens.
- Older Codex CLI versions receive one compatibility retry with conservative arguments.

## Commands

```text
/codex
/codex status
/codex budget 1024
```

The budget accepts 512–8,000 tokens. It is a response-size target; the active Ephemera context budget still controls conversation input.

Workspace authority is controlled by Ephemera, not by the isolated inner Codex process:

```text
/approval safe
/approval workspace-write
/approval read-only
```

Use `/debuglog` after any failed Codex request. Provider launch errors, malformed stream events, compatibility fallbacks, nested-tool attempts, empty responses, and outer agent failures are persisted with provider/model/session context.
