package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestProjectMemoryIncludesOnlyRelevantSuccessfulRuns(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, ".ephemera")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	memory := episodicMemory{Version: 1, Episodes: []memoryEpisode{
		{CompletedAt: time.Now().Add(-time.Hour), Goal: "fix authentication session refresh", Summary: "repaired auth expiry", ChangedPaths: []string{"internal/auth/session.go"}, ToolSequence: []string{"read_file", "grep_regex", "apply_patch", "go_test"}, Verified: true},
		{CompletedAt: time.Now(), Goal: "redesign terminal colors", Summary: "updated palette", ChangedPaths: []string{"internal/tui/theme.go"}, ToolSequence: []string{"read_file", "apply_patch"}, Verified: true},
	}}
	data, err := json.Marshal(memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "memory.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	runner := NewRunner(cfg, nil)

	got := runner.projectMemory("debug auth session expiry")
	if !strings.Contains(got, "authentication session refresh") || !strings.Contains(got, "read_file → grep_regex → apply_patch → go_test") {
		t.Fatalf("relevant learned pattern missing:\n%s", got)
	}
	if strings.Contains(got, "terminal colors") {
		t.Fatalf("unrelated memory leaked into context:\n%s", got)
	}
}
