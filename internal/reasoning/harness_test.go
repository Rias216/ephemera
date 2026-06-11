package reasoning

import (
	"strings"
	"testing"
)

func TestParseModes(t *testing.T) {
	t.Parallel()

	for _, want := range []Mode{ModeNormal, ModeDeep, ModeConcise, ModeCreative} {
		got, err := Parse("  " + strings.ToUpper(string(want)) + "  ")
		if err != nil {
			t.Fatalf("Parse(%q): %v", want, err)
		}
		if got != want {
			t.Fatalf("Parse(%q) = %q, want %q", want, got, want)
		}
	}
	if _, err := Parse("oracle"); err == nil {
		t.Fatal("Parse(oracle) unexpectedly succeeded")
	}
}

func TestSystemPromptKeepsReasoningPrivate(t *testing.T) {
	t.Parallel()

	prompt := SystemPrompt(ModeDeep)
	for _, fragment := range []string{"Keep private reasoning private", "MODE: DEEP-REASON", "Critique"} {
		if !strings.Contains(prompt, fragment) {
			t.Errorf("SystemPrompt() missing %q", fragment)
		}
	}
}

func TestTemperatureOrdering(t *testing.T) {
	t.Parallel()

	if !(ModeConcise.Temperature() < ModeNormal.Temperature() &&
		ModeNormal.Temperature() < ModeCreative.Temperature()) {
		t.Fatal("expected concise < normal < creative temperature")
	}
}
