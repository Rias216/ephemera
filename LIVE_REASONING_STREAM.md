# Live reasoning stream

Ephemera now separates provider output into two incremental channels:

- `text` — answer or structured decision tokens;
- `reasoning` — provider-published summaries or a non-sensitive activity signal.

Both channels update the footer and live timeline while the request is running.
The structured agent envelope is parsed before completion, so fields such as
`current_state`, `tool_rationale`, `verification`, and `next_step` appear as soon
as their characters arrive.

## Provider behavior

- **OpenAI** uses the Responses API and streams
  `response.reasoning_summary_text.delta` when the selected reasoning model
  supports summaries. Non-reasoning OpenAI models continue without the
  unsupported `reasoning` request field.
- **Anthropic** requests adaptive thinking with `display: summarized` on models
  that support it and streams `thinking_delta` summaries.
- **Codex CLI** consumes `reasoning` and `agent_message` items from
  `codex exec --json` independently.
- **OpenAI-compatible endpoints** display explicit `reasoning_summary` fields.
  Raw `reasoning_content` or `thinking` fields are reduced to an activity signal.
- **Ollama** keeps raw `message.thinking` and `<think>` blocks out of the UI. They
  trigger a live local-reasoning indicator; the safe structured decision preview
  takes over when visible output begins.

## UI behavior

- The default **Context** inspector shows the newest thought summary during a run.
- **Surface** (`Alt+3`) shows the same live stream with goal and plan context.
- The agent timeline includes a live `thinking` row and reasoning character count.
- `/thinking off` disables reasoning requests and hides all reasoning surfaces.

Raw hidden chain-of-thought is not rendered, added to chat history, or persisted.
Completed sessions retain the explicit structured reasoning trace used by the
agent loop.
