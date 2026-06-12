package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScanSSE(t *testing.T) {
	input := "event: message\ndata: {\"value\":1}\n\ndata: [DONE]\n\n"
	var frames []string
	err := scanSSE(strings.NewReader(input), func(data string) error {
		frames = append(frames, data)
		if data == "[DONE]" {
			return errSSEDone
		}
		return nil
	})
	if err != errSSEDone {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 2 || frames[0] != `{"value":1}` || frames[1] != "[DONE]" {
		t.Fatalf("unexpected frames: %#v", frames)
	}
}

func TestThinkTagSplitterHandlesSplitTags(t *testing.T) {
	var got []Delta
	splitter := newThinkTagSplitter(func(kind DeltaKind, text string) error {
		got = append(got, Delta{Kind: kind, Text: text})
		return nil
	})
	for _, chunk := range []string{"before <thi", "nk>inspect", " files</th", "ink> after"} {
		if err := splitter.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := splitter.Close(); err != nil {
		t.Fatal(err)
	}

	var visible, reasoning strings.Builder
	for _, delta := range got {
		if delta.Kind == DeltaReasoning {
			reasoning.WriteString(delta.Text)
		} else {
			visible.WriteString(delta.Text)
		}
	}
	if visible.String() != "before  after" {
		t.Fatalf("visible = %q", visible.String())
	}
	if reasoning.String() != "inspect files" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
}

func TestRawTextSupportsCompatibleReasoningShapes(t *testing.T) {
	for _, raw := range []string{`"thinking"`, `{"text":"thinking"}`, `{"content":"thinking"}`} {
		if got := rawText([]byte(raw)); got != "thinking" {
			t.Fatalf("rawText(%s) = %q", raw, got)
		}
	}
}

func TestOpenAIReasoningModelDetection(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-5.4-mini", "o3", "openai/o4-mini"} {
		if !supportsOpenAIReasoning(model) {
			t.Fatalf("expected reasoning support for %q", model)
		}
	}
	for _, model := range []string{"gpt-4.1-mini", "gpt-4o", "text-embedding-3-small"} {
		if supportsOpenAIReasoning(model) {
			t.Fatalf("unexpected reasoning support for %q", model)
		}
	}
}

func TestOpenAIResponsesInputUsesPortableMessageContent(t *testing.T) {
	input := openAIResponsesInput([]Message{
		{Role: "user", Content: "inspect"},
		{Role: "assistant", Content: "done"},
	})
	if len(input) != 2 {
		t.Fatalf("input length = %d", len(input))
	}
	if input[1]["role"] != "assistant" || input[1]["content"] != "done" {
		t.Fatalf("assistant input = %#v", input[1])
	}
}

func TestEmitMissingDeltaAvoidsCompletedEventReplay(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("already streamed")
	var emitted []Delta
	if err := emitMissingDelta(&builder, "already streamed", DeltaReasoning, func(delta Delta) error {
		emitted = append(emitted, delta)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 0 {
		t.Fatalf("replayed completed event: %#v", emitted)
	}
}

func TestToolActivityTextReportsIncrementalProgress(t *testing.T) {
	if got := toolActivityText("read_file", 0); got != "Preparing read_file…" {
		t.Fatalf("initial activity = %q", got)
	}
	if got := toolActivityText("read_file", 64); got != "Preparing read_file · 64 argument chars" {
		t.Fatalf("incremental activity = %q", got)
	}
}

func TestOpenAIWireMessagesPreserveNativeToolHistory(t *testing.T) {
	messages := openAIWireMessages(Request{
		System: "system",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
			{Role: "tool", ToolResult: &ToolResult{ID: "call_1", Name: "list_files", OK: true, Output: "main.go"}},
		},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"role":"assistant"`, `"tool_calls"`, `"id":"call_1"`, `"role":"tool"`, `"tool_call_id":"call_1"`, `\"output\":\"main.go\"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("wire messages missing %s: %s", want, text)
		}
	}
}

func TestOpenAIResponsesInputPreservesFunctionCallAndOutput(t *testing.T) {
	input := openAIResponsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
		{Role: "tool", ToolResult: &ToolResult{ID: "call_1", Name: "list_files", OK: true, Output: "main.go"}},
	})
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"type":"function_call"`, `"call_id":"call_1"`, `"name":"list_files"`, `"type":"function_call_output"`, `"output"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("responses input missing %s: %s", want, text)
		}
	}
}

func TestAnthropicWireMessagesPreserveToolUseAndResult(t *testing.T) {
	messages := anthropicWireMessages([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "toolu_1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
		{Role: "tool", ToolResult: &ToolResult{ID: "toolu_1", Name: "list_files", OK: true, Output: "main.go"}},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"type":"tool_use"`, `"id":"toolu_1"`, `"type":"tool_result"`, `"tool_use_id":"toolu_1"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("anthropic messages missing %s: %s", want, text)
		}
	}
}

func TestOllamaWireMessagesPreserveToolHistory(t *testing.T) {
	messages := ollamaWireMessages(Request{
		System: "system",
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
			{Role: "tool", ToolResult: &ToolResult{ID: "call_1", Name: "list_files", OK: true, Output: "main.go"}},
		},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"tool_calls"`, `"name":"list_files"`, `"role":"tool"`, `"tool_name":"list_files"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("ollama messages missing %s: %s", want, text)
		}
	}
}
