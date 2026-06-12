package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestApplyMultiPatchWritesAllTargetsAtomically(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old-a"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)
	call := Call{Name: "apply_multi_patch", Arguments: map[string]any{"patches": []any{
		map[string]any{"path": "a.txt", "content": "new-a"},
		map[string]any{"path": "nested/b.txt", "content": "new-b"},
	}}}

	result := registry.Execute(context.Background(), call)
	if !result.OK {
		t.Fatalf("apply_multi_patch failed: %s", result.Error)
	}
	for path, want := range map[string]string{"a.txt": "new-a", "nested/b.txt": "new-b"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", path, data, want)
		}
	}
	paths, ok := result.Metadata["paths"].([]string)
	if !ok || len(paths) != 2 || !metadataBoolForTest(result.Metadata, "atomic") {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
}

func TestApplyMultiPatchRejectsDuplicateTargetBeforeWriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)
	result := registry.Execute(context.Background(), Call{Name: "apply_multi_patch", Arguments: map[string]any{"patches": []any{
		map[string]any{"path": "a.txt", "content": "one"},
		map[string]any{"path": "a.txt", "content": "two"},
	}}})
	if result.OK || !strings.Contains(result.Error, "repeats target") {
		t.Fatalf("duplicate target result = %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before" {
		t.Fatalf("file changed before validation completed: %q", data)
	}
}

func TestPreviewMultiPatchDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentDryRun = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)
	result := registry.Execute(context.Background(), Call{Name: "apply_multi_patch", Arguments: map[string]any{"patches": []any{
		map[string]any{"path": "a.txt", "content": "a"},
		map[string]any{"path": "b.txt", "content": "b"},
	}}})
	if !result.OK || !strings.Contains(result.Output, "DRY RUN diff") {
		t.Fatalf("dry-run result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote file: %v", err)
	}
}

func TestRollbackMultiPatchRestoresExistingAndRemovesNew(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a.txt")
	created := filepath.Join(root, "nested", "b.txt")
	if err := os.WriteFile(existing, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	targets := []multiPatchTarget{
		{path: existing, rel: "a.txt", existed: true, mode: 0o640, before: []byte("before")},
		{path: created, rel: "nested/b.txt", existed: false, mode: 0o600},
	}
	if err := os.WriteFile(existing, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(created), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackMultiPatch(root, targets); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before" {
		t.Fatalf("existing file = %q", data)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("new file survived rollback: %v", err)
	}
}

func TestMultiPatchSchemaDefinesArrayItems(t *testing.T) {
	tool, ok := Lookup("apply_multi_patch")
	if !ok {
		t.Fatal("apply_multi_patch missing from tool catalog")
	}
	schema := tool.ParameterSchema()
	patches := schema.Properties["patches"]
	if patches.Type != "array" || patches.Items == nil || patches.Items.Type != "object" {
		t.Fatalf("invalid patches schema: %#v", patches)
	}
	if len(patches.Items.Required) != 2 {
		t.Fatalf("patch item required fields = %#v", patches.Items.Required)
	}
}

func metadataBoolForTest(metadata map[string]any, key string) bool {
	value, _ := metadata[key].(bool)
	return value
}

func TestNormalizeMultiPatchAcceptsPortableAliasesAndJSONString(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)
	call, err := registry.Normalize(Call{Name: "apply_multi_patch", Arguments: map[string]any{
		"files": `[{"file":"a.txt","text":"a"},{"filename":"b.txt","body":"b"}]`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	patches, ok := call.Arguments["patches"].([]any)
	if !ok || len(patches) != 2 {
		t.Fatalf("patches = %#v", call.Arguments["patches"])
	}
	first := patches[0].(map[string]any)
	second := patches[1].(map[string]any)
	if first["path"] != "a.txt" || first["content"] != "a" || second["path"] != "b.txt" || second["content"] != "b" {
		t.Fatalf("normalized patches = %#v", patches)
	}
}
