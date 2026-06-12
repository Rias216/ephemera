package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type anthropicStreamBlock struct {
	Index int
	Type  string
	ID    string
	Name  string
	Input strings.Builder
}

func (p *Anthropic) generateMessageStream(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return ToolDecision{}, fmt.Errorf("ANTHROPIC_API_KEY is not set; run /connect anthropic")
	}

	messages := anthropicWireMessages(req.Messages)
	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"system":     req.System,
		"messages":   messages,
		"stream":     true,
	}
	if len(specs) > 0 {
		payload["tools"] = anthropicStreamTools(specs)
	}
	if req.ReasoningSummary && supportsAdaptiveClaude(req.Model) && !hasNativeToolHistory(req.Messages) {
		payload["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
			payload["output_config"] = map[string]any{"effort": effort}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpReq)
	if err != nil {
		return ToolDecision{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return ToolDecision{}, fmt.Errorf("Anthropic streaming request failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}

	var text strings.Builder
	blocks := map[int]*anthropicStreamBlock{}
	err = scanSSE(response.Body, func(data string) error {
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		switch event.Type {
		case "content_block_start":
			blocks[event.Index] = &anthropicStreamBlock{
				Index: event.Index,
				Type:  event.ContentBlock.Type,
				ID:    event.ContentBlock.ID,
				Name:  event.ContentBlock.Name,
			}
			if event.ContentBlock.Type == "tool_use" {
				if err := emitDelta(onDelta, DeltaActivity, toolActivityText(event.ContentBlock.Name, 0)); err != nil {
					return err
				}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					text.WriteString(event.Delta.Text)
					return emitDelta(onDelta, DeltaText, event.Delta.Text)
				}
			case "thinking_delta":
				if req.ReasoningSummary && event.Delta.Thinking != "" {
					return emitDelta(onDelta, DeltaReasoning, event.Delta.Thinking)
				}
			case "input_json_delta":
				block, ok := blocks[event.Index]
				if !ok {
					block = &anthropicStreamBlock{Index: event.Index, Type: "tool_use"}
					blocks[event.Index] = block
				}
				merged := mergeStreamFragment(block.Input.String(), event.Delta.PartialJSON)
				block.Input.Reset()
				block.Input.WriteString(merged)
				return emitDelta(onDelta, DeltaActivity, toolActivityText(block.Name, block.Input.Len()))
			}
		case "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("Anthropic stream failed: %s", event.Error.Message)
			}
		}
		return nil
	})
	if err != nil && err != errSSEDone {
		return ToolDecision{}, err
	}

	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	calls := make([]ToolCall, 0)
	for _, index := range indexes {
		block := blocks[index]
		if block.Type != "tool_use" || strings.TrimSpace(block.Name) == "" {
			continue
		}
		raw := strings.TrimSpace(block.Input.String())
		args, _, truncated, decodeErr := decodeToolArgumentsForStream(raw)
		if decodeErr != nil {
			return ToolDecision{}, newToolProtocolError("Anthropic", block.Name, raw, decodeErr)
		}
		calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: args, Truncated: truncated})
	}
	visible := strings.TrimSpace(text.String())
	if visible == "" && len(calls) == 0 {
		return ToolDecision{}, fmt.Errorf("Anthropic returned an empty streamed response")
	}
	return ToolDecision{Text: visible, ToolCalls: calls, Transport: ToolTransportNative}, nil
}

func anthropicWireMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	var pendingResults []map[string]any
	flushResults := func() {
		if len(pendingResults) == 0 {
			return
		}
		content := make([]map[string]any, len(pendingResults))
		copy(content, pendingResults)
		out = append(out, map[string]any{"role": "user", "content": content})
		pendingResults = nil
	}
	for _, message := range messages {
		if message.Role == "tool" {
			if message.ToolResult == nil {
				continue
			}
			result := *message.ToolResult
			pendingResults = append(pendingResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": result.ID,
				"content":     toolResultContent(result),
				"is_error":    !result.OK,
			})
			continue
		}
		flushResults()
		switch message.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "content": message.Content})
		case "assistant":
			if len(message.ToolCalls) == 0 {
				out = append(out, map[string]any{"role": "assistant", "content": message.Content})
				continue
			}
			content := make([]map[string]any, 0, len(message.ToolCalls)+1)
			if strings.TrimSpace(message.Content) != "" {
				content = append(content, map[string]any{"type": "text", "text": message.Content})
			}
			for index, call := range message.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    stableToolCallID(call, index),
					"name":  call.Name,
					"input": call.Arguments,
				})
			}
			out = append(out, map[string]any{"role": "assistant", "content": content})
		}
	}
	flushResults()
	return out
}

func anthropicStreamTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"name":         spec.Name,
			"description":  spec.Description,
			"input_schema": spec.Parameters,
		})
	}
	return out
}

func supportsAdaptiveClaude(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, marker := range []string{"sonnet-4-6", "opus-4-6", "opus-4-7", "opus-4-8", "fable-5", "mythos"} {
		if strings.Contains(model, marker) {
			return true
		}
	}
	return false
}
