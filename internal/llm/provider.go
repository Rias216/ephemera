// Package llm presents one small interface over supported model providers.
package llm

import (
	"context"
	"fmt"

	"github.com/ephemera-ai/ephemera/internal/config"
)

// Message is a provider-neutral chat message.
type Message struct {
	Role    string
	Content string
}

// Request contains a complete stateless conversation.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int64
	Temperature float64
}

// Provider produces one assistant response.
type Provider interface {
	Name() string
	Generate(context.Context, Request) (string, error)
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
