package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

type fakeProvider struct {
	responses []string
	calls     int
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Generate(context.Context, llm.Request) (string, error) {
	if p.calls >= len(p.responses) {
		return `{"final":"done"}`, nil
	}
	response := p.responses[p.calls]
	p.calls++
	return response, nil
}

func TestRunExecutesReadToolsAndContinuesToFinal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	provider := &fakeProvider{responses: []string{
		`{"summary":"inspect module","plan":["read module"],"actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}]}`,
		`{"final":"The module is example."}`,
	}}
	session := history.New("agent", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect the module")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Pending != nil {
		t.Fatal("read-only tool unexpectedly required approval")
	}
	if !strings.Contains(result.Text, "example") {
		t.Fatalf("final text = %q, want module summary", result.Text)
	}
	if !hasEvent(result.Events, "tool_result") || !hasEvent(result.Events, "final") {
		t.Fatalf("events = %#v, want tool_result and final", result.Events)
	}
}

func TestRunStopsForWriteApproval(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	provider := &fakeProvider{responses: []string{
		`{"summary":"create file","plan":["write file"],"actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
	}}
	session := history.New("agent", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create a project")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Pending == nil || result.Pending.Call.Name != "apply_patch" {
		t.Fatalf("pending = %#v, want apply_patch approval", result.Pending)
	}
	if !hasEvent(result.Events, "approval_request") {
		t.Fatalf("events = %#v, want approval_request", result.Events)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); !os.IsNotExist(err) {
		t.Fatalf("main.go exists before approval: %v", err)
	}
}

func hasEvent(events []history.Event, kind string) bool {
	for _, event := range events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

func TestRunAutoApproveExecutesWriteAndContinues(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	provider := &fakeProvider{responses: []string{
		`{"reasoning":{"goal":"create the requested file","assumptions":"the workspace is writable","approach":["write the file","verify completion"],"tool_rationale":["apply_patch performs the requested workspace change"],"verification":"confirm the tool result"},"summary":"create file","plan":["write file"],"actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		`{"reasoning":{"goal":"report completion","approach":"summarize the verified result","verification":"the write tool returned success"},"summary":"done","plan":[],"actions":[],"final":"Created main.go."}`,
	}}
	session := history.New("agent", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create main.go")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Pending != nil {
		t.Fatalf("auto-approve unexpectedly paused: %#v", result.Pending)
	}
	data, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("main.go = %q", data)
	}
	if !hasEvent(result.Events, "reasoning_trace") {
		t.Fatalf("events = %#v, want reasoning_trace", result.Events)
	}
	if !strings.Contains(result.Text, "Created main.go") {
		t.Fatalf("final text = %q", result.Text)
	}
}

func TestParseModelActionAcceptsCompactReasoningShapes(t *testing.T) {
	action, ok := parseModelAction(`{
		"reasoning": {
			"goal": "fix rendering",
			"assumptions": "the artifact is an unpainted cell",
			"approach": ["paint every row", "verify exact widths"],
			"tool_rationale": ["inspect source", "run tests"],
			"verification": "all rows match viewport width"
		},
		"summary": "repair the renderer",
		"plan": ["patch", "test"],
		"actions": []
	}`)
	if !ok {
		t.Fatal("expected model action to parse")
	}
	trace := formatReasoningTrace(action.Reasoning)
	for _, want := range []string{"Goal", "Assumptions", "Approach", "Tool rationale", "Verification", "fix rendering"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("trace missing %q:\n%s", want, trace)
		}
	}
}
