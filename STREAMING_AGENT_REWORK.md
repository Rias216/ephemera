# Streaming agent rework

This build replaces the request/response freeze with a Bubble Tea channel-driven
agent stream.

## Live event path

1. The provider emits visible text and supported reasoning-summary deltas.
2. The agent parses the structured decision envelope while it is still arriving.
3. Current state, rationale, verification, and next-step previews update token by token.
4. Tool calls are shown as `running`, then updated to `done` or `error`.
5. Tool results appear before the next model round starts.
6. The final response is persisted when the stream completes.

OpenAI Responses, OpenAI-compatible, and Anthropic transports use SSE. Ollama
uses NDJSON. Codex uses `codex exec --json` JSONL events and falls back to the
final message file if that version of Codex does not publish incremental
agent-message updates.

## Interaction

- `Ctrl+X` or `/stop` cancels the active run.
- `Alt+1..4` selects Context, Agent, Surface, and Keys.
- `Ctrl+Left/Right` cycles inspector tabs.
- Context usage includes the current prompt draft and live generated output.
- The Context tab shows the newest thought summary without requiring a tab switch.
- Beneath the Surface displays provider-published summaries and explicit
  structured progress, never raw hidden chain-of-thought.
