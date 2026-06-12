package debuglog

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWriteRecentRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	t.Setenv("EPHEMERA_DEBUG_LOG", path)
	Error("provider", "request", errors.New("Authorization: Bearer secret-value"), map[string]any{
		"api_key": "very-secret",
		"model":   "test-model",
	})
	entries, err := Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if strings.Contains(entries[0].Message, "secret-value") {
		t.Fatal("bearer token was not redacted")
	}
	if entries[0].Fields["api_key"] != "[REDACTED]" {
		t.Fatal("sensitive field was not redacted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("debug log permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestClearRemovesLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	t.Setenv("EPHEMERA_DEBUG_LOG", path)
	Failure("tui", "stream", "closed", nil)
	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected log removal, got %v", err)
	}
}

func TestNestedFieldsAreBoundedAndRedacted(t *testing.T) {
	fields := map[string]any{
		"outer": map[string]any{
			"token": "secret-value",
			"next": map[string]any{
				"next": map[string]any{
					"next": map[string]any{
						"next": map[string]any{
							"next": map[string]any{"value": "too-deep"},
						},
					},
				},
			},
		},
	}
	sanitized := sanitizeFields(fields)
	outer := sanitized["outer"].(map[string]any)
	if outer["token"] != "[REDACTED]" {
		t.Fatalf("nested token was not redacted: %#v", outer["token"])
	}
	if strings.Contains(strings.TrimSpace(toJSON(sanitized)), "too-deep") {
		t.Fatal("deep nested debug field was not truncated")
	}
}

func toJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestScopedLogsMirrorIntoSessionBundle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("EPHEMERA_DEBUG_LOG", filepath.Join(root, "global", "debug.log"))
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", filepath.Join(root, "sessions"))

	ctx := WithScope(context.Background(), Scope{
		Session:   "tool calling / repro",
		RunID:     "run-1",
		Provider:  "codex",
		Model:     "gpt-test",
		Workspace: root,
		Iteration: 2,
		Tool:      "apply_patch",
	})
	if err := WriteCtx(ctx, "info", "agent", "decision", "captured", map[string]any{"context_tokens": 1234}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(SessionDebugPath("tool-calling-repro"))
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Fields["session"] != "tool-calling-repro" || entry.Fields["run_id"] != "run-1" {
		t.Fatalf("scope was not preserved: %#v", entry.Fields)
	}
	if entry.Fields["context_tokens"] != float64(1234) && entry.Fields["context_tokens"] != 1234 {
		t.Fatalf("token count was incorrectly redacted: %#v", entry.Fields["context_tokens"])
	}
}

func TestAppendContextPersistsNormalizedRedactedPayload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", filepath.Join(root, "sessions"))
	ctx := WithScope(context.Background(), Scope{Session: "utf8", RunID: "run-2", Provider: "codex", Model: "gpt-test", Iteration: 1})
	invalid := string([]byte{'o', 'k', 0xff, 'x'})
	if err := AppendContext(ctx, "provider_request", 1, "text", map[string]any{
		"request": map[string]any{
			"content":    invalid,
			"api_key":    "super-secret",
			"max_tokens": 2048,
		},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(SessionContextPath("utf8"))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(data) {
		t.Fatal("context log is not valid UTF-8")
	}
	text := string(data)
	if strings.Contains(text, "super-secret") {
		t.Fatal("context secret was not redacted")
	}
	if !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "2048") || !strings.Contains(text, "�") {
		t.Fatalf("unexpected context record: %s", text)
	}
}

func TestToolLifecycleCrashReportAndExport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("EPHEMERA_SESSION_LOG_DIR", filepath.Join(root, "sessions"))
	t.Setenv("EPHEMERA_DEBUG_LOG", filepath.Join(root, "global", "debug.log"))
	ctx := WithScope(context.Background(), Scope{
		Session: "folder crash", RunID: "run-folder", Provider: "compatible", Model: "test-model",
		Workspace: filepath.Join(root, "workspace"), Iteration: 2, Tool: "create_directory",
	})
	ok := false
	if err := AppendTool(ctx, ToolRecord{
		Stage: "completed", Tool: "create_directory", Fingerprint: "create_directory:abc", Risk: "write",
		Arguments: map[string]any{"path": "new-folder", "api_key": "must-not-leak"},
		OK:        &ok, Error: "simulated failure", Metadata: map[string]any{"approval": "granted"},
	}); err != nil {
		t.Fatal(err)
	}
	toolData, err := os.ReadFile(SessionToolPath("folder-crash"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(toolData)
	if !strings.Contains(text, "create_directory") || !strings.Contains(text, "new-folder") {
		t.Fatalf("tool lifecycle record missing evidence: %s", text)
	}
	if strings.Contains(text, "must-not-leak") || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("tool lifecycle record did not redact secrets: %s", text)
	}

	crashPath, err := RecordCrash(ctx, "agent.test", "boom", []byte("goroutine 1 [running]:\nagent.test"), map[string]any{"phase": "tool execution"})
	if err != nil {
		t.Fatal(err)
	}
	crashData, err := os.ReadFile(crashPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crashData), "agent.test") || !strings.Contains(string(crashData), "goroutine 1") {
		t.Fatalf("crash report missing stack/context: %s", crashData)
	}

	if err := os.WriteFile(filepath.Join(SessionDirectory("folder-crash"), "session.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := ExportSession("folder-crash")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	seen := map[string]bool{}
	for _, file := range archive.File {
		seen[filepath.Base(file.Name)] = true
	}
	for _, name := range []string{"session.json", "debug.jsonl", "context.jsonl", "tools.jsonl", "crash.json"} {
		if !seen[name] {
			t.Fatalf("diagnostic bundle missing %s: %#v", name, seen)
		}
	}
}
