package config

import (
	"encoding/json"
	"strings"
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
	if !ValidProvider("compatible") {
		t.Fatal("compatible provider should be supported")
	}
}

func TestNormalizeRepairsPartialConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai", Models: map[string]string{"openai": "custom-model"}}
	cfg.normalize()

	if cfg.Model() != "custom-model" {
		t.Fatalf("normalize overwrote selected model: %q", cfg.Model())
	}
	if cfg.Models["ollama"] == "" || cfg.Models["anthropic"] == "" || cfg.Models["compatible"] == "" {
		t.Fatal("normalize did not restore missing provider models")
	}
	if cfg.MaxTokens <= 0 || cfg.ContextTokens <= 0 || cfg.OllamaURL == "" || cfg.CompatibleURL == "" {
		t.Fatal("normalize did not restore scalar defaults")
	}
}

func TestNormalizeMigratesRetiredOpenAIModelDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai", Models: map[string]string{"openai": "gpt-5.4"}}
	cfg.normalize()

	if got := cfg.Model(); got != Default().Models["openai"] {
		t.Fatalf("retired OpenAI model normalized to %q, want %q", got, Default().Models["openai"])
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

func TestRuntimeKeysAreNeverSerialized(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.OpenAIKey = "openai-secret"
	cfg.AnthropicKey = "anthropic-secret"
	cfg.CompatibleKey = "compatible-secret"

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, secret := range []string{cfg.OpenAIKey, cfg.AnthropicKey, cfg.CompatibleKey} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("serialized config leaked runtime key %q", secret)
		}
	}
}

func TestCompatiblePresets(t *testing.T) {
	t.Parallel()

	for name, wantURL := range map[string]string{
		"nvidia":     NVIDIABaseURL,
		"openrouter": OpenRouterBaseURL,
		"groq":       GroqBaseURL,
		"together":   TogetherBaseURL,
		"lm-studio":  LMStudioBaseURL,
	} {
		preset, ok := Preset(name)
		if !ok {
			t.Fatalf("Preset(%q) was not found", name)
		}
		if preset.Protocol != ProtocolOpenAICompatible {
			t.Fatalf("Preset(%q) protocol = %q, want OpenAI-compatible", name, preset.Protocol)
		}
		if preset.BaseURL != wantURL {
			t.Fatalf("Preset(%q) base URL = %q, want %q", name, preset.BaseURL, wantURL)
		}
	}
}
