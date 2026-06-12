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
	for _, path := range []string{debuglog.SessionDebugPath(session.Name), debuglog.SessionContextPath(session.Name), debuglog.SessionToolPath(session.Name)} {
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

// This is the exact regression for the reported "create a new folder" run.
// The agent must use the directory primitive, verify the empty directory, and
// finish without inventing a placeholder file or consuming every step.
func TestCreateEmptyFolderSessionCompletesAndLogs(t *testing.T) {
	root := t.TempDir()
	logRoot := filepath.Join(t.TempDir(), "session-logs")
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", logRoot)
	t.Setenv("EPHEMERA_DEBUG_LOG", filepath.Join(logRoot, "global-debug.jsonl"))

	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = true
	cfg.AgentAutoReview = false
	cfg.AgentSelfCritique = false
	cfg.AgentMaxSteps = 6
	cfg.AutoTestCommand = ""

	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{Text: "create the requested empty folder", ToolCalls: []llm.ToolCall{{ID: "mkdir-1", Name: "create_directory", Arguments: map[string]any{"path": "new-folder"}}}},
		{Text: `{"final":"Created the empty new-folder directory and verified it is accessible."}`},
	}}
	session := history.New("create folder session", "native-fake", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create a new folder named new-folder")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("folder creation unexpectedly paused: %#v", result.Pending)
	}
	if strings.Contains(strings.ToLower(result.Text), "step limit") {
		t.Fatalf("folder creation exhausted the step limit: %q", result.Text)
	}
	if !strings.Contains(strings.ToLower(result.Text), "created") || result.Usage.ToolCalls != 1 {
		t.Fatalf("unexpected result=%q usage=%#v", result.Text, result.Usage)
	}
	info, err := os.Stat(filepath.Join(root, "new-folder"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty folder was not created: info=%#v err=%v", info, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "new-folder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("folder contains unwanted placeholder files: %#v", entries)
	}
	if !toolSpecNamed(provider.specs, "create_directory") {
		t.Fatal("provider catalog did not include create_directory")
	}
	toolData, err := os.ReadFile(debuglog.SessionToolPath(session.Name))
	if err != nil {
		t.Fatal(err)
	}
	toolLog := string(toolData)
	if !strings.Contains(toolLog, `"tool":"create_directory"`) ||
		!strings.Contains(toolLog, `"stage":"started"`) ||
		!strings.Contains(toolLog, `"stage":"completed"`) {
		t.Fatalf("folder lifecycle missing from tools.jsonl: %s", toolLog)
	}
	if _, err := os.Stat(debuglog.SessionContextPath(session.Name)); err != nil {
		t.Fatalf("context log missing: %v", err)
	}
}

func TestTaskScopedVerificationIgnoresUnrelatedPreexistingFailures(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                "module scoped-session\n\ngo 1.23\n",
		"broken/broken_test.go": "package broken\n\nimport \"testing\"\n\nfunc TestPreexistingFailure(t *testing.T) { t.Fatal(\"pre-existing unrelated failure\") }\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logRoot := filepath.Join(t.TempDir(), "session-logs")
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", logRoot)
	t.Setenv("EPHEMERA_DEBUG_LOG", filepath.Join(logRoot, "global-debug.jsonl"))

	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = true
	cfg.AgentAutoReview = false
	cfg.AgentSelfCritique = false
	cfg.AgentMaxSteps = 6
	cfg.AutoTestCommand = "go test ./..."

	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{Text: "create pong package", ToolCalls: []llm.ToolCall{{ID: "pong-write", Name: "apply_multi_patch", Arguments: map[string]any{"patches": []any{
			map[string]any{"path": "pong/pong.go", "content": "package pong\n\nfunc Score() int { return 5 }\n"},
			map[string]any{"path": "pong/pong_test.go", "content": "package pong\n\nimport \"testing\"\n\nfunc TestScore(t *testing.T) { if Score() != 5 { t.Fatal(Score()) } }\n"},
			map[string]any{"path": ".ephemera/run.sh", "content": "go test ./...\n"},
		}}}}},
		{Text: `{"final":"Created and verified the pong package."}`},
	}}
	session := history.New("scoped verification session", "native-fake", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create a pong package")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("task-scoped run unexpectedly paused: %#v", result.Pending)
	}
	if result.Completion == nil || !result.Completion.Passed {
		t.Fatalf("unrelated pre-existing failure blocked completion: text=%q completion=%#v", result.Text, result.Completion)
	}
	foundScoped := false
	for _, event := range result.Events {
		if event.Type != history.EventVerification {
			continue
		}
		if event.Metadata["scope"] == "task" && event.Metadata["command"] == "go test ./pong/..." {
			foundScoped = true
		}
	}
	if !foundScoped {
		t.Fatalf("task-scoped verification evidence missing: %#v", result.Events)
	}
	debugData, err := os.ReadFile(debuglog.SessionDebugPath(session.Name))
	if err != nil {
		t.Fatal(err)
	}
	debugText := string(debugData)
	if !strings.Contains(debugText, `"verification_scope":"task"`) ||
		!strings.Contains(debugText, `go test ./pong/...`) ||
		!strings.Contains(debugText, `.ephemera/run.sh`) {
		t.Fatalf("session log did not retain scoped verification and ignored-runtime evidence: %s", debugText)
	}
	toolData, err := os.ReadFile(debuglog.SessionToolPath(session.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toolData), `"tool":"go_test"`) || !strings.Contains(string(toolData), `go test ./pong/...`) {
		t.Fatalf("tool lifecycle log missing scoped verification command: %s", toolData)
	}
}
