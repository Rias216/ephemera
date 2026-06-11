package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestReassertBackgroundAfterANSIReset(t *testing.T) {
	background := lipgloss.Color("#140A12")
	result := reassertBackground("text\x1b[0m   tail", background)
	want := "\x1b[0m\x1b[48;2;20;10;18m"
	if !strings.Contains(result, want) {
		t.Fatalf("background was not restored after reset: %q", result)
	}
}

func TestReassertBackgroundOnEveryRow(t *testing.T) {
	background := lipgloss.Color("#08050A")
	result := reassertBackground("one\ntwo", background)
	if strings.Count(result, "\x1b[48;2;8;5;10m") < 2 {
		t.Fatalf("background was not applied to every row: %q", result)
	}
}
