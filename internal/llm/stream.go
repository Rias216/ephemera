package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// StreamingProvider is implemented by providers that can expose incremental
// output rather than waiting for the complete response.
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
		if err := onDelta(Delta{Kind: DeltaText, Text: text}); err != nil {
			return "", err
		}
	}
	return text, nil
}

func (p *OpenAI) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	if p.baseURL == "" {
		decision, err := p.generateResponsesStream(ctx, req, nil, onDelta)
		return decision.Text, err
	}
	decision, err := p.generateChatCompletionsStream(ctx, req, nil, onDelta)
	return decision.Text, err
}

func (p *Anthropic) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	decision, err := p.generateMessageStream(ctx, req, nil, onDelta)
	return decision.Text, err
}

func (p *Ollama) GenerateStream(ctx context.Context, req Request, onDelta DeltaFunc) (string, error) {
	decision, err := p.generateChatStream(ctx, req, nil, onDelta)
	return decision.Text, err
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

	var latestAgent string
	latestItems := map[string]string{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				ID   string `json:"id"`
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
		if event.Type != "item.updated" && event.Type != "item.completed" {
			continue
		}
		kind := DeltaText
		switch event.Item.Type {
		case "agent_message":
			kind = DeltaText
		case "reasoning":
			if !req.ReasoningSummary {
				continue
			}
			kind = DeltaReasoning
		default:
			continue
		}
		current := event.Item.Text
		key := firstNonEmpty(event.Item.ID, event.Item.Type)
		previous := latestItems[key]
		if current == "" || current == previous {
			continue
		}
		delta := current
		if strings.HasPrefix(current, previous) {
			delta = strings.TrimPrefix(current, previous)
		}
		latestItems[key] = current
		if kind == DeltaText {
			latestAgent = current
		}
		if delta != "" && onDelta != nil {
			if err := onDelta(Delta{Kind: kind, Text: delta}); err != nil {
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
		finalText = strings.TrimSpace(latestAgent)
	}
	if finalText == "" {
		return "", fmt.Errorf("codex returned an empty response")
	}
	if latestAgent == "" && onDelta != nil {
		if err := onDelta(Delta{Kind: DeltaText, Text: finalText}); err != nil {
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
