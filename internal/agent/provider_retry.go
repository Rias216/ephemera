package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ephemera-ai/ephemera/internal/llm"
)

var providerPortableToolModes sync.Map

func providerToolModeKey(provider llm.Provider) string {
	if provider == nil {
		return ""
	}
	return fmt.Sprintf("%T:%s", provider, strings.ToLower(strings.TrimSpace(provider.Name())))
}

func prefersPortableTools(provider llm.Provider) bool {
	key := providerToolModeKey(provider)
	if key == "" {
		return false
	}
	value, ok := providerPortableToolModes.Load(key)
	return ok && value == true
}

func rememberPortableTools(provider llm.Provider) {
	if key := providerToolModeKey(provider); key != "" {
		providerPortableToolModes.Store(key, true)
	}
}

type providerErrorClass string

const (
	providerErrorPermanent    providerErrorClass = "permanent"
	providerErrorTransient    providerErrorClass = "transient"
	providerErrorRateLimit    providerErrorClass = "rate_limit"
	providerErrorContext      providerErrorClass = "context_too_long"
	providerErrorToolProtocol providerErrorClass = "tool_protocol"
)

func classifyProviderError(err error) providerErrorClass {
	if err == nil {
		return providerErrorPermanent
	}
	if llm.IsNativeToolCompatibilityError(err) {
		return providerErrorToolProtocol
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "context length"), strings.Contains(text, "context_length"), strings.Contains(text, "too many tokens"), strings.Contains(text, "maximum context"):
		return providerErrorContext
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"), strings.Contains(text, "too many requests"):
		return providerErrorRateLimit
	case strings.Contains(text, "timeout"), strings.Contains(text, "temporar"), strings.Contains(text, "connection reset"), strings.Contains(text, "503"), strings.Contains(text, "502"):
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
	preferPortable bool,
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
	portable := preferPortable
	var nativeFailure error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		emitted := false
		var decision llm.ToolDecision
		var err error
		if portable {
			decision, err = llm.GeneratePortableToolDecision(ctx, r.Provider, current, specs, nativeFailure, func(delta llm.Delta) error {
				// Portable mode returns a JSON envelope as ordinary text. Keep that
				// transport detail out of the transcript; tool events will expose the
				// parsed calls immediately afterward.
				if delta.Kind == llm.DeltaText {
					return ctx.Err()
				}
				emitted = emitted || strings.TrimSpace(delta.Text) != ""
				if onDelta == nil {
					return nil
				}
				return onDelta(delta)
			})
		} else {
			decision, err = llm.GenerateToolDecision(ctx, r.Provider, current, specs, func(delta llm.Delta) error {
				emitted = emitted || strings.TrimSpace(delta.Text) != ""
				if onDelta == nil {
					return nil
				}
				return onDelta(delta)
			})
		}
		if err == nil {
			return decision, nil
		}
		class := classifyProviderError(err)
		if !portable && class == providerErrorToolProtocol && len(specs) > 0 && ctx.Err() == nil {
			nativeFailure = err
			portable = true
			rememberPortableTools(r.Provider)
			if onRetry != nil {
				onRetry(attempt+1, class, err)
			}
			if onDelta != nil {
				_ = onDelta(llm.Delta{Kind: llm.DeltaActivity, Text: "Native tools were malformed; switching to universal tool mode…"})
			}
			// Switching transports is not a normal provider retry and is safe even
			// after streamed activity because no local tool has executed. Do not
			// consume the configured retry budget merely for changing transports.
			attempt--
			continue
		}
		if attempt >= maxRetries || class == providerErrorPermanent || ctx.Err() != nil {
			return llm.ToolDecision{}, err
		}
		// Ordinary transient retries remain conservative after visible text. The
		// tool-protocol fallback above is the sole exception because it is parsed
		// and executed only after a complete replacement decision arrives.
		if emitted && !portable {
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
