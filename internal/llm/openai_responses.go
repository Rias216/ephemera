package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type openAIResponseCall struct {
	Order     int
	ID        string
	CallID    string
	Name      string
	Arguments string
}

func (p *OpenAI) generateResponsesStream(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	key, err := p.resolvedAPIKey()
	if err != nil {
		return ToolDecision{}, err
	}
	payload := map[string]any{
		"model":             req.Model,
		"instructions":      req.System,
		"input":             openAIResponsesInput(req.Messages),
		"max_output_tokens": req.MaxTokens,
		"stream":            true,
	}
	if len(specs) > 0 {
		payload["tools"] = openAIResponsesTools(specs)
		payload["tool_choice"] = "auto"
	}
	if req.ReasoningSummary && supportsOpenAIReasoning(req.Model) {
		reasoning := map[string]any{"summary": "auto"}
		if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
			reasoning["effort"] = effort
		}
		payload["reasoning"] = reasoning
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpReq)
	if err != nil {
		return ToolDecision{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return ToolDecision{}, fmt.Errorf("OpenAI Responses request failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}

	var text strings.Builder
	var reasoning strings.Builder
	calls := map[string]*openAIResponseCall{}
	callKey := func(itemID string, outputIndex int) string {
		if itemID != "" {
			return itemID
		}
		return fmt.Sprintf("output:%d", outputIndex)
	}
	ensureCall := func(itemID string, outputIndex int) *openAIResponseCall {
		key := callKey(itemID, outputIndex)
		if call, ok := calls[key]; ok {
			return call
		}
		call := &openAIResponseCall{Order: outputIndex, ID: itemID}
		calls[key] = call
		return call
	}

	err = scanSSE(response.Body, func(data string) error {
		if data == "[DONE]" {
			return errSSEDone
		}
		var event struct {
			Type        string `json:"type"`
			Delta       string `json:"delta"`
			ItemID      string `json:"item_id"`
			OutputIndex int    `json:"output_index"`
			Item        struct {
				ID        string          `json:"id"`
				Type      string          `json:"type"`
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Arguments string          `json:"arguments"`
				Content   json.RawMessage `json:"content"`
				Summary   json.RawMessage `json:"summary"`
			} `json:"item"`
			Response struct {
				Output json.RawMessage `json:"output"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				text.WriteString(event.Delta)
				return emitDelta(onDelta, DeltaText, event.Delta)
			}
		case "response.reasoning_summary_text.delta":
			if req.ReasoningSummary && event.Delta != "" {
				reasoning.WriteString(event.Delta)
				return emitDelta(onDelta, DeltaReasoning, event.Delta)
			}
		case "response.output_item.added", "response.output_item.done":
			if event.Item.Type == "function_call" {
				call := ensureCall(firstNonEmpty(event.Item.ID, event.ItemID), event.OutputIndex)
				call.ID = firstNonEmpty(event.Item.ID, call.ID)
				call.CallID = firstNonEmpty(event.Item.CallID, call.CallID)
				call.Name = firstNonEmpty(event.Item.Name, call.Name)
				if event.Item.Arguments != "" {
					call.Arguments = event.Item.Arguments
				}
			}
		case "response.function_call_arguments.delta":
			call := ensureCall(event.ItemID, event.OutputIndex)
			call.Arguments += event.Delta
		case "response.completed":
			if len(event.Response.Output) > 0 {
				if err := parseOpenAIResponseOutput(event.Response.Output, &text, &reasoning, calls, onDelta, req.ReasoningSummary); err != nil {
					return err
				}
			}
		case "response.failed", "response.incomplete":
			message := "OpenAI response did not complete"
			if event.Response.Error != nil && event.Response.Error.Message != "" {
				message = event.Response.Error.Message
			}
			return fmt.Errorf("%s", message)
		case "error":
			if event.Error != nil && event.Error.Message != "" {
				return fmt.Errorf("OpenAI stream failed: %s", event.Error.Message)
			}
		}
		return nil
	})
	if err != nil && err != errSSEDone {
		return ToolDecision{}, err
	}

	ordered := make([]*openAIResponseCall, 0, len(calls))
	for _, call := range calls {
		ordered = append(ordered, call)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	toolCalls := make([]ToolCall, 0, len(ordered))
	for _, call := range ordered {
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		args := map[string]any{}
		if strings.TrimSpace(call.Arguments) != "" {
			dec := json.NewDecoder(strings.NewReader(call.Arguments))
			dec.UseNumber()
			if err := dec.Decode(&args); err != nil {
				return ToolDecision{}, fmt.Errorf("OpenAI returned invalid arguments for tool %q: %w", call.Name, err)
			}
		}
		toolCalls = append(toolCalls, ToolCall{ID: firstNonEmpty(call.CallID, call.ID), Name: call.Name, Arguments: args})
	}
	visible := strings.TrimSpace(text.String())
	if visible == "" && len(toolCalls) == 0 {
		return ToolDecision{}, fmt.Errorf("OpenAI returned an empty response")
	}
	return ToolDecision{Text: visible, ToolCalls: toolCalls}, nil
}

func openAIResponsesInput(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		out = append(out, map[string]any{
			"role":    message.Role,
			"content": message.Content,
		})
	}
	return out
}

func openAIResponsesTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        spec.Name,
			"description": spec.Description,
			"parameters":  spec.Parameters,
			"strict":      false,
		})
	}
	return out
}

func parseOpenAIResponseOutput(raw json.RawMessage, text, reasoning *strings.Builder, calls map[string]*openAIResponseCall, onDelta DeltaFunc, showReasoning bool) error {
	var items []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	for index, item := range items {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type != "output_text" || part.Text == "" {
					continue
				}
				if err := emitMissingDelta(text, part.Text, DeltaText, onDelta); err != nil {
					return err
				}
			}
		case "reasoning":
			if !showReasoning {
				continue
			}
			for _, part := range item.Summary {
				if part.Text != "" {
					if err := emitMissingDelta(reasoning, part.Text, DeltaReasoning, onDelta); err != nil {
						return err
					}
				}
			}
		case "function_call":
			key := firstNonEmpty(item.ID, fmt.Sprintf("output:%d", index))
			calls[key] = &openAIResponseCall{Order: index, ID: item.ID, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments}
		}
	}
	return nil
}

func emitMissingDelta(builder *strings.Builder, complete string, kind DeltaKind, onDelta DeltaFunc) error {
	current := builder.String()
	if complete == "" || current == complete || strings.Contains(current, complete) {
		return nil
	}
	delta := complete
	if strings.HasPrefix(complete, current) {
		delta = strings.TrimPrefix(complete, current)
	}
	if delta == "" {
		return nil
	}
	builder.WriteString(delta)
	return emitDelta(onDelta, kind, delta)
}

func emitDelta(onDelta DeltaFunc, kind DeltaKind, text string) error {
	if onDelta == nil || text == "" {
		return nil
	}
	return onDelta(Delta{Kind: kind, Text: text})
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func supportsOpenAIReasoning(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = model[slash+1:]
	}
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}
