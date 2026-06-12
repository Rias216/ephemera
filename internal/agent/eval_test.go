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
	if len(report.Results) < 30 {
		t.Fatalf("eval cases = %d, want at least 30", len(report.Results))
	}
	rendered := FormatEvalReport(report)
	for _, want := range []string{"json-read-tool", "native-tool-call", "parallel-read-batch", "dry-run-write-preview", "semantic-codebase-index", "snapshot-auto-rollback", "provider-prompt-profile", "github-tool-catalog", "security-audit-dry-run", "Passed: 32 / 32"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("report missing %q:\n%s", want, rendered)
		}
	}
}
