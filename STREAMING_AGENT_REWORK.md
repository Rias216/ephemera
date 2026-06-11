# Streaming agent rework

This build replaces the request/response freeze with a Bubble Tea channel-driven
agent stream.

## Live event path

1. The provider emits visible text deltas.
2. The agent parses the structured decision envelope.
3. Reasoning summaries and plans are appended immediately.
4. Tool calls are shown as `running`, then updated to `done` or `error`.
5. Tool results appear before the next model round starts.
6. The final response is persisted when the stream completes.

OpenAI-compatible and Anthropic transports use SSE. Ollama uses its native chat
callback. Codex uses `codex exec --json` JSONL events and falls back to the final
message file if that version of Codex does not publish incremental agent-message
updates.

## Interaction

- `Ctrl+X` or `/stop` cancels the active run.
- `Alt+1..4` selects Context, Agent, Surface, and Keys.
- `Ctrl+Left/Right` cycles inspector tabs.
- Context usage includes the current prompt draft and live generated output.
- Beneath the Surface displays only explicit concise reasoning summaries, never
  private chain-of-thought.
