package glamour

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/styles"
)

var sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(value string) string { return sgrPattern.ReplaceAllString(value, "") }

func newTestRenderer(t *testing.T, width int) *TermRenderer {
	t.Helper()
	renderer, err := NewTermRenderer(WithStyles(styles.DarkStyleConfig), WithWordWrap(width))
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

func TestRenderUsesCLIBlocks(t *testing.T) {
	renderer := newTestRenderer(t, 42)
	output, err := renderer.Render("# Result\n\n- first\n- second\n\n```go\nfmt.Println(\"hello\")\n```")
	if err != nil {
		t.Fatal(err)
	}
	got := plain(output)
	for _, expected := range []string{"╭─ RESULT", "• first", "╭─ go", "1 │ fmt.Println", "╰"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in output:\n%s", expected, got)
		}
	}
}

func TestRenderTableFitsWidth(t *testing.T) {
	renderer := newTestRenderer(t, 36)
	output, err := renderer.Render("| Name | State |\n| --- | --- |\n| renderer | ready |")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(plain(output)), "\n") {
		if cellWidth(line) > 36 {
			t.Fatalf("line width %d exceeds renderer width: %q", cellWidth(line), line)
		}
	}
}

func TestRenderWrapsUnicodeAndInlineMarkup(t *testing.T) {
	renderer := newTestRenderer(t, 24)
	output, err := renderer.Render("Use **strong output**, `commands`, and emoji 🚀 without breaking the viewport.")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(plain(output)), "\n") {
		if cellWidth(line) > 24 {
			t.Fatalf("line width %d exceeds renderer width: %q", cellWidth(line), line)
		}
	}
	if !strings.Contains(plain(output), "commands") {
		t.Fatalf("inline code disappeared: %s", plain(output))
	}
}
