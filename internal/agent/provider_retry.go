package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/llm"
	agentmetrics "github.com/ephemera-ai/ephemera/internal/metrics"
)

var providerPortableToolModes sync.Map

var providerHealthCache sync.Map

type providerHealthState struct {
	checkedAt time.Time
	errorText string
}

func providerHealthKey(provider llm.Provider) string {
	return providerToolModeKey(provider, "")
}

func checkProviderHealth(ctx context.Context, provider llm.Provider) error {
	checker, ok := provider.(llm.HealthChecker)
	if !ok || provider == nil {
		return nil
	}
	key := providerHealthKey(provider)
	if cached, ok := providerHealthCache.Load(key); ok {
		state := cached.(providerHealthState)
		if time.Since(state.checkedAt) < 60*time.Second {
			if state.errorText != "" {
				return fmt.Errorf("%s", state.errorText)
			}
			return nil
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := checker.HealthCheck(probeCtx)
	state := providerHealthState{checkedAt: time.Now()}
	if err != nil {
		state.errorText = err.Error()
	}
	providerHealthCache.Store(key, state)
	return err
}

func providerToolModeKey(provider llm.Provider, model string) string {
	if provider == nil {
		return ""
	}
	return fmt.Sprintf("%T:%s:%s", provider, strings.ToLower(strings.TrimSpace(provider.Name())), strings.ToLower(strings.TrimSpace(model)))
}

func prefersPortableTools(provider llm.Provider, model string) bool {
	key := providerToolModeKey(provider, model)
	if key == "" {
		return false
	}
	value, ok := providerPortableToolModes.Load(key)
	return ok && value == true
}

func rememberPortableTools(provider llm.Provider, model string) {
	if key := providerToolModeKey(provider, model); key != "" {
		providerPortableToolModes.Store(key, true)
	}
}

type providerErrorClass string

const (
	providerErrorPermanent     providerErrorClass = "permanent"
	providerErrorTransient     providerErrorClass = "transient"
	providerErrorRateLimit     providerErrorClass = "rate_limit"
	providerErrorContext       providerErrorClass = "context_too_long"
	providerErrorToolTruncated providerErrorClass = "tool_truncated"
	providerErrorToolProtocol  providerErrorClass = "tool_protocol"
)

func classifyProviderError(provider llm.Provider, err error) providerErrorClass {
	if err == nil {
		return providerErrorPermanent
	}
	if llm.IsTruncatedToolProtocolError(err) {
		return providerErrorToolTruncated
	}
	if llm.IsNativeToolCompatibilityError(provider, err) {
		return providerErrorToolProtocol
	}
	taxonomy := llm.ClassifyError(provider, err)
	switch taxonomy.Class {
	case "context_too_long":
		return providerErrorContext
	case "rate_limit":
		return providerErrorRateLimit
	case "transient", "timeout", "overloaded":
		return providerErrorTransient
	default:
		return providerErrorPermanent
	}
}

func providerRetryDelay(provider llm.Provider, err error, configured time.Duration, attempt int) (time.Duration, string) {
	if configured < 50*time.Millisecond {
		configured = 350 * time.Millisecond
	}
	delay := configured * time.Duration(1<<attempt)
	source := "configured"
	taxonomy := llm.ClassifyError(provider, err)
	if taxonomy.Backoff > delay {
		delay = taxonomy.Backoff
		source = "provider"
	}
	if taxonomy.Class == "rate_limit" && delay < 2*time.Second {
		delay = 2 * time.Second
		source = "rate_limit_floor"
	}
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return delay, source
}

func providerRetryExhausted(provider llm.Provider, class providerErrorClass, attempts int, err error) error {
	if class != providerErrorRateLimit {
		return err
	}
	name := "provider"
	if provider != nil && strings.TrimSpace(provider.Name()) != "" {
		name = provider.Name()
	}
	return fmt.Errorf("%s rate limit remained active after %d attempt(s); wait before retrying or switch providers: %w", name, attempts, err)
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
	if err := checkProviderHealth(ctx, r.Provider); err != nil {
		req, _ = llm.NormalizeRequestUTF8(req)
		_ = debuglog.AppendContext(ctx, "provider_request", 1, "health-check", map[string]any{
			"request": req,
			"tools":   specs,
		})
		_ = debuglog.AppendContext(ctx, "provider_response", 1, "health-check", map[string]any{
			"error": err.Error(),
		})
		debuglog.WarningCtx(ctx, "agent", "provider health check failed", err.Error(), map[string]any{
			"provider": r.Provider.Name(),
			"model":    req.Model,
		})
		return llm.ToolDecision{}, fmt.Errorf("%s provider health check failed: %w", r.Provider.Name(), err)
	}
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
	truncationRetries := 0
	for attempt := 0; attempt <= maxRetries; attempt++ {
		emitted := false
		var decision llm.ToolDecision
		var err error
		var replacements int
		current, replacements = llm.NormalizeRequestUTF8(current)
		transport := "native"
		if portable {
			transport = "portable"
		} else if !llm.Capabilities(r.Provider).NativeTools || len(specs) == 0 {
			transport = "text"
		}
		if replacements > 0 {
			debuglog.WarningCtx(ctx, "agent", "invalid utf-8 normalized", "provider context contained invalid UTF-8 and was normalized before transport", map[string]any{
				"replacement_fields": replacements,
				"attempt":            attempt + 1,
				"transport":          transport,
			})
		}
		_ = debuglog.AppendContext(ctx, "provider_request", attempt+1, transport, map[string]any{
			"request":       current,
			"tools":         specs,
			"portable_mode": portable,
			"native_failure": func() string {
				if nativeFailure != nil {
					return nativeFailure.Error()
				}
				return ""
			}(),
		})
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
		if err == nil && !portable {
			err = llm.TruncatedToolDecisionError(r.Provider.Name(), decision)
		}
		responsePayload := map[string]any{
			"decision":      decision,
			"portable_mode": portable,
			"streamed":      emitted,
		}
		if err != nil {
			responsePayload["error"] = err.Error()
		}
		_ = debuglog.AppendContext(ctx, "provider_response", attempt+1, transport, responsePayload)
		if err == nil {
			return decision, nil
		}
		class := classifyProviderError(r.Provider, err)
		if !portable && class == providerErrorToolTruncated && len(specs) > 0 && truncationRetries < 1 && ctx.Err() == nil {
			truncationRetries++
			debuglog.WarningCtx(ctx, "agent", "truncated native tool retry", err.Error(), map[string]any{
				"provider": r.Provider.Name(),
				"model":    current.Model,
				"attempt":  truncationRetries,
			})
			agentmetrics.Default().Inc("agent_provider_retries_total")
			if onRetry != nil {
				onRetry(truncationRetries, class, err)
			}
			if onDelta != nil {
				_ = onDelta(llm.Delta{Kind: llm.DeltaActivity, Text: "Tool arguments were truncated; requesting one fresh native tool call…"})
			}
			// A transport truncation occurs before any local tool executes. Retry the
			// same native request once without consuming ProviderMaxRetries.
			attempt--
			continue
		}
		if !portable && (class == providerErrorToolProtocol || class == providerErrorToolTruncated) && len(specs) > 0 && ctx.Err() == nil {
			nativeFailure = err
			portable = true
			rememberPortableTools(r.Provider, current.Model)
			debuglog.WarningCtx(ctx, "agent", "native tool fallback", err.Error(), map[string]any{
				"provider": r.Provider.Name(),
				"model":    current.Model,
				"class":    string(class),
			})
			agentmetrics.Default().Inc("agent_provider_retries_total")
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
		if ctx.Err() != nil {
			return llm.ToolDecision{}, ctx.Err()
		}
		if attempt >= maxRetries || class == providerErrorPermanent {
			return llm.ToolDecision{}, providerRetryExhausted(r.Provider, class, attempt+1, err)
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
		delay, delaySource := providerRetryDelay(r.Provider, err, backoff, attempt)
		agentmetrics.Default().Inc("agent_provider_retries_total")
		if onRetry != nil {
			onRetry(attempt+1, class, err)
		}
		debuglog.WarningCtx(ctx, "agent", "provider retry", err.Error(), map[string]any{
			"provider":        r.Provider.Name(),
			"model":           current.Model,
			"attempt":         attempt + 1,
			"class":           string(class),
			"backoff_ms":      delay.Milliseconds(),
			"backoff_source":  delaySource,
			"next_attempt_at": time.Now().Add(delay).UTC().Format(time.RFC3339Nano),
		})
		if onDelta != nil {
			label := "Provider unavailable"
			if class == providerErrorRateLimit {
				label = "Provider rate limited"
			}
			_ = onDelta(llm.Delta{Kind: llm.DeltaActivity, Text: fmt.Sprintf("%s; retrying in %s…", label, delay.Round(100*time.Millisecond))})
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.ToolDecision{}, ctx.Err()
		case <-timer.C:
		}
	}
	return llm.ToolDecision{}, fmt.Errorf("provider retry loop exhausted")
}
