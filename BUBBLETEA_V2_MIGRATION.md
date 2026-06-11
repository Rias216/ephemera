# Bubble Tea v2 rendering migration

This source tree uses Bubble Tea v2's cell-diff renderer instead of relying on
animation pauses or localized redraw workarounds.

## Rendering changes

- Alternate-screen, focus-reporting, mouse mode, foreground/background colors,
  window title, and cursor state are declared in `Model.View()`.
- The renderer is capped at 60 FPS with `tea.WithFPS`.
- Outline motion is based on elapsed time, so coalesced or dropped frames never
  slow the animation.
- The entire pink base gradient shifts continuously beneath the faster glimmer.
- The text input uses a real terminal cursor; cursor blinking no longer mutates
  the rendered input string.
- Autocomplete geometry remains fixed while a command is being entered.
- No code outside Bubble Tea writes to stdout while the TUI is active.

## Toolchain

Bubble Tea v2.0.6 and Bubbles v2.1.0 require Go 1.25. Run `go mod tidy` after
installing that toolchain to create `go.sum`, then build normally.

## Upgrading over an older checkout

Bubble Tea v2 uses different module paths from v1. If this source is copied over
an existing checkout, the old `go.sum` may not contain the v2 checksums. The
updated `run.bat` runs `go mod tidy` and `go mod verify` before each development
build, so it safely refreshes the lockfile.

To repair an existing checkout manually:

```bat
go mod tidy
go mod verify
go build -mod=mod -o bin\ephemera.exe .\cmd\ephemera
```

Do not run the package-specific `go get` commands printed once for every import;
a single `go mod tidy` resolves the complete module graph.
