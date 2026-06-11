# CLI-native transcript renderer

This source archive includes the complete Ephemera project with a local
terminal-native renderer applied through `third_party/glamourcli`.

The renderer keeps the existing `glamour.NewTermRenderer` integration while
rendering assistant output as compact CLI structures:

- terminal headings;
- framed code blocks with line numbers;
- bounded tables;
- ordered, unordered, and task lists;
- quotes, links, inline code, emphasis, and Unicode-aware wrapping.

Build and test with the Go version declared in `go.mod`:

```sh
go test ./...
go build -o bin/ephemera ./cmd/ephemera
```
