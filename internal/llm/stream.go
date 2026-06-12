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

	"github.com/ephemera-ai/ephemera/internal/debuglog"
)

// StreamingProvider is implemented by providers that can expose incremental
// output rather than waiting for the complete response.
type StreamingProvider interface {
	GenerateStream(context.Context, Request, DeltaFunc) (string, error)
}

// GenerateStreaming uses native provider streaming when available and falls
// back to one final delta for providers whose transport cannot stream safely.
func GenerateStreaming(ctx context.Context, provider Provider, req Request, onDelta DeltaFunc) (string, error) {
	var replacements int
	req, replacements = NormalizeRequestUTF8(req)
	if replacements > 0 {
		debuglog.WarningCtx(ctx, "provider", "invalid utf-8 normalized", "provider request contained invalid UTF-8 and was normalized before transport", providerLogFields(provider, req, map[string]any{
			"replacement_fields": replacements,
		}))
	}
	if stream, ok := provider.(StreamingProvider); ok {
		text, err := stream.GenerateStream(ctx, req, onDelta)
		if err != nil {
			debuglog.ErrorCtx(ctx, "provider", "stream generation failed", err, providerLogFields(provider, req, map[string]any{
				"streaming": true,
			}))
		}
		return text, err
	}
	text, err := provider.Generate(ctx, req)
	if err != nil {
		debuglog.ErrorCtx(ctx, "provider", "generation failed", err, providerLogFields(provider, req, map[string]any{
			"streaming": false,
		}))
		return "", err
	}
	if onDelta != nil && text != "" {
		if err := onDelta(Delta{Kind: DeltaText, Text: text}); err != nil {
			debuglog.ErrorCtx(ctx, "provider", "delta callback failed", err, providerLogFields(provider, req, nil))
			return "", err
		}
	}
	return text, nil
}

func providerLogFields(provider Provider, req Request, extra map[string]any) map[string]any {
	fields := map[string]any{
		"model": req.Model,
	}
	if provider != nil {
		fields["provider"] = provider.Name()
	}
	for key, value := range extra {
		fields[key] = value
	}
	return fields
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

	for attempt, optimized := range []bool{true, false} {
		if attempt > 0 {
			_ = os.WriteFile(outputPath, nil, 0o600)
		}
		text, emitted, commandOutput, runErr := p.generateStreamAttempt(ctx, exe, req, outputPath, optimized, onDelta)
		if runErr == nil {
			return text, nil
		}
		if optimized && !emitted && codexBridgeCompatibilityFailure(commandOutput) && ctx.Err() == nil {
			debuglog.WarningCtx(ctx, "provider", "codex bridge compatibility fallback", trimCommandOutput(commandOutput), providerLogFields(p, req, nil))
			continue
		}
		return "", runErr
	}
	return "", fmt.Errorf("codex bridge retry exhausted")
}

func (p *Codex) generateStreamAttempt(
	ctx context.Context,
	exe string,
	req Request,
	outputPath string,
	optimized bool,
	onDelta DeltaFunc,
) (string, bool, []byte, error) {
	bridgeDir, err := codexBridgeDirectory()
	if err != nil {
		return "", false, nil, err
	}
	cmd := exec.CommandContext(ctx, exe, p.execArgs(req, outputPath, true, optimized)...)
	cmd.Dir = bridgeDir
	cmd.Stdin = strings.NewReader(p.prompt(req))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", false, stderr.Bytes(), err
	}

	var latestAgent string
	latestItems := map[string]string{}
	emitted := false
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
			debuglog.WarningCtx(ctx, "provider", "codex stream event decode failed", err.Error(), providerLogFields(p, req, map[string]any{
				"event_bytes": len(scanner.Bytes()),
			}))
			continue
		}
		if event.Type == "error" || event.Type == "turn.failed" {
			message := firstNonEmpty(event.Error.Message, event.Message)
			if message != "" {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return "", emitted, stderr.Bytes(), fmt.Errorf("codex stream failed: %s", message)
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
		case "command_execution", "file_change", "mcp_tool_call", "web_search":
			// Nested Codex tools are deliberately disabled. Ephemera's portable
			// gateway is the sole tool authority so writes retain approvals,
			// snapshots, logs, and deterministic history.
			debuglog.WarningCtx(ctx, "provider", "codex attempted nested tool", event.Item.Type, providerLogFields(p, req, nil))
			continue
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
				return "", emitted, stderr.Bytes(), err
			}
			emitted = true
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", emitted, stderr.Bytes(), err
	}
	if err := cmd.Wait(); err != nil {
		return "", emitted, stderr.Bytes(), fmt.Errorf("codex exec failed: %w\n\n%s", err, trimCommandOutput(stderr.Bytes()))
	}

	result, err := os.ReadFile(outputPath)
	if err != nil {
		return "", emitted, stderr.Bytes(), fmt.Errorf("codex did not write a final response: %w", err)
	}
	finalText := strings.TrimSpace(string(result))
	if finalText == "" {
		finalText = strings.TrimSpace(latestAgent)
	}
	if finalText == "" {
		return "", emitted, stderr.Bytes(), fmt.Errorf("codex returned an empty response")
	}
	if latestAgent == "" && onDelta != nil {
		if err := onDelta(Delta{Kind: DeltaText, Text: finalText}); err != nil {
			return "", emitted, stderr.Bytes(), err
		}
		emitted = true
	}
	return finalText, emitted, stderr.Bytes(), nil
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
