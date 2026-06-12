package agent

import (
	"testing"

	workruntime "github.com/ephemera-ai/ephemera/internal/runtime"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestAcceptanceContractRequiresReadbackDiffAndTests(t *testing.T) {
	contract := newAcceptanceContract("fix it", workruntime.ProjectManifest{
		ProtectedPaths:   []string{".env"},
		AcceptanceChecks: []workruntime.AcceptanceCheck{{ID: "tests", Description: "tests pass", Command: "go test ./...", Required: true}},
	}, "test")
	changed := map[string]bool{"main.go": true}
	if report := contract.Evaluate(changed); report.Passed {
		t.Fatal("contract passed without evidence")
	}
	contract.Observe(tools.Call{Name: "read_file", Arguments: map[string]any{"path": "main.go"}}, tools.Result{Tool: "read_file", OK: true, Output: "package main", Metadata: map[string]any{"path": "main.go"}})
	contract.Observe(tools.Call{Name: "git_diff"}, tools.Result{Tool: "git_diff", OK: true, Output: "diff"})
	contract.Observe(tools.Call{Name: "go_test"}, tools.Result{Tool: "go_test", OK: true, Output: "ok"})
	if report := contract.Evaluate(changed); !report.Passed {
		t.Fatalf("expected pass: %#v", report)
	}
}

func TestAcceptanceContractBlocksProtectedPath(t *testing.T) {
	contract := newAcceptanceContract("fix it", workruntime.ProjectManifest{ProtectedPaths: []string{".env*"}}, "test")
	report := contract.Evaluate(map[string]bool{".env.local": true})
	if report.Passed || len(report.Blockers) == 0 {
		t.Fatalf("expected protected-path blocker: %#v", report)
	}
}
