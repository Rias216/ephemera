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
		evalParallelReadCase(),
		evalParallelWriteCase(),
		evalDryRunCase(),
		evalDuplicateReadCase(),
		evalTokenBudgetCase(),
		evalSemanticIndexCase(),
		evalTDDPromptCase(),
		evalMemoryLearningCase(),
		evalSnapshotRollbackCase(),
		evalProviderRetryCase(),
		evalPromptProfileCase(),
		evalContextCompactionCase(),
		evalManualSnapshotRetentionCase(),
		evalGitHubCatalogCase(),
		evalGitHubDryRunCase(),
		evalRegexSearchCase(),
		evalFindSymbolCase(),
		evalFileSummaryCase(),
		evalDependencyGraphCase(),
		evalProjectDetectionCase(),
		evalDependencyListingCase(),
		evalGitCommitDryRunCase(),
		evalFormatterDryRunCase(),
		evalLinterDryRunCase(),
		evalSecurityAuditDryRunCase(),
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
	Seed       func(*history.Session)
	Provider   llm.Provider
	Check      func(RunResult, string) error
}

type evalTextProvider struct {
	responses []string
	calls     int
	requests  []llm.Request
}

func (p *evalTextProvider) Name() string { return "eval-text" }

func (p *evalTextProvider) Generate(ctx context.Context, request llm.Request) (string, error) {
	return p.generate(ctx, request)
}

func (p *evalTextProvider) generate(_ context.Context, request llm.Request) (string, error) {
	p.requests = append(p.requests, request)
	if p.calls >= len(p.responses) {
		return `{"final":"done"}`, nil
	}
	response := p.responses[p.calls]
	p.calls++
	return response, nil
}

type evalParallelProvider struct{ evalTextProvider }

func (p *evalParallelProvider) Generate(ctx context.Context, request llm.Request) (string, error) {
	return p.generate(ctx, request)
}

func (p *evalParallelProvider) Capabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{MaxParallelTools: 4, ToolCallFormat: "text", StreamingFormat: "buffered"}
}

type evalFailingProvider struct {
	responses []string
	calls     int
	failAt    int
}

func (p *evalFailingProvider) Name() string { return "eval-failing" }

func (p *evalFailingProvider) Generate(context.Context, llm.Request) (string, error) {
	if p.calls == p.failAt {
		p.calls++
		return "", fmt.Errorf("deterministic provider failure")
	}
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
	specNames map[string]bool
}

func (p *evalNativeProvider) Name() string { return "eval-native" }

func (p *evalNativeProvider) Generate(context.Context, llm.Request) (string, error) {
	return `{"final":"fallback"}`, nil
}

func (p *evalNativeProvider) GenerateWithTools(_ context.Context, _ llm.Request, specs []llm.ToolSpec, emit llm.DeltaFunc) (llm.ToolDecision, error) {
	p.specs = len(specs)
	p.specNames = map[string]bool{}
	for _, spec := range specs {
		p.specNames[spec.Name] = true
	}
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
	if testCase.Seed != nil {
		testCase.Seed(&session)
	}
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

func evalParallelReadCase() evalCase {
	provider := &evalParallelProvider{evalTextProvider: evalTextProvider{responses: []string{
		`{"summary":"inspect both","actions":[{"id":"a","tool":"read_file","arguments":{"path":"a.txt"}},{"id":"b","tool":"read_file","arguments":{"path":"b.txt"}}]}`,
		`{"final":"Parallel reads completed."}`,
	}}}
	return evalCase{
		Name:       "parallel-read-batch",
		Capability: "dispatch independent read tools concurrently with stable result metadata",
		Setup: func(root string) error {
			if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o600)
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			parallel := 0
			for _, event := range result.Events {
				if event.Type == history.EventToolResult && metadataBool(event.Metadata, "parallel") {
					parallel++
				}
			}
			return requireEval(result, parallel == 2, fmt.Sprintf("expected 2 parallel results, got %d", parallel))
		},
	}
}

func evalParallelWriteCase() evalCase {
	provider := &evalParallelProvider{evalTextProvider: evalTextProvider{responses: []string{
		`{"summary":"create independent files","actions":[{"id":"a","tool":"apply_patch","arguments":{"path":"a.txt","content":"a\n"}},{"id":"b","tool":"apply_patch","arguments":{"path":"b.txt","content":"b\n"}}]}`,
		`{"final":"Parallel writes completed."}`,
	}}}
	return evalCase{
		Name:       "parallel-disjoint-writes",
		Capability: "run approval-free writes concurrently only when target paths are disjoint",
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			cfg.AgentMaxParallelTools = 4
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			a, errA := os.ReadFile(filepath.Join(root, "a.txt"))
			b, errB := os.ReadFile(filepath.Join(root, "b.txt"))
			return requireEval(result, errA == nil && errB == nil && string(a) == "a\n" && string(b) == "b\n", "expected both disjoint writes")
		},
	}
}

func evalDryRunCase() evalCase {
	return evalCase{
		Name:       "dry-run-write-preview",
		Capability: "preview a write diff without changing the workspace",
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			cfg.AgentDryRun = true
		},
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"preview","actions":[{"tool":"apply_patch","arguments":{"path":"preview.txt","content":"preview\n"}}]}`,
			`{"final":"Dry run completed."}`,
		}},
		Check: func(result RunResult, root string) error {
			_, statErr := os.Stat(filepath.Join(root, "preview.txt"))
			dryRun := false
			for _, event := range result.Events {
				if event.Type == history.EventToolResult && metadataBool(event.Metadata, "dry_run") {
					dryRun = true
				}
			}
			return requireEval(result, os.IsNotExist(statErr) && dryRun, "expected dry-run metadata and no file")
		},
	}
}

func evalDuplicateReadCase() evalCase {
	return evalCase{
		Name:       "duplicate-read-cache",
		Capability: "reuse an exact successful read instead of executing it twice",
		Setup:      writeEvalFile("data.txt", "cached\n"),
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"read","actions":[{"tool":"read_file","arguments":{"path":"data.txt"}}]}`,
			`{"summary":"read again","actions":[{"tool":"read_file","arguments":{"path":"data.txt"}}]}`,
			`{"final":"Cache used."}`,
		}},
		Check: func(result RunResult, root string) error {
			for _, event := range result.Events {
				if event.Type == history.EventToolResult && metadataBool(event.Metadata, "cache_hit") {
					return nil
				}
			}
			return requireEval(result, false, "expected a cached duplicate read")
		},
	}
}

func evalTokenBudgetCase() evalCase {
	return evalCase{
		Name:       "task-token-budget",
		Capability: "pause the run when the configured task token budget is reached",
		Setup:      writeEvalFile("budget.txt", "budget\n"),
		Configure: func(cfg *config.Config) {
			cfg.AgentTaskTokenBudget = 1
		},
		Provider: &evalTextProvider{responses: []string{
			`{"summary":"read","actions":[{"tool":"read_file","arguments":{"path":"budget.txt"}}]}`,
		}},
		Check: func(result RunResult, root string) error {
			return requireEval(result, strings.Contains(result.Text, "token budget"), "expected token-budget pause")
		},
	}
}

func evalSemanticIndexCase() evalCase {
	provider := &evalTextProvider{responses: []string{`{"final":"Index observed."}`}}
	return evalCase{
		Name:       "semantic-codebase-index",
		Capability: "use the RouteToolCall definition from the semantic codebase index",
		Setup:      writeEvalFile(filepath.Join("internal", "router.go"), "package internal\n\nfunc RouteToolCall() {}\n"),
		Configure: func(cfg *config.Config) {
			cfg.AgentSemanticIndex = true
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			if len(provider.requests) == 0 {
				return requireEval(result, false, "provider did not receive a request")
			}
			system := provider.requests[0].System
			return requireEval(result, strings.Contains(system, "RELEVANT CODEBASE INDEX") && strings.Contains(system, "RouteToolCall"), "expected relevant source definition in system prompt")
		},
	}
}

func evalTDDPromptCase() evalCase {
	provider := &evalTextProvider{responses: []string{`{"final":"TDD contract observed."}`}}
	return evalCase{
		Name:       "tdd-contract",
		Capability: "activate test-first implementation rules when TDD mode is enabled",
		Configure: func(cfg *config.Config) {
			cfg.AgentTDDMode = true
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			return requireEval(result, len(provider.requests) > 0 && strings.Contains(provider.requests[0].System, "TDD mode is enabled"), "expected TDD operating rule")
		},
	}
}

func evalMemoryLearningCase() evalCase {
	return evalCase{
		Name:       "episodic-memory-write",
		Capability: "persist a bounded successful task episode for future runs",
		Configure: func(cfg *config.Config) {
			cfg.AgentLearnMemory = true
		},
		Provider: &evalTextProvider{responses: []string{`{"final":"Remembered completion."}`}},
		Check: func(result RunResult, root string) error {
			data, err := os.ReadFile(filepath.Join(root, ".ephemera", "memory.json"))
			return requireEval(result, err == nil && strings.Contains(string(data), "Remembered completion"), "expected memory.json episode")
		},
	}
}

func evalSnapshotRollbackCase() evalCase {
	return evalCase{
		Name:       "snapshot-auto-rollback",
		Capability: "restore the workspace when a run fails after making changes",
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			cfg.SandboxMode = config.SandboxSnapshot
			cfg.AgentAutoRollback = true
			cfg.ProviderMaxRetries = 0
		},
		Provider: &evalFailingProvider{
			responses: []string{`{"summary":"write","actions":[{"tool":"apply_patch","arguments":{"path":"temporary.txt","content":"temporary\n"}}]}`},
			failAt:    1,
		},
		Check: func(result RunResult, root string) error {
			_, statErr := os.Stat(filepath.Join(root, "temporary.txt"))
			return requireEval(result, os.IsNotExist(statErr) && evalHasEvent(result.Events, "recovery") && strings.Contains(result.Text, "rolled back"), "expected failed run rollback")
		},
	}
}

type evalRetryProvider struct {
	calls    int
	requests []llm.Request
}

func (p *evalRetryProvider) Name() string { return "eval-retry" }

func (p *evalRetryProvider) Generate(_ context.Context, request llm.Request) (string, error) {
	p.calls++
	p.requests = append(p.requests, request)
	if p.calls == 1 {
		return "", fmt.Errorf("503 temporary provider outage")
	}
	return `{"final":"Retry recovered."}`, nil
}

type evalProfileProvider struct {
	evalTextProvider
	format string
	name   string
}

func (p *evalProfileProvider) Name() string { return p.name }

func (p *evalProfileProvider) Generate(ctx context.Context, request llm.Request) (string, error) {
	return p.generate(ctx, request)
}

func (p *evalProfileProvider) Capabilities() llm.ProviderCapabilities {
	return llm.ProviderCapabilities{ToolCallFormat: p.format, StreamingFormat: "buffered", MaxParallelTools: 1}
}

func evalProviderRetryCase() evalCase {
	provider := &evalRetryProvider{}
	return evalCase{
		Name:       "provider-transient-retry",
		Capability: "retry one transient provider failure without duplicating visible output",
		Configure: func(cfg *config.Config) {
			cfg.ProviderMaxRetries = 1
			cfg.ProviderRetryBackoffMS = 50
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			return requireEval(result, provider.calls == 2 && strings.Contains(result.Text, "Retry recovered"), fmt.Sprintf("expected two provider attempts, got %d", provider.calls))
		},
	}
}

func evalPromptProfileCase() evalCase {
	provider := &evalProfileProvider{
		evalTextProvider: evalTextProvider{responses: []string{`{"final":"Profile observed."}`}},
		format:           "anthropic",
		name:             "anthropic",
	}
	return evalCase{
		Name:       "provider-prompt-profile",
		Capability: "adapt system instructions to the negotiated provider prompt profile",
		Provider:   provider,
		Check: func(result RunResult, root string) error {
			if len(provider.requests) == 0 {
				return requireEval(result, false, "provider received no request")
			}
			return requireEval(result, strings.Contains(provider.requests[0].System, "Prompt profile: anthropic"), "expected anthropic prompt profile")
		},
	}
}

func evalContextCompactionCase() evalCase {
	provider := &evalTextProvider{responses: []string{`{"final":"Compacted context observed."}`}}
	return evalCase{
		Name:       "hierarchical-context-compaction",
		Capability: "compact omitted history while retaining the latest request",
		Configure: func(cfg *config.Config) {
			cfg.ContextTokens = 10_000
			cfg.AgentContextSummaryTok = 600
			cfg.AgentContextRecall = 2
		},
		Seed: func(session *history.Session) {
			for index := 0; index < 28; index++ {
				role := "user"
				if index%2 == 1 {
					role = "assistant"
				}
				session.Append(role, fmt.Sprintf("historical turn %d renderer.go %s", index, strings.Repeat("bounded context evidence ", 80)))
			}
		},
		Provider: provider,
		Check: func(result RunResult, root string) error {
			if len(provider.requests) == 0 {
				return requireEval(result, false, "provider received no request")
			}
			condensed := false
			for _, message := range provider.requests[0].Messages {
				if strings.Contains(message.Content, "Condensed earlier conversation context") {
					condensed = true
				}
			}
			return requireEval(result, condensed, "expected a condensed earlier-context message")
		},
	}
}

func evalManualSnapshotRetentionCase() evalCase {
	return evalCase{
		Name:       "snapshot-manual-retention",
		Capability: "retain a rollback point after a failed changed run when automatic rollback is disabled",
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			cfg.SandboxMode = config.SandboxSnapshot
			cfg.AgentAutoRollback = false
			cfg.ProviderMaxRetries = 0
		},
		Provider: &evalFailingProvider{
			responses: []string{`{"summary":"write","actions":[{"tool":"apply_patch","arguments":{"path":"retained.txt","content":"retained\n"}}]}`},
			failAt:    1,
		},
		Check: func(result RunResult, root string) error {
			snapshot := ""
			for _, event := range result.Events {
				if path := metadataString(event.Metadata, "snapshot_path"); path != "" {
					snapshot = path
				}
			}
			if snapshot != "" {
				defer os.RemoveAll(snapshot)
			}
			_, fileErr := os.Stat(filepath.Join(root, "retained.txt"))
			_, manifestErr := os.Stat(filepath.Join(snapshot, "manifest.json"))
			return requireEval(result, fileErr == nil && snapshot != "" && manifestErr == nil && strings.Contains(result.Text, "snapshot retained"), "expected retained changed file and snapshot manifest")
		},
	}
}

func evalGitHubCatalogCase() evalCase {
	provider := &evalNativeProvider{decisions: []llm.ToolDecision{{Text: `{"final":"GitHub catalog observed."}`}}}
	return evalCase{
		Name:       "github-tool-catalog",
		Capability: "advertise GitHub issue and pull-request integrations through native tool schemas",
		Provider:   provider,
		Check: func(result RunResult, root string) error {
			return requireEval(result, provider.specNames["github_issue"] && provider.specNames["github_pr"], "expected both GitHub tool contracts")
		},
	}
}

func evalGitHubDryRunCase() evalCase {
	return evalToolOutputCase(
		"github-dry-run",
		"preview a GitHub mutation without network access or credentials",
		nil,
		"github_issue",
		map[string]any{"action": "create", "repository": "owner/repo", "title": "Agent issue", "body": "Preview"},
		"DRY RUN",
		func(cfg *config.Config) { cfg.AgentDryRun = true },
	)
}

func evalRegexSearchCase() evalCase {
	return evalToolOutputCase("regex-code-search", "run bounded regular-expression search", writeEvalFile("router.go", "package main\nfunc RouteToolCall() {}\n"), "grep_regex", map[string]any{"pattern": "RouteToolCall", "path": "."}, "RouteToolCall", nil)
}

func evalFindSymbolCase() evalCase {
	return evalToolOutputCase("symbol-definition-search", "find a top-level source definition", writeEvalFile("router.go", "package main\nfunc RouteToolCall() {}\n"), "find_symbol", map[string]any{"symbol": "RouteToolCall", "path": "."}, "RouteToolCall", nil)
}

func evalFileSummaryCase() evalCase {
	return evalToolOutputCase("ast-file-summary", "summarize package imports and top-level definitions", writeEvalFile("router.go", "package main\nimport \"fmt\"\nfunc RouteToolCall() { fmt.Println() }\n"), "file_summary", map[string]any{"path": "router.go"}, "RouteToolCall", nil)
}

func evalDependencyGraphCase() evalCase {
	return evalToolOutputCase("source-dependency-graph", "build a bounded source import graph", writeEvalFile("router.go", "package main\nimport \"fmt\"\nfunc main() { fmt.Println() }\n"), "dependency_graph", map[string]any{"path": "."}, "router.go", nil)
}

func evalProjectDetectionCase() evalCase {
	return evalToolOutputCase("project-type-detection", "detect the project language, build system, and test command", writeEvalFile("go.mod", "module projectdetect\ngo 1.23\n"), "detect_project_type", map[string]any{}, "Go", nil)
}

func evalDependencyListingCase() evalCase {
	return evalToolOutputCase("manifest-dependency-listing", "list declared dependencies and versions", writeEvalFile("go.mod", "module deps\ngo 1.23\nrequire example.com/dependency v1.2.3\n"), "list_dependencies", map[string]any{}, "example.com/dependency", nil)
}

func evalGitCommitDryRunCase() evalCase {
	return evalToolOutputCase("git-commit-dry-run", "preview a git commit without staging or committing", nil, "git_commit", map[string]any{"message": "test commit"}, "DRY RUN", func(cfg *config.Config) { cfg.AgentDryRun = true })
}

func evalFormatterDryRunCase() evalCase {
	return evalToolOutputCase("formatter-dry-run", "preview project-aware formatting without rewriting files", writeEvalFile("go.mod", "module format\ngo 1.23\n"), "run_formatter", map[string]any{}, "DRY RUN", func(cfg *config.Config) { cfg.AgentDryRun = true })
}

func evalLinterDryRunCase() evalCase {
	return evalToolOutputCase("linter-dry-run", "preview project-aware lint execution", writeEvalFile("go.mod", "module lint\ngo 1.23\n"), "run_linter", map[string]any{}, "DRY RUN", func(cfg *config.Config) { cfg.AgentDryRun = true })
}

func evalSecurityAuditDryRunCase() evalCase {
	return evalToolOutputCase("security-audit-dry-run", "preview dependency security auditing", writeEvalFile("go.mod", "module audit\ngo 1.23\n"), "security_audit", map[string]any{}, "DRY RUN", func(cfg *config.Config) { cfg.AgentDryRun = true })
}

func evalToolOutputCase(name, capability string, setup func(string) error, tool string, arguments map[string]any, expected string, configure func(*config.Config)) evalCase {
	decision, _ := json.Marshal(map[string]any{
		"summary": "exercise " + tool,
		"actions": []map[string]any{{"tool": tool, "arguments": arguments}},
	})
	return evalCase{
		Name:       name,
		Capability: capability,
		Setup:      setup,
		Configure: func(cfg *config.Config) {
			cfg.ApprovalPolicy = config.ApprovalAutoApprove
			if configure != nil {
				configure(cfg)
			}
		},
		Provider: &evalTextProvider{responses: []string{string(decision), `{"final":"Tool case complete."}`}},
		Check: func(result RunResult, root string) error {
			for _, event := range result.Events {
				if event.Type == history.EventToolResult && event.Tool == tool && strings.Contains(event.Content, expected) {
					return nil
				}
			}
			return requireEval(result, false, "expected "+tool+" output containing "+expected)
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
