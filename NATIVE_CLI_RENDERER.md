# Native fixed-cell CLI renderer

This build removes the Glamour compatibility layer from the transcript path and
renders model output directly inside `internal/tui`.

## What changed

- Every transcript row is painted to an exact terminal-cell width.
- Every span and every trailing padding cell explicitly uses the active panel
  background, preventing the terminal's default black background from leaking
  after text.
- Model-provided ANSI/OSC control sequences are removed before rendering.
- Explicit line breaks are preserved for provider errors and tool logs.
- Markdown headings, lists, task items, quotes, tables, inline emphasis, links,
  and fenced code blocks are rendered as compact CLI structures.
- Long code lines wrap within framed code blocks instead of overrunning the
  viewport.
- Agent timeline rows use the same fixed-cell renderer.
- Duplicate long agent-event bodies are hidden when the assistant response has
  already displayed the same content.
- The viewport now explicitly fills every unused row tail with the panel color.

## Main files

- `internal/tui/cli_renderer.go`
- `internal/tui/cli_renderer_test.go`
- `internal/tui/model.go`
- `internal/tui/agent_ui.go`
- `internal/tui/texture.go`

## Build

On Windows:

```bat
run.bat
```

Or with Go 1.25+:

```sh
go mod tidy
go test ./...
go build -o bin/ephemera ./cmd/ephemera
```

The renderer unit tests were also type-checked and run in an isolated local
harness. The complete project test suite could not be executed in the packaging
environment because it only has Go 1.23 and cannot download the Go 1.25
toolchain without network access.
