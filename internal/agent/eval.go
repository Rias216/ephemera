package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

// EvalReport is a deterministic local capability check for the agent loop.
type EvalReport struct {
	StartedAt time.Time
	Duration  time.Duration
	Results   []EvalResult
}

type EvalResult struct {
	Name       string
	Capability string
	Passed     bool
	Detail     string
}

func (r EvalReport) Passed() int {
	count := 0
	for _, result := range r.Results {
		if result.Passed {
			count++
		}
	}
	return count
}

func (r EvalReport) Failed() int { return len(r.Results) - r.Passed() }

// RunDeterministicEval exercises the core agent loop without network calls.
func RunDeterministicEval(ctx context.Context) (EvalReport, error) {
	started := time.Now()
	report := EvalReport{StartedAt: started}
	cases := []evalCase{
		evalJSONReadCase(),
		evalNativeToolCase(),
		evalPlainTextRepairCase(),
		evalStructuredReasoningCase(),
		evalApprovalStopCase(),
		evalInspectBeforeEditCase(),
		evalVerifiedWriteCase(),
	}
	for _, testCase := range cases {
		result := runEvalCase(ctx, testCase)
		report.Results = append(report.Results, result)
	}
	report.Duration = time.Since(started)
	return report, nil
}

func FormatEvalReport(report EvalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Agent capability eval\n\n")
	fmt.Fprintf(&b, "- Passed: %d / %d\n", report.Passed(), len(report.Results))
	fmt.Fprintf(&b, "- Duration: %s\n\n", report.Duration.Round(time.Millisecond))
	for _, result := range report.Results {
		mark := "FAIL"
		if result.Passed {
			mark = "PASS"
		}
		fmt.Fprintf(&b, "- [%s] **%s** — %s. %s\n", mark, result.Name, result.Capability, result.Detail)
	}
	return strings.TrimSpace(b.String())
}

type evalCase struct {
	Name       string
	Capability string
	Configure  func(*config.Config)
	Setup      func(string) error
	Provider   llm.Provider
	Check      func(RunResult, string) error
}

type evalTextProvider struct {
	responses []string
	calls     int
}

func (p *evalTextProvider) Name() string { return "eval-text" }

func (p *evalTextProvider) Generate(context.Context, llm.Request) (string, error) {
	if p.calls >= len(p.responses) {
		return `{"final":"done"}`, nil
	}
	response := p.responses[p.calls]
	p.calls++
	return response, nil
}

type evalNativeProvider struct {
	decisions []llm.ToolDecision
	calls     int
	specs     int
}

func (p *evalNativeProvider) Name() string { return "eval-native" }

func (p *evalNativeProvider) Generate(context.Context, llm.Request) (string, error) {
	return `{"final":"fallback"}`, nil
}

func (p *evalNativeProvider) GenerateWithTools(_ context.Context, _ llm.Request, specs []llm.ToolSpec, emit llm.DeltaFunc) (llm.ToolDecision, error) {
	p.specs = len(specs)
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

func runEvalCase(ctx context.Context, testCase evalCase) EvalResult {
	root, err := os.MkdirTemp("", "ephemera-agent-eval-*")
	if err != nil {
		return evalFail(testCase, err)
	}
	defer os.RemoveAll(root)
	if testCase.Setup != nil {
		if err := testCase.Setup(root); err != nil {
			return evalFail(testCase, err)
		}
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.AgentAutoReview = false
	cfg.AgentAutoVerify = false
	cfg.AutoTestCommand = ""
	cfg.ApprovalPolicy = config.ApprovalApproveWrites
	if testCase.Configure != nil {
		testCase.Configure(&cfg)
	}
	session := history.New("eval-"+history.Sanitize(testCase.Name), cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	session.Append("user", testCase.Capability)
	result := NewRunner(cfg, testCase.Provider).Run(ctx, session)
	if testCase.Check != nil {
		if err := testCase.Check(result, root); err != nil {
			return evalFail(testCase, err)
		}
	}
	return EvalResult{Name: testCase.Name, Capability: testCase.Capability, Passed: true, Detail: evalResultDetail(result)}
}

func evalFail(testCase evalCase, err error) EvalResult {
	return EvalResult{Name: testCase.Name, Capability: testCase.Capability, Passed: false, Detail: err.Error()}
}

func evalJSONReadCase() evalCase {
	return evalCase{
		Name:       "json-read-tool",
		Capability: "parse JSON decision, run read_file, continue to final",
		Setup:      writeEvalFile("go.mod", "module evalread\n"),
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"inspect module","actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}]}`,
			`{"final":"The module is evalread."}`,
		}},
		Check: func(result RunResult, root string) error {
			return requireEval(result, result.Pending == nil && strings.Contains(result.Text, "evalread") && evalHasEvent(result.Events, history.EventToolResult), "expected read tool result and evalread final")
		},
	}
}

func evalNativeToolCase() evalCase {
	provider := &evalNativeProvider{decisions: []llm.ToolDecision{
		{Text: "inspect module", ToolCalls: []llm.ToolCall{{Name: "read_file", Arguments: map[string]any{"path": "go.mod"}}}},
		{Text: `{"final":"Native tool flow completed."}`},
	}}
	return evalCase{
		Name:       "native-tool-call",
		Capability: "route provider-native tool calls into the local tool loop",
		Setup:      writeEvalFile("go.mod", "module evalnative\n"),
		Provider:   provider,
		Check: func(result RunResult, root string) error {
			return requireEval(result, provider.specs > 0 && result.Pending == nil && evalHasEvent(result.Events, history.EventToolResult), "expected native specs and tool_result")
		},
	}
}

func evalPlainTextRepairCase() evalCase {
	return evalCase{
		Name:       "plain-text-repair",
		Capability: "recover when a model narrates instead of returning the JSON contract",
		Setup:      writeEvalFile("go.mod", "module evalrepair\n"),
		Provider: &evalTextProvider{responses: []string{
			`I will inspect go.mod and then answer.`,
			`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}]}`,
			`{"final":"Recovered and read evalrepair."}`,
		}},
		Check: func(result RunResult, root string) error {
			return requireEval(result, strings.Contains(result.Text, "evalrepair") && evalHasEvent(result.Events, history.EventDecision) && evalHasEvent(result.Events, history.EventToolResult), "expected decision repair and tool_result")
		},
	}
}

func evalApprovalStopCase() evalCase {
	return evalCase{
		Name:       "write-approval-stop",
		Capability: "stop before write tools under safe approval policy",
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"write","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
		}},
		Check: func(result RunResult, root string) error {
			_, statErr := os.Stat(filepath.Join(root, "main.go"))
			return requireEval(result, result.Pending != nil && result.Pending.Call.Name == "apply_patch" && os.IsNotExist(statErr) && evalHasEvent(result.Events, history.EventApprovalRequest), "expected pending apply_patch without file write")
		},
	}
}

func evalStructuredReasoningCase() evalCase {
	return evalCase{
		Name:       "structured-reasoning-surface",
		Capability: "surface goal, approach, tool rationale, verification, and next step even from compact model output",
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"answer directly","plan":["state the result"],"completion":{"verified":true,"evidence":["no tool needed"],"remaining_risks":["none"]},"final":"Direct answer."}`,
		}},
		Check: func(result RunResult, root string) error {
			trace, ok := evalLatestTrace(result.Events)
			return requireEval(result, ok &&
				trace.Goal != "" &&
				len(trace.Approach) > 0 &&
				len(trace.Evidence) > 0 &&
				len(trace.Risks) > 0 &&
				trace.Verification != "" &&
				trace.NextStep != "",
				"expected structured reasoning trace with completion evidence")
		},
	}
}

func evalInspectBeforeEditCase() evalCase {
	return evalCase{
		Name:       "inspect-before-edit",
		Capability: "block edits to existing files until read_file has inspected them",
		Setup:      writeEvalFile("main.go", "package old\n"),
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
		},
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"edit","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
			`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{"path":"main.go"}}]}`,
			`{"summary":"edit","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
			`{"final":"Edited after inspection."}`,
		}},
		Check: func(result RunResult, root string) error {
			data, err := os.ReadFile(filepath.Join(root, "main.go"))
			if err != nil {
				return err
			}
			return requireEval(result, string(data) == "package main\n" && evalEventContains(result.Events, "inspect-before-edit"), "expected guard event then successful edit")
		},
	}
}

func evalVerifiedWriteCase() evalCase {
	return evalCase{
		Name:       "verified-write",
		Capability: "verify workspace changes before finalizing",
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			cfg.AgentAutoVerify = true
		},
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"write","actions":[{"tool":"apply_patch","arguments":{"path":"main.go","content":"package main\n"}}]}`,
			`{"final":"Verified write complete."}`,
		}},
		Check: func(result RunResult, root string) error {
			return requireEval(result, strings.Contains(result.Text, "Verified write") && evalVerified(result.Events), "expected verified final event")
		},
	}
}

func writeEvalFile(path, content string) func(string) error {
	return func(root string) error {
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0o600)
	}
}

func requireEval(result RunResult, ok bool, message string) error {
	if ok {
		return nil
	}
	return fmt.Errorf("%s; final=%q pending=%t events=%s", message, result.Text, result.Pending != nil, evalEventSummary(result.Events))
}

func evalResultDetail(result RunResult) string {
	detail := fmt.Sprintf("final=%q", strings.TrimSpace(result.Text))
	if result.Pending != nil {
		detail += "; pending=" + result.Pending.Call.Name
	}
	detail += "; events=" + evalEventSummary(result.Events)
	return detail
}

func evalHasEvent(events []history.Event, kind string) bool {
	for _, event := range events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

func evalEventContains(events []history.Event, text string) bool {
	for _, event := range events {
		if strings.Contains(event.Content, text) || strings.Contains(event.Title, text) {
			return true
		}
	}
	return false
}

func evalVerified(events []history.Event) bool {
	for _, event := range events {
		if event.Type != history.EventFinal && event.Type != history.EventVerification {
			continue
		}
		if value, ok := event.Metadata["verified"].(bool); ok && value {
			return true
		}
	}
	return false
}

func evalLatestTrace(events []history.Event) (history.AgentTrace, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != history.EventReasoningTrace || event.Metadata == nil {
			continue
		}
		if trace, ok := event.Metadata["trace"].(history.AgentTrace); ok {
			return trace, true
		}
		data, err := json.Marshal(event.Metadata["trace"])
		if err != nil {
			continue
		}
		var trace history.AgentTrace
		if err := json.Unmarshal(data, &trace); err == nil && !trace.Empty() {
			return trace, true
		}
	}
	return history.AgentTrace{}, false
}

func evalEventSummary(events []history.Event) string {
	if len(events) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(events))
	for _, event := range events {
		label := event.Type
		if event.Status != "" {
			label += "/" + event.Status
		}
		if event.Tool != "" {
			label += ":" + event.Tool
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ",")
}
