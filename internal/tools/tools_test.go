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

func TestReadFileBlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": filepath.Join("outside", "secret.txt")},
	})

	if result.OK || !strings.Contains(result.Error, "escapes workspace") {
		t.Fatalf("symlink escape read = %#v, want blocked", result)
	}
}

func TestApplyPatchBlocksSymlinkEscapeForNewFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name: "apply_patch",
		Arguments: map[string]any{
			"path":    filepath.Join("outside", "created.txt"),
			"content": "nope\n",
		},
	})

	if result.OK || !strings.Contains(result.Error, "escapes workspace") {
		t.Fatalf("symlink escape write = %#v, want blocked", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was created: %v", err)
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

func TestAutoApproveNeverCreatesApprovalPrompts(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)

	for _, name := range []string{"read_file", "apply_patch", "shell", "go_test", "unknown_tool"} {
		if registry.RequiresApproval(name) {
			t.Fatalf("%s unexpectedly requires approval in auto mode", name)
		}
	}
}

func TestToolSpecsExposeSchemas(t *testing.T) {
	specs := ToolSpecs()
	if len(specs) == 0 {
		t.Fatal("expected tool specs")
	}
	var readFileFound bool
	for _, spec := range specs {
		if spec.Name != "read_file" {
			continue
		}
		readFileFound = true
		if spec.Parameters.Type != "object" || spec.Parameters.AdditionalProperties {
			t.Fatalf("read_file schema = %#v, want closed object schema", spec.Parameters)
		}
		if spec.Parameters.Properties["path"].Type != "string" {
			t.Fatalf("path property = %#v, want string", spec.Parameters.Properties["path"])
		}
		if len(spec.Parameters.Required) == 0 || spec.Parameters.Required[0] != "path" {
			t.Fatalf("required = %#v, want path", spec.Parameters.Required)
		}
	}
	if !readFileFound {
		t.Fatal("read_file tool spec not found")
	}
}

func TestValidateRejectsUnknownAndWrongTypedArguments(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)

	if err := registry.Validate(Call{Name: "read_file", Arguments: map[string]any{"path": "x.txt", "surprise": true}}); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("unknown argument error = %v, want surprise rejection", err)
	}
	if err := registry.Validate(Call{Name: "read_file", Arguments: map[string]any{"path": "x.txt", "start_line": "one"}}); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("wrong type error = %v, want integer rejection", err)
	}
	if err := registry.Validate(Call{Name: "apply_patch", Arguments: map[string]any{"path": "x.txt"}}); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("missing content error = %v, want content rejection", err)
	}
}

func TestExecuteAddsStableResultMetadata(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{Name: "git_status", Arguments: map[string]any{}})

	if result.Metadata == nil {
		t.Fatal("expected metadata")
	}
	if _, ok := result.Metadata["duration_ms"]; !ok {
		t.Fatalf("metadata = %#v, want duration_ms", result.Metadata)
	}
	if result.Metadata["risk"] != string(RiskRead) {
		t.Fatalf("risk = %#v, want read", result.Metadata["risk"])
	}
}

func TestFingerprintIsStableCompactAndArgumentSensitive(t *testing.T) {
	first := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}})
	reordered := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"content": "package main\n", "path": "main.go"}})
	changed := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package changed\n"}})

	if first != reordered {
		t.Fatalf("fingerprint changed with map order: %q != %q", first, reordered)
	}
	if first == changed {
		t.Fatalf("fingerprint ignored argument change: %q", first)
	}
	if len(first) > 96 || strings.Contains(first, "package main") {
		t.Fatalf("fingerprint is not compact/redacted: %q", first)
	}
}

func TestListFilesReturnsExplicitEmptyDirectoryEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pong game"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(config.Config{WorkspaceRoot: root, ApprovalPolicy: config.ApprovalAutoApprove})
	result := registry.Execute(context.Background(), Call{
		Name:      "list_files",
		Arguments: map[string]any{"path": "pong game"},
	})
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Output, "No files found") {
		t.Fatalf("output = %q", result.Output)
	}
	if result.Metadata["count"] != 0 {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}
