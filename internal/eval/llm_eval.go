package eval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

// LLMResult combines a real agent run with the deterministic workspace grader.
// TokenCost is provider-neutral token usage, not a fabricated currency amount.
type LLMResult struct {
	Task             string        `json:"task"`
	Provider         string        `json:"provider"`
	Model            string        `json:"model"`
	Passed           bool          `json:"passed"`
	Latency          time.Duration `json:"latency"`
	InputTokens      int           `json:"input_tokens"`
	OutputTokens     int           `json:"output_tokens"`
	TokenCost        int           `json:"token_cost"`
	ToolCalls        int           `json:"tool_calls"`
	ReasoningQuality int           `json:"reasoning_quality"`
	FinalText        string        `json:"final_text,omitempty"`
	Report           Report        `json:"report"`
}

// RunLLM executes a task through the configured real provider, then grades the
// resulting workspace with the same deterministic checks used by CI.
func RunLLM(ctx context.Context, root string, task Task, cfg config.Config, provider llm.Provider, timeout time.Duration) (LLMResult, error) {
	if provider == nil {
		return LLMResult{}, fmt.Errorf("real-LLM eval requires a configured provider")
	}
	if err := PrepareWorkspace(root, task); err != nil {
		return LLMResult{}, fmt.Errorf("prepare eval workspace: %w", err)
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cfg.WorkspaceRoot = root
	cfg.AgentEnabled = true
	cfg.ApprovalPolicy = config.ApprovalAutoApprove
	cfg.AgentDryRun = false
	session := history.New("llm-eval-"+task.Name, cfg.Provider, cfg.Model(), cfg.Mode)
	session.Append("user", task.Prompt)

	started := time.Now()
	run := agent.NewRunner(cfg, provider).Run(runCtx, session)
	latency := time.Since(started)
	if runCtx.Err() != nil && runCtx.Err() != context.Canceled {
		return LLMResult{}, fmt.Errorf("LLM eval timed out: %w", runCtx.Err())
	}
	report := Grade(ctx, root, task, timeout)
	quality := reasoningQuality(run)
	name := provider.Name()
	result := LLMResult{
		Task:             task.Name,
		Provider:         name,
		Model:            cfg.Model(),
		Passed:           report.Passed(),
		Latency:          latency,
		InputTokens:      run.Usage.InputTokens,
		OutputTokens:     run.Usage.OutputTokens,
		TokenCost:        run.Usage.InputTokens + run.Usage.OutputTokens,
		ToolCalls:        run.Usage.ToolCalls,
		ReasoningQuality: quality,
		FinalText:        strings.TrimSpace(run.Text),
		Report:           report,
	}
	return result, nil
}

func reasoningQuality(run agent.RunResult) int {
	score := 0
	seenReasoning := false
	seenVerification := false
	seenPlan := false
	for _, event := range run.Events {
		switch event.Type {
		case history.EventReasoningTrace, history.EventReasoningSummary:
			seenReasoning = true
		case history.EventPlanUpdate:
			seenPlan = true
		case history.EventVerification:
			seenVerification = true
		}
	}
	if seenReasoning {
		score += 35
	}
	if seenPlan {
		score += 20
	}
	if seenVerification {
		score += 25
	}
	if run.Completion != nil && run.Completion.Passed {
		score += 20
	}
	if score > 100 {
		return 100
	}
	return score
}

func FormatLLMResult(result LLMResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Real-LLM evaluation: %s\n", result.Task)
	fmt.Fprintf(&b, "Passed: %t · Provider: %s/%s · Latency: %s\n", result.Passed, result.Provider, result.Model, result.Latency.Round(time.Millisecond))
	fmt.Fprintf(&b, "Usage: %d input + %d output = %d tokens · %d tools\n", result.InputTokens, result.OutputTokens, result.TokenCost, result.ToolCalls)
	fmt.Fprintf(&b, "Reasoning quality: %d/100\n", result.ReasoningQuality)
	b.WriteString(FormatReport(result.Report))
	return strings.TrimSpace(b.String())
}
