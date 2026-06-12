package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if !ValidProvider("codex") {
		t.Fatal("codex provider should be supported")
	}
}

func TestNormalizeRepairsPartialConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai", Models: map[string]string{"openai": "custom-model"}}
	cfg.normalize()

	if cfg.Model() != "custom-model" {
		t.Fatalf("normalize overwrote selected model: %q", cfg.Model())
	}
	if cfg.Models["ollama"] == "" || cfg.Models["codex"] == "" || cfg.Models["anthropic"] == "" || cfg.Models["compatible"] == "" {
		t.Fatal("normalize did not restore missing provider models")
	}
	if cfg.MaxTokens <= 0 || cfg.ContextTokens <= 0 || cfg.OllamaURL == "" || cfg.CompatibleURL == "" {
		t.Fatal("normalize did not restore scalar defaults")
	}
	if cfg.ApprovalPolicy != ApprovalApproveWrites || cfg.MaxToolOutputTokens <= 0 || cfg.AutoTestCommand == "" {
		t.Fatal("normalize did not restore agent defaults")
	}
}

func TestDefaultAgentPolicy(t *testing.T) {
	t.Parallel()

	cfg := Default()
	if !cfg.AgentEnabled {
		t.Fatal("agent mode should be always on by default")
	}
	if cfg.ApprovalPolicy != ApprovalApproveWrites {
		t.Fatalf("approval policy = %q, want approve-writes", cfg.ApprovalPolicy)
	}
	if cfg.ThemeDensity != "comfortable" {
		t.Fatalf("theme density = %q, want comfortable", cfg.ThemeDensity)
	}
	if cfg.AgentMaxSteps < 8 || cfg.AgentLoopLimit < 1 || !cfg.AgentAutoVerify || !cfg.AgentAutoReview || !cfg.RequireReadBeforeEdit {
		t.Fatalf("unexpected agent quality defaults: %+v", cfg)
	}
}

func TestNormalizePreservesExplicitOpenAIModel(t *testing.T) {
	t.Parallel()

	cfg := Config{Provider: "openai", Models: map[string]string{"openai": "gpt-5.5"}}
	cfg.normalize()

	if got := cfg.Model(); got != "gpt-5.5" {
		t.Fatalf("explicit OpenAI model normalized to %q", got)
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

func TestSaveDoesNotRewriteExplicitOpenAIModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := Default()
	cfg.Provider = "openai"
	cfg.SetModel("gpt-5.5")

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Model(); got != "gpt-5.5" {
		t.Fatalf("Save rewrote in-memory model to %q", got)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Model(); got != "gpt-5.5" {
		t.Fatalf("Load rewrote saved model to %q", got)
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

func TestParseApprovalPolicyAliases(t *testing.T) {
	cases := map[string]ApprovalPolicy{
		"auto":            ApprovalAutoApprove,
		"auto-approve":    ApprovalAutoApprove,
		"safe":            ApprovalApproveWrites,
		"read-only":       ApprovalReadOnly,
		"workspace-write": ApprovalWorkspaceWrite,
		"chat":            ApprovalChat,
	}
	for input, want := range cases {
		got, ok := ParseApprovalPolicy(input)
		if !ok || got != want {
			t.Fatalf("ParseApprovalPolicy(%q) = %q, %t; want %q, true", input, got, ok, want)
		}
	}
}

func TestConnectionRegistryKeepsModelsAndCredentialsPerRoute(t *testing.T) {
	t.Parallel()

	cfg := Default()
	openRouterID := cfg.RememberConnection(SavedConnection{
		Provider: "compatible",
		Name:     "openrouter",
		BaseURL:  OpenRouterBaseURL,
		Model:    "openai/gpt-test",
	}, "router-secret")
	openAIID := cfg.RememberConnection(SavedConnection{
		Provider: "openai",
		Model:    "gpt-test",
	}, "openai-secret")

	if len(cfg.ConnectedConnections()) != 3 { // default Ollama + two connected routes
		t.Fatalf("connected routes = %d, want 3", len(cfg.ConnectedConnections()))
	}
	if !cfg.ActivateConnection(openRouterID) {
		t.Fatal("failed to reactivate OpenRouter")
	}
	if cfg.Provider != "compatible" || cfg.CompatibleName != "openrouter" || cfg.Model() != "openai/gpt-test" {
		t.Fatalf("unexpected OpenRouter activation: provider=%q name=%q model=%q", cfg.Provider, cfg.CompatibleName, cfg.Model())
	}
	if cfg.CompatibleKey != "router-secret" {
		t.Fatalf("compatible key = %q, want remembered credential", cfg.CompatibleKey)
	}
	if !cfg.ActivateConnection(openAIID) || cfg.OpenAIKey != "openai-secret" || cfg.Model() != "gpt-test" {
		t.Fatalf("unexpected OpenAI activation: key=%q model=%q", cfg.OpenAIKey, cfg.Model())
	}
}

func TestSetModelIsRememberedPerConnection(t *testing.T) {
	t.Parallel()

	cfg := Default()
	openAIID := cfg.RememberConnection(SavedConnection{Provider: "openai", Model: "gpt-old"}, "secret")
	cfg.SetModel("gpt-new")
	cfg.ActivateConnection("ollama")
	cfg.SetModel("qwen-new")

	cfg.ActivateConnection(openAIID)
	if got := cfg.Model(); got != "gpt-new" {
		t.Fatalf("OpenAI model = %q, want gpt-new", got)
	}
	cfg.ActivateConnection("ollama")
	if got := cfg.Model(); got != "qwen-new" {
		t.Fatalf("Ollama model = %q, want qwen-new", got)
	}
}

func TestSaveLoadPersistsConnectionCredentialOutsideConfigJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	cfg := Default()
	id := cfg.RememberConnection(SavedConnection{
		Provider: "compatible",
		Name:     "openrouter",
		BaseURL:  OpenRouterBaseURL,
		Model:    "openai/gpt-test",
	}, "remember-me")
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "remember-me") {
		t.Fatal("config.json leaked the remembered credential")
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveConnection != id || loaded.CredentialForConnection(id) != "remember-me" {
		t.Fatalf("credential or active route not restored: active=%q credential=%q", loaded.ActiveConnection, loaded.CredentialForConnection(id))
	}
	if loaded.CompatibleKey != "remember-me" || loaded.Model() != "openai/gpt-test" {
		t.Fatalf("active route was not hydrated: key=%q model=%q", loaded.CompatibleKey, loaded.Model())
	}
}

func TestNormalizeMigratesLegacyProviderToConnection(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Provider:       "compatible",
		Models:         map[string]string{"compatible": "legacy-model"},
		CompatibleName: "legacy-host",
		CompatibleURL:  "https://legacy.example/v1",
	}
	cfg.normalize()

	id := ConnectionID("compatible", "legacy-host")
	connection, ok := cfg.Connections[id]
	if !ok {
		t.Fatalf("legacy route %q was not migrated", id)
	}
	if cfg.ActiveConnection != id || connection.Model != "legacy-model" || connection.BaseURL != "https://legacy.example/v1" {
		t.Fatalf("unexpected migrated route: active=%q route=%+v", cfg.ActiveConnection, connection)
	}
}

func TestConfigForConnectionDoesNotMutateActiveModelMap(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.RememberConnection(SavedConnection{Provider: "compatible", Name: "first", BaseURL: "https://first.example/v1", Model: "first-model"}, "")
	cfg.RememberConnection(SavedConnection{Provider: "compatible", Name: "second", BaseURL: "https://second.example/v1", Model: "second-model"}, "")
	cfg.ActivateConnection("compatible:first")

	candidate, ok := cfg.ConfigForConnection("compatible:second")
	if !ok || candidate.Model() != "second-model" {
		t.Fatalf("unexpected candidate: ok=%t model=%q", ok, candidate.Model())
	}
	if got := cfg.Model(); got != "first-model" {
		t.Fatalf("active config mutated while inspecting another route: %q", got)
	}
	if got := cfg.Models["compatible"]; got != "first-model" {
		t.Fatalf("active model map mutated to %q", got)
	}
}
