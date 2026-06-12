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

type chatToolCallAccumulator struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

func (p *OpenAI) generateChatCompletionsStream(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	key, err := p.resolvedAPIKey()
	if err != nil {
		return ToolDecision{}, err
	}
	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	payload := map[string]any{
		"model":    req.Model,
		"messages": openAIWireMessages(req),
		"stream":   true,
	}
	if len(specs) > 0 {
		payload["tools"] = openAIWireTools(specs)
		payload["tool_choice"] = "auto"
	}
	if p.baseURL == "" {
		payload["max_completion_tokens"] = req.MaxTokens
		if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
			payload["reasoning_effort"] = effort
		}
	} else {
		payload["max_tokens"] = req.MaxTokens
		payload["temperature"] = req.Temperature
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
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
		return ToolDecision{}, fmt.Errorf("%s streaming request failed: %s: %s", p.Name(), response.Status, strings.TrimSpace(string(data)))
	}

	var text strings.Builder
	calls := map[int]*chatToolCallAccumulator{}
	reasoningActivityEmitted := false
	err = scanSSE(response.Body, func(data string) error {
		if data == "[DONE]" {
			return errSSEDone
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content              string          `json:"content"`
					ReasoningSummary     json.RawMessage `json:"reasoning_summary"`
					ReasoningSummaryText json.RawMessage `json:"reasoning_summary_text"`
					ReasoningContent     json.RawMessage `json:"reasoning_content"`
					Reasoning            json.RawMessage `json:"reasoning"`
					Thinking             json.RawMessage `json:"thinking"`
					ToolCalls            []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("%s stream failed: %s", p.Name(), chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if summary := firstNonEmpty(rawText(choice.Delta.ReasoningSummary), rawText(choice.Delta.ReasoningSummaryText)); req.ReasoningSummary && summary != "" {
				if err := emitDelta(onDelta, DeltaReasoning, summary); err != nil {
					return err
				}
			} else if req.ReasoningSummary && !reasoningActivityEmitted && firstNonEmpty(rawText(choice.Delta.ReasoningContent), rawText(choice.Delta.Reasoning), rawText(choice.Delta.Thinking)) != "" {
				reasoningActivityEmitted = true
				if err := emitDelta(onDelta, DeltaReasoning, "Provider is reasoning…"); err != nil {
					return err
				}
			}
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if err := emitDelta(onDelta, DeltaText, choice.Delta.Content); err != nil {
					return err
				}
			}
			for _, incoming := range choice.Delta.ToolCalls {
				call, ok := calls[incoming.Index]
				if !ok {
					call = &chatToolCallAccumulator{Index: incoming.Index}
					calls[incoming.Index] = call
				}
				call.ID = firstNonEmpty(incoming.ID, call.ID)
				if incoming.Function.Name != "" {
					call.Name += incoming.Function.Name
				}
				call.Arguments += incoming.Function.Arguments
			}
		}
		return nil
	})
	if err != nil && err != errSSEDone {
		return ToolDecision{}, err
	}

	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	toolCalls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := calls[index]
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		args := map[string]any{}
		if strings.TrimSpace(call.Arguments) != "" {
			dec := json.NewDecoder(strings.NewReader(call.Arguments))
			dec.UseNumber()
			if err := dec.Decode(&args); err != nil {
				return ToolDecision{}, fmt.Errorf("%s returned invalid arguments for tool %q: %w", p.Name(), call.Name, err)
			}
		}
		toolCalls = append(toolCalls, ToolCall{ID: call.ID, Name: call.Name, Arguments: args})
	}
	visible := strings.TrimSpace(text.String())
	if visible == "" && len(toolCalls) == 0 {
		return ToolDecision{}, fmt.Errorf("%s returned an empty streaming response", p.Name())
	}
	return ToolDecision{Text: visible, ToolCalls: toolCalls}, nil
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var object struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return firstNonEmpty(object.Text, object.Content)
	}
	return ""
}
