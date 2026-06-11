# Ephemera CLI renderer

This module is a deliberately small compatibility implementation of the
Glamour API surface used by Ephemera. It keeps the existing TUI integration
stable while rendering model output as terminal-native structures:

- compact command-line headings instead of document headings;
- framed, numbered code blocks;
- bounded tables that never overflow the viewport;
- task, ordered, and unordered lists;
- inline emphasis, code, links, and Unicode-aware wrapping.

It is local to the repository through a `replace` directive. The compatibility
types under `ansi` and `styles` exist only for the fields configured by
`internal/tui/model.go`.
