package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodebaseIndexBuildsAndRefreshesRelevantDefinitions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "config", "loader.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package config\n\nfunc LoadConfig() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newCodebaseIndexManager(root)
	first := manager.Relevant("LoadConfig configuration", 8)
	if !strings.Contains(first, "internal/config/loader.go") || !strings.Contains(first, "LoadConfig") {
		t.Fatalf("initial index = %q", first)
	}
	if err := os.WriteFile(path, []byte("package config\n\nfunc LoadConfig() {}\nfunc NormalizeConfig() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := manager.Relevant("NormalizeConfig", 8)
	if !strings.Contains(second, "NormalizeConfig") {
		t.Fatalf("refreshed index = %q", second)
	}
	if _, err := os.Stat(filepath.Join(root, ".ephemera", "codebase-index.json")); err != nil {
		t.Fatalf("index was not persisted: %v", err)
	}
}
