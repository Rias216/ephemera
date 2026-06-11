package history

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

func TestSanitize(t *testing.T) {
	t.Parallel()

	got := Sanitize("  architecture / notes?!  ")
	if got != "architecture-notes" {
		t.Fatalf("Sanitize() = %q, want architecture-notes", got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := &Store{dir: dir}
	session := New("deep work", "ollama", "qwen3:8b", reasoning.ModeDeep)
	session.Append("user", "What remains?")
	session.Append("assistant", "Only the useful trace.")

	if err := store.Save(session); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	loaded, err := store.Load("deep-work")
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Name != "deep-work" || len(loaded.Messages) != 2 {
		t.Fatalf("unexpected loaded session: %#v", loaded)
	}

	names, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(names) != 1 || names[0] != "deep-work" {
		t.Fatalf("List() = %#v", names)
	}

	info, err := os.Stat(filepath.Join(dir, "deep-work.json"))
	if err != nil {
		t.Fatalf("Stat(): %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestLoadMissingSession(t *testing.T) {
	t.Parallel()

	store := &Store{dir: t.TempDir()}
	if _, err := store.Load("absent"); err == nil {
		t.Fatal("Load(absent) unexpectedly succeeded")
	}
}
