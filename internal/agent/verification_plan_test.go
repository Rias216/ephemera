package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestVerificationPlanNarrowsGoSuiteToChangedPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AutoTestCommand = "go test ./..."
	runner := Runner{Config: cfg, Tools: tools.NewRegistry(cfg)}
	state := &runState{
		changedPaths:          map[string]bool{"pong/pong.go": true, "pong/pong_test.go": true},
		changedDirectories:    map[string]bool{"pong": true},
		runtimeChangedPaths:   map[string]bool{".ephemera/run.sh": true},
		projectManifestSource: "discovered",
	}
	plan := runner.buildVerificationPlan(state)
	if !plan.Applicable || plan.Scope != "task" || plan.Command != "go test ./pong/..." {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestVerificationPlanSkipsUnrelatedSuiteForNonCodeTask(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AutoTestCommand = "go test ./..."
	runner := Runner{Config: cfg, Tools: tools.NewRegistry(cfg)}
	state := &runState{changedPaths: map[string]bool{"README.md": true}, projectManifestSource: "discovered"}
	plan := runner.buildVerificationPlan(state)
	if plan.Applicable || plan.Scope != "not-applicable" {
		t.Fatalf("unexpected non-code plan: %#v", plan)
	}
}

func TestVerificationPlanHonorsExplicitManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AutoTestCommand = "go test ./..."
	runner := Runner{Config: cfg, Tools: tools.NewRegistry(cfg)}
	state := &runState{changedPaths: map[string]bool{"pong/pong.go": true}, projectManifestSource: "file"}
	plan := runner.buildVerificationPlan(state)
	if !plan.Applicable || plan.Scope != "manifest" || plan.Command != "go test ./..." {
		t.Fatalf("explicit manifest was narrowed: %#v", plan)
	}
}
