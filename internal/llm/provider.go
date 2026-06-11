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

// New constructs the configured provider. Secrets are read by each provider
// from environment variables at request time.
func New(cfg config.Config) (Provider, error) {
	switch cfg.Provider {
	case "openai":
		return NewOpenAI(), nil
	case "anthropic":
		return NewAnthropic(), nil
	case "ollama":
		return NewOllama(cfg.OllamaURL), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}
