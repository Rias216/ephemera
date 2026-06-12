package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/theme"
)

func TestSafetyCommandsUpdateRuntimeConfig(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/sandbox snapshot")
	if m.cfg.SandboxMode != config.SandboxSnapshot {
		t.Fatalf("sandbox mode = %q, want snapshot", m.cfg.SandboxMode)
	}

	_, _ = m.handleCommand("/dry-run on")
	if !m.cfg.AgentDryRun {
		t.Fatal("dry run was not enabled")
	}

	_, _ = m.handleCommand("/rollback auto")
	if !m.cfg.AgentAutoRollback {
		t.Fatal("automatic rollback was not enabled")
	}
	if !strings.Contains(m.notice, "Execution safety") {
		t.Fatalf("safety notice = %q", m.notice)
	}
}

func TestIntelligenceCommandsUpdateRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, ".ephemera", "codebase-index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.WorkspaceRoot = root

	_, _ = m.handleCommand("/index rebuild")
	if !m.cfg.AgentSemanticIndex {
		t.Fatal("index rebuild did not enable semantic indexing")
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("index file still exists or stat failed: %v", err)
	}

	_, _ = m.handleCommand("/tdd on")
	_, _ = m.handleCommand("/learn on")
	if !m.cfg.AgentTDDMode || !m.cfg.AgentLearnMemory {
		t.Fatalf("intelligence flags = tdd:%t learn:%t", m.cfg.AgentTDDMode, m.cfg.AgentLearnMemory)
	}
	if !strings.Contains(m.notice, "Agent intelligence") {
		t.Fatalf("intelligence notice = %q", m.notice)
	}
}

func TestRollbackCommandRestoresRetainedSnapshot(t *testing.T) {
	root := t.TempDir()
	snapshotDir := t.TempDir()
	original := []byte("before\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(snapshotDir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "files", "tracked.txt"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"version":    1,
		"root":       root,
		"directory":  snapshotDir,
		"created_at": time.Now().UTC(),
		"entries": map[string]any{
			"tracked.txt": map[string]any{"mode": 420},
		},
		"bytes": len(original),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.WorkspaceRoot = root
	m.session = history.New("rollback", m.cfg.Provider, m.cfg.Model(), m.cfg.Mode)
	m.session.AppendEvent(history.Event{Type: "recovery", Metadata: map[string]any{"snapshot_path": snapshotDir}})

	_, _ = m.handleCommand("/rollback")

	got, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("tracked.txt = %q, want %q", got, original)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new path survived rollback: %v", err)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory survived successful rollback: %v", err)
	}
	if !strings.Contains(m.status, "complete") || m.session.Agent.Status != "rolled_back" {
		t.Fatalf("status = %q, agent status = %q", m.status, m.session.Agent.Status)
	}
}

func TestNewCommandSpecsExposeSafetyAndIntelligenceControls(t *testing.T) {
	for _, name := range []string{"/sandbox", "/dry-run", "/rollback", "/index", "/tdd", "/learn"} {
		if _, ok := findCommandSpec(name); !ok {
			t.Fatalf("missing command spec %s", name)
		}
	}
}
