package llm

import (
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
