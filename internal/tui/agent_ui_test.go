package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/theme"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

func TestTimelineFilteringAndExpansion(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := Model{
		cfg:            cfg,
		styles:         theme.New("rose"),
		session:        history.New("timeline", cfg.Provider, cfg.Model(), reasoning.ModeNormal),
		expandedEvents: make(map[string]bool),
		followLive:     true,
	}
	m.session.AppendEvent(history.Event{ID: "think", Type: "reasoning_trace", Title: "Beneath the Surface", Content: "**Goal**\nMake events useful", Status: "done", CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{ID: "tool", Type: "tool_result", Title: "go test", Tool: "shell", Content: "failed", Status: "error", CreatedAt: time.Now()})

	if got := len(m.visibleAgentEvents()); got != 2 {
		t.Fatalf("visible events = %d, want 2", got)
	}
	m.timelineFilter = "tools"
	if events := m.visibleAgentEvents(); len(events) != 1 || events[0].ID != "tool" {
		t.Fatalf("tools filter = %#v, want only tool", events)
	}
	m.timelineFilter = "errors"
	if events := m.visibleAgentEvents(); len(events) != 1 || events[0].Status != "error" {
		t.Fatalf("errors filter = %#v, want error event", events)
	}

	m.timelineFilter = ""
	m.selectedEvent = 0
	m.toggleSelectedEvent()
	if !m.eventExpanded(m.session.Events[0], 0) {
		t.Fatal("selected event did not expand")
	}
}

func TestAgentTimelineRendersCards(t *testing.T) {
	cfg := config.Default()
	cfg.ShowThinking = true
	m := Model{cfg: cfg, styles: theme.New("rose"), session: history.New("timeline", cfg.Provider, cfg.Model(), reasoning.ModeNormal), width: 100, height: 40}
	m.resize()
	m.session.AppendEvent(history.Event{ID: "think", Type: "reasoning_trace", Title: "Beneath the Surface", Content: "**Goal**\nShip card timeline", Status: "done", CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{ID: "call", Type: "tool_call", Title: "read_file", Tool: "read_file", Content: "purpose: inspect renderer", Status: "running", CreatedAt: time.Now()})

	rendered := m.renderAgentTimeline()
	if !strings.Contains(rendered, "thinking surface") || !strings.Contains(rendered, "tool read_file") {
		t.Fatalf("timeline did not render expected card titles: %q", rendered)
	}
}

func TestSurfaceNoticePrefersStructuredTrace(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, styles: theme.New("rose"), session: history.New("surface", cfg.Provider, cfg.Model(), reasoning.ModeNormal)}
	m.session.Agent = history.AgentSnapshot{
		Status: "complete",
		Trace: history.AgentTrace{
			Goal:         "make the agent legible",
			Evidence:     []string{"tool call completed"},
			NextStep:     "ship the structured surface",
			Verification: "go test ./...",
		},
		Verified: true,
	}

	rendered := m.surfaceNotice()

	for _, want := range []string{"make the agent legible", "tool call completed", "ship the structured surface", "Verification · verified"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("surface notice missing %q:\n%s", want, rendered)
		}
	}
}

func TestMemoryNoticeReportsSources(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ephemera"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ephemera", "instructions.md"), []byte("Prefer focused edits.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Run go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorkspaceRoot = root
	m := Model{cfg: cfg, styles: theme.New("rose"), session: history.New("memory", cfg.Provider, cfg.Model(), reasoning.ModeNormal)}

	rendered := m.memoryNotice()

	for _, want := range []string{"Loaded sources: 2 / 4", ".ephemera/instructions.md", "AGENTS.md", "Prefer focused edits", "CLAUDE.md` — missing"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("memory notice missing %q:\n%s", want, rendered)
		}
	}
}

func TestApprovalPromptIsNotPersistedAsAssistantConversation(t *testing.T) {
	cfg := config.Default()
	m := New(cfg, nil, "approval-transcript")
	m.session.Append("user", "create main.go")
	before := len(m.session.Messages)
	pending := &agent.PendingApproval{
		Call:        tools.Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}},
		Reason:      "create the requested file",
		Fingerprint: "apply_patch:test",
	}

	updated, _ := m.Update(responseMsg{text: "Approval required for apply_patch", pending: pending})
	got := updated.(Model)
	if len(got.session.Messages) != before {
		t.Fatalf("approval control text was persisted as conversation: %#v", got.session.Messages)
	}
	if got.notice == "" || got.pendingApproval == nil {
		t.Fatalf("approval state was not surfaced: notice=%q pending=%#v", got.notice, got.pendingApproval)
	}
}

func TestResolvePendingApprovalUpdatesExactEvents(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, session: history.New("approval-events", cfg.Provider, cfg.Model(), reasoning.ModeNormal)}
	fingerprint := "apply_patch:{\"path\":\"main.go\"}"
	pending := agent.PendingApproval{
		Call:            tools.Call{Name: "apply_patch", Arguments: map[string]any{"path": "main.go", "content": "package main\n"}},
		Fingerprint:     fingerprint,
		CallEventID:     "call-1",
		ApprovalEventID: "approval-1",
	}
	m.session.AppendEvent(history.Event{ID: "call-1", Type: history.EventToolCall, Tool: "apply_patch", Status: "pending", Metadata: map[string]any{"call_fingerprint": fingerprint}, CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{ID: "approval-1", Type: history.EventApprovalRequest, Tool: "apply_patch", Status: "pending", Metadata: map[string]any{"call_fingerprint": fingerprint}, CreatedAt: time.Now()})
	result := history.Event{ID: "result-1", Type: history.EventToolResult, Tool: "apply_patch", Status: "done", Metadata: map[string]any{"call_fingerprint": fingerprint}, CreatedAt: time.Now()}
	m.session.AppendEvent(result)

	m.resolvePendingApproval(pending, result)

	if m.session.Events[0].Status != "done" {
		t.Fatalf("tool call status = %q, want done", m.session.Events[0].Status)
	}
	if m.session.Events[1].Status != "approved" {
		t.Fatalf("approval status = %q, want approved", m.session.Events[1].Status)
	}
	if m.session.Events[1].Metadata["result_event_id"] != "result-1" {
		t.Fatalf("approval metadata = %#v", m.session.Events[1].Metadata)
	}
}

func TestRejectPendingResolvesExistingEventsWithoutDuplicate(t *testing.T) {
	cfg := config.Default()
	fingerprint := "shell:{\"command\":\"rm temp\"}"
	pending := &agent.PendingApproval{
		Call:            tools.Call{Name: "shell", Arguments: map[string]any{"command": "rm temp"}},
		Fingerprint:     fingerprint,
		CallEventID:     "call-1",
		ApprovalEventID: "approval-1",
	}
	m := Model{
		cfg:             cfg,
		session:         history.New("reject-events", cfg.Provider, cfg.Model(), reasoning.ModeNormal),
		pendingApproval: pending,
	}
	m.session.AppendEvent(history.Event{ID: "call-1", Type: history.EventToolCall, Tool: "shell", Status: "pending", Metadata: map[string]any{"call_fingerprint": fingerprint}, CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{ID: "approval-1", Type: history.EventApprovalRequest, Tool: "shell", Status: "pending", Metadata: map[string]any{"call_fingerprint": fingerprint}, CreatedAt: time.Now()})

	m.rejectPending()

	if len(m.session.Events) != 2 {
		t.Fatalf("reject appended duplicate event: %#v", m.session.Events)
	}
	if m.session.Events[0].Status != "rejected" || m.session.Events[1].Status != "rejected" {
		t.Fatalf("events not resolved: %#v", m.session.Events)
	}
	if m.pendingApproval != nil {
		t.Fatal("pending approval was not cleared")
	}
}

func TestApprovePendingDoesNotStartDuplicateExecution(t *testing.T) {
	cfg := config.Default()
	m := Model{
		cfg:  cfg,
		busy: true,
		pendingApproval: &agent.PendingApproval{
			Call: tools.Call{Name: "shell", Arguments: map[string]any{"command": "echo once"}},
		},
	}

	if cmd := m.approvePending(); cmd != nil {
		t.Fatal("busy approval started a second execution")
	}
	if !strings.Contains(m.status, "already running") {
		t.Fatalf("status = %q", m.status)
	}
}
