package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/llm"
)

type providerErrorClass string

const (
	providerErrorPermanent providerErrorClass = "permanent"
	providerErrorTransient providerErrorClass = "transient"
	providerErrorRateLimit providerErrorClass = "rate_limit"
	providerErrorContext   providerErrorClass = "context_too_long"
)

func classifyProviderError(err error) providerErrorClass {
	if err == nil {
		return providerErrorPermanent
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "context length"), strings.Contains(text, "context_length"), strings.Contains(text, "too many tokens"), strings.Contains(text, "maximum context"):
		return providerErrorContext
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"), strings.Contains(text, "too many requests"):
		return providerErrorRateLimit
	case strings.Contains(text, "timeout"), strings.Contains(text, "temporar"), strings.Contains(text, "connection reset"), strings.Contains(text, "503"), strings.Contains(text, "502"), strings.Contains(text, "eof"):
		return providerErrorTransient
	default:
		return providerErrorPermanent
	}
}

func compactProviderRequest(req llm.Request) llm.Request {
	if len(req.Messages) <= 2 {
		return req
	}
	keep := len(req.Messages) * 2 / 3
	if keep < 2 {
		keep = 2
	}
	req.Messages = append([]llm.Message(nil), req.Messages[len(req.Messages)-keep:]...)
	for len(req.Messages) > 0 && req.Messages[0].Role == "assistant" {
		req.Messages = req.Messages[1:]
	}
	return req
}

func (r Runner) generateToolDecisionWithRetry(
	ctx context.Context,
	req llm.Request,
	specs []llm.ToolSpec,
	onDelta llm.DeltaFunc,
	onRetry func(attempt int, class providerErrorClass, err error),
) (llm.ToolDecision, error) {
	maxRetries := r.Config.ProviderMaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	backoff := time.Duration(r.Config.ProviderRetryBackoffMS) * time.Millisecond
	if backoff < 50*time.Millisecond {
		backoff = 350 * time.Millisecond
	}
	current := req
	for attempt := 0; attempt <= maxRetries; attempt++ {
		emitted := false
		decision, err := llm.GenerateToolDecision(ctx, r.Provider, current, specs, func(delta llm.Delta) error {
			emitted = emitted || strings.TrimSpace(delta.Text) != ""
			if onDelta == nil {
				return nil
			}
			return onDelta(delta)
		})
		if err == nil {
			return decision, nil
		}
		class := classifyProviderError(err)
		if emitted || attempt >= maxRetries || class == providerErrorPermanent || ctx.Err() != nil {
			return llm.ToolDecision{}, err
		}
		if class == providerErrorContext {
			compacted := compactProviderRequest(current)
			if len(compacted.Messages) == len(current.Messages) {
				return llm.ToolDecision{}, err
			}
			current = compacted
		}
		if onRetry != nil {
			onRetry(attempt+1, class, err)
		}
		timer := time.NewTimer(backoff * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.ToolDecision{}, ctx.Err()
		case <-timer.C:
		}
	}
	return llm.ToolDecision{}, fmt.Errorf("provider retry loop exhausted")
}
