# ✦ Ephemera

> What vanishes may still illuminate.

Ephemera is a compact reasoning harness with a native terminal interface. It
keeps model deliberation private, applies a provider-neutral reasoning contract,
and returns dense, useful answers through a rose-lit Bubble Tea TUI.

## Features

- Native Go TUI using Bubble Tea v2, Bubbles v2, Lip Gloss v2, and a fixed-cell CLI renderer
- Rose-pink dark theme (`#FF69B4` / `#DB2777`) plus monochrome mode
- 60 FPS elapsed-time pink outline fade with a moving knife glimmer
- Cell-diff rendering, synchronized terminal updates, and a native terminal cursor
- OpenCode-style command palette, live agent timeline, and interactive bottom inspector
- Guided `/connect` flow with masked API-key input
- Native Ollama, OpenAI, and Anthropic providers
- Any OpenAI-compatible API, including local servers
- Named JSON sessions with automatic persistence
- Normal, deep-reason, concise, and creative reasoning modes
- Scrollable CLI-rendered answers with framed code, tables, lists, and mouse-wheel support
- Native token streaming for OpenAI, Anthropic, Ollama, compatible APIs, and Codex JSONL
- Live context/output estimates, agent rounds, tool status, structured reasoning summaries, and cancellation
- Clipboard copy through `/copy` or `Ctrl+Y`
- Single Windows launcher that resolves modules, compiles, and runs
- One static binary when built with `make static`

## Requirements

- Go 1.25.0 or newer
- A terminal with ANSI color support
- One model provider:
  - Ollama running locally
  - OpenAI
  - Anthropic
  - An OpenAI-compatible endpoint

The project is pure Go. `requirements.txt` intentionally contains no Python
dependencies.

## Windows: one-click compile and run

Open the project folder and double-click:

```text
run.bat
```

From PowerShell or Command Prompt, arguments are passed through to Ephemera:

```powershell
.\run.bat --session architecture-notes
.\run.bat --provider ollama --model qwen3:8b
```

`run.bat` performs these steps:

1. checks that Go is available,
2. runs `go mod tidy` when `go.sum` is missing,
3. builds a temporary executable in `bin\`,
4. launches the compiled executable.

## Linux and macOS

```bash
git clone https://github.com/ephemera-ai/ephemera.git
cd ephemera
go mod tidy
make build
./bin/ephemera
```

Without `make`:

```bash
mkdir -p bin
go build -trimpath -ldflags "-s -w -X main.version=dev" -o bin/ephemera ./cmd/ephemera
./bin/ephemera
```

Build a stripped, CGO-free binary:

```bash
make static VERSION=0.1.0
```

## Command autocomplete

Type `/` to open the command palette.

| Key | Action |
|---|---|
| `Tab` | Complete the selected command or value |
| `↑` / `↓` | Move through suggestions |
| `Enter` | Execute the current input or activate the highlighted suggestion |
| `Esc` | Cancel the `/connect` wizard |

Autocomplete also suggests:

- provider names and known compatible presets for `/connect`,
- provider names for `/provider`,
- reasoning modes for `/mode`,
- themes for `/theme`,
- saved session names for `/load`,
- live provider models, current model, and curated fallback models for `/model`,
- common OpenAI-compatible endpoints during `/connect`.

## Connect from inside the TUI

Run the guided wizard:

```text
/connect
```

Or begin with a provider already selected:

```text
/connect ollama
/connect openai
/connect anthropic
/connect compatible
/connect openrouter
/connect groq
/connect nvidia
```

The wizard asks only for the fields the selected provider needs.

### Ollama

```text
/connect ollama
Ollama URL: http://localhost:11434
Model: qwen3:8b
```

### OpenAI

```text
/connect openai
API key: ********
Model: your-model-id
```

Press Enter on the API-key step to use `OPENAI_API_KEY` when it is already set.

### Anthropic

```text
/connect anthropic
API key: ********
Model: your-model-id
```

Press Enter on the API-key step to use `ANTHROPIC_API_KEY` when it is already
set.

### Any OpenAI-compatible provider

Use `compatible` for custom providers and local servers exposing the standard
`/chat/completions` API:

```text
/connect compatible
Connection name: openrouter
Base URL: https://openrouter.ai/api/v1
API key: ********
Model: provider/model-id
```

Known presets such as `openrouter`, `groq`, `nvidia`, `together`, and
`lm-studio` prefill the matching base URL while still storing the runtime
provider as `compatible`. The wizard also accepts any custom HTTP or HTTPS base
URL.

API keys entered in `/connect` are masked and kept only in process memory. They
are never written to `config.json`. For persistent credentials, set:

```text
OPENAI_API_KEY
ANTHROPIC_API_KEY
EPHEMERA_API_KEY
```

Non-secret connection metadata such as base URLs, provider names, and model IDs
is persisted.

## Commands

```text
/connect [ollama|openai|anthropic|compatible|openrouter|groq|nvidia|together|lm-studio]
/help
/clear
/new [name]
/save [name]
/load <name>
/sessions
/provider <ollama|openai|anthropic|compatible>
/model <model-id>
/models
/mode <normal|deep-reason|concise|creative>
/theme <rose|mono>
/agent <auto|safe|read-only|status>
/approval <auto|safe|read-only|workspace-write|chat>
/thinking <on|off|toggle>
/run
/stop
/copy
/quit
```

## General keyboard shortcuts

| Key | Action |
|---|---|
| `Enter` | Send a prompt or run a command |
| `PgUp` / `PgDn` | Scroll the transcript |
| `Ctrl+U` / `Ctrl+D` | Half-page scroll |
| `Ctrl+X` | Cancel the active streaming agent run |
| `Alt+1..4` | Open Context, Agent, Surface, or Keys inspector tab |
| `Ctrl+Left` / `Ctrl+Right` | Cycle inspector tabs |
| `Ctrl+Y` | Copy the last answer |
| `Ctrl+C` | Save and quit |

## Startup flags

Open or create a named session:

```bash
ephemera --session architecture-notes
```

Override settings:

```bash
ephemera \
  --provider anthropic \
  --model your-model-id \
  --mode deep-reason \
  --session compiler-design
```

## Persistence

Ephemera uses the platform user config directory:

- Linux: `~/.config/ephemera/`
- macOS: `~/Library/Application Support/ephemera/`
- Windows: `%AppData%\ephemera\`

Files:

```text
config.json
sessions/<session-name>.json
```

Runtime API keys are excluded from serialized configuration.

## Reasoning design

The harness instructs the selected model to:

1. identify objectives and constraints,
2. compare approaches,
3. test assumptions and edge cases,
4. critique and repair its draft,
5. return only the smallest complete final answer.

Private chain-of-thought is neither requested for display nor stored separately.
Anthropic thinking blocks are ignored; only final text enters the transcript.

## Project structure

```text
ephemera/
├── cmd/ephemera/main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── history/
│   │   ├── store.go
│   │   └── store_test.go
│   ├── llm/
│   │   ├── anthropic.go
│   │   ├── ollama.go
│   │   ├── openai.go
│   │   └── provider.go
│   ├── reasoning/
│   │   ├── harness.go
│   │   └── harness_test.go
│   ├── theme/theme.go
│   └── tui/
│       ├── commands.go
│       ├── connect.go
│       └── model.go
├── .env.example
├── .gitignore
├── go.mod
├── LICENSE
├── Makefile
├── README.md
├── requirements.txt
└── run.bat
```

## Development

```bash
make fmt
make vet
make test
make run
```

On Windows without `make`:

```powershell
gofmt -w .
go vet ./...
go test ./...
go run .\cmd\ephemera
```

## Static binary notes

`CGO_ENABLED=0` produces a self-contained executable. Clipboard integration is
best-effort and depends on facilities available on the host desktop or terminal
session; Ephemera reports a non-fatal status message when copying is unavailable.

## License

MIT

## UI rendering

Ephemera uses Bubble Tea v2's cell-diff renderer with synchronized terminal updates when the terminal supports them. The rose interface uses elapsed-time animation, fixed component geometry, a native terminal cursor, and localized micro-animation so visual detail does not require full-screen redraws.

## Guided workflow quality of life

The provider wizard uses a reversible five-stage flow with a live route preview,
inline validation, environment-key detection, a final review gate, and fast
keyboard navigation. See `UI_POLISH_V9.md` for the complete interaction notes.


## Auto-approve agent mode

Run `/agent auto` or `/approval auto` to execute every built-in agent tool immediately without approval prompts. Run `/agent safe` or `/approval safe` to restore confirmation for writes and shell commands. Workspace path guards and destructive-command checks remain active.

## Renderer, auto-approve, and Beneath the Surface

The transcript renderer now paints the complete viewport itself, preventing the
unstyled final-column padding that appeared as a black strip in some terminals.

- `/agent auto` or `/approval auto` automatically executes supported agent tools.
- `/agent safe` restores write and shell confirmations.
- `/thinking on` shows the structured **Beneath the Surface** decision trace.
- `/thinking off` hides visible reasoning traces.

See `RENDERER_AUTO_APPROVE_REASONING.md` for implementation and safety details.


## Live agent and context inspector

Agent runs now publish updates throughout execution rather than returning one
large result at the end. The transcript receives structured reasoning summaries,
plans, tool calls, and tool results as they occur. The bottom inspector updates
without moving the composer:

- `Alt+1` — live input/output context estimate and trimming state
- `Alt+2` — active round, phase, tool, approval policy, and elapsed time
- `Alt+3` — Beneath the Surface goal and current plan
- `Alt+4` — keyboard map

Use `Ctrl+X` or `/stop` to cancel the current provider request or tool loop.
`/agent auto` keeps the existing unrestricted auto-approve mode.

Only visible model output and explicit structured reasoning summaries are shown.
Private hidden chain-of-thought and provider thinking blocks are not forwarded.
