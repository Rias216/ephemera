package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

type instrumentSeverity string

const (
	instrumentClean  instrumentSeverity = "clean"
	instrumentLow    instrumentSeverity = "low"
	instrumentMedium instrumentSeverity = "medium"
	instrumentHigh   instrumentSeverity = "high"
)

type instrumentReview struct {
	Text        string
	Severity    instrumentSeverity
	Incorporate bool
}

// DirectorSession owns the advisory provider used by dual-model director mode.
// The instrument never receives tool schemas and therefore cannot mutate the
// workspace or request approvals.
type DirectorSession struct {
	provider llm.Provider
	config   config.Config
	weight   int
}

func newDirectorSession(r Runner) (*DirectorSession, error) {
	if !r.Config.DirectorEnabled || r.delegationDepth > 0 {
		return nil, nil
	}
	instrumentCfg, err := r.Config.ConfigForRole(r.Config.InstrumentProvider, r.Config.InstrumentModel)
	if err != nil {
		return nil, err
	}
	provider, err := llm.NewInstrumentProvider(r.Config)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		provider = r.Provider
		instrumentCfg = r.Config
	}
	instrumentCfg.ApprovalPolicy = config.ApprovalReadOnly
	instrumentCfg.AgentEnabled = false
	instrumentCfg.DirectorEnabled = false
	instrumentCfg.MaxTokens = int64(maxInt(500, minInt(2400, instrumentCfg.InstrumentMaxSteps*600)))
	return &DirectorSession{provider: provider, config: instrumentCfg, weight: r.Config.InstrumentWeight}, nil
}

func (d *DirectorSession) review(ctx context.Context, state *runState, iteration int, stage, proposedFinal string, emit func(StreamUpdate)) (instrumentReview, error) {
	if d == nil || d.provider == nil {
		return instrumentReview{}, nil
	}
	if emit != nil {
		emit(StreamUpdate{Kind: StreamStatus, Phase: "instrument review", Iteration: iteration, Tool: "instrument", Verified: state.verified})
	}
	prompt := fmt.Sprintf(`You are the read-only instrument in a director/instrument coding council.
The director has final authority. Review only for concrete correctness, regression, safety, or missing-verification issues.

STAGE: %s
DIRECTOR PLAN:
%s

RECENT TOOL EVIDENCE:
%s

CHANGED PATHS: %s
VERIFIED: %t
PROPOSED FINAL:
%s

Return exactly this compact format:
VERDICT: CLEAN|LOW|MEDIUM|HIGH
FEEDBACK: 1-4 concise sentences

Use CLEAN when no concrete issue exists. Do not request tools, do not restate the work, and do not give style-only feedback.`,
		stage,
		compact(state.lastPlan, 1200),
		compact(strings.Join(tailStrings(state.observations, 6), "\n"), 2600),
		strings.Join(sortedKeys(state.changedPaths), ", "),
		state.verified,
		compact(proposedFinal, 1200),
	)
	req := llm.Request{
		Model:       d.config.Model(),
		System:      "You are a strict read-only reviewer. Never call tools or propose workspace mutations directly.",
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:   d.config.MaxTokens,
		Temperature: 0,
	}
	req, replacements := llm.NormalizeRequestUTF8(req)
	reviewCtx := debuglog.WithScope(ctx, debuglog.Scope{Provider: d.provider.Name(), Model: d.config.Model(), Iteration: iteration, Tool: "instrument"})
	if replacements > 0 {
		debuglog.WarningCtx(reviewCtx, "agent", "invalid utf-8 normalized", "instrument context contained invalid UTF-8 and was normalized before transport", map[string]any{
			"replacement_fields": replacements,
		})
	}
	_ = debuglog.AppendContext(reviewCtx, "instrument_request", 1, "text", map[string]any{
		"stage":   stage,
		"request": req,
	})
	text, err := llm.GenerateStreaming(reviewCtx, d.provider, req, nil)
	responsePayload := map[string]any{"stage": stage, "text": text}
	if err != nil {
		responsePayload["error"] = err.Error()
	}
	_ = debuglog.AppendContext(reviewCtx, "instrument_response", 1, "text", responsePayload)
	if err != nil {
		return instrumentReview{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return instrumentReview{}, nil
	}
	severity := parseInstrumentSeverity(text)
	return instrumentReview{Text: text, Severity: severity, Incorporate: d.shouldIncorporate(severity)}, nil
}

func parseInstrumentSeverity(text string) instrumentSeverity {
	upper := strings.ToUpper(text)
	switch {
	case strings.Contains(upper, "VERDICT: HIGH"), strings.Contains(upper, "REGRESSION"), strings.Contains(upper, "SECURITY"), strings.Contains(upper, "DATA LOSS"):
		return instrumentHigh
	case strings.Contains(upper, "VERDICT: MEDIUM"), strings.Contains(upper, "CONCRETE BUG"), strings.Contains(upper, "FAILS"):
		return instrumentMedium
	case strings.Contains(upper, "VERDICT: LOW"):
		return instrumentLow
	case strings.Contains(upper, "VERDICT: CLEAN"), strings.Contains(upper, "CLEAN —"), strings.Contains(upper, "NO ISSUES"):
		return instrumentClean
	default:
		// Unstructured feedback is advisory but never treated as a high-severity
		// blocker solely because a weaker reviewer ignored the output contract.
		return instrumentLow
	}
}

func (d *DirectorSession) shouldIncorporate(severity instrumentSeverity) bool {
	switch severity {
	case instrumentHigh:
		return true
	case instrumentMedium:
		return d.weight >= 20
	case instrumentLow:
		return d.weight >= 60
	default:
		return false
	}
}

func (d *DirectorSession) event(runID string, iteration int, stage string, review instrumentReview, err error) history.Event {
	status := "clean"
	content := review.Text
	if err != nil {
		status = "error"
		content = "Instrument review failed: " + err.Error()
	} else if review.Incorporate {
		status = "action-required"
	} else if review.Severity != instrumentClean {
		status = "noted"
	}
	event := runEvent(runID, iteration, "instrument_review", "Instrument review · "+stage, content, "instrument", status)
	event.Metadata["instrument"] = true
	event.Metadata["stage"] = stage
	event.Metadata["severity"] = string(review.Severity)
	event.Metadata["incorporated"] = review.Incorporate
	if d != nil && d.provider != nil {
		event.Metadata["provider"] = d.provider.Name()
		event.Metadata["model"] = d.config.Model()
		event.Metadata["weight"] = d.weight
	}
	return event
}

func (r Runner) directorShouldReviewActions(action modelAction) bool {
	if !r.Config.DirectorEnabled || r.delegationDepth > 0 {
		return false
	}
	if len(action.Actions) > 1 {
		return true
	}
	for _, item := range action.Actions {
		name := firstNonEmpty(item.Name, item.Tool)
		if r.toolRisk(name) != tools.RiskRead || name == "delegate" {
			return true
		}
	}
	return false
}
