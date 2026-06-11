# ✦ Ephemera

> What vanishes may still illuminate.

Ephemera is a compact reasoning harness with a native terminal interface. It
keeps model deliberation private, applies a provider-neutral reasoning contract,
and returns dense, useful answers through a rose-lit Bubble Tea TUI.

## Features

- Native Go TUI: Bubble Tea, Bubbles, Lip Gloss, and Glamour
- Rose-pink dark theme (`#FF69B4` / `#DB2777`) plus a monochrome theme
- Providers: Ollama, OpenAI, and Anthropic
- Official Go clients for Ollama, OpenAI, and Anthropic
- Named JSON sessions with automatic persistence
- Normal, deep-reason, concise, and creative modes
- Scrollable Markdown answers and terminal mouse-wheel support
- Clipboard copy through `/copy` or `Ctrl+Y`
- One static binary when built with `make static`

## Requirements

- Go 1.22.8 or newer
- A terminal with ANSI color support
- One of:
  - Ollama running locally
  - `OPENAI_API_KEY`
  - `ANTHROPIC_API_KEY`

The project is otherwise pure Go. `requirements.txt` intentionally contains no
Python dependencies.

## Install

```bash
git clone https://github.com/ephemera-ai/ephemera.git
cd ephemera
go mod tidy
make build
./bin/ephemera
```

Build a stripped, CGO-free binary:

```bash
make static VERSION=0.1.0
```

Install into your Go binary path:

```bash
make install VERSION=0.1.0
```

## Provider setup

### Ollama

```bash
ollama pull qwen3:8b
ollama serve
./bin/ephemera --provider ollama --model qwen3:8b
```

`OLLAMA_HOST` is optional and defaults to `http://localhost:11434`.

### OpenAI

```bash
export OPENAI_API_KEY='...'
./bin/ephemera --provider openai --model gpt-5.4-mini
```

### Anthropic

```bash
export ANTHROPIC_API_KEY='...'
./bin/ephemera --provider anthropic --model claude-sonnet-4-6
```

Model identifiers are configuration, not hard-coded capability checks. Use
`/model <id>` whenever a provider adds or retires a model.

## Usage

Start a new unnamed session:

```bash
ephemera
```

Open or create a named session:

```bash
ephemera --session architecture-notes
```

Override startup settings:

```bash
ephemera \
  --provider anthropic \
  --model claude-sonnet-4-6 \
  --mode deep-reason \
  --session compiler-design
```

Inside the TUI:

```text
/help
/clear
/new [name]
/save [name]
/load <name>
/sessions
/provider <ollama|openai|anthropic>
/model <model-id>
/mode <normal|deep-reason|concise|creative>
/theme <rose|mono>
/copy
/quit
```

Keyboard shortcuts:

| Key | Action |
|---|---|
| `Enter` | Send prompt or run command |
| `PgUp` / `PgDn` | Scroll the transcript |
| `Ctrl+U` / `Ctrl+D` | Half-page scroll |
| `Ctrl+Y` | Copy last answer |
| `Ctrl+C` | Save and quit |

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

API keys are never written to disk.

## Reasoning design

The harness sends a system/developer prompt that instructs the model to:

1. identify objectives and constraints,
2. compare approaches,
3. test assumptions and edge cases,
4. critique and repair its draft,
5. return only the smallest complete final answer.

Private chain-of-thought is neither requested for display nor stored separately.
Anthropic thinking blocks are explicitly ignored; only final text blocks enter
the transcript.

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
│   └── tui/model.go
├── .env.example
├── .gitignore
├── go.mod
├── LICENSE
├── Makefile
├── README.md
└── requirements.txt
```

## Development

```bash
make fmt
make vet
make test
make run
```

## Static binary notes

`CGO_ENABLED=0` produces a self-contained executable. Clipboard integration is
best-effort and depends on facilities available on the host desktop/session;
Ephemera reports a non-fatal status message when copying is unavailable.

## License

MIT
