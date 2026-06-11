package config

import (
	"testing"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

func TestDefault(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if cfg.Provider != "ollama" || cfg.Model() == "" {
		t.Fatalf("unexpected default provider/model: %q/%q", cfg.Provider, cfg.Model())
	}
	if cfg.Mode != reasoning.ModeNormal || cfg.Theme != "rose" {
		t.Fatalf("unexpected default mode/theme: %q/%q", cfg.Mode, cfg.Theme)
	}
	if connection, ok := cfg.Connection("nvidia"); !ok || connection.BaseURL != NVIDIABaseURL {
		t.Fatalf("missing NVIDIA preset: %#v, %v", connection, ok)
	}
}

func TestNormalizeRepairsPartialConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai", Models: map[string]string{"openai": "custom-model"}}
	cfg.normalize()

	if cfg.Model() != "custom-model" {
		t.Fatalf("normalize overwrote selected model: %q", cfg.Model())
	}
	if cfg.Models["ollama"] == "" || cfg.Models["anthropic"] == "" {
		t.Fatal("normalize did not restore missing provider models")
	}
	if cfg.MaxTokens <= 0 || cfg.OllamaURL == "" {
		t.Fatal("normalize did not restore scalar defaults")
	}
	if _, ok := cfg.Connection("openai"); !ok {
		t.Fatal("normalize did not restore provider connections")
	}
}

func TestNormalizeKeepsCustomConnection(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Provider = "openrouter"
	cfg.SetConnection("openrouter", Connection{
		Protocol:  ProtocolOpenAICompatible,
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
	})
	cfg.SetModel("openai/gpt-4.1")
	cfg.normalize()

	if cfg.Provider != "openrouter" || cfg.Model() != "openai/gpt-4.1" {
		t.Fatalf("custom provider was not preserved: %q/%q", cfg.Provider, cfg.Model())
	}
}

func TestSetModelInitializesMap(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai"}
	cfg.SetModel("gpt-test")
	if got := cfg.Model(); got != "gpt-test" {
		t.Fatalf("Model() = %q, want gpt-test", got)
	}
}

func TestDefaultAPIKeyEnv(t *testing.T) {
	t.Parallel()
	if got := DefaultAPIKeyEnv("my-provider.io"); got != "MY_PROVIDER_IO_API_KEY" {
		t.Fatalf("DefaultAPIKeyEnv() = %q", got)
	}
}

func TestNormalizeMigratesLegacyOllamaURL(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "ollama", OllamaURL: "http://ollama.internal:11434"}
	cfg.normalize()
	connection, ok := cfg.Connection("ollama")
	if !ok || connection.BaseURL != cfg.OllamaURL {
		t.Fatalf("legacy Ollama URL was not migrated: %#v", connection)
	}
}
