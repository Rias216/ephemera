package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestCodeIntelligenceTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n\nrequire example.com/dep v1.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package demo\n\nimport \"fmt\"\n\nfunc RenderThing() { fmt.Println(\"x\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	for _, call := range []Call{
		{Name: "grep_regex", Arguments: map[string]any{"pattern": "Render.*", "path": "."}},
		{Name: "find_symbol", Arguments: map[string]any{"symbol": "RenderThing"}},
		{Name: "find_refs", Arguments: map[string]any{"symbol": "RenderThing"}},
		{Name: "file_summary", Arguments: map[string]any{"path": "main.go"}},
		{Name: "dependency_graph", Arguments: map[string]any{"path": "."}},
		{Name: "detect_project_type"},
		{Name: "list_dependencies"},
	} {
		result := registry.Execute(context.Background(), call)
		if !result.OK {
			t.Fatalf("%s failed: %#v", call.Name, result)
		}
		if strings.TrimSpace(result.Output) == "" {
			t.Fatalf("%s returned empty output", call.Name)
		}
	}
}

func TestVersionedToolContracts(t *testing.T) {
	for _, spec := range ToolSpecs() {
		if spec.Version == "" {
			t.Fatalf("tool %s has no contract version", spec.Name)
		}
	}
}
