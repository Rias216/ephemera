package tui

import "testing"

func TestPartialDecisionPreview(t *testing.T) {
	raw := `{"reasoning":{"goal":"fix black cells","approach":["replace unpainted padding"]},"summary":"renderer repair","plan":["paint every row"]}`
	if got := partialJSONStringField(raw, "goal"); got != "fix black cells" {
		t.Fatalf("goal = %q", got)
	}
	if got := partialJSONArrayFirstString(raw, "plan"); got != "paint every row" {
		t.Fatalf("plan = %q", got)
	}
}

func TestPartialDecisionPreviewWaitsForCompleteString(t *testing.T) {
	raw := `{"reasoning":{"goal":"still streaming`
	if got := partialJSONStringField(raw, "goal"); got != "" {
		t.Fatalf("expected incomplete field to be hidden, got %q", got)
	}
}
