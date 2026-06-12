package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tools"
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

type nativeToolProvider struct {
	decisions []llm.ToolDecision
	calls     int
	specs     []llm.ToolSpec
	requests  []llm.Request
}

func (p *nativeToolProvider) Name() string { return "native-fake" }

func (p *nativeToolProvider) Generate(context.Context, llm.Request) (string, error) {
	return `{"final":"fallback"}`, nil
}

func (p *nativeToolProvider) GenerateWithTools(_ context.Context, req llm.Request, specs []llm.ToolSpec, emit llm.DeltaFunc) (llm.ToolDecision, error) {
	p.specs = specs
	p.requests = append(p.requests, req)
	if p.calls >= len(p.decisions) {
		return llm.ToolDecision{Text: `{"final":"done"}`}, nil
	}
	decision := p.decisions[p.calls]
	p.calls++
	if emit != nil && decision.Text != "" {
		if err := emit(llm.Delta{Kind: llm.DeltaText, Text: decision.Text}); err != nil {
			return llm.ToolDecision{}, err
		}
	}
	return decision, nil
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

func TestRunExecutesNativeToolCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module native\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{
			Text: "inspect module",
			ToolCalls: []llm.ToolCall{{
				ID:        "call-1",
				Name:      "read_file",
				Arguments: map[string]any{"path": "go.mod"},
			}},
		},
		{Text: `{"final":"Native tool flow read go.mod."}`},
	}}
	session := history.New("native", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect the module")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Pending != nil {
		t.Fatalf("native read unexpectedly paused: %#v", result.Pending)
	}
	if !strings.Contains(result.Text, "Native tool flow") {
		t.Fatalf("final text = %q", result.Text)
	}
	if len(provider.specs) == 0 {
		t.Fatal("native provider did not receive tool specs")
	}
	if !hasEvent(result.Events, "tool_result") || !hasEvent(result.Events, "reasoning_trace") {
		t.Fatalf("events = %#v, want native reasoning and tool result", result.Events)
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
	cfg.AgentAutoReview = false
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

func TestParseModelActionRepairsTrailingCommas(t *testing.T) {
	action, ok := parseModelAction(`{
		"summary": "repairable",
		"actions": [
			{"tool":"read_file","arguments":{"path":"go.mod",},},
		],
	}`)
	if !ok {
		t.Fatal("expected trailing comma JSON to be repaired")
	}
	if len(action.Actions) != 1 || action.Actions[0].Name != "read_file" {
		t.Fatalf("action = %#v, want repaired read_file", action)
	}
}

func TestMalformedAgentDecisionRetriesBeforeFallback(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`{"summary":"broken","actions":[`,
		`{"final":"Recovered after parse retry."}`,
	}}
	session := history.New("parse-retry", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "do agent work")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Text != "Recovered after parse retry." {
		t.Fatalf("result = %q", result.Text)
	}
	var parseError bool
	for _, event := range result.Events {
		if event.Type == history.EventDecision && event.Status == "error" {
			parseError = true
		}
	}
	if !parseError {
		t.Fatalf("events = %#v, want decision parse error", result.Events)
	}
}

func TestPlainTextAgentResponseRetriesBeforeFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`I'll inspect go.mod and then summarize it.`,
		`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}]}`,
		`{"final":"The module is plain."}`,
	}}
	session := history.New("plain-retry", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect the module")

	result := NewRunner(cfg, provider).Run(context.Background(), session)

	if result.Text != "The module is plain." {
		t.Fatalf("result = %q", result.Text)
	}
	if !hasEvent(result.Events, history.EventDecision) || !hasEvent(result.Events, history.EventToolResult) {
		t.Fatalf("events = %#v, want decision repair and tool result", result.Events)
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

func TestInspectBeforeEditGuardForExistingFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`{"summary":"edit","plan":["edit"],"actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		`{"summary":"inspect","plan":["read"],"actions":[{"tool":"read_file","arguments":{"path":"main.go"}}]}`,
		`{"summary":"edit","plan":["edit"],"actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		`{"final":"updated"}`,
	}}
	session := history.New("guard", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "update main.go")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Text != "updated" {
		t.Fatalf("result = %q", result.Text)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("file was not updated: %q", data)
	}
	var guarded bool
	for _, event := range result.Events {
		if event.Type == "tool_result" && strings.Contains(event.Content, "inspect-before-edit") {
			guarded = true
		}
	}
	if !guarded {
		t.Fatal("expected inspect-before-edit guard event")
	}
}

func TestDoomLoopGuardStopsIdenticalCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	cfg.AgentLoopLimit = 1
	provider := &fakeProvider{responses: []string{
		`{"actions":[{"tool":"read_file","arguments":{"path":"x.txt"}}]}`,
		`{"actions":[{"tool":"read_file","arguments":{"path":"x.txt"}}]}`,
		`{"final":"stopped repeating"}`,
	}}
	session := history.New("loop", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect x")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if !strings.Contains(strings.ToLower(result.Text), "stopped") {
		t.Fatalf("result = %q, want an early convergence stop", result.Text)
	}
	var guarded bool
	for _, event := range result.Events {
		if event.Type == "tool_result" && strings.Contains(event.Content, "duplicate read suppressed") {
			guarded = true
		}
	}
	if !guarded {
		t.Fatal("expected duplicate-read guard event")
	}
}

func TestAutoReviewRunsAfterVerifiedChange(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AutoTestCommand = ""
	cfg.AgentAutoVerify = true
	cfg.AgentAutoReview = true
	provider := &fakeProvider{responses: []string{
		`{"actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		`{"final":"candidate"}`,
		`{"final":"review clean"}`,
		`{"final":"done reviewed"}`,
	}}
	session := history.New("review", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create main.go")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Text != "done reviewed" {
		t.Fatalf("result = %q", result.Text)
	}
	var delegated bool
	for _, event := range result.Events {
		if event.Type == "tool_result" && event.Tool == "delegate" {
			delegated = true
		}
	}
	if !delegated {
		t.Fatal("expected independent review delegate")
	}
}

func TestToolObservationIncludesMetadataEvidence(t *testing.T) {
	observation := formatToolObservation(tools.Result{
		Tool:   "read_file",
		OK:     true,
		Output: "1: package main",
		Metadata: map[string]any{
			"ok":          true,
			"path":        "cmd/ephemera/main.go",
			"start_line":  1,
			"end_line":    20,
			"duration_ms": int64(12),
			"risk":        "read",
		},
	})

	for _, want := range []string{"[read_file ok]", "metadata:", "path=cmd/ephemera/main.go", "start_line=1", "end_line=20", "duration_ms=12", "risk=read", "1: package main"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("observation missing %q: %q", want, observation)
		}
	}
	if strings.Contains(observation, "ok=true") {
		t.Fatalf("observation leaked redundant ok metadata: %q", observation)
	}
}

func TestApprovedActionIsDeduplicatedAcrossContinuation(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false

	callJSON := `{"summary":"create file","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`
	firstProvider := &fakeProvider{responses: []string{callJSON}}
	session := history.New("approval-resume", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create main.go")

	first := NewRunner(cfg, firstProvider).Run(context.Background(), session)
	if first.Pending == nil {
		t.Fatal("expected write approval")
	}
	for _, event := range first.Events {
		session.AppendEvent(event)
	}
	approved := NewRunner(cfg, nil).ExecuteApproved(context.Background(), *first.Pending)
	session.AppendEvent(approved)

	provider := &fakeProvider{responses: []string{
		callJSON,
		`{"final":"Created main.go without asking twice."}`,
	}}
	resumed := NewRunner(cfg, provider).Run(context.Background(), session)
	if resumed.Pending != nil {
		t.Fatalf("duplicate completed action requested approval again: %#v", resumed.Pending)
	}
	if !strings.Contains(resumed.Text, "without asking twice") {
		t.Fatalf("final text = %q", resumed.Text)
	}
	data, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("main.go = %q", data)
	}
	var deduplicated bool
	for _, event := range resumed.Events {
		if event.Type == history.EventToolResult && metadataBool(event.Metadata, "deduplicated") {
			deduplicated = true
		}
	}
	if !deduplicated {
		t.Fatalf("events = %#v, want deduplicated tool result", resumed.Events)
	}
}

func TestRejectedActionDoesNotRePromptDuringSameRequest(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false

	callJSON := `{"summary":"create file","actions":[{"tool":"apply_patch","arguments":{"path":"blocked.go","content":"package blocked\n"}}]}`
	session := history.New("rejected-resume", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create blocked.go")
	first := NewRunner(cfg, &fakeProvider{responses: []string{callJSON}}).Run(context.Background(), session)
	if first.Pending == nil {
		t.Fatal("expected write approval")
	}

	for _, event := range first.Events {
		if event.ID == first.Pending.CallEventID || event.ID == first.Pending.ApprovalEventID {
			event.Status = "rejected"
		}
		session.AppendEvent(event)
	}

	provider := &fakeProvider{responses: []string{
		callJSON,
		`{"final":"The rejected action was not requested again."}`,
	}}
	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("rejected action requested approval again: %#v", result.Pending)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.go")); !os.IsNotExist(err) {
		t.Fatalf("blocked.go should not exist: %v", err)
	}
	var rejectedGuard bool
	for _, event := range result.Events {
		if event.Type == history.EventToolResult && strings.Contains(event.Content, "user rejected this exact action") {
			rejectedGuard = true
		}
	}
	if !rejectedGuard {
		t.Fatalf("events = %#v, want rejection guard result", result.Events)
	}
}

func TestFailedActionHaltsRemainingBatch(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`{"summary":"bad batch","actions":[{"tool":"read_file","arguments":{}},{"tool":"apply_patch","arguments":{"path":"should-not-exist.go","content":"package nope\n"}}]}`,
		`{"final":"Recovered from the failed first action."}`,
	}}
	session := history.New("batch-stop", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "run the batch")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("unexpected pending approval: %#v", result.Pending)
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist.go")); !os.IsNotExist(err) {
		t.Fatalf("later batch action executed after failure: %v", err)
	}
	if !strings.Contains(result.Text, "Recovered") {
		t.Fatalf("final text = %q", result.Text)
	}
}

func TestDoomLoopGuardCountsInvalidCalls(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.AgentEnabled = true
	cfg.AgentLoopLimit = 1
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`{"actions":[{"tool":"read_file","arguments":{}}]}`,
		`{"actions":[{"tool":"read_file","arguments":{}}]}`,
		`{"final":"replanned"}`,
	}}
	session := history.New("invalid-loop", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect a file")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	var guarded bool
	for _, event := range result.Events {
		if event.Type == history.EventToolResult && strings.Contains(event.Content, "identical invalid tool call") {
			guarded = true
		}
	}
	if !guarded {
		t.Fatalf("events = %#v, want invalid-call loop guard", result.Events)
	}
}

func TestFailedApprovedActionMustChangeBeforeRetry(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false

	callJSON := `{"summary":"create file","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`
	session := history.New("failed-approval-resume", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "create main.go")
	first := NewRunner(cfg, &fakeProvider{responses: []string{callJSON}}).Run(context.Background(), session)
	if first.Pending == nil {
		t.Fatal("expected write approval")
	}
	for _, event := range first.Events {
		session.AppendEvent(event)
	}
	session.AppendEvent(history.Event{
		Type:    history.EventToolResult,
		Tool:    first.Pending.Call.Name,
		Status:  "error",
		Content: "simulated approved execution failure",
		Metadata: map[string]any{
			"call_fingerprint": first.Pending.Fingerprint,
			"approved":         true,
		},
		CreatedAt: time.Now(),
	})

	provider := &fakeProvider{responses: []string{
		callJSON,
		`{"final":"Changed approach after the failed approved action."}`,
	}}
	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("failed approved action requested the same approval again: %#v", result.Pending)
	}
	var guarded bool
	for _, event := range result.Events {
		if event.Type == history.EventToolResult && strings.Contains(event.Content, "exact approved action already failed") {
			guarded = true
		}
	}
	if !guarded {
		t.Fatalf("events = %#v, want failed-approved-action guard", result.Events)
	}
}

func TestConversationMessagesDropsLegacyApprovalControlText(t *testing.T) {
	messages := []history.Message{
		{Role: "user", Content: "create main.go"},
		{Role: "assistant", Content: "Approval required for `apply_patch`: create the requested file\n\nRun `/approve` to execute it or `/reject` to skip it."},
		{Role: "user", Content: "continue"},
	}

	got := conversationMessages(messages)
	if len(got) != 2 {
		t.Fatalf("messages = %#v, want legacy approval prompt filtered", got)
	}
	for _, message := range got {
		if strings.Contains(message.Content, "Approval required") {
			t.Fatalf("legacy approval control text leaked into model context: %#v", got)
		}
	}
}

func TestPlainTextDirectAnswerCompletesInOneModelRound(t *testing.T) {
	cfg := config.Default()
	cfg.AgentEnabled = true
	provider := &fakeProvider{responses: []string{"A goroutine is a lightweight concurrently executing function in Go."}}
	session := history.New("direct-answer", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "What is a goroutine?")

	var updates []StreamUpdate
	result := NewRunner(cfg, provider).RunStream(context.Background(), session, func(update StreamUpdate) {
		updates = append(updates, update)
	})

	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want exactly one", provider.calls)
	}
	if !strings.Contains(result.Text, "lightweight") {
		t.Fatalf("result = %q", result.Text)
	}
	for _, event := range result.Events {
		if event.Type == history.EventDecision && event.Status == "error" {
			t.Fatalf("direct answer entered structured decision retry: %#v", result.Events)
		}
	}
	if len(updates) == 0 {
		t.Fatal("expected live stream updates")
	}
}

func TestUnfinishedActionNarrationStillRequestsStructuredRetry(t *testing.T) {
	for _, text := range []string{
		"I'll inspect the repository and then answer.",
		"Let me run the tests first.",
		"I need to read main.go before I can fix it.",
	} {
		if !looksLikeUnfinishedActionNarration(text) {
			t.Fatalf("%q should be treated as unfinished action narration", text)
		}
	}
	for _, text := range []string{
		"Hello! How can I help?",
		"A goroutine is a lightweight function.",
		"I will explain the concept directly.",
	} {
		if looksLikeUnfinishedActionNarration(text) {
			t.Fatalf("%q should be accepted as a direct answer", text)
		}
	}
}

func TestNativeProviderDirectAnswerCompletesWithoutJSONRetry(t *testing.T) {
	cfg := config.Default()
	cfg.AgentEnabled = true
	provider := &nativeToolProvider{decisions: []llm.ToolDecision{{Text: "Hello there."}}}
	session := history.New("native-direct", "openai", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "hello")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want one", provider.calls)
	}
	if result.Text != "Hello there." {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestRepeatedFailedDecisionStopsBeforeStepLimit(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.AgentEnabled = true
	cfg.AgentLoopLimit = 1
	cfg.AgentMaxSteps = 10
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &fakeProvider{responses: []string{
		`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{}}]}`,
		`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{}}]}`,
		`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{}}]}`,
		`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{}}]}`,
	}}
	session := history.New("stalled", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect it")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if provider.calls >= cfg.AgentMaxSteps {
		t.Fatalf("provider calls = %d, loop reached the full step limit", provider.calls)
	}
	if !strings.Contains(strings.ToLower(result.Text), "stopped") {
		t.Fatalf("result = %q, want a clear convergence stop", result.Text)
	}
}

func TestNativeToolResultIsFedBackAsARealToolTurn(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pong game"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{ToolCalls: []llm.ToolCall{{
			ID:        "call-list",
			Name:      "list_files",
			Arguments: map[string]any{"path": "pong game"},
		}}},
		{Text: "The pong game directory is empty, so the next step is to create the requested project files."},
	}}
	session := history.New("native-result-turn", "compatible", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "Create a pong game in the pong game folder")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil {
		t.Fatalf("unexpected approval: %#v", result.Pending)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	second := provider.requests[1]
	var sawCall, sawResult bool
	for _, message := range second.Messages {
		if message.Role == "assistant" && len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == "call-list" {
			sawCall = true
		}
		if message.Role == "tool" && message.ToolResult != nil && message.ToolResult.ID == "call-list" {
			sawResult = true
			if !message.ToolResult.OK {
				t.Fatalf("tool result was not successful: %#v", message.ToolResult)
			}
			if !strings.Contains(message.ToolResult.Output, "No files found") {
				t.Fatalf("tool result output = %q", message.ToolResult.Output)
			}
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("second request did not contain native call/result history: %#v", second.Messages)
	}
	if !strings.Contains(result.Text, "directory is empty") {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestRepeatedReadUsesCachedEvidenceBeforeLoopGuard(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentLoopLimit = 2
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	provider := &nativeToolProvider{decisions: []llm.ToolDecision{
		{ToolCalls: []llm.ToolCall{{ID: "list-1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
		{ToolCalls: []llm.ToolCall{{ID: "list-2", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
		{Text: "The workspace contains main.go."},
	}}
	session := history.New("cached-read", "compatible", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "What files are here?")

	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if !strings.Contains(result.Text, "main.go") {
		t.Fatalf("result = %q", result.Text)
	}
	var cacheHit bool
	for _, event := range result.Events {
		if event.Type == history.EventToolResult && metadataBool(event.Metadata, "cache_hit") {
			cacheHit = true
			if event.Status != "done" || !strings.Contains(event.Content, "main.go") {
				t.Fatalf("cached event = %#v", event)
			}
		}
	}
	if !cacheHit {
		t.Fatalf("events = %#v, want cached duplicate read result", result.Events)
	}
	for _, spec := range provider.specs {
		if spec.Name == "list_files" {
			t.Fatal("list_files remained available after an exact duplicate discovery call")
		}
	}
}

func TestReconstructNativeTurnsSkipsPendingApprovalCalls(t *testing.T) {
	callID := "call-pending"
	events := []history.Event{
		{
			Type:   history.EventToolCall,
			Tool:   "apply_patch",
			Status: "pending",
			Metadata: map[string]any{
				"provider_call_id": callID,
				"tool_arguments":   map[string]any{"path": "main.go", "content": "package main\n"},
			},
		},
		{
			Type:   history.EventApprovalRequest,
			Tool:   "apply_patch",
			Status: "pending",
			Metadata: map[string]any{
				"provider_call_id": callID,
			},
		},
	}
	if turns := reconstructNativeToolTurns(events); len(turns) != 0 {
		t.Fatalf("pending native call leaked into provider history: %#v", turns)
	}
}

func TestRunDispatchesIndependentReadsInParallelWithStableOrdering(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	cfg.AgentMaxParallelTools = 2
	provider := &fakeProvider{responses: []string{
		`{"summary":"inspect both","actions":[{"tool":"read_file","arguments":{"path":"a.txt"}},{"tool":"read_file","arguments":{"path":"b.txt"}}]}`,
		`{"final":"both inspected"}`,
	}}
	session := history.New("parallel", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "inspect both files")
	result := NewRunner(cfg, provider).Run(context.Background(), session)
	if result.Pending != nil || result.Text != "both inspected" {
		t.Fatalf("result = %#v", result)
	}
	var indexes []int
	for _, event := range result.Events {
		if event.Type != history.EventToolResult || !metadataBool(event.Metadata, "parallel") {
			continue
		}
		if value, ok := event.Metadata["parallel_batch_index"].(int); ok {
			indexes = append(indexes, value)
		}
	}
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("parallel result indexes = %#v", indexes)
	}
}

func TestRunStreamPublishesLiveShellOutput(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentAutoVerify = false
	cfg.AgentAutoReview = false
	command := "printf 'live-output\\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output live-output"
	}
	provider := &fakeProvider{responses: []string{
		fmt.Sprintf(`{"summary":"run","actions":[{"tool":"shell","arguments":{"command":%q}}]}`, command),
		`{"final":"done"}`,
	}}
	session := history.New("stream-command", "ollama", cfg.Model(), reasoning.ModeNormal)
	session.Append("user", "run command")
	var live strings.Builder
	result := NewRunner(cfg, provider).RunStream(context.Background(), session, func(update StreamUpdate) {
		if update.Kind == StreamToolProgress && update.Phase == "tool output" {
			live.WriteString(update.Delta)
		}
	})
	if result.Text != "done" || !strings.Contains(live.String(), "live-output") {
		t.Fatalf("result=%q live=%q", result.Text, live.String())
	}
}
