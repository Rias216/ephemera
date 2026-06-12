package tools

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestReadFileStaysInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": filepath.Join(root, "..", "outside.txt")},
	})

	if result.OK || !strings.Contains(result.Error, "outside the active workspace") {
		t.Fatalf("read outside workspace = %#v, want blocked", result)
	}
}

func TestReadFileBlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": filepath.Join("outside", "secret.txt")},
	})

	if result.OK || !strings.Contains(result.Error, "outside the active workspace") {
		t.Fatalf("symlink escape read = %#v, want blocked", result)
	}
}

func TestApplyPatchBlocksSymlinkEscapeForNewFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name: "apply_patch",
		Arguments: map[string]any{
			"path":    filepath.Join("outside", "created.txt"),
			"content": "nope\n",
		},
	})

	if result.OK || !strings.Contains(result.Error, "outside the active workspace") {
		t.Fatalf("symlink escape write = %#v, want blocked", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file was created: %v", err)
	}
}

func TestApplyPatchWritesCompleteContentWhenApproved(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name: "apply_patch",
		Arguments: map[string]any{
			"path":    "hello.txt",
			"content": "rose\n",
		},
	})
	if !result.OK {
		t.Fatalf("apply_patch failed: %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rose\n" {
		t.Fatalf("file content = %q", data)
	}
}

func TestApproveWritesRequiresApprovalForShellAndPatch(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	registry := NewRegistry(cfg)

	if registry.RequiresApproval("read_file") {
		t.Fatal("read_file should not require approval")
	}
	for _, name := range []string{"apply_patch", "shell", "go_test"} {
		if !registry.RequiresApproval(name) {
			t.Fatalf("%s should require approval", name)
		}
	}
}

func TestShellBlocksDestructiveCommands(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "Remove-Item -Recurse ."},
	})

	if result.OK || !strings.Contains(result.Error, "destructive") {
		t.Fatalf("destructive shell result = %#v, want blocked", result)
	}
}

func TestAutoApproveNeverCreatesApprovalPrompts(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)

	for _, name := range []string{"read_file", "apply_patch", "shell", "go_test", "unknown_tool"} {
		if registry.RequiresApproval(name) {
			t.Fatalf("%s unexpectedly requires approval in auto mode", name)
		}
	}
}

func TestToolSpecsExposeSchemas(t *testing.T) {
	specs := ToolSpecs()
	if len(specs) == 0 {
		t.Fatal("expected tool specs")
	}
	var readFileFound bool
	for _, spec := range specs {
		if spec.Name != "read_file" {
			continue
		}
		readFileFound = true
		if spec.Parameters.Type != "object" || spec.Parameters.AdditionalProperties {
			t.Fatalf("read_file schema = %#v, want closed object schema", spec.Parameters)
		}
		if spec.Parameters.Properties["path"].Type != "string" {
			t.Fatalf("path property = %#v, want string", spec.Parameters.Properties["path"])
		}
		if len(spec.Parameters.Required) == 0 || spec.Parameters.Required[0] != "path" {
			t.Fatalf("required = %#v, want path", spec.Parameters.Required)
		}
	}
	if !readFileFound {
		t.Fatal("read_file tool spec not found")
	}
}

func TestValidateRejectsUnknownAndWrongTypedArguments(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)

	if err := registry.Validate(Call{Name: "read_file", Arguments: map[string]any{"path": "x.txt", "surprise": true}}); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("unknown argument error = %v, want surprise rejection", err)
	}
	if err := registry.Validate(Call{Name: "read_file", Arguments: map[string]any{"path": "x.txt", "start_line": "one"}}); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("wrong type error = %v, want integer rejection", err)
	}
	if err := registry.Validate(Call{Name: "apply_patch", Arguments: map[string]any{"path": "x.txt"}}); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("missing content error = %v, want content rejection", err)
	}
}

func TestExecuteAddsStableResultMetadata(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	registry := NewRegistry(cfg)

	result := registry.Execute(context.Background(), Call{Name: "git_status", Arguments: map[string]any{}})

	if result.Metadata == nil {
		t.Fatal("expected metadata")
	}
	if _, ok := result.Metadata["duration_ms"]; !ok {
		t.Fatalf("metadata = %#v, want duration_ms", result.Metadata)
	}
	if result.Metadata["risk"] != string(RiskRead) {
		t.Fatalf("risk = %#v, want read", result.Metadata["risk"])
	}
}

func TestFingerprintIsStableCompactAndArgumentSensitive(t *testing.T) {
	first := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}})
	reordered := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"content": "package main\n", "path": "main.go"}})
	changed := Fingerprint(Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package changed\n"}})

	if first != reordered {
		t.Fatalf("fingerprint changed with map order: %q != %q", first, reordered)
	}
	if first == changed {
		t.Fatalf("fingerprint ignored argument change: %q", first)
	}
	if len(first) > 96 || strings.Contains(first, "package main") {
		t.Fatalf("fingerprint is not compact/redacted: %q", first)
	}
}

func TestListFilesReturnsExplicitEmptyDirectoryEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pong game"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(config.Config{AgentSettings: config.AgentSettings{WorkspaceRoot: root, ApprovalPolicy: config.ApprovalAutoApprove}})
	result := registry.Execute(context.Background(), Call{
		Name:      "list_files",
		Arguments: map[string]any{"path": "pong game"},
	})
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Output, "No files found") {
		t.Fatalf("output = %q", result.Output)
	}
	if result.Metadata["count"] != 0 {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestNormalizeRepairsAliasesAndScalarTypes(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)

	call, err := registry.Normalize(Call{Name: "read_file", Arguments: map[string]any{
		"filename": "main.go",
		"start":    "2",
		"end":      7.0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if call.Arguments["path"] != "main.go" || call.Arguments["start_line"] != int64(2) || call.Arguments["end_line"] != int64(7) {
		t.Fatalf("normalized call = %#v", call)
	}
	if _, exists := call.Arguments["filename"]; exists {
		t.Fatalf("alias survived normalization: %#v", call.Arguments)
	}

	_, err = registry.Normalize(Call{Name: "read_file", Arguments: map[string]any{"path": "a.go", "file": "b.go"}})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestExecuteStreamPublishesCommandOutputBeforeCompletion(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	cfg.AgentToolTimeoutSec = 10
	registry := NewRegistry(cfg)
	command := "printf 'first\\n'; sleep 0.25; printf 'second\\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output first; Start-Sleep -Milliseconds 250; Write-Output second"
	}
	chunks := make(chan string, 8)
	done := make(chan Result, 1)
	go func() {
		done <- registry.ExecuteStream(context.Background(), Call{Name: "shell", Arguments: map[string]any{"command": command}}, func(chunk string) {
			chunks <- chunk
		})
	}()
	select {
	case chunk := <-chunks:
		if !strings.Contains(chunk, "first") {
			t.Fatalf("first chunk = %q", chunk)
		}
	case result := <-done:
		t.Fatalf("command completed before streaming a chunk: %#v", result)
	case <-time.After(2 * time.Second):
		t.Fatal("no streamed command output")
	}
	result := <-done
	if !result.OK || !strings.Contains(result.Output, "second") {
		t.Fatalf("result = %#v", result)
	}
}

type staticHTTPDoer struct {
	response *http.Response
	err      error
}

func (d staticHTTPDoer) Do(*http.Request) (*http.Response, error) { return d.response, d.err }

func TestWebFetchExtractsReadableTextAndRejectsPrivateHosts(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)
	registry.WebClient = staticHTTPDoer{response: &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(`<html><body><h1>Frontier</h1><script>steal()</script><p>Useful text</p></body></html>`)),
	}}
	result := registry.Execute(context.Background(), Call{Name: "web_fetch", Arguments: map[string]any{"url": "https://example.com/docs"}})
	if !result.OK || !strings.Contains(result.Output, "Frontier") || !strings.Contains(result.Output, "Useful text") || strings.Contains(result.Output, "steal") {
		t.Fatalf("web result = %#v", result)
	}

	blocked := registry.Execute(context.Background(), Call{Name: "web_fetch", Arguments: map[string]any{"url": "http://127.0.0.1/private"}})
	if blocked.OK || !strings.Contains(strings.ToLower(blocked.Error), "private") && !strings.Contains(strings.ToLower(blocked.Error), "local") {
		t.Fatalf("private fetch = %#v", blocked)
	}
}

func TestDryRunPreviewsWritesWithoutMutatingWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentDryRun = true
	registry := NewRegistry(cfg)
	result := registry.Execute(context.Background(), Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}})
	if !result.OK || result.Metadata["dry_run"] != true || !strings.Contains(result.Output, "DRY RUN diff") {
		t.Fatalf("dry run result = %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package old\n" {
		t.Fatalf("dry run mutated file: %q, %v", data, err)
	}
}

func TestDryRunDoesNotExecuteShellCommands(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentDryRun = true
	registry := NewRegistry(cfg)
	result := registry.Execute(context.Background(), Call{Name: "shell", Arguments: map[string]any{"command": "printf changed > changed.txt"}})
	if !result.OK || result.Metadata["dry_run"] != true {
		t.Fatalf("dry shell result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "changed.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry shell executed: %v", err)
	}
}

func TestApprovalMiddlewareRequiresScopedGrant(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	registry := NewRegistry(cfg)
	call := Call{Name: "apply_patch", Arguments: map[string]any{"path": "approved.txt", "content": "ok\n"}}

	blocked := registry.Execute(context.Background(), call)
	if blocked.OK || !strings.Contains(blocked.Error, "explicit user approval") {
		t.Fatalf("unapproved write = %#v", blocked)
	}
	approved := registry.Execute(WithApproval(context.Background()), call)
	if !approved.OK {
		t.Fatalf("approved write = %#v", approved)
	}
}

func TestExternalReadRequiresCallApprovalAndRunsAfterGrant(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	external := filepath.Join(parent, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(external, "note.txt")
	if err := os.WriteFile(path, []byte("outside data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)
	call := Call{Name: "read_file", Arguments: map[string]any{"path": path}}

	if !registry.RequiresApprovalCall(call) {
		t.Fatal("external read did not request approval")
	}
	if reason := registry.ApprovalReason(call); !strings.Contains(reason, path) {
		t.Fatalf("approval reason = %q, want external target", reason)
	}
	blocked := registry.Execute(context.Background(), call)
	if blocked.OK || !strings.Contains(blocked.Error, "requires explicit approval") {
		t.Fatalf("unapproved external read = %#v", blocked)
	}
	approved := registry.Execute(WithApproval(context.Background()), call)
	if !approved.OK || !strings.Contains(approved.Output, "outside data") {
		t.Fatalf("approved external read = %#v", approved)
	}
	if got, _ := approved.Metadata["path"].(string); filepath.Clean(got) != filepath.Clean(path) {
		t.Fatalf("external metadata path = %q, want %q", got, path)
	}
}

func TestExternalWriteRequiresApprovalAndWritesExactTarget(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	external := filepath.Join(parent, "external")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalWorkspaceWrite
	registry := NewRegistry(cfg)
	path := filepath.Join(external, "created.txt")
	call := Call{Name: "apply_patch", Arguments: map[string]any{"path": path, "content": "approved\n"}}

	blocked := registry.Execute(context.Background(), call)
	if blocked.OK {
		t.Fatalf("external write ran without approval: %#v", blocked)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("external file exists before approval: %v", err)
	}
	approved := registry.Execute(WithApproval(context.Background()), call)
	if !approved.OK {
		t.Fatalf("approved external write = %#v", approved)
	}
	if got, _ := approved.Metadata["path"].(string); filepath.Clean(got) != filepath.Clean(path) {
		t.Fatalf("external metadata path = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "approved\n" {
		t.Fatalf("external content = %q, err=%v", data, err)
	}
}

func TestAutoApproveAllowsExternalPathWithoutPrompt(t *testing.T) {
	workspace := t.TempDir()
	external := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(external, []byte("auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = workspace
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	registry := NewRegistry(cfg)
	call := Call{Name: "read_file", Arguments: map[string]any{"path": external}}
	if registry.RequiresApprovalCall(call) {
		t.Fatal("auto-approve requested an external-path prompt")
	}
	result := registry.Execute(context.Background(), call)
	if !result.OK || !strings.Contains(result.Output, "auto") {
		t.Fatalf("auto-approved external read = %#v", result)
	}
}
