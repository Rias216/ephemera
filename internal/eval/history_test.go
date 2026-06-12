package eval

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryAppendFormatAndDiff(t *testing.T) {
	root := t.TempDir()
	first, err := AppendHistory(root, HistoryEntry{ID: "before", Task: "reasoning", Passed: true, TokenCost: 100, ReasoningQuality: 80, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	second, err := AppendHistory(root, HistoryEntry{ID: "after", Task: "reasoning", Passed: false, TokenCost: 75, ReasoningQuality: 70, Duration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	history, err := LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Runs) != 2 || first.ID != "before" || second.ID != "after" {
		t.Fatalf("history = %#v", history)
	}
	if formatted := FormatHistory(history); !strings.Contains(formatted, "before") || !strings.Contains(formatted, "after") {
		t.Fatalf("formatted = %q", formatted)
	}
	diff, err := DiffHistory(history, "before", "after")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "regression=true") || !strings.Contains(diff, "-25") {
		t.Fatalf("diff = %q", diff)
	}
}
