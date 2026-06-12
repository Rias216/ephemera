package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodeToolArgumentsRepairsMissingStructuralClosers(t *testing.T) {
	args, repaired, err := decodeToolArgumentsString(`{"patches":[{"path":"a.txt","content":"a"},{"path":"b.txt","content":"b"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired {
		t.Fatal("expected structural repair")
	}
	patches, ok := args["patches"].([]any)
	if !ok || len(patches) != 2 {
		t.Fatalf("patches = %#v", args["patches"])
	}
}

func TestDecodeToolArgumentsNeverInventsTruncatedStringContent(t *testing.T) {
	_, _, err := decodeToolArgumentsString(`{"path":"main.go","content":"package main`)
	if err == nil {
		t.Fatal("expected unterminated string to remain invalid")
	}
}

func TestParsePortableToolDecisionAcceptsCanonicalAndOpenAIShapes(t *testing.T) {
	canonical, err := ParsePortableToolDecision(`{"text":"inspect","tool_calls":[{"id":"one","name":"read_file","arguments":{"path":"go.mod"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Transport != ToolTransportPortable || len(canonical.ToolCalls) != 1 || canonical.ToolCalls[0].Arguments["path"] != "go.mod" {
		t.Fatalf("canonical decision = %#v", canonical)
	}

	openAIShape, err := ParsePortableToolDecision(`{"tool_calls":[{"id":"two","function":{"name":"search","arguments":"{\"query\":\"needle\"}"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(openAIShape.ToolCalls) != 1 || openAIShape.ToolCalls[0].Name != "search" || openAIShape.ToolCalls[0].Arguments["query"] != "needle" {
		t.Fatalf("OpenAI-shaped decision = %#v", openAIShape)
	}
}

func TestParsePortableToolDecisionAcceptsEphemeraActions(t *testing.T) {
	decision, err := ParsePortableToolDecision(`{"summary":"inspect","actions":[{"tool":"read_file","arguments":{"path":"go.mod"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Text != "inspect" || len(decision.ToolCalls) != 1 || decision.ToolCalls[0].Name != "read_file" {
		t.Fatalf("decision = %#v", decision)
	}
}

type portableCaptureProvider struct {
	request Request
	answer  string
}

func (p *portableCaptureProvider) Name() string { return "portable-capture" }

func (p *portableCaptureProvider) Generate(_ context.Context, req Request) (string, error) {
	p.request = req
	return p.answer, nil
}

func TestGeneratePortableToolDecisionUsesTextOnlyHistoryAndFullCatalog(t *testing.T) {
	provider := &portableCaptureProvider{answer: `{"tool_calls":[{"name":"read_file","arguments":{"path":"go.mod"}}]}`}
	decision, err := GeneratePortableToolDecision(context.Background(), provider, Request{
		System: "base",
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "native-1", Name: "list_files", Arguments: map[string]any{"path": "."}}}},
			{Role: "tool", ToolResult: &ToolResult{ID: "native-1", Name: "list_files", OK: true, Output: "go.mod"}},
		},
	}, []ToolSpec{{Name: "read_file", Description: "Read", Parameters: ToolSchema{Type: "object"}}}, errors.New("unexpected EOF"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Transport != ToolTransportPortable || len(decision.ToolCalls) != 1 {
		t.Fatalf("decision = %#v", decision)
	}
	if !strings.Contains(provider.request.System, "UNIVERSAL TOOL GATEWAY") || !strings.Contains(provider.request.System, `"name":"read_file"`) {
		t.Fatalf("portable system prompt missing gateway/catalog: %s", provider.request.System)
	}
	for _, message := range provider.request.Messages {
		if len(message.ToolCalls) > 0 || message.ToolResult != nil || message.Role == "tool" {
			t.Fatalf("native history leaked into portable request: %#v", provider.request.Messages)
		}
	}
}

func TestRepairTruncatedToolCallClosesOnlyStructures(t *testing.T) {
	repaired, ok := RepairTruncatedToolCall(`{"path":"go.mod","options":{"line":4`)
	if !ok {
		t.Fatal("expected safe structural repair")
	}
	args, err := DecodeToolArgumentsLenient(repaired)
	if err != nil {
		t.Fatal(err)
	}
	if args["path"] != "go.mod" {
		t.Fatalf("args = %#v", args)
	}
}

func TestRepairTruncatedToolCallRejectsUnterminatedString(t *testing.T) {
	if repaired, ok := RepairTruncatedToolCall(`{"path":"go.mod`); ok || repaired != "" {
		t.Fatalf("unsafe repair accepted: %q", repaired)
	}
}

func TestStreamDecoderMarksStructuralRepairAsTruncated(t *testing.T) {
	args, repaired, truncated, err := decodeToolArgumentsForStream(`{"path":"go.mod"`)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired || !truncated || args["path"] != "go.mod" {
		t.Fatalf("args=%#v repaired=%t truncated=%t", args, repaired, truncated)
	}
	decision := ToolDecision{ToolCalls: []ToolCall{{Name: "read_file", Arguments: args, Truncated: true}}}
	if err := TruncatedToolDecisionError("test", decision); !IsTruncatedToolProtocolError(err) {
		t.Fatalf("error was not classified as truncation: %v", err)
	}
}

func TestMergeStreamFragmentHandlesDeltaCumulativeAndReplay(t *testing.T) {
	value := ""
	for _, fragment := range []string{"read_", "file", "read_file", "read_file"} {
		value = mergeStreamFragment(value, fragment)
	}
	if value != "read_file" {
		t.Fatalf("merged function name = %q, want read_file", value)
	}

	arguments := ""
	for _, fragment := range []string{`{"path":`, `{"path":"go.mod"`, `}`} {
		arguments = mergeStreamFragment(arguments, fragment)
	}
	if arguments != `{"path":"go.mod"}` {
		t.Fatalf("merged arguments = %q", arguments)
	}
}
