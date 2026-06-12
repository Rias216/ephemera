# Persistent reasoning agent

Ephemera's agent loop is built around a public, structured trace rather than
raw private chain-of-thought. The trace is safe to render and persist because it
contains concise fields such as goal, current state, assumptions, plan, evidence,
risks, tool rationale, verification, and next step.

## Tool contract

Local tools are cataloged with JSON-object argument schemas. Providers that can
emit native tool calls use the provider-neutral `llm.ToolSpec` and `llm.ToolCall`
interfaces. Providers without native tools continue through the JSON decision
fallback described in the system prompt.

The safe default policy is:

- read/search/git inspection tools run without approval;
- write tools, shell commands, and configured tests require approval;
- `/agent auto` or `/approval auto` runs all built-in tools immediately;
- `/agent safe` restores approval prompts for write and shell tools.

## Agent loop

Each run follows an observe, decide, act, verify, review, finalize loop.

- Observe: recent events, project memory, and tool results are folded into the
  next request.
- Decide: native tool calls are preferred when available; otherwise the model
  returns a structured JSON decision.
- Act: every call is validated against the local schema and approval policy.
- Verify: workspace changes trigger git status, diff/readback, and the
  configured test command when applicable.
- Review: non-trivial verified changes can be passed to a read-only specialist.
- Finalize: completion is marked verified or unverified based on evidence.

Malformed JSON-like decisions are repaired when possible. If repair fails, the
agent asks the model for one corrected decision before falling back to a normal
assistant answer.

## UI surfaces

- `/surface` opens the persisted structured trace and verification state.
- `/tools` shows the schema-backed built-in tool catalog.
- `/usage` shows request budget, included/dropped messages, tool output budget,
  and detected memory sources.
- The timeline supports filters, expandable event bodies, live tool status,
  duration metadata, approval events, verification events, and final status.
