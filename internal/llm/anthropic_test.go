package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestAnthropicToolDecisionParsesToolUseBlocks(t *testing.T) {
	var blocks []anthropic.ContentBlockUnion
	if err := json.Unmarshal([]byte(`[
		{"type":"text","text":"need a file"},
		{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}
	]`), &blocks); err != nil {
		t.Fatal(err)
	}

	var streamed strings.Builder
	decision, err := anthropicToolDecision(blocks, func(delta Delta) error {
		streamed.WriteString(delta.Text)
		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
	if decision.Text != "need a file" || streamed.String() != "need a file" {
		t.Fatalf("text=%q streamed=%q", decision.Text, streamed.String())
	}
	if len(decision.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", decision.ToolCalls)
	}
	call := decision.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "read_file" || call.Arguments["path"] != "go.mod" {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestAnthropicWireToolsIncludesInputSchema(t *testing.T) {
	tools := anthropicWireTools([]ToolSpec{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters: ToolSchema{
			Type:                 "object",
			Properties:           map[string]ToolProperty{"path": {Type: "string", Description: "Workspace-relative path"}},
			Required:             []string{"path"},
			AdditionalProperties: false,
		},
	}})
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"name":"read_file"`, `"input_schema"`, `"path"`, `"required":["path"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool schema missing %s: %s", want, text)
		}
	}
}
