package reasoning

import "testing"

func TestAdaptiveModeClassifiesTaskDepth(t *testing.T) {
	cases := []struct {
		prompt string
		want   Mode
	}{
		{"hello", ModeConcise},
		{"fix this parsing bug", ModeNormal},
		{"implement a multi-file provider architecture with tests and migration", ModeDeep},
	}
	for _, item := range cases {
		if got := AdaptiveMode(ModeNormal, item.prompt, true); got != item.want {
			t.Fatalf("AdaptiveMode(%q) = %q, want %q", item.prompt, got, item.want)
		}
	}
	if got := AdaptiveMode(ModeCreative, "implement everything", true); got != ModeCreative {
		t.Fatalf("explicit mode was overridden: %q", got)
	}
}
