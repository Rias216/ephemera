// Package llm presents one small interface over supported model providers.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

// Message is a provider-neutral conversation item. Besides normal text turns,
// it can preserve native assistant tool calls and their corresponding tool
// results across stateless provider requests.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolResult *ToolResult
}

// Request contains a complete stateless conversation.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int64
	Temperature float64
	// ReasoningSummary asks providers to return a streamable reasoning summary
	// when their API supports one. Raw private reasoning is never surfaced or persisted.
	ReasoningSummary bool
	ReasoningEffort  string
}

// Provider produces one assistant response.
type Provider interface {
	Name() string
	Generate(context.Context, Request) (string, error)
}

// ErrorTaxonomy is a provider-owned classification used by recovery policy.
type ErrorTaxonomy struct {
	Code      string
	Class     string
	Provider  string
	Retryable bool
	Backoff   time.Duration
}

// ErrorClassifier lets an adapter classify its native errors without brittle
// cross-provider substring rules in the agent loop.
type ErrorClassifier interface {
	ClassifyError(error) ErrorTaxonomy
}

// NativeToolCompatibilityClassifier lets each adapter decide whether its own
// transport rejected native tool fields. The shared agent never infers this
// from provider names or a global list of error substrings.
type NativeToolCompatibilityClassifier interface {
	IsNativeToolCompatibilityError(error) bool
}

// HealthChecker is an optional lightweight provider readiness probe.
type HealthChecker interface {
	HealthCheck(context.Context) error
}

// ModelCatalogProvider is implemented by adapters that can enumerate models
// from their own endpoint or local runtime. Catalog behavior stays with the
// adapter instead of adding provider branches to shared code.
type ModelCatalogProvider interface {
	ListModels(context.Context) ([]string, error)
}

// ClassifyError delegates to a provider classifier and falls back to a
// conservative common taxonomy.
func ClassifyError(provider Provider, err error) ErrorTaxonomy {
	name := ""
	if provider != nil {
		name = provider.Name()
		if classifier, ok := provider.(ErrorClassifier); ok {
			classified := classifier.ClassifyError(err)
			if classified.Provider == "" {
				classified.Provider = name
			}
			return classified
		}
	}
	if err == nil {
		return ErrorTaxonomy{Code: "unknown", Class: "permanent", Provider: name}
	}
	text := strings.ToLower(err.Error())
	result := ErrorTaxonomy{Code: "provider_error", Class: "permanent", Provider: name}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		if result.Provider == "" {
			result.Provider = statusErr.Provider
		}
		switch {
		case statusErr.StatusCode == http.StatusTooManyRequests:
			result.Code, result.Class, result.Retryable = "rate_limit", "rate_limit", true
			result.Backoff = statusErr.RetryAfter
			if result.Backoff <= 0 {
				result.Backoff = 2 * time.Second
			}
			return result
		case statusErr.StatusCode == http.StatusRequestTimeout || statusErr.StatusCode == http.StatusBadGateway || statusErr.StatusCode == http.StatusServiceUnavailable || statusErr.StatusCode == http.StatusGatewayTimeout || statusErr.StatusCode >= 500:
			result.Code, result.Class, result.Retryable, result.Backoff = "transient", "transient", true, 500*time.Millisecond
			if statusErr.RetryAfter > result.Backoff {
				result.Backoff = statusErr.RetryAfter
			}
			return result
		case statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden:
			result.Code, result.Class = "auth", "permanent"
			return result
		}
	}
	switch {
	case strings.Contains(text, "context length"), strings.Contains(text, "context_length"), strings.Contains(text, "too many tokens"), strings.Contains(text, "maximum context"):
		result.Code, result.Class = "context_length", "context_too_long"
	case strings.Contains(text, "429"), strings.Contains(text, "rate limit"), strings.Contains(text, "too many requests"):
		result.Code, result.Class, result.Retryable, result.Backoff = "rate_limit", "rate_limit", true, 2*time.Second
	case strings.Contains(text, "timeout"), strings.Contains(text, "temporar"), strings.Contains(text, "connection reset"), strings.Contains(text, "empty streaming response"), strings.Contains(text, "empty response"), strings.Contains(text, "503"), strings.Contains(text, "502"):
		result.Code, result.Class, result.Retryable, result.Backoff = "transient", "transient", true, 500*time.Millisecond
	case strings.Contains(text, "401"), strings.Contains(text, "unauthorized"), strings.Contains(text, "api key"), strings.Contains(text, "authentication"):
		result.Code, result.Class = "auth", "permanent"
	}
	return result
}

// ProviderCapabilities describe optional transports a provider can expose.
// Providers that do not implement CapableProvider are treated as plain text
// generators with fallback streaming.
type ProviderCapabilities struct {
	Streaming         bool
	NativeTools       bool
	MaxContextWindow  int
	SupportsVision    bool
	SupportsReasoning bool
	MaxParallelTools  int
	ToolCallFormat    string // openai, anthropic, ollama, or text
	StreamingFormat   string // sse, newline-delimited, process, or buffered
}

// CapableProvider reports provider-specific optional behavior.
type CapableProvider interface {
	Capabilities() ProviderCapabilities
}

// ToolProperty, ToolSchema, and ToolSpec alias the executable tool contract.
// The provider layer sees the same definition the registry executes.
type ToolProperty = tools.ToolProperty
type ToolSchema = tools.ToolSchema
type ToolSpec = tools.Tool

// ToolCall is a provider-neutral request to execute a local tool.
type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	// Truncated is true only when a stream ended with structurally incomplete
	// JSON that was repaired without inventing string content. Local schema
	// validation still runs before the call can execute.
	Truncated bool `json:"truncated,omitempty"`
}

// ToolResult is a provider-neutral tool execution result.
type ToolResult struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	OK       bool           `json:"ok"`
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// DeltaKind distinguishes final response text, provider-supported reasoning
// summaries, and safe transport activity such as an in-progress tool call.
type DeltaKind string

const (
	DeltaText      DeltaKind = "text"
	DeltaReasoning DeltaKind = "reasoning"
	DeltaActivity  DeltaKind = "activity"
)

// Delta is one incremental provider stream part.
type Delta struct {
	Kind DeltaKind
	Text string
}

// DeltaFunc receives incremental provider output.
type DeltaFunc func(Delta) error

func toolActivityText(name string, argumentChars int) string {
	if name == "" {
		name = "tool call"
	}
	if argumentChars > 0 {
		return fmt.Sprintf("Preparing %s · %d argument chars", name, argumentChars)
	}
	return "Preparing " + name + "…"
}

// ToolDecision is the assistant's visible text plus any native tool calls.
type ToolDecision struct {
	Text      string
	ToolCalls []ToolCall
	Transport ToolTransport
}

// ToolCallingProvider can ask the model for native structured tool calls.
// Implementations may forward visible answer text and provider-supported
// reasoning summaries through onDelta.
type ToolCallingProvider interface {
	GenerateWithTools(context.Context, Request, []ToolSpec, DeltaFunc) (ToolDecision, error)
}

// Capabilities returns conservative defaults for providers that do not opt in.
func Capabilities(provider Provider) ProviderCapabilities {
	if provider == nil {
		return ProviderCapabilities{}
	}
	caps := ProviderCapabilities{ToolCallFormat: "text", StreamingFormat: "buffered", MaxParallelTools: 1}
	if _, ok := provider.(StreamingProvider); ok {
		caps.Streaming = true
		caps.StreamingFormat = "stream"
	}
	if capable, ok := provider.(CapableProvider); ok {
		reported := capable.Capabilities()
		caps.Streaming = caps.Streaming || reported.Streaming
		caps.NativeTools = reported.NativeTools
		if reported.MaxContextWindow > 0 {
			caps.MaxContextWindow = reported.MaxContextWindow
		}
		caps.SupportsVision = reported.SupportsVision
		caps.SupportsReasoning = reported.SupportsReasoning
		if reported.MaxParallelTools > 0 {
			caps.MaxParallelTools = reported.MaxParallelTools
		}
		if reported.ToolCallFormat != "" {
			caps.ToolCallFormat = reported.ToolCallFormat
		}
		if reported.StreamingFormat != "" {
			caps.StreamingFormat = reported.StreamingFormat
		}
	}
	if _, ok := provider.(ToolCallingProvider); ok {
		caps.NativeTools = true
		if caps.ToolCallFormat == "text" {
			caps.ToolCallFormat = "openai"
		}
	}
	if caps.MaxParallelTools < 1 {
		caps.MaxParallelTools = 1
	}
	return caps
}

// GenerateToolDecision uses native provider tools when available, and otherwise
// falls back to the existing visible-text streaming path.
func GenerateToolDecision(ctx context.Context, provider Provider, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	var replacements int
	req, replacements = NormalizeRequestUTF8(req)
	if replacements > 0 {
		debuglog.WarningCtx(ctx, "provider", "invalid utf-8 normalized", "provider tool request contained invalid UTF-8 and was normalized before transport", providerLogFields(provider, req, map[string]any{
			"replacement_fields": replacements,
			"tool_count":         len(specs),
		}))
	}
	if toolProvider, ok := provider.(ToolCallingProvider); ok && len(specs) > 0 {
		decision, err := toolProvider.GenerateWithTools(ctx, req, specs, onDelta)
		if err != nil {
			fields := providerLogFields(provider, req, map[string]any{
				"tool_count": len(specs),
				"transport":  "native",
			})
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				_ = debuglog.WriteCtx(ctx, "info", "provider", "generation cancelled", "native tool request cancelled", fields)
			} else {
				debuglog.ErrorCtx(ctx, "provider", "native tool generation failed", err, fields)
			}
		}
		return decision, err
	}
	text, err := GenerateStreaming(ctx, provider, req, onDelta)
	if err != nil {
		return ToolDecision{}, err
	}
	return ToolDecision{Text: text, Transport: ToolTransportText}, nil
}

// NewSubagentProvider creates the separately configured lightweight specialist.
// A nil provider means the subagent should inherit the parent provider.
func NewSubagentProvider(cfg config.Config) (Provider, error) {
	return newRoleProvider(cfg, cfg.SubagentProvider, cfg.SubagentModel)
}

// NewDirectorProvider creates an optional explicit primary provider for director
// mode. A nil provider means the active main-agent route remains the director.
func NewDirectorProvider(cfg config.Config) (Provider, error) {
	return newRoleProvider(cfg, cfg.DirectorProvider, cfg.DirectorModel)
}

// NewInstrumentProvider creates the read-only reviewer used by director mode.
// A nil provider means the instrument inherits the director provider.
func NewInstrumentProvider(cfg config.Config) (Provider, error) {
	return newRoleProvider(cfg, cfg.InstrumentProvider, cfg.InstrumentModel)
}

func newRoleProvider(cfg config.Config, routeOrProvider, model string) (Provider, error) {
	routeOrProvider = strings.ToLower(strings.TrimSpace(routeOrProvider))
	model = strings.TrimSpace(model)
	if routeOrProvider == "" && model == "" {
		return nil, nil
	}
	temp, err := cfg.ConfigForRole(routeOrProvider, model)
	if err != nil {
		return nil, err
	}
	return New(temp)
}
