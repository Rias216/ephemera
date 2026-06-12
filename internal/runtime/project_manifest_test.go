package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverGoProjectManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := DiscoverProjectManifest(root, "go test ./internal/...")
	if len(manifest.Tests) != 1 || manifest.Tests[0] != "go test ./internal/..." {
		t.Fatalf("unexpected tests: %#v", manifest.Tests)
	}
	if len(manifest.ProtectedPaths) == 0 || len(manifest.AcceptanceChecks) != 1 {
		t.Fatalf("manifest missing safety/check defaults: %#v", manifest)
	}
}

func TestWriteAndLoadProjectManifest(t *testing.T) {
	root := t.TempDir()
	want := ProjectManifest{Version: 1, Tests: []string{"go test ./..."}, AcceptanceChecks: []AcceptanceCheck{{ID: "tests", Description: "tests pass", Command: "go test ./...", Required: true}}}
	if err := WriteProjectManifest(root, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjectManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tests) != 1 || got.Tests[0] != want.Tests[0] {
		t.Fatalf("unexpected manifest: %#v", got)
	}
}

func TestDiscoveryRespectsDisabledVerification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := DiscoverProjectManifest(root, "")
	if len(manifest.Tests) != 0 || len(manifest.AcceptanceChecks) != 0 {
		t.Fatalf("disabled verification should not synthesize required test checks: %#v", manifest)
	}
}
