package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	defer store.Close()
	session := New("deep work", "ollama", "qwen3:8b", reasoning.ModeDeep)
	session.Append("user", "What remains?")
	session.Append("assistant", "Only the useful trace.")
	session.AppendEvent(Event{
		ID:     "evt-1",
		Type:   EventToolCall,
		Title:  "Read file",
		Tool:   "read_file",
		Status: "ok",
		Metadata: map[string]any{
			"path": "README.md",
		},
	})

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
	if len(loaded.Events) != 1 || loaded.Events[0].Metadata["path"] != "README.md" {
		t.Fatalf("unexpected loaded events: %#v", loaded.Events)
	}

	names, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(names) != 1 || names[0] != "deep-work" {
		t.Fatalf("List() = %#v", names)
	}

	info, err := os.Stat(filepath.Join(dir, "history.sqlite"))
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
	defer store.Close()
	if _, err := store.Load("absent"); err == nil {
		t.Fatal("Load(absent) unexpectedly succeeded")
	}
}

func TestStoreLoadsLegacyJSONSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := &Store{dir: dir}
	defer store.Close()
	session := New("legacy", "ollama", "model", reasoning.ModeNormal)
	session.Append("user", "older file")
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "legacy" || len(loaded.Messages) != 1 {
		t.Fatalf("unexpected legacy session: %#v", loaded)
	}
	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "legacy" {
		t.Fatalf("List() = %#v", names)
	}
}

func TestAgentSnapshotPersistsWithSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	store, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := New("snapshot", "ollama", "model", reasoning.ModeNormal)
	session.Agent = AgentSnapshot{
		RunID:        "run-1",
		Status:       "complete",
		Phase:        "complete",
		Iteration:    3,
		Goal:         "repair the renderer",
		Reasoning:    "**Goal**\nrepair the renderer",
		Plan:         "- [x] inspect\n- [x] patch",
		Verification: "tests passed",
		Verified:     true,
		Completed:    true,
		UpdatedAt:    time.Now(),
	}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(session.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agent.RunID != "run-1" || !loaded.Agent.Verified || loaded.Agent.Reasoning == "" {
		t.Fatalf("snapshot was not preserved: %#v", loaded.Agent)
	}
}
