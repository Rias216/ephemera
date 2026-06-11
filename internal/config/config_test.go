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
}

func TestSetModelInitializesMap(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai"}
	cfg.SetModel("gpt-test")
	if got := cfg.Model(); got != "gpt-test" {
		t.Fatalf("Model() = %q, want gpt-test", got)
	}
}
