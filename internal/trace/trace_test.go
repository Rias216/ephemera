package trace

import (
	"strings"
	"testing"
	"time"

	"github.com/ephemera-ai/ephemera/internal/history"
)

func TestTraceRoundTripAndRender(t *testing.T) {
	root := t.TempDir()
	run := Run{
		ID: "run-1", StartedAt: time.Now().UTC(), Duration: 1500 * time.Millisecond,
		Provider: "test", Model: "model", Verified: true,
		Usage: Usage{InputTokens: 10, OutputTokens: 5, ToolCalls: 1},
		Events: []history.Event{
			{Type: history.EventToolCall, Title: "Read file", Tool: "read_file", Status: "active", Metadata: map[string]any{"iteration": 1}},
			{Type: history.EventToolResult, Title: "Read complete", Tool: "read_file", Status: "done"},
			{Type: history.EventFinal, Title: "Finished", Status: "done"},
		},
	}
	path, err := Write(root, run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "run-1.json") {
		t.Fatalf("path = %q", path)
	}
	loaded, err := Load(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != run.ID || len(loaded.Events) != 3 {
		t.Fatalf("loaded = %#v", loaded)
	}
	ids, err := List(root)
	if err != nil || len(ids) != 1 || ids[0] != "run-1" {
		t.Fatalf("ids=%#v err=%v", ids, err)
	}
	if tree := RenderTree(loaded); !strings.Contains(tree, "read_file") || !strings.Contains(tree, "in=10") {
		t.Fatalf("tree = %q", tree)
	}
	if mermaid := RenderMermaid(loaded); !strings.Contains(mermaid, "sequenceDiagram") || !strings.Contains(mermaid, "A->>T") {
		t.Fatalf("mermaid = %q", mermaid)
	}
}
