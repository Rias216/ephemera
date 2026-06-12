package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ollamaStreamCall struct {
	Index     int
	ID        string
	Name      string
	Arguments map[string]any
}

func (p *Ollama) generateChatStream(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	base, err := url.Parse(p.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		if err == nil {
			err = fmt.Errorf("URL must include scheme and host")
		}
		return ToolDecision{}, fmt.Errorf("invalid Ollama URL %q: %w", p.baseURL, err)
	}
	messages := ollamaWireMessages(req)
	payload := map[string]any{
		"model":    req.Model,
		"messages": messages,
		"stream":   true,
		"options": map[string]any{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
		},
	}
	if len(specs) > 0 {
		payload["tools"] = ollamaJSONTools(specs)
	}
	if req.ReasoningSummary {
		payload["think"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolDecision{}, err
	}
	endpoint := strings.TrimRight(p.baseURL, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ToolDecision{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpReq)
	if err != nil {
		return ToolDecision{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return ToolDecision{}, fmt.Errorf("Ollama streaming request failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}

	var text strings.Builder
	calls := map[int]*ollamaStreamCall{}
	reasoningActivityEmitted := false
	emitReasoningActivity := func() error {
		if !req.ReasoningSummary || reasoningActivityEmitted {
			return nil
		}
		reasoningActivityEmitted = true
		return emitDelta(onDelta, DeltaReasoning, "Local model is reasoning…")
	}
	splitter := newThinkTagSplitter(func(kind DeltaKind, value string) error {
		if kind == DeltaReasoning {
			return emitReasoningActivity()
		}
		text.WriteString(value)
		return emitDelta(onDelta, DeltaText, value)
	})

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var chunk struct {
			Done    bool   `json:"done"`
			Error   string `json:"error"`
			Message struct {
				Content   string `json:"content"`
				Thinking  string `json:"thinking"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Index     int             `json:"index"`
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue
		}
		if chunk.Error != "" {
			return ToolDecision{}, fmt.Errorf("Ollama stream failed: %s", chunk.Error)
		}
		if chunk.Message.Thinking != "" {
			if err := emitReasoningActivity(); err != nil {
				return ToolDecision{}, err
			}
		}
		if chunk.Message.Content != "" {
			if err := splitter.Write(chunk.Message.Content); err != nil {
				return ToolDecision{}, err
			}
		}
		for _, incoming := range chunk.Message.ToolCalls {
			index := incoming.Function.Index
			call, ok := calls[index]
			if !ok {
				call = &ollamaStreamCall{Index: index}
				calls[index] = call
			}
			call.ID = firstNonEmpty(incoming.ID, call.ID)
			call.Name = firstNonEmpty(incoming.Function.Name, call.Name)
			if len(incoming.Function.Arguments) > 0 {
				args, _, decodeErr := decodeRawToolArguments(incoming.Function.Arguments)
				if decodeErr != nil {
					return ToolDecision{}, newToolProtocolError("Ollama", call.Name, string(incoming.Function.Arguments), decodeErr)
				}
				call.Arguments = args
			}
			if err := emitDelta(onDelta, DeltaActivity, toolActivityText(call.Name, len(incoming.Function.Arguments))); err != nil {
				return ToolDecision{}, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ToolDecision{}, err
	}
	if err := splitter.Close(); err != nil {
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
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		toolCalls = append(toolCalls, ToolCall{ID: call.ID, Name: call.Name, Arguments: args})
	}
	visible := strings.TrimSpace(text.String())
	if visible == "" && len(toolCalls) == 0 {
		return ToolDecision{}, fmt.Errorf("Ollama returned an empty streaming response")
	}
	return ToolDecision{Text: visible, ToolCalls: toolCalls, Transport: ToolTransportNative}, nil
}

func ollamaWireMessages(req Request) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	messages = append(messages, map[string]any{"role": "system", "content": req.System})
	for _, message := range req.Messages {
		switch message.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": message.Content})
		case "assistant":
			wire := map[string]any{"role": "assistant", "content": message.Content}
			if len(message.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(message.ToolCalls))
				for index, call := range message.ToolCalls {
					calls = append(calls, map[string]any{
						"id": stableToolCallID(call, index),
						"function": map[string]any{
							"name":      call.Name,
							"arguments": call.Arguments,
						},
					})
				}
				wire["tool_calls"] = calls
			}
			messages = append(messages, wire)
		case "tool":
			if message.ToolResult == nil {
				continue
			}
			result := *message.ToolResult
			messages = append(messages, map[string]any{
				"role":      "tool",
				"content":   toolResultContent(result),
				"tool_name": result.Name,
			})
		}
	}
	return messages
}

func ollamaJSONTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        spec.Name,
				"description": spec.Description,
				"parameters":  spec.Parameters,
			},
		})
	}
	return out
}

type thinkTagSplitter struct {
	pending  string
	thinking bool
	emit     func(DeltaKind, string) error
}

func newThinkTagSplitter(emit func(DeltaKind, string) error) *thinkTagSplitter {
	return &thinkTagSplitter{emit: emit}
}

func (s *thinkTagSplitter) Write(value string) error {
	s.pending += value
	for {
		tag := "<think>"
		kind := DeltaText
		if s.thinking {
			tag = "</think>"
			kind = DeltaReasoning
		}
		index := strings.Index(s.pending, tag)
		if index >= 0 {
			if index > 0 {
				if err := s.emit(kind, s.pending[:index]); err != nil {
					return err
				}
			}
			s.pending = s.pending[index+len(tag):]
			s.thinking = !s.thinking
			continue
		}
		keep := len(tag) - 1
		if len(s.pending) <= keep {
			return nil
		}
		emitText := s.pending[:len(s.pending)-keep]
		s.pending = s.pending[len(s.pending)-keep:]
		if emitText != "" {
			return s.emit(kind, emitText)
		}
		return nil
	}
}

func (s *thinkTagSplitter) Close() error {
	if s.pending == "" {
		return nil
	}
	kind := DeltaText
	if s.thinking {
		kind = DeltaReasoning
	}
	err := s.emit(kind, s.pending)
	s.pending = ""
	return err
}
