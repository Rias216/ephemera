package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRunDeterministicEvalPassesCoreCases(t *testing.T) {
	report, err := RunDeterministicEval(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed() != 0 {
		t.Fatalf("eval failed:\n%s", FormatEvalReport(report))
	}
	if len(report.Results) < 7 {
		t.Fatalf("eval cases = %d, want at least 7", len(report.Results))
	}
	rendered := FormatEvalReport(report)
	for _, want := range []string{"json-read-tool", "native-tool-call", "plain-text-repair", "structured-reasoning-surface", "verified-write", "Passed: 7 / 7"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("report missing %q:\n%s", want, rendered)
		}
	}
}
