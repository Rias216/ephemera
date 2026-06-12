# Session diagnostics and recovery

Ephemera creates a diagnostic bundle for **every session immediately**, including empty sessions and sessions that fail before the first model response.

## Bundle contents

Each session has its own directory:

```text
sessions/<session-name>/
├── session.json   # crash-safe transcript, agent events, provider/model, and state
├── debug.jsonl    # lifecycle, provider, tool, verification, retry, and failure events
└── context.jsonl  # redacted provider requests/responses and context-selection details
```

`session.json` is written independently of SQLite and is used as a recovery source if the history database is missing or unavailable. It is refreshed after messages, agent events, approvals, configuration changes, explicit saves, session switches, and shutdown.

`debug.jsonl` records both successful and failed operations. Records are correlated with session, run ID, provider, model, iteration, tool, workspace, duration, transport, status, and argument fingerprint where available. Raw tool arguments are deliberately omitted.

`context.jsonl` records the actual normalized model boundary for each attempt, including the system prompt, selected conversation context, tool schemas, provider response, retry/fallback state, instrument review, and errors. Each payload has a SHA-256 hash so repeated or changed contexts can be correlated.

## What is captured

- Session creation, saves, loads, recovery, and persistence failures
- Agent run start/finish, reasoning iterations, completion gates, and verification
- Provider requests/responses, retries, health checks, transports, and fallbacks
- Native and universal tool-call decisions
- Every tool execution start/result, including duration and failure reason
- Context compaction and selected-history statistics
- Codex and other provider encoding failures
- Unexpected stream closure, worker panic, and CLI fatal errors

All provider-bound text is normalized to valid UTF-8 before execution and before logging. This prevents corrupted file/context bytes from breaking Codex stdin requests.

## Location and UI

Use:

```text
/debuglog
/debuglog tail
/debuglog clear
```

`/logs` is an alias. `/debuglog` prints the current session’s snapshot, debug log, context log, global log, and recent events.

Default session root:

- Windows: `%AppData%\ephemera\sessions\`
- Linux: `$XDG_CONFIG_HOME/ephemera/sessions/` or `~/.config/ephemera/sessions/`
- macOS: `~/Library/Application Support/ephemera/sessions/`

Environment overrides:

```text
EPHEMERA_SESSION_LOG_DIR=<custom session bundle directory>
EPHEMERA_DEBUG_LOG=<custom global debug log path>
```

## Privacy and retention

- Directories and files use user-only permissions where supported.
- Authorization headers, API keys, access tokens, secrets, passwords, credentials, and common key formats are redacted recursively.
- Invalid UTF-8 is replaced safely instead of being discarded.
- Individual strings, collections, and nesting depth are bounded.
- `debug.jsonl` rotates at 5 MiB with three backups.
- `context.jsonl` rotates at 32 MiB with five backups.
- `session.json` remains after `/debuglog clear`; only diagnostic streams are cleared.

For a bug report, include the affected session directory rather than only the global log. It contains the exact run-correlated evidence needed to reproduce provider and tool-routing failures.
