package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestPartialDecisionPreview(t *testing.T) {
	raw := `{"reasoning":{"goal":"fix black cells","approach":["replace unpainted padding"]},"summary":"renderer repair","plan":["paint every row"]}`
	if got := partialJSONStringField(raw, "goal"); got != "fix black cells" {
		t.Fatalf("goal = %q", got)
	}
	if got := partialJSONArrayFirstString(raw, "plan"); got != "paint every row" {
		t.Fatalf("plan = %q", got)
	}
}

func TestPartialDecisionPreviewShowsStreamingString(t *testing.T) {
	raw := `{"reasoning":{"goal":"still streaming`
	if got := partialJSONStringField(raw, "goal"); got != "still streaming" {
		t.Fatalf("expected live partial field, got %q", got)
	}
}

func TestPartialDecisionPreviewDecodesEscapesWhileStreaming(t *testing.T) {
	raw := `{"reasoning":{"current_state":"reading src\nchecking \u0066iles`
	if got := partialJSONStringField(raw, "current_state"); got != "reading src checking files" {
		t.Fatalf("decoded field = %q", got)
	}
}

func TestLatestReasoningPreviewTracksNewestText(t *testing.T) {
	raw := strings.Repeat("old ", 80) + "checking the latest tool result"
	got := latestReasoningPreview(raw)
	if !strings.Contains(got, "latest tool result") {
		t.Fatalf("preview did not track stream tail: %q", got)
	}
}

func TestReasoningDeltaUpdatesLiveThought(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "stream-reasoning")
	m.liveAgent.Active = true
	m.applyAgentStream(agent.StreamUpdate{
		Kind:      agent.StreamReasoning,
		Phase:     "reasoning",
		Iteration: 1,
		Delta:     "Inspecting the renderer state",
	})

	if m.liveAgent.Thought != "Inspecting the renderer state" {
		t.Fatalf("thought = %q", m.liveAgent.Thought)
	}
	if m.liveAgent.ReasoningChars == 0 {
		t.Fatal("reasoning chars were not tracked")
	}
}

func TestContextInspectorShowsLiveReasoning(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "context-reasoning")
	m.inspectorTab = inspectorContext
	m.liveAgent = liveAgentState{
		Active:         true,
		Thought:        "Checking the newest provider event",
		ReasoningChars: 34,
	}

	left, right := m.footerPaneDetail()
	if !strings.Contains(left, "Checking the newest provider event") {
		t.Fatalf("context detail = %q", left)
	}
	if !strings.Contains(right, "reasoning") {
		t.Fatalf("context metadata = %q", right)
	}
}

func TestFinishAgentStreamKeepsApprovalPromptOutOfConversation(t *testing.T) {
	cfg := config.Default()
	m := New(cfg, nil, "stream-approval")
	m.session.Append("user", "create main.go")
	before := len(m.session.Messages)
	pending := &agent.PendingApproval{
		Call:        tools.Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}},
		Reason:      "create the requested file",
		Fingerprint: "apply_patch:test",
	}

	m.finishAgentStream(agent.StreamUpdate{
		Kind:    agent.StreamDone,
		Phase:   "awaiting approval",
		Text:    "Approval required for apply_patch",
		Pending: pending,
	})

	if len(m.session.Messages) != before {
		t.Fatalf("stream approval prompt was persisted as conversation: %#v", m.session.Messages)
	}
	if m.pendingApproval == nil || m.notice == "" {
		t.Fatalf("stream approval state missing: pending=%#v notice=%q", m.pendingApproval, m.notice)
	}
}

func TestActivityDeltaUpdatesThinkingDisplayImmediately(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "stream-activity")
	m.liveAgent.Active = true
	m.applyAgentStream(agent.StreamUpdate{
		Kind:      agent.StreamActivity,
		Phase:     "preparing action",
		Iteration: 1,
		Delta:     "Preparing read_file · 42 argument chars",
	})

	if got := m.liveThoughtPreview(); got != "Preparing read_file · 42 argument chars" {
		t.Fatalf("thinking display = %q", got)
	}
	if m.liveAgent.ReasoningChars != 0 {
		t.Fatalf("safe activity was counted as reasoning: %d", m.liveAgent.ReasoningChars)
	}
}

func TestPhaseActivityProvidesImmediateThinkingFallback(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "phase-activity")
	m.liveAgent = liveAgentState{Active: true, Phase: "deliberating"}

	if got := m.liveThoughtPreview(); got != "Analyzing the request…" {
		t.Fatalf("thinking fallback = %q", got)
	}
}

func TestNewestActivityReplacesStaleThoughtInDisplay(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "newest-thinking-signal")
	m.liveAgent = liveAgentState{
		Active:            true,
		Thought:           "Old reasoning summary",
		ThoughtUpdatedAt:  time.Now().Add(-time.Second),
		Activity:          "Preparing read_file…",
		ActivityUpdatedAt: time.Now(),
	}

	if got := m.liveThoughtPreview(); got != "Preparing read_file…" {
		t.Fatalf("thinking display = %q, want newest activity", got)
	}
}

func TestPlainTextDeltaAdvancesThinkingDisplay(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := New(cfg, nil, "plain-text-progress")
	m.liveAgent.Active = true
	m.applyAgentStream(agent.StreamUpdate{
		Kind:      agent.StreamDelta,
		Phase:     "receiving decision",
		Iteration: 1,
		Delta:     "Hello there",
	})

	if got := m.liveThoughtPreview(); !strings.Contains(got, "11 chars") {
		t.Fatalf("thinking display = %q, want incremental response progress", got)
	}
}

func TestPlanUpdateRefreshesVisiblePlan(t *testing.T) {
	cfg := config.Default()
	m := New(cfg, nil, "stream-plan")
	plan := &agent.Plan{Goal: "upgrade", Steps: []agent.PlanStep{{ID: 1, Description: "inspect", Status: agent.PlanRunning}}}
	m.applyAgentStream(agent.StreamUpdate{Kind: agent.StreamPlan, Phase: "plan step running", Plan: plan})
	if !strings.Contains(m.liveAgent.Plan, "Goal: upgrade") || !strings.Contains(m.liveAgent.Plan, "inspect") {
		t.Fatalf("visible plan = %q", m.liveAgent.Plan)
	}
}
