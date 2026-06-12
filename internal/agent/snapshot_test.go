package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSnapshotRestoresModifiedDeletedAndNewFiles(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "src", "main.go")
	deleted := filepath.Join(root, "README.md")
	if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("package original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deleted, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := createWorkspaceSnapshot(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Cleanup()
	if err := os.WriteFile(original, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "new.txt")
	if err := os.WriteFile(created, []byte("remove me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := snapshot.Restore()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(original)
	if string(data) != "package original\n" {
		t.Fatalf("modified file = %q", data)
	}
	if data, err = os.ReadFile(deleted); err != nil || string(data) != "keep me\n" {
		t.Fatalf("deleted file restore = %q, %v", data, err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("new file survived rollback: %v", err)
	}
	if report.RestoredFiles < 2 || report.RemovedFiles < 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestWorkspaceSnapshotHonorsSizeLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := createWorkspaceSnapshot(root, 1024); err == nil {
		snapshot.Cleanup()
		t.Fatal("expected snapshot size limit error")
	}
}
