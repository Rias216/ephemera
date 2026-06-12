// Package llm presents one small interface over supported model providers.
package llm

import (
	"context"
	"fmt"

	"github.com/ephemera-ai/ephemera/internal/config"
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

// ProviderCapabilities describe optional transports a provider can expose.
// Providers that do not implement CapableProvider are treated as plain text
// generators with fallback streaming.
type ProviderCapabilities struct {
	Streaming   bool
	NativeTools bool
}

// CapableProvider reports provider-specific optional behavior.
type CapableProvider interface {
	Capabilities() ProviderCapabilities
}

// ToolProperty is a small JSON-schema-compatible property description used by
// provider-native tool APIs and by local validation/help surfaces.
type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// ToolSchema describes a JSON object accepted by a tool.
type ToolSchema struct {
	Type                 string                  `json:"type"`
	Properties           map[string]ToolProperty `json:"properties,omitempty"`
	Required             []string                `json:"required,omitempty"`
	AdditionalProperties bool                    `json:"additionalProperties"`
}

// ToolSpec is the provider-neutral shape of one callable tool.
type ToolSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  ToolSchema `json:"parameters"`
}

// ToolCall is a provider-neutral request to execute a local tool.
type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
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
	caps := ProviderCapabilities{}
	if _, ok := provider.(StreamingProvider); ok {
		caps.Streaming = true
	}
	if capable, ok := provider.(CapableProvider); ok {
		reported := capable.Capabilities()
		caps.Streaming = caps.Streaming || reported.Streaming
		caps.NativeTools = reported.NativeTools
	}
	if _, ok := provider.(ToolCallingProvider); ok {
		caps.NativeTools = true
	}
	return caps
}

// GenerateToolDecision uses native provider tools when available, and otherwise
// falls back to the existing visible-text streaming path.
func GenerateToolDecision(ctx context.Context, provider Provider, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	if toolProvider, ok := provider.(ToolCallingProvider); ok && len(specs) > 0 {
		return toolProvider.GenerateWithTools(ctx, req, specs, onDelta)
	}
	text, err := GenerateStreaming(ctx, provider, req, onDelta)
	if err != nil {
		return ToolDecision{}, err
	}
	return ToolDecision{Text: text}, nil
}

// New constructs the configured provider. Environment variables remain valid,
// while /connect may supply runtime-only credentials in cfg.
func New(cfg config.Config) (Provider, error) {
	switch cfg.Provider {
	case "openai":
		return NewOpenAI(cfg.OpenAIKey), nil
	case "codex":
		return NewCodex(), nil
	case "anthropic":
		return NewAnthropic(cfg.AnthropicKey), nil
	case "ollama":
		return NewOllama(cfg.OllamaURL), nil
	case "compatible":
		return NewOpenAICompatible(cfg.CompatibleName, cfg.CompatibleURL, cfg.CompatibleKey), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}
