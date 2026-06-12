package agent

import (
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestParseInstrumentSeverity(t *testing.T) {
	cases := map[string]instrumentSeverity{
		"VERDICT: CLEAN\nFEEDBACK: sound": instrumentClean,
		"VERDICT: LOW\nFEEDBACK: minor":   instrumentLow,
		"VERDICT: MEDIUM\nFEEDBACK: bug":  instrumentMedium,
		"VERDICT: HIGH\nFEEDBACK: risk":   instrumentHigh,
	}
	for input, want := range cases {
		if got := parseInstrumentSeverity(input); got != want {
			t.Fatalf("parseInstrumentSeverity(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInstrumentIncorporationIsDeterministic(t *testing.T) {
	lowWeight := DirectorSession{weight: 20}
	if !lowWeight.shouldIncorporate(instrumentHigh) || !lowWeight.shouldIncorporate(instrumentMedium) || lowWeight.shouldIncorporate(instrumentLow) || lowWeight.shouldIncorporate(instrumentClean) {
		t.Fatal("unexpected 20% incorporation policy")
	}
	highWeight := DirectorSession{weight: 60}
	if !highWeight.shouldIncorporate(instrumentLow) {
		t.Fatal("60% weight should incorporate low-severity advice")
	}
}

func TestAutoRouteSimpleReadToSubagent(t *testing.T) {
	runner := Runner{Config: config.Default()}
	runner.Config.SubagentModel = "cheap-model"
	runner.Config.SubagentAutoRoute = true
	action := modelAction{Actions: []modelToolAction{{Name: "search", Tool: "search", Arguments: map[string]any{"query": "needle"}, ProviderCallID: "native-1"}}}
	routed := runner.autoRouteSimpleActions(action)
	if len(routed.Actions) != 1 || routed.Actions[0].Name != "delegate" {
		t.Fatalf("routed action = %#v", routed.Actions)
	}
	if routed.Actions[0].ProviderCallID != "" {
		t.Fatal("auto-routed action retained the native provider call ID")
	}

	runner.Config.SubagentEnabled = false
	unrouted := runner.autoRouteSimpleActions(action)
	if unrouted.Actions[0].Name != "search" {
		t.Fatalf("disabled subagent still routed action: %#v", unrouted.Actions)
	}
}
