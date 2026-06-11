package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestReadFileStaysInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": filepath.Join(root, "..", "outside.txt")},
	})

	if result.OK || !strings.Contains(result.Error, "escapes workspace") {
		t.Fatalf("read outside workspace = %#v, want blocked", result)
	}
}

func TestApplyPatchWritesCompleteContentWhenApproved(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name: "apply_patch",
		Arguments: map[string]any{
			"path":    "hello.txt",
			"content": "rose\n",
		},
	})
	if !result.OK {
		t.Fatalf("apply_patch failed: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rose\n" {
		t.Fatalf("file content = %q", data)
	}
}

func TestApproveWritesRequiresApprovalForShellAndPatch(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	registry := NewRegistry(cfg)

	if registry.RequiresApproval("read_file") {
		t.Fatal("read_file should not require approval")
	}
	for _, name := range []string{"apply_patch", "shell", "go_test"} {
		if !registry.RequiresApproval(name) {
			t.Fatalf("%s should require approval", name)
		}
	}
}

func TestShellBlocksDestructiveCommands(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "Remove-Item -Recurse ."},
	})

	if result.OK || !strings.Contains(result.Error, "destructive") {
		t.Fatalf("destructive shell result = %#v, want blocked", result)
	}
}
