package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestExternalEditReachesApprovalWithoutBypassingInspectGuard(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	externalDir := filepath.Join(parent, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(externalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(externalDir, "note.txt")
	if err := os.WriteFile(external, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	cfg.RequireReadBeforeEdit = true
	runner := Runner{Config: cfg, Tools: tools.NewRegistry(cfg)}
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{"path": external, "content": "after\n"}}
	state := &runState{inspectedPaths: map[string]bool{}}

	if !runner.requiresApproval(call) {
		t.Fatal("external edit did not require approval")
	}
	if err := runner.enforceInspectBeforeEdit(state, call); err == nil || !strings.Contains(err.Error(), "inspect-before-edit") {
		t.Fatalf("guard error = %v, want inspect-before-edit guidance", err)
	}

	identity, err := runner.Tools.PathIdentity(external)
	if err != nil {
		t.Fatal(err)
	}
	state.inspectedPaths[normalizePath(identity)] = true
	if err := runner.enforceInspectBeforeEdit(state, call); err != nil {
		t.Fatalf("inspected external path was still blocked before approval: %v", err)
	}
}

func TestExternalReadProducesPendingApprovalAndExecutesAfterGrant(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	externalDir := filepath.Join(parent, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(externalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(externalDir, "note.txt")
	if err := os.WriteFile(external, []byte("approved read\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	runner := Runner{Config: cfg, Tools: tools.NewRegistry(cfg)}
	state := &runState{
		runID:               "external-path-test",
		callCounts:          map[string]int{},
		completedCalls:      map[string]int{},
		resultCache:         map[string]cachedToolResult{},
		suppressedTools:     map[string]bool{},
		rejectedCalls:       map[string]bool{},
		failedApprovedCalls: map[string]bool{},
		inspectedPaths:      map[string]bool{},
		changedPaths:        map[string]bool{},
	}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{"path": external}}

	result, pending := runner.executeAction(context.Background(), state, call, "Read the user-requested external file.", 1, nil)
	if pending == nil {
		t.Fatalf("external read result = %#v, want pending approval", result)
	}
	if !strings.Contains(pending.Reason, external) {
		t.Fatalf("approval reason = %q, want target path", pending.Reason)
	}

	event := runner.ExecuteApproved(context.Background(), *pending)
	if event.Status == "error" || !strings.Contains(event.Content, "approved read") {
		t.Fatalf("approved external event = %#v", event)
	}
}
