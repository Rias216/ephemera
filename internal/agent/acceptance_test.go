package agent

import (
	"strings"
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

func TestAcceptanceContractVerifiesCreatedDirectory(t *testing.T) {
	contract := newAcceptanceContract("create folder", workruntime.ProjectManifest{}, "test")
	contract.Observe(tools.Call{Name: "list_files", Arguments: map[string]any{"path": "new-folder"}}, tools.Result{Tool: "list_files", OK: true, Output: "empty", Metadata: map[string]any{"path": "new-folder"}})
	contract.Observe(tools.Call{Name: "git_status"}, tools.Result{Tool: "git_status", OK: true})
	report := contract.EvaluateArtifacts(nil, map[string]bool{"new-folder": true})
	if !report.Passed {
		t.Fatalf("created directory did not satisfy readback contract: %#v", report)
	}
}

func TestAcceptanceContractAcceptsTaskScopedVerification(t *testing.T) {
	contract := newAcceptanceContract("create pong", workruntime.ProjectManifest{AcceptanceChecks: []workruntime.AcceptanceCheck{{ID: "tests", Description: "full tests", Command: "go test ./...", Required: true}}}, "discovered")
	contract.Observe(tools.Call{Name: "read_file", Arguments: map[string]any{"path": "pong/pong.go"}}, tools.Result{Tool: "read_file", OK: true, Metadata: map[string]any{"path": "pong/pong.go"}})
	contract.Observe(tools.Call{Name: "git_diff"}, tools.Result{Tool: "git_diff", OK: true})
	contract.Observe(tools.Call{Name: "go_test", Arguments: map[string]any{"command": "go test ./..."}}, tools.Result{Tool: "go_test", OK: false, Error: "unrelated package failed", Metadata: map[string]any{"command": "go test ./...", "verification_scope": "full"}})
	contract.Observe(tools.Call{Name: "go_test", Arguments: map[string]any{"command": "go test ./pong/..."}}, tools.Result{Tool: "go_test", OK: true, Metadata: map[string]any{"command": "go test ./pong/...", "verification_scope": "task"}})
	report := contract.Evaluate(map[string]bool{"pong/pong.go": true})
	if !report.Passed {
		t.Fatalf("task-scoped verification did not supersede unrelated full-suite failure: %#v", report)
	}
	for _, evidence := range report.Evidence {
		if strings.Contains(strings.ToLower(evidence), "failed") {
			t.Fatalf("superseded full-suite failure leaked into completion evidence: %#v", report.Evidence)
		}
	}
}
