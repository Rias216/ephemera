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
	"os"
	"os/exec"
	"strings"
	"time"

	ollama "github.com/ollama/ollama/api"
)

// DeltaFunc receives visible assistant text as it arrives. Providers must not
// send hidden reasoning or private thinking blocks through this callback.
type DeltaFunc func(string) error

// StreamingProvider is implemented by providers that can expose incremental
// visible output rather than waiting for the complete response.
type StreamingProvider interface {
	GenerateStream(context.Context, Request, DeltaFunc) (string, error)
}

// GenerateStreaming uses native provider streaming when available and falls
// back to one final delta for providers whose transport cannot stream safely.
func GenerateStreaming(ctx context.Context, provider Provider, req Request, onDelta DeltaFunc) (string, error) {
	if stream, ok := provider.(StreamingProvider); ok {
		return stream.GenerateStream(ctx, req, onDelta)
	}
	text, err := provider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if onDelta != nil && text != "" {
		if err := onDelta(text); err != nil {
			return "", err
		}
	}
	return text, nil
}

func (p *OpenAI) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		if p.baseURL == "" {
			key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		} else {
			key = firstNonEmpty(os.Getenv(p.apiKeyEnv), os.Getenv("EPHEMERA_API_KEY"))
		}
	}
	if key == "" && p.baseURL == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set; run /connect openai")
	}
	if key == "" {
		key = "not-needed"
	}

	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	endpoint := baseURL + "/chat/completions"

	messages := make([]map[string]string, 0, len(req.Messages)+1)
	messages = append(messages, map[string]string{"role": "system", "content": req.System})
	for _, message := range req.Messages {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
		}
	}
	payload := map[string]any{
		"model":    req.Model,
		"messages": messages,
		"stream":   true,
	}
	if p.baseURL == "" {
		payload["max_completion_tokens"] = req.MaxTokens
	} else {
		payload["max_tokens"] = req.MaxTokens
		payload["temperature"] = req.Temperature
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpReq)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return "", fmt.Errorf("%s streaming request failed: %s: %s", p.Name(), response.Status, strings.TrimSpace(string(data)))
	}

	var out strings.Builder
	err = scanSSE(response.Body, func(data string) error {
		if data == "[DONE]" {
			return errSSEDone
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil // ignore keepalive/provider extension frames
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta.Content
			if delta == "" {
				continue
			}
			out.WriteString(delta)
			if onDelta != nil {
				if err := onDelta(delta); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && err != errSSEDone {
		return "", err
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("%s returned an empty streaming response", p.Name())
	}
	return text, nil
}

func (p *Anthropic) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not set; run /connect anthropic")
	}

	messages := make([]map[string]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
		}
	}
	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"system":     req.System,
		"messages":   messages,
		"stream":     true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpReq)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		return "", fmt.Errorf("Anthropic streaming request failed: %s: %s", response.Status, strings.TrimSpace(string(data)))
	}

	var out strings.Builder
	err = scanSSE(response.Body, func(data string) error {
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil
		}
		// Deliberately forward only visible text deltas. Thinking/signature
		// blocks remain private and are not rendered or persisted.
		if event.Type != "content_block_delta" || event.Delta.Type != "text_delta" || event.Delta.Text == "" {
			return nil
		}
		out.WriteString(event.Delta.Text)
		if onDelta != nil {
			return onDelta(event.Delta.Text)
		}
		return nil
	})
	if err != nil && err != errSSEDone {
		return "", err
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("Anthropic returned no streamed text content")
	}
	return text, nil
}

func (p *Ollama) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	base, err := url.Parse(p.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		if err == nil {
			err = fmt.Errorf("URL must include scheme and host")
		}
		return "", fmt.Errorf("invalid Ollama URL %q: %w", p.baseURL, err)
	}
	client := ollama.NewClient(base, p.client)
	messages := make([]ollama.Message, 0, len(req.Messages)+1)
	messages = append(messages, ollama.Message{Role: "system", Content: req.System})
	for _, message := range req.Messages {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, ollama.Message{Role: message.Role, Content: message.Content})
		}
	}
	stream := true
	request := &ollama.ChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   &stream,
		Options: map[string]interface{}{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
		},
	}
	var out strings.Builder
	if err := client.Chat(ctx, request, func(response ollama.ChatResponse) error {
		delta := response.Message.Content
		if delta == "" {
			return nil
		}
		out.WriteString(delta)
		if onDelta != nil {
			return onDelta(delta)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("Ollama streaming request failed: %w", err)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("Ollama returned an empty streaming response")
	}
	return text, nil
}

func (p *Codex) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	if _, err := loadCodexAccessToken(); err != nil {
		return "", err
	}
	exe, err := codexExecutable()
	if err != nil {
		return "", err
	}

	output, err := os.CreateTemp("", "ephemera-codex-stream-*.txt")
	if err != nil {
		return "", err
	}
	outputPath := output.Name()
	_ = output.Close()
	defer os.Remove(outputPath)

	args := []string{
		"exec",
		"--json",
		"--model", req.Model,
		"--sandbox", "read-only",
		"--ephemeral",
		"--skip-git-repo-check",
		"--color", "never",
		"--output-last-message", outputPath,
		"-",
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = strings.NewReader(codexPrompt(req))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var latest string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "error" || event.Type == "turn.failed" {
			message := firstNonEmpty(event.Error.Message, event.Message)
			if message != "" {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return "", fmt.Errorf("codex stream failed: %s", message)
			}
		}
		if (event.Type != "item.updated" && event.Type != "item.completed") || event.Item.Type != "agent_message" {
			continue
		}
		current := event.Item.Text
		if current == "" || current == latest {
			continue
		}
		delta := current
		if strings.HasPrefix(current, latest) {
			delta = strings.TrimPrefix(current, latest)
		}
		latest = current
		if delta != "" && onDelta != nil {
			if err := onDelta(delta); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return "", err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", err
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("codex exec failed: %w\n\n%s", err, trimCommandOutput(stderr.Bytes()))
	}

	result, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("codex did not write a final response: %w", err)
	}
	finalText := strings.TrimSpace(string(result))
	if finalText == "" {
		finalText = strings.TrimSpace(latest)
	}
	if finalText == "" {
		return "", fmt.Errorf("codex returned an empty response")
	}
	if latest == "" && onDelta != nil {
		if err := onDelta(finalText); err != nil {
			return "", err
		}
	}
	return finalText, nil
}

var errSSEDone = fmt.Errorf("SSE stream complete")

func scanSSE(reader io.Reader, handle func(string) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if err := handle(data); err != nil {
			return err
		}
	}
	return scanner.Err()
}
