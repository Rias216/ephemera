package eval

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPrepareAndGradeWorkspace(t *testing.T) {
	root := t.TempDir()
	task := Task{
		Name:   "fixture",
		Prompt: "write a greeting",
		Setup:  []FileFixture{{Path: "hello.txt", Content: "frontier\n"}},
		Checks: []Check{
			{Name: "content", Path: "hello.txt", Contains: "frontier", Required: true},
			{Name: "command", Command: "test -f hello.txt", Required: true},
		},
	}
	if err := PrepareWorkspace(root, task); err != nil {
		t.Fatal(err)
	}
	report := Grade(context.Background(), root, task, time.Second)
	if !report.Passed() {
		t.Fatalf("expected pass: %#v", report)
	}
}

func TestPrepareRejectsPathEscape(t *testing.T) {
	err := PrepareWorkspace(t.TempDir(), Task{Setup: []FileFixture{{Path: "../escape", Content: "x"}}})
	if err == nil {
		t.Fatal("expected path escape rejection")
	}
}

func TestFormatReportIncludesFailure(t *testing.T) {
	report := Report{Task: "broken", Results: []CheckResult{{Name: "tests", Passed: false, Evidence: "exit 1"}}}
	formatted := FormatReport(report)
	if !strings.Contains(formatted, "[FAIL] tests") || !strings.Contains(formatted, "exit 1") {
		t.Fatalf("unexpected report: %s", formatted)
	}
}
