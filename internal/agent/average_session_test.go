package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

// This exercises the normal harness shape rather than one isolated tool: a
// provider receives the catalog, inspects the workspace, edits an existing
// file, verifies the project, and completes with persistent session diagnostics.
func TestAverageHarnessSessionAcrossToolIterations(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"go.mod":    "module average-session\n\ngo 1.23\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"README.md": "# Before\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logRoot := filepath.Join(t.TempDir(), "session-logs")
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", logRoot)
	t.Setenv("EPHEMERA_DEBUG_LOG", filepath.Join(logRoot, "global-debug.jsonl"))

	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	cfg.AgentSelfCritique = false
	cfg.AgentMaxSteps = 10
	cfg.AutoTestCommand = "go test ./..."

	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{Text: "inspect files", ToolCalls: []llm.ToolCall{{ID: "average-1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
		{Text: "read target", ToolCalls: []llm.ToolCall{{ID: "average-2", Name: "read_file", Arguments: map[string]any{"path": "README.md"}}}},
		{Text: "update target", ToolCalls: []llm.ToolCall{{ID: "average-3", Name: "replace_in_file", Arguments: map[string]any{"path": "README.md", "old": "# Before", "new": "# After"}}}},
		{Text: "verify", ToolCalls: []llm.ToolCall{{ID: "average-4", Name: "go_test", Arguments: map[string]any{}}}},
		{Text: `{"final":"Updated README.md and verified the project."}`},
	}}
	session := history.New("average harness session", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "Update the README heading and verify the project")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("average session unexpectedly paused: %#v", result.Pending)
	}
	if !strings.Contains(result.Text, "verified") || result.Usage.ToolCalls < 4 {
		t.Fatalf("result=%q usage=%#v", result.Text, result.Usage)
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# After\n" {
		t.Fatalf("README.md = %q", data)
	}
	for _, name := range []string{"list_files", "read_file", "replace_in_file", "go_test"} {
		if !toolSpecNamed(provider.specs, name) {
			t.Fatalf("provider catalog did not include %q", name)
		}
	}
	for _, path := range []string{debuglog.SessionDebugPath(session.Name), debuglog.SessionContextPath(session.Name)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("session diagnostic %s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("session diagnostic %s is empty", path)
		}
	}
}

func toolSpecNamed(specs []llm.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}
