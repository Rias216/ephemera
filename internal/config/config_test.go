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
	if cfg.CodexBridgeMaxTokens != 2_048 {
		t.Fatalf("Codex bridge budget = %d, want 2048", cfg.CodexBridgeMaxTokens)
	}
}

func TestCodexBridgeBudgetBounds(t *testing.T) {
	cfg := Default()
	cfg.CodexBridgeMaxTokens = 10
	cfg.normalize()
	if cfg.CodexBridgeMaxTokens != 2_048 {
		t.Fatalf("small Codex bridge budget = %d, want default 2048", cfg.CodexBridgeMaxTokens)
	}

	cfg.CodexBridgeMaxTokens = 99_000
	cfg.normalize()
	if cfg.CodexBridgeMaxTokens != 8_000 {
		t.Fatalf("large Codex bridge budget = %d, want 8000", cfg.CodexBridgeMaxTokens)
	}
}

func TestNormalizeRepairsPartialConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{ProviderSettings: ProviderSettings{Provider: "openai", Models: map[string]string{"openai": "custom-model"}}}
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

	cfg := Config{ProviderSettings: ProviderSettings{Provider: "openai", Models: map[string]string{"openai": "gpt-5.5"}}}
	cfg.normalize()

	if got := cfg.Model(); got != "gpt-5.5" {
		t.Fatalf("explicit OpenAI model normalized to %q", got)
	}
}

func TestSetModelInitializesMap(t *testing.T) {
	t.Parallel()

	cfg := Config{ProviderSettings: ProviderSettings{Provider: "openai"}}
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

	cfg := Config{ProviderSettings: ProviderSettings{
		Provider:       "compatible",
		Models:         map[string]string{"compatible": "legacy-model"},
		CompatibleName: "legacy-host",
		CompatibleURL:  "https://legacy.example/v1",
	}}
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

func TestFrontierAgentDefaultsAndBounds(t *testing.T) {
	cfg := Default()
	if cfg.AgentMaxParallelTools != 4 || cfg.AgentToolTimeoutSec != 120 || cfg.AgentReadRetries != 1 || cfg.AgentContextSummaryTok != 800 {
		t.Fatalf("frontier defaults = parallel:%d timeout:%d retries:%d summary:%d", cfg.AgentMaxParallelTools, cfg.AgentToolTimeoutSec, cfg.AgentReadRetries, cfg.AgentContextSummaryTok)
	}
	if !cfg.AgentSemanticIndex || cfg.SandboxMode != SandboxNone || cfg.AgentSnapshotMaxMB != 128 || cfg.AgentContextRecall != 8 {
		t.Fatalf("phase-two defaults = semantic:%t sandbox:%q snapshot:%d recall:%d", cfg.AgentSemanticIndex, cfg.SandboxMode, cfg.AgentSnapshotMaxMB, cfg.AgentContextRecall)
	}
	cfg.AgentMaxParallelTools = 99
	cfg.AgentToolTimeoutSec = 9999
	cfg.AgentReadRetries = 99
	cfg.AgentContextSummaryTok = 99999
	cfg.AgentSnapshotMaxMB = 99999
	cfg.AgentContextRecall = 999
	cfg.SandboxMode = "unknown"
	cfg.normalize()
	if cfg.AgentMaxParallelTools != 8 || cfg.AgentToolTimeoutSec != 900 || cfg.AgentReadRetries != 3 || cfg.AgentContextSummaryTok != 4000 {
		t.Fatalf("bounded config = parallel:%d timeout:%d retries:%d summary:%d", cfg.AgentMaxParallelTools, cfg.AgentToolTimeoutSec, cfg.AgentReadRetries, cfg.AgentContextSummaryTok)
	}
	if cfg.AgentSnapshotMaxMB != 4096 || cfg.AgentContextRecall != 64 || cfg.SandboxMode != SandboxNone {
		t.Fatalf("phase-two bounds = snapshot:%d recall:%d sandbox:%q", cfg.AgentSnapshotMaxMB, cfg.AgentContextRecall, cfg.SandboxMode)
	}
}

func TestNormalizeMCPServerConfiguration(t *testing.T) {
	cfg := Default()
	cfg.MCPServers = map[string]MCPServerConfig{
		" Local FS ": {Command: "  npx  ", Args: []string{"server"}, TimeoutSec: 1},
		"REMOTE":     {URL: " https://example.test/mcp/ ", TimeoutSec: 9999},
		"broken":     {Transport: "websocket", URL: "ws://example.test"},
	}
	cfg.normalize()
	stdio, ok := cfg.MCPServers["local fs"]
	if !ok || stdio.Transport != "stdio" || stdio.Command != "npx" || stdio.TimeoutSec != cfg.AgentToolTimeoutSec {
		t.Fatalf("stdio config = %#v", stdio)
	}
	remote, ok := cfg.MCPServers["remote"]
	if !ok || remote.Transport != "http" || remote.URL != "https://example.test/mcp" || remote.TimeoutSec != 900 {
		t.Fatalf("remote config = %#v", remote)
	}
	if !cfg.MCPServers["broken"].Disabled {
		t.Fatalf("unsupported transport was not disabled: %#v", cfg.MCPServers["broken"])
	}
}

func TestSubagentAndDirectorDefaultsAndBounds(t *testing.T) {
	cfg := Default()
	if !cfg.SubagentEnabled || cfg.SubagentAutoRoute || cfg.SubagentMaxSteps != 4 || cfg.SubagentMaxTokens != 2000 {
		t.Fatalf("unexpected subagent defaults: %+v", cfg)
	}
	if cfg.DirectorEnabled || cfg.InstrumentWeight != 20 || cfg.DirectorMaxSteps != 12 || cfg.InstrumentMaxSteps != 2 {
		t.Fatalf("unexpected director defaults: %+v", cfg)
	}

	cfg.SubagentMaxSteps = 99
	cfg.SubagentMaxTokens = 10
	cfg.InstrumentWeight = 150
	cfg.DirectorMaxSteps = 1
	cfg.InstrumentMaxSteps = 99
	cfg.normalize()
	if cfg.SubagentMaxSteps != 8 || cfg.SubagentMaxTokens != 500 || cfg.InstrumentWeight != 100 || cfg.DirectorMaxSteps != 4 || cfg.InstrumentMaxSteps != 6 {
		t.Fatalf("role bounds were not normalized: %+v", cfg)
	}
}

func TestConfigForRoleUsesRememberedRouteWithoutMutatingMainSelection(t *testing.T) {
	cfg := Default()
	mainProvider, mainModel := cfg.Provider, cfg.Model()
	routeID := cfg.RememberConnection(SavedConnection{Provider: "openai", Name: "work", Model: "gpt-main"}, "secret")
	cfg.ActivateConnection("ollama")

	role, err := cfg.ConfigForRole(routeID, "gpt-light")
	if err != nil {
		t.Fatal(err)
	}
	if role.Provider != "openai" || role.Model() != "gpt-light" || role.OpenAIKey != "secret" {
		t.Fatalf("unexpected role config: provider=%q model=%q key=%q", role.Provider, role.Model(), role.OpenAIKey)
	}
	if cfg.Provider != mainProvider || cfg.Model() != mainModel {
		t.Fatalf("main config mutated: provider=%q model=%q", cfg.Provider, cfg.Model())
	}
}

func TestConfigMarshalsHierarchicalSchema(t *testing.T) {
	cfg := Default()
	cfg.PluginDirectories = []string{"plugins"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"providers", "agent", "ui", "mcp", "plugins", "orchestration"} {
		if _, ok := root[group]; !ok {
			t.Fatalf("hierarchical config is missing %q: %s", group, data)
		}
	}
	if _, flat := root["provider"]; flat {
		t.Fatalf("provider leaked into the root object: %s", data)
	}
}

func TestConfigUnmarshalMigratesLegacyFlatSchema(t *testing.T) {
	legacy := []byte(`{
		"provider":"openai",
		"models":{"openai":"gpt-legacy"},
		"max_tokens":1234,
		"approval_policy":"read-only",
		"agent_max_steps":7,
		"theme":"mono",
		"mcp_servers":{"local":{"transport":"stdio","command":"helper"}},
		"subagent_enabled":false
	}`)
	var cfg Config
	if err := json.Unmarshal(legacy, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.normalize()
	if cfg.Provider != "openai" || cfg.Model() != "gpt-legacy" || cfg.MaxTokens != 1234 {
		t.Fatalf("legacy provider settings were not migrated: %#v", cfg.ProviderSettings)
	}
	if cfg.ApprovalPolicy != ApprovalReadOnly || cfg.AgentMaxSteps != 7 || cfg.Theme != "mono" {
		t.Fatalf("legacy agent/UI settings were not migrated: %#v %#v", cfg.AgentSettings, cfg.UISettings)
	}
	if _, ok := cfg.MCPServers["local"]; !ok {
		t.Fatalf("legacy MCP settings were not migrated: %#v", cfg.MCPServers)
	}
}

func TestValidProviderAcceptsRegisteredAdapterKeysWithoutEnumeration(t *testing.T) {
	for _, value := range []string{"custom-provider", "provider_2", "x"} {
		if !ValidProvider(value) {
			t.Fatalf("ValidProvider(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-bad", "bad/provider", "bad provider"} {
		if ValidProvider(value) {
			t.Fatalf("ValidProvider(%q) = true", value)
		}
	}
}

func TestConfigUnmarshalNestedGroupsOverrideLegacyFlatFields(t *testing.T) {
	data := []byte(`{
		"provider":"openai",
		"max_tokens":999,
		"theme":"legacy",
		"providers":{"provider":"anthropic","max_tokens":2048},
		"ui":{"theme":"nested"}
	}`)
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "anthropic" || cfg.MaxTokens != 2048 {
		t.Fatalf("nested provider settings did not win: %#v", cfg.ProviderSettings)
	}
	if cfg.Theme != "nested" {
		t.Fatalf("nested UI settings did not win: %#v", cfg.UISettings)
	}
}

func TestSaveDoesNotPersistRuntimeWorkspace(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)

	cfg := Default()
	cfg.WorkspaceRoot = filepath.Join(t.TempDir(), "temporary-workspace")
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(configHome, "ephemera", fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "temporary-workspace") || strings.Contains(string(data), "workspace_root") {
		t.Fatalf("runtime workspace leaked into config: %s", data)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkspaceRoot != "" {
		t.Fatalf("loaded workspace = %q, want process-local empty value", loaded.WorkspaceRoot)
	}
}

func TestLoadDropsLegacyPersistedWorkspace(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)
	dir := filepath.Join(configHome, "ephemera")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"workspace_root":"C:\\Users\\test\\AppData\\Local\\Temp\\TestPoisonedWorkspace"}`)
	if err := os.WriteFile(filepath.Join(dir, fileName), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceRoot != "" {
		t.Fatalf("legacy workspace survived load: %q", cfg.WorkspaceRoot)
	}
}
