# Ephemera Reliability Core

The Reliability Core turns agent completion from a model assertion into a machine-checked state transition.

## Components

- **Acceptance contracts** derive a definition of done from the user goal and `.ephemera/project.json`.
- **Completion gates** require changed-file readback, diff/status inspection, configured verification, and no protected-path edits.
- **Semantic progress guards** fingerprint workspace, plan, evidence, and verification state. A repeated state forces a new strategy; repeating after that stops safely so rollback can run.
- **Project manifests** define bootstrap, build, tests, lint, services, protected paths, and custom acceptance checks.
- **Evaluation tasks** provide reproducible file fixtures and deterministic command/file graders.
- **Isolated worktrees** provide the base primitive for future branch-and-select candidate execution.
- **Process supervision** bounds command time and output while preserving exit evidence.

## Initialize a project contract

```bash
ephemera --init-project
```

This writes `.ephemera/project.json` using conservative marker-based discovery. Edit it to reflect the project’s real commands and protected paths.

Example:

```json
{
  "version": 1,
  "bootstrap": ["go mod download"],
  "build": ["go build ./..."],
  "tests": ["go test ./..."],
  "lint": ["go vet ./..."],
  "protected_paths": [".git", ".env", ".env.*", ".ephemera/secrets"],
  "acceptance_checks": [
    {
      "id": "tests-1",
      "description": "Verification command passes: go test ./...",
      "command": "go test ./...",
      "required": true
    }
  ]
}
```

## Grade a workspace

```bash
ephemera --grade-eval evals/example-task.json
```

The command exits non-zero when a required check fails, making it suitable for CI and harness A/B comparisons.

## Completion behavior

When automatic verification is enabled, a workspace-changing run cannot complete unless its acceptance contract passes. Plain-text answers cannot bypass this gate. A blocked run retains its snapshot for continuation or rollback.

## Failure diagnostics

Every session is created with a crash-safe `session.json`, a correlated `debug.jsonl`, and a redacted provider-boundary `context.jsonl`. SQLite is only a searchable index, so an index outage cannot block recovery. Open paths and recent records with `/debuglog`; see [`DEBUG_LOGGING.md`](DEBUG_LOGGING.md) for format, retention, and privacy behavior.

Codex routes use an isolated model-only bridge so Ephemera remains the sole workspace/tool authority. See [`CODEX_BRIDGE.md`](CODEX_BRIDGE.md) for permission and token behavior.
