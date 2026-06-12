package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type parallelTestProvider struct{}

func (parallelTestProvider) Name() string { return "parallel-test" }

func (parallelTestProvider) Generate(context.Context, llm.Request) (string, error) {
	return `{"final":"done"}`, nil
}

func (parallelTestProvider) Capabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{MaxParallelTools: 8}
}

func TestAtomicWriteSnapshotsRestoreExistingAndRemoveNewFiles(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.go")
	created := filepath.Join(root, "nested", "created.go")
	if err := os.WriteFile(existing, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = root
	runner := NewRunner(cfg, parallelTestProvider{})
	actions := []modelToolAction{
		{Name: "apply_patch", Arguments: map[string]any{"path": "existing.go", "content": "unused"}},
		{Name: "apply_patch", Arguments: map[string]any{"path": "nested/created.go", "content": "unused"}},
	}
	snapshots, err := runner.snapshotAtomicWriteTargets(actions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(created), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := restoreAtomicWriteTargets(root, snapshots); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatalf("existing file = %q, want original content", data)
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("existing file mode = %o, want 640", info.Mode().Perm())
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("new file survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(created)); !os.IsNotExist(err) {
		t.Fatalf("empty parent survived rollback: %v", err)
	}
}

func TestParallelWritesRequireDisjointTargets(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	runner := NewRunner(cfg, parallelTestProvider{})

	disjoint := []modelToolAction{
		{Name: "apply_patch", Arguments: map[string]any{"path": "a.go", "content": "x"}},
		{Name: "replace_in_file", Arguments: map[string]any{"path": "b.go", "old": "x", "new": "y"}},
	}
	if !runner.canParallelActions(disjoint) {
		t.Fatal("disjoint write batch was not accepted")
	}

	conflicting := append([]modelToolAction(nil), disjoint...)
	conflicting[1].Arguments = map[string]any{"path": "a.go", "old": "x", "new": "y"}
	if runner.canParallelActions(conflicting) {
		t.Fatal("conflicting write batch was accepted")
	}
}
