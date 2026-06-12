package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

func TestNormalTranscriptHidesRoutineAgentTimelineAndDuplicateFinal(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg}
	m.session = history.New("session", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "Where is the folder?")
	m.session.AppendEvent(history.Event{
		Type:      "acceptance_contract",
		Title:     "Acceptance contract",
		Content:   "Forbidden changes: .git, .env",
		Status:    "active",
		CreatedAt: time.Now(),
	})
	answer := "The folder is in the workspace root and contains README.md."
	m.session.AppendEvent(history.Event{
		Type:      "final",
		Title:     "Final",
		Content:   answer,
		Status:    "done",
		CreatedAt: time.Now().Add(time.Millisecond),
	})
	m.session.Append("assistant", answer)

	sections := m.buildTranscriptSections()
	for _, section := range sections {
		if section.kind == sectionAgentEvent {
			t.Fatalf("routine or duplicate agent event leaked into normal transcript: %#v", section.event)
		}
	}
}

func TestTimelineFocusShowsFullAgentHistory(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, timelineFocus: true}
	m.session = history.New("session", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.AppendEvent(history.Event{Type: "acceptance_contract", Content: "contract", Status: "active", CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{Type: "tool_result", Tool: "read_file", Content: "README.md", Status: "done", CreatedAt: time.Now().Add(time.Millisecond)})

	sections := m.buildTranscriptSections()
	count := 0
	for _, section := range sections {
		if section.kind == sectionAgentEvent {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("timeline event count = %d, want 2", count)
	}
}

func TestNormalTranscriptStillSurfacesPendingApprovalAndErrors(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg}
	m.session = history.New("session", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.AppendEvent(history.Event{Type: history.EventApprovalRequest, Content: "outside path", Status: "pending", CreatedAt: time.Now()})
	m.session.AppendEvent(history.Event{Type: "tool_result", Tool: "read_file", Content: "permission denied", Status: "error", CreatedAt: time.Now().Add(time.Millisecond)})

	sections := m.buildTranscriptSections()
	var contents []string
	for _, section := range sections {
		if section.kind == sectionAgentEvent && section.event != nil {
			contents = append(contents, section.event.Content)
		}
	}
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "outside path") || !strings.Contains(joined, "permission denied") {
		t.Fatalf("high-signal events missing from normal transcript: %q", joined)
	}
}
