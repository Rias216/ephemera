package tui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/theme"
)

var cliANSIPattern = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\a]*(?:\a|\x1b\\))`)

func cliPlain(value string) string { return cliANSIPattern.ReplaceAllString(value, "") }

func TestCLIRendererPaintsEveryRowToExactWidth(t *testing.T) {
	renderer := newCLIRenderer(theme.New("rose"), 54)
	output := renderer.Render("### Result\n\nA short line.\n\n- item\n\n```go\nfmt.Println(\"hello\")\n```")
	for index, line := range strings.Split(output, "\n") {
		if got := lipgloss.Width(line); got != 54 {
			t.Fatalf("row %d width = %d, want 54: %q", index, got, cliPlain(line))
		}
	}
}

func TestCLIRendererRemovesUntrustedANSI(t *testing.T) {
	renderer := newCLIRenderer(theme.New("rose"), 42)
	output := renderer.Render("safe \x1b[40mblack-background\x1b[0m text")
	if strings.Contains(output, "\x1b[40m") || strings.Contains(output, "\x1b[0m") {
		t.Fatalf("untrusted ANSI survived rendering: %q", output)
	}
	if !strings.Contains(cliPlain(output), "black-background") {
		t.Fatalf("text was removed with ANSI: %q", cliPlain(output))
	}
}

func TestCLIRendererPreservesExplicitLineBreaks(t *testing.T) {
	renderer := newCLIRenderer(theme.New("mono"), 38)
	output := cliPlain(renderer.Render("first log line\nsecond log line"))
	lines := strings.Split(output, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two rendered rows, got %d: %q", len(lines), output)
	}
	if !strings.Contains(lines[0], "first log line") || !strings.Contains(lines[1], "second log line") {
		t.Fatalf("explicit line breaks were not preserved: %q", output)
	}
}

func TestCLIRendererKeepsTablesAndCodeInsideViewport(t *testing.T) {
	renderer := newCLIRenderer(theme.New("rose"), 40)
	output := renderer.Render("| Component | State |\n| --- | --- |\n| renderer | ready |\n\n```text\nthis is a deliberately very long line that must wrap safely\n```")
	plain := cliPlain(output)
	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "╭─ text") {
		t.Fatalf("expected table and code frames:\n%s", plain)
	}
	for index, line := range strings.Split(output, "\n") {
		if got := lipgloss.Width(line); got != 40 {
			t.Fatalf("row %d width = %d, want 40", index, got)
		}
	}
}
