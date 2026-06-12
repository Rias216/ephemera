// Package config owns Ephemera's small, human-readable configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

const (
	fileName            = "config.json"
	credentialsFileName = "credentials.json"
)

// ProviderSettings owns provider routing, model selection, retry behavior, and
// provider-specific bridge configuration.
type ProviderSettings struct {
	Provider               string                     `json:"provider"`
	Models                 map[string]string          `json:"models"`
	Connections            map[string]SavedConnection `json:"connections,omitempty"`
	ActiveConnection       string                     `json:"active_connection,omitempty"`
	MaxTokens              int64                      `json:"max_tokens"`
	ContextTokens          int                        `json:"context_tokens"`
	OllamaURL              string                     `json:"ollama_url"`
	CompatibleName         string                     `json:"compatible_name,omitempty"`
	CompatibleURL          string                     `json:"compatible_url,omitempty"`
	ProviderMaxRetries     int                        `json:"provider_max_retries"`
	ProviderRetryBackoffMS int                        `json:"provider_retry_backoff_ms"`
	CodexBridgeMaxTokens   int64                      `json:"codex_bridge_max_tokens"`
}

// AgentSettings owns the execution loop, safety policy, workspace, verification,
// context, and sandbox controls.
type AgentSettings struct {
	AgentConfigVersion     int            `json:"agent_config_version"`
	AgentEnabled           bool           `json:"agent_enabled"`
	ApprovalPolicy         ApprovalPolicy `json:"approval_policy"`
	WorkspaceRoot          string         `json:"workspace_root,omitempty"`
	AutoTestCommand        string         `json:"auto_test_command,omitempty"`
	MaxToolOutputTokens    int            `json:"max_tool_output_tokens"`
	AgentMaxSteps          int            `json:"agent_max_steps"`
	AgentLoopLimit         int            `json:"agent_loop_limit"`
	AgentMaxParallelTools  int            `json:"agent_max_parallel_tools"`
	AgentToolTimeoutSec    int            `json:"agent_tool_timeout_seconds"`
	AgentReadRetries       int            `json:"agent_read_retries"`
	AgentContextSummaryTok int            `json:"agent_context_summary_tokens"`
	AgentAutoVerify        bool           `json:"agent_auto_verify"`
	AgentAutoReview        bool           `json:"agent_auto_review"`
	AgentSelfCritique      bool           `json:"agent_self_critique"`
	AgentAdaptiveReasoning bool           `json:"agent_adaptive_reasoning"`
	AgentTDDMode           bool           `json:"agent_tdd_mode"`
	AgentLearnMemory       bool           `json:"agent_learn_memory"`
	AgentSemanticIndex     bool           `json:"agent_semantic_index"`
	AgentDryRun            bool           `json:"agent_dry_run"`
	AgentAutoRollback      bool           `json:"agent_auto_rollback"`
	SandboxMode            SandboxMode    `json:"sandbox_mode"`
	AgentSnapshotMaxMB     int            `json:"agent_snapshot_max_mb"`
	AgentContextRecall     int            `json:"agent_context_recall_messages"`
	AgentTaskTokenBudget   int            `json:"agent_task_token_budget"`
	RequireReadBeforeEdit  bool           `json:"require_read_before_edit"`
}

// UISettings owns reasoning mode and terminal presentation preferences.
type UISettings struct {
	Mode         reasoning.Mode `json:"mode"`
	Theme        string         `json:"theme"`
	ThemeDensity string         `json:"theme_density"`
	ShowThinking bool           `json:"show_thinking"`
	ToolDetails  bool           `json:"tool_details"`
}

// MCPSettings owns external Model Context Protocol server definitions.
type MCPSettings struct {
	MCPServers map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
}

// PluginSettings owns cross-platform subprocess plugin discovery.
type PluginSettings struct {
	PluginDirectories []string `json:"directories,omitempty"`
	PluginManifests   []string `json:"manifests,omitempty"`
}

// OrchestrationSettings owns subagent, director, and instrument routing.
type OrchestrationSettings struct {
	SubagentEnabled    bool   `json:"subagent_enabled"`
	SubagentAutoRoute  bool   `json:"subagent_auto_route"`
	SubagentProvider   string `json:"subagent_provider,omitempty"`
	SubagentModel      string `json:"subagent_model,omitempty"`
	SubagentMaxSteps   int    `json:"subagent_max_steps"`
	SubagentMaxTokens  int64  `json:"subagent_max_tokens"`
	DirectorEnabled    bool   `json:"director_enabled"`
	DirectorProvider   string `json:"director_provider,omitempty"`
	DirectorModel      string `json:"director_model,omitempty"`
	InstrumentProvider string `json:"instrument_provider,omitempty"`
	InstrumentModel    string `json:"instrument_model,omitempty"`
	InstrumentWeight   int    `json:"instrument_weight"`
	DirectorMaxSteps   int    `json:"director_max_steps"`
	InstrumentMaxSteps int    `json:"instrument_max_steps"`
}

// RuntimeSecrets are loaded from credentials.json and are never serialized.
type RuntimeSecrets struct {
	OpenAIKey     string            `json:"-"`
	AnthropicKey  string            `json:"-"`
	CompatibleKey string            `json:"-"`
	Credentials   map[string]string `json:"-"`
}

// Config is hierarchical on disk while embedded groups preserve concise field
// access throughout the runtime.
type Config struct {
	ProviderSettings      `json:"providers"`
	AgentSettings         `json:"agent"`
	UISettings            `json:"ui"`
	MCPSettings           `json:"mcp"`
	PluginSettings        `json:"plugins"`
	OrchestrationSettings `json:"orchestration"`
	RuntimeSecrets        `json:"-"`
}

// UnmarshalJSON accepts both the hierarchical schema and every legacy flat
// configuration written by earlier Ephemera versions. Nested groups win when a
// file contains both forms.
func (c *Config) UnmarshalJSON(data []byte) error {
	type wire Config
	var nested wire
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	var legacy struct {
		ProviderSettings
		AgentSettings
		UISettings
		MCPSettings
		PluginSettings
		OrchestrationSettings
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	if _, ok := root["providers"]; !ok {
		nested.ProviderSettings = legacy.ProviderSettings
	}
	if _, ok := root["agent"]; !ok {
		nested.AgentSettings = legacy.AgentSettings
	}
	if _, ok := root["ui"]; !ok {
		nested.UISettings = legacy.UISettings
	}
	if _, ok := root["mcp"]; !ok {
		nested.MCPSettings = legacy.MCPSettings
	}
	if _, ok := root["plugins"]; !ok {
		nested.PluginSettings = legacy.PluginSettings
	}
	if _, ok := root["orchestration"]; !ok {
		nested.OrchestrationSettings = legacy.OrchestrationSettings
	}
	*c = Config(nested)
	return nil
}

// MCPServerConfig describes one Model Context Protocol server. Standard
// transports are stdio and Streamable HTTP. Secrets should be referenced with
// ${ENV_VAR} placeholders rather than stored directly in config.json.
type MCPServerConfig struct {
	Transport  string            `json:"transport,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
	TimeoutSec int               `json:"timeout_seconds,omitempty"`
}

// SavedConnection is one reusable provider route. Connecting once records the
// endpoint, authentication convention, and last selected model so every model
// from that route can later be selected without running /connect again.
type SavedConnection struct {
	Provider  string `json:"provider"`
	Name      string `json:"name,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Model     string `json:"model"`
}

// NamedConnection pairs a stable route ID with its saved metadata.
type NamedConnection struct {
	ID         string
	Connection SavedConnection
}

// ApprovalPolicy controls how much autonomy the local agent has.
type ApprovalPolicy string

const (
	ApprovalChat           ApprovalPolicy = "chat"
	ApprovalReadOnly       ApprovalPolicy = "read-only"
	ApprovalApproveWrites  ApprovalPolicy = "approve-writes"
	ApprovalWorkspaceWrite ApprovalPolicy = "workspace-write" // legacy unrestricted mode
	ApprovalAutoApprove    ApprovalPolicy = "auto-approve"
)

// SandboxMode controls optional local execution isolation. Snapshot mode is
// implemented natively; docker is negotiated but only used when a compatible
// runtime is available.
type SandboxMode string

const (
	SandboxNone     SandboxMode = "none"
	SandboxSnapshot SandboxMode = "snapshot"
	SandboxDocker   SandboxMode = "docker"
)

// Protocol names the wire protocol used by a connection preset.
type Protocol string

const (
	ProtocolOllama           Protocol = "ollama"
	ProtocolOpenAI           Protocol = "openai"
	ProtocolCodex            Protocol = "codex"
	ProtocolAnthropic        Protocol = "anthropic"
	ProtocolOpenAICompatible Protocol = "openai-compatible"
	NVIDIABaseURL                     = "https://integrate.api.nvidia.com/v1"
	OpenRouterBaseURL                 = "https://openrouter.ai/api/v1"
	GroqBaseURL                       = "https://api.groq.com/openai/v1"
	TogetherBaseURL                   = "https://api.together.xyz/v1"
	LMStudioBaseURL                   = "http://localhost:1234/v1"
)

// Connection describes non-secret provider connection metadata.
type Connection struct {
	Protocol  Protocol `json:"protocol"`
	BaseURL   string   `json:"base_url,omitempty"`
	APIKeyEnv string   `json:"api_key_env,omitempty"`
}

// Default returns a useful local-first configuration.
func Default() Config {
	cfg := Config{
		ProviderSettings: ProviderSettings{
			Provider: "ollama",
			Models: map[string]string{
				"ollama":     "qwen3:8b",
				"openai":     "gpt-4.1-mini",
				"codex":      "gpt-5.5",
				"anthropic":  "claude-sonnet-4-6",
				"compatible": "model-name",
			},
			MaxTokens:              4096,
			ContextTokens:          16_000,
			OllamaURL:              "http://localhost:11434",
			ProviderMaxRetries:     2,
			ProviderRetryBackoffMS: 350,
			CodexBridgeMaxTokens:   2_048,
			CompatibleName:         "compatible",
			CompatibleURL:          "http://localhost:1234/v1",
			Connections:            map[string]SavedConnection{},
		},
		AgentSettings: AgentSettings{
			AgentConfigVersion:     7,
			AgentEnabled:           true,
			ApprovalPolicy:         ApprovalApproveWrites,
			AutoTestCommand:        "go test ./...",
			MaxToolOutputTokens:    6_000,
			AgentMaxSteps:          10,
			AgentLoopLimit:         2,
			AgentMaxParallelTools:  4,
			AgentToolTimeoutSec:    120,
			AgentReadRetries:       1,
			AgentContextSummaryTok: 800,
			AgentAutoVerify:        true,
			AgentAutoReview:        true,
			AgentSelfCritique:      false,
			AgentAdaptiveReasoning: true,
			AgentTDDMode:           false,
			AgentLearnMemory:       false,
			AgentSemanticIndex:     true,
			AgentDryRun:            false,
			AgentAutoRollback:      false,
			SandboxMode:            SandboxNone,
			AgentSnapshotMaxMB:     128,
			AgentContextRecall:     8,
			AgentTaskTokenBudget:   100_000,
			RequireReadBeforeEdit:  true,
		},
		UISettings: UISettings{
			Mode:         reasoning.ModeNormal,
			Theme:        "rose",
			ThemeDensity: "comfortable",
			ShowThinking: true,
			ToolDetails:  true,
		},
		MCPSettings: MCPSettings{MCPServers: map[string]MCPServerConfig{}},
		PluginSettings: PluginSettings{
			PluginDirectories: []string{},
			PluginManifests:   []string{},
		},
		OrchestrationSettings: OrchestrationSettings{
			SubagentEnabled:    true,
			SubagentAutoRoute:  false,
			SubagentMaxSteps:   4,
			SubagentMaxTokens:  2_000,
			DirectorEnabled:    false,
			InstrumentWeight:   20,
			DirectorMaxSteps:   12,
			InstrumentMaxSteps: 2,
		},
		RuntimeSecrets: RuntimeSecrets{Credentials: map[string]string{}},
	}
	cfg.RememberConnection(SavedConnection{
		Provider: "ollama",
		BaseURL:  cfg.OllamaURL,
		Model:    cfg.Models["ollama"],
	}, "")
	return cfg
}

// ProviderNames returns all provider types understood by Ephemera.
func ProviderNames() []string {
	return []string{"ollama", "openai", "codex", "anthropic", "compatible"}
}

// ConnectNames returns provider and preset names accepted by /connect.
func ConnectNames() []string {
	return []string{"ollama", "openai", "codex", "chatgpt", "anthropic", "compatible", "nvidia", "openrouter", "groq", "together", "lm-studio"}
}

// Preset returns built-in connection metadata for known providers.
func Preset(name string) (Connection, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ollama":
		return Connection{Protocol: ProtocolOllama, BaseURL: "http://localhost:11434"}, true
	case "openai":
		return Connection{Protocol: ProtocolOpenAI, APIKeyEnv: "OPENAI_API_KEY"}, true
	case "codex", "chatgpt":
		return Connection{Protocol: ProtocolCodex}, true
	case "anthropic":
		return Connection{Protocol: ProtocolAnthropic, APIKeyEnv: "ANTHROPIC_API_KEY"}, true
	case "nvidia":
		return Connection{Protocol: ProtocolOpenAICompatible, BaseURL: NVIDIABaseURL, APIKeyEnv: "NVIDIA_API_KEY"}, true
	case "openrouter":
		return Connection{Protocol: ProtocolOpenAICompatible, BaseURL: OpenRouterBaseURL, APIKeyEnv: "OPENROUTER_API_KEY"}, true
	case "groq":
		return Connection{Protocol: ProtocolOpenAICompatible, BaseURL: GroqBaseURL, APIKeyEnv: "GROQ_API_KEY"}, true
	case "together":
		return Connection{Protocol: ProtocolOpenAICompatible, BaseURL: TogetherBaseURL, APIKeyEnv: "TOGETHER_API_KEY"}, true
	case "lm-studio":
		return Connection{Protocol: ProtocolOpenAICompatible, BaseURL: LMStudioBaseURL, APIKeyEnv: "LM_STUDIO_API_KEY"}, true
	default:
		return Connection{}, false
	}
}

// DefaultAPIKeyEnv derives a conventional API-key environment variable name.
func DefaultAPIKeyEnv(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('_')
		}
	}
	prefix := strings.Trim(b.String(), "_")
	if prefix == "" {
		prefix = "EPHEMERA"
	}
	return prefix + "_API_KEY"
}

// ValidProvider reports whether value is a safe provider registry key. Runtime
// support is decided by llm.ProviderRegistry, so adding an adapter does not
// require changing the config package's built-in provider list.
func ValidProvider(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return false
	}
	for index, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !valid || (index == 0 && (r == '-' || r == '_')) {
			return false
		}
	}
	return true
}

// ValidApprovalPolicy reports whether value is a supported agent autonomy mode.
func ValidApprovalPolicy(value ApprovalPolicy) bool {
	switch value {
	case ApprovalChat, ApprovalReadOnly, ApprovalApproveWrites, ApprovalWorkspaceWrite, ApprovalAutoApprove:
		return true
	default:
		return false
	}
}

// ParseApprovalPolicy accepts both persisted policy names and concise CLI aliases.
func ParseApprovalPolicy(value string) (ApprovalPolicy, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "auto-approve", "unrestricted":
		return ApprovalAutoApprove, true
	case "safe", "approve", "approve-writes":
		return ApprovalApproveWrites, true
	case "read-only", "readonly", "read":
		return ApprovalReadOnly, true
	case "workspace-write", "workspace":
		return ApprovalWorkspaceWrite, true
	case "chat":
		return ApprovalChat, true
	default:
		return "", false
	}
}

// ApprovalPolicyChoices lists the concise values exposed by the command palette.
func ApprovalPolicyChoices() []string {
	return []string{"auto", "safe", "read-only", "workspace-write", "chat"}
}

// Dir returns Ephemera's platform-appropriate config directory.
func Dir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ephemera"), nil
}

// Load reads config.json. A missing file is not an error.
func Load() (Config, error) {
	cfg := Default()
	dir, err := Dir()
	if err != nil {
		return cfg, err
	}

	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	cfg = Config{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	credentials, err := loadCredentials(dir)
	if err != nil {
		return Default(), err
	}
	cfg.Credentials = credentials
	cfg.normalize()
	// Workspaces are process-local. Older releases persisted this field, which
	// allowed tests or a one-off launch to poison every later session with a
	// stale temporary directory. The CLI resolves the active root each launch.
	cfg.WorkspaceRoot = ""
	return cfg, nil
}

// Save atomically writes config.json and the separate local credential file.
// API keys carry json:"-" tags and are never embedded in config.json.
func Save(cfg Config) (retErr error) {
	defer func() {
		if retErr != nil {
			debuglog.Error("config", "save failed", retErr, nil)
		}
	}()
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if cfg.Credentials == nil {
		cfg.Credentials, _ = loadCredentials(dir)
	}
	// WorkspaceRoot is runtime state, not a global preference. Never serialize
	// it, so unit tests and temporary projects cannot redirect future launches.
	cfg.WorkspaceRoot = ""
	cfg.normalize()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := filepath.Join(dir, fileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, fileName)); err != nil {
		return err
	}
	return saveCredentials(dir, cfg.Credentials)
}

// Model returns the selected model for the active provider.
func (c Config) Model() string {
	if route, ok := c.Connections[c.ActiveConnection]; ok && route.Provider == c.Provider && strings.TrimSpace(route.Model) != "" {
		return route.Model
	}
	return c.Models[c.Provider]
}

// SetModel changes the model for the active provider.
func (c *Config) SetModel(model string) {
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	c.Models[c.Provider] = model
	if c.Connections != nil && c.ActiveConnection != "" {
		route := c.Connections[c.ActiveConnection]
		if route.Provider == c.Provider {
			route.Model = model
			c.Connections[c.ActiveConnection] = route
		}
	}
}

// ConnectionID returns the stable ID used for a saved route.
func ConnectionID(provider, name string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.ToLower(strings.TrimSpace(name))
	if provider == "compatible" {
		if name == "" {
			name = "compatible"
		}
		return "compatible:" + name
	}
	return provider
}

// DisplayName returns the user-facing route name.
func (c SavedConnection) DisplayName() string {
	if c.Provider == "compatible" && strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	return c.Provider
}

// RememberConnection stores or updates a reusable route and activates it.
// A blank apiKey preserves any credential already saved for the route.
func (c *Config) RememberConnection(connection SavedConnection, apiKey string) string {
	connection.Provider = strings.ToLower(strings.TrimSpace(connection.Provider))
	connection.Name = strings.ToLower(strings.TrimSpace(connection.Name))
	connection.BaseURL = strings.TrimRight(strings.TrimSpace(connection.BaseURL), "/")
	connection.Model = strings.TrimSpace(connection.Model)
	if connection.APIKeyEnv == "" {
		connection.APIKeyEnv = defaultCredentialEnv(connection)
	}
	id := ConnectionID(connection.Provider, connection.Name)
	if c.Connections == nil {
		c.Connections = map[string]SavedConnection{}
	}
	if previous, ok := c.Connections[id]; ok {
		if connection.Model == "" {
			connection.Model = previous.Model
		}
		if connection.BaseURL == "" {
			connection.BaseURL = previous.BaseURL
		}
		if connection.APIKeyEnv == "" {
			connection.APIKeyEnv = previous.APIKeyEnv
		}
	}
	c.Connections[id] = connection
	if c.Credentials == nil {
		c.Credentials = map[string]string{}
	}
	if strings.TrimSpace(apiKey) != "" {
		c.Credentials[id] = strings.TrimSpace(apiKey)
	}
	c.ActiveConnection = id
	c.applyConnection(id)
	return id
}

// ActivateConnection switches to a saved route without reconnecting.
func (c *Config) ActivateConnection(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := c.Connections[id]; !ok {
		return false
	}
	c.ActiveConnection = id
	c.applyConnection(id)
	return true
}

// ConfigForConnection returns a copy configured for one saved route.
func (c Config) ConfigForConnection(id string) (Config, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if _, ok := c.Connections[id]; !ok {
		return Config{}, false
	}
	models := make(map[string]string, len(c.Models))
	for provider, model := range c.Models {
		models[provider] = model
	}
	c.Models = models
	c.ActiveConnection = id
	c.applyConnection(id)
	return c, true
}

// ConfigForRole resolves a remembered route (preferred) or provider name and
// applies a role-specific model without mutating the active user selection.
// Empty values inherit the current route/model.
func (c Config) ConfigForRole(routeOrProvider, model string) (Config, error) {
	routeOrProvider = strings.ToLower(strings.TrimSpace(routeOrProvider))
	model = strings.TrimSpace(model)
	if routeOrProvider != "" {
		if routeID, ok := c.FindConnection(routeOrProvider); ok {
			resolved, found := c.ConfigForConnection(routeID)
			if !found {
				return Config{}, fmt.Errorf("configured route %q is no longer available", routeOrProvider)
			}
			c = resolved
		} else if ValidProvider(routeOrProvider) {
			models := make(map[string]string, len(c.Models))
			for provider, selected := range c.Models {
				models[provider] = selected
			}
			c.Models = models
			c.Provider = routeOrProvider
			c.ActiveConnection = ""
		} else {
			return Config{}, fmt.Errorf("unknown or disconnected route %q", routeOrProvider)
		}
	}
	if model != "" {
		c.SetModel(model)
	}
	return c, nil
}

// ConnectedConnections returns saved routes in deterministic display order.
func (c Config) ConnectedConnections() []NamedConnection {
	out := make([]NamedConnection, 0, len(c.Connections))
	for id, connection := range c.Connections {
		out = append(out, NamedConnection{ID: id, Connection: connection})
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].Connection.DisplayName()
		right := out[j].Connection.DisplayName()
		if left == right {
			return out[i].ID < out[j].ID
		}
		return left < right
	})
	return out
}

// FindConnection resolves either a stable route ID or a user-facing route name.
func (c Config) FindConnection(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := c.Connections[value]; ok {
		return value, true
	}
	routes := c.ConnectedConnections()
	for _, route := range routes {
		if strings.EqualFold(route.Connection.DisplayName(), value) {
			return route.ID, true
		}
	}
	var matches []string
	for _, route := range routes {
		if strings.EqualFold(route.Connection.Provider, value) {
			matches = append(matches, route.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	for _, id := range matches {
		if id == c.ActiveConnection {
			return id, true
		}
	}
	return "", false
}

// FindConnectionForModel resolves the route that most recently used a model.
// It is primarily used when loading sessions written before route IDs existed.
func (c Config) FindConnectionForModel(provider, model string) (string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	for id, connection := range c.Connections {
		if connection.Provider == provider && connection.Model == model {
			return id, true
		}
	}
	if id, ok := c.FindConnection(provider); ok {
		return id, true
	}
	return "", false
}

// CredentialForConnection returns the remembered runtime credential.
func (c Config) CredentialForConnection(id string) string {
	return strings.TrimSpace(c.Credentials[strings.ToLower(strings.TrimSpace(id))])
}

func (c *Config) applyConnection(id string) {
	connection, ok := c.Connections[id]
	if !ok {
		return
	}
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	c.Provider = connection.Provider
	if strings.TrimSpace(connection.Model) != "" {
		c.Models[connection.Provider] = connection.Model
	}
	credential := c.CredentialForConnection(id)
	switch connection.Provider {
	case "ollama":
		if connection.BaseURL != "" {
			c.OllamaURL = connection.BaseURL
		}
	case "openai":
		c.OpenAIKey = credential
	case "anthropic":
		c.AnthropicKey = credential
	case "compatible":
		c.CompatibleName = connection.Name
		c.CompatibleURL = connection.BaseURL
		c.CompatibleKey = credential
	}
}

func defaultCredentialEnv(connection SavedConnection) string {
	switch connection.Provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "compatible":
		if preset, ok := Preset(connection.Name); ok && preset.APIKeyEnv != "" {
			return preset.APIKeyEnv
		}
		return DefaultAPIKeyEnv(connection.Name)
	default:
		return ""
	}
}

func loadCredentials(dir string) (map[string]string, error) {
	credentials := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, credentialsFileName))
	if errors.Is(err, os.ErrNotExist) {
		return credentials, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return credentials, nil
	}
	if err := json.Unmarshal(data, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

func saveCredentials(dir string, credentials map[string]string) error {
	if credentials == nil {
		credentials = map[string]string{}
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, credentialsFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, credentialsFileName))
}

func (c *Config) normalize() {
	defaults := Default()
	if !ValidProvider(c.Provider) {
		c.Provider = defaults.Provider
	}
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	for provider, model := range defaults.Models {
		if strings.TrimSpace(c.Models[provider]) == "" {
			c.Models[provider] = model
		}
	}
	if !c.Mode.Valid() {
		c.Mode = defaults.Mode
	}
	if c.Theme != "rose" && c.Theme != "mono" {
		c.Theme = defaults.Theme
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = defaults.MaxTokens
	}
	if c.ContextTokens <= 0 {
		c.ContextTokens = defaults.ContextTokens
	}
	if !ValidApprovalPolicy(c.ApprovalPolicy) {
		c.ApprovalPolicy = defaults.ApprovalPolicy
	}
	c.AgentEnabled = true
	c.WorkspaceRoot = strings.TrimSpace(c.WorkspaceRoot)
	if strings.TrimSpace(c.AutoTestCommand) == "" {
		c.AutoTestCommand = defaults.AutoTestCommand
	}
	if c.MaxToolOutputTokens <= 0 {
		c.MaxToolOutputTokens = defaults.MaxToolOutputTokens
	}
	if c.AgentMaxSteps < 2 {
		c.AgentMaxSteps = defaults.AgentMaxSteps
	}
	if c.AgentMaxSteps > 32 {
		c.AgentMaxSteps = 32
	}
	if c.AgentLoopLimit < 1 {
		c.AgentLoopLimit = defaults.AgentLoopLimit
	}
	if c.AgentLoopLimit > 6 {
		c.AgentLoopLimit = 6
	}
	if c.AgentMaxParallelTools < 1 {
		c.AgentMaxParallelTools = defaults.AgentMaxParallelTools
	}
	if c.AgentMaxParallelTools > 8 {
		c.AgentMaxParallelTools = 8
	}
	if c.AgentToolTimeoutSec < 5 {
		c.AgentToolTimeoutSec = defaults.AgentToolTimeoutSec
	}
	if c.AgentToolTimeoutSec > 900 {
		c.AgentToolTimeoutSec = 900
	}
	if c.AgentReadRetries < 0 {
		c.AgentReadRetries = 0
	}
	if c.AgentReadRetries > 3 {
		c.AgentReadRetries = 3
	}
	if c.AgentContextSummaryTok < 0 {
		c.AgentContextSummaryTok = 0
	}
	if c.AgentContextSummaryTok > 4000 {
		c.AgentContextSummaryTok = 4000
	}
	if c.AgentContextRecall < 0 {
		c.AgentContextRecall = 0
	}
	if c.AgentContextRecall > 64 {
		c.AgentContextRecall = 64
	}
	if c.AgentSnapshotMaxMB < 1 {
		c.AgentSnapshotMaxMB = defaults.AgentSnapshotMaxMB
	}
	if c.AgentSnapshotMaxMB > 4096 {
		c.AgentSnapshotMaxMB = 4096
	}
	switch c.SandboxMode {
	case SandboxNone, SandboxSnapshot, SandboxDocker:
	default:
		c.SandboxMode = defaults.SandboxMode
	}
	if c.ProviderMaxRetries < 0 {
		c.ProviderMaxRetries = 0
	}
	if c.ProviderMaxRetries > 6 {
		c.ProviderMaxRetries = 6
	}
	if c.ProviderRetryBackoffMS < 50 {
		c.ProviderRetryBackoffMS = defaults.ProviderRetryBackoffMS
	}
	if c.ProviderRetryBackoffMS > 10000 {
		c.ProviderRetryBackoffMS = 10000
	}
	if c.AgentTaskTokenBudget < 0 {
		c.AgentTaskTokenBudget = 0
	}
	if c.AgentTaskTokenBudget > 2_000_000 {
		c.AgentTaskTokenBudget = 2_000_000
	}
	if c.CodexBridgeMaxTokens < 512 {
		c.CodexBridgeMaxTokens = defaults.CodexBridgeMaxTokens
	}
	if c.CodexBridgeMaxTokens > 8_000 {
		c.CodexBridgeMaxTokens = 8_000
	}
	c.SubagentProvider = strings.ToLower(strings.TrimSpace(c.SubagentProvider))
	c.SubagentModel = strings.TrimSpace(c.SubagentModel)
	if c.SubagentMaxSteps < 1 {
		c.SubagentMaxSteps = 1
	}
	if c.SubagentMaxSteps > 8 {
		c.SubagentMaxSteps = 8
	}
	if c.SubagentMaxTokens < 500 {
		c.SubagentMaxTokens = 500
	}
	if c.SubagentMaxTokens > 8_000 {
		c.SubagentMaxTokens = 8_000
	}
	c.DirectorProvider = strings.ToLower(strings.TrimSpace(c.DirectorProvider))
	c.DirectorModel = strings.TrimSpace(c.DirectorModel)
	c.InstrumentProvider = strings.ToLower(strings.TrimSpace(c.InstrumentProvider))
	c.InstrumentModel = strings.TrimSpace(c.InstrumentModel)
	if c.InstrumentWeight < 0 {
		c.InstrumentWeight = 0
	}
	if c.InstrumentWeight > 100 {
		c.InstrumentWeight = 100
	}
	if c.DirectorMaxSteps < 4 {
		c.DirectorMaxSteps = 4
	}
	if c.DirectorMaxSteps > 20 {
		c.DirectorMaxSteps = 20
	}
	if c.InstrumentMaxSteps < 1 {
		c.InstrumentMaxSteps = 1
	}
	if c.InstrumentMaxSteps > 6 {
		c.InstrumentMaxSteps = 6
	}
	// Configs written before the advanced agent settings existed have version 0.
	// Apply the new safe defaults once, then preserve explicit false values.
	if c.AgentConfigVersion <= 0 {
		c.AgentAutoVerify = defaults.AgentAutoVerify
		c.AgentAutoReview = defaults.AgentAutoReview
		c.AgentAdaptiveReasoning = defaults.AgentAdaptiveReasoning
		c.ProviderMaxRetries = defaults.ProviderMaxRetries
		c.ProviderRetryBackoffMS = defaults.ProviderRetryBackoffMS
		c.RequireReadBeforeEdit = defaults.RequireReadBeforeEdit
	}
	if c.AgentConfigVersion < 2 {
		c.AgentMaxParallelTools = defaults.AgentMaxParallelTools
		c.AgentToolTimeoutSec = defaults.AgentToolTimeoutSec
		c.AgentReadRetries = defaults.AgentReadRetries
		c.AgentContextSummaryTok = defaults.AgentContextSummaryTok
	}
	if c.AgentConfigVersion < 3 {
		c.AgentAdaptiveReasoning = defaults.AgentAdaptiveReasoning
		c.ProviderMaxRetries = defaults.ProviderMaxRetries
		c.ProviderRetryBackoffMS = defaults.ProviderRetryBackoffMS
		c.AgentTaskTokenBudget = defaults.AgentTaskTokenBudget
	}
	if c.AgentConfigVersion < 4 {
		c.AgentSemanticIndex = defaults.AgentSemanticIndex
		c.AgentSnapshotMaxMB = defaults.AgentSnapshotMaxMB
		c.AgentContextRecall = defaults.AgentContextRecall
		c.SandboxMode = defaults.SandboxMode
	}
	if c.AgentConfigVersion < 5 {
		c.SubagentEnabled = defaults.SubagentEnabled
		c.SubagentMaxSteps = defaults.SubagentMaxSteps
		c.SubagentMaxTokens = defaults.SubagentMaxTokens
		c.InstrumentWeight = defaults.InstrumentWeight
		c.DirectorMaxSteps = defaults.DirectorMaxSteps
		c.InstrumentMaxSteps = defaults.InstrumentMaxSteps
	}
	if c.AgentConfigVersion < 6 {
		// Automatic delegation used to be implicit. Keep it opt-in so the main
		// agent always receives exact file/tool evidence unless the user enables it.
		c.SubagentAutoRoute = defaults.SubagentAutoRoute
	}
	if c.AgentConfigVersion < 7 {
		if c.PluginDirectories == nil {
			c.PluginDirectories = append([]string(nil), defaults.PluginDirectories...)
		}
		if c.PluginManifests == nil {
			c.PluginManifests = append([]string(nil), defaults.PluginManifests...)
		}
		c.AgentConfigVersion = defaults.AgentConfigVersion
	}
	if c.ThemeDensity != "compact" && c.ThemeDensity != "comfortable" {
		c.ThemeDensity = defaults.ThemeDensity
	}
	if strings.TrimSpace(c.OllamaURL) == "" {
		c.OllamaURL = defaults.OllamaURL
	}
	if strings.TrimSpace(c.CompatibleName) == "" {
		c.CompatibleName = defaults.CompatibleName
	}
	if strings.TrimSpace(c.CompatibleURL) == "" {
		c.CompatibleURL = defaults.CompatibleURL
	}
	c.PluginDirectories = normalizeStringList(c.PluginDirectories)
	c.PluginManifests = normalizeStringList(c.PluginManifests)
	if c.MCPServers == nil {
		c.MCPServers = map[string]MCPServerConfig{}
	}
	normalizedMCP := make(map[string]MCPServerConfig, len(c.MCPServers))
	for rawName, server := range c.MCPServers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			continue
		}
		server.Transport = strings.ToLower(strings.TrimSpace(server.Transport))
		server.Command = strings.TrimSpace(server.Command)
		server.Cwd = strings.TrimSpace(server.Cwd)
		server.URL = strings.TrimRight(strings.TrimSpace(server.URL), "/")
		if server.Transport == "" {
			if server.Command != "" {
				server.Transport = "stdio"
			} else if server.URL != "" {
				server.Transport = "http"
			}
		}
		if server.Transport != "stdio" && server.Transport != "http" {
			server.Disabled = true
		}
		if server.Transport == "stdio" && server.Command == "" {
			server.Disabled = true
		}
		if server.Transport == "http" && server.URL == "" {
			server.Disabled = true
		}
		if server.TimeoutSec < 5 {
			server.TimeoutSec = c.AgentToolTimeoutSec
		}
		if server.TimeoutSec > 900 {
			server.TimeoutSec = 900
		}
		if server.Env == nil {
			server.Env = map[string]string{}
		}
		if server.Headers == nil {
			server.Headers = map[string]string{}
		}
		normalizedMCP[name] = server
	}
	c.MCPServers = normalizedMCP
	if c.Credentials == nil {
		c.Credentials = map[string]string{}
	}
	if c.Connections == nil {
		c.Connections = map[string]SavedConnection{}
	}

	// Migrate pre-registry configs into one reusable route.
	legacy := SavedConnection{
		Provider: c.Provider,
		Model:    strings.TrimSpace(c.Models[c.Provider]),
	}
	switch c.Provider {
	case "ollama":
		legacy.BaseURL = c.OllamaURL
	case "openai":
		legacy.APIKeyEnv = "OPENAI_API_KEY"
	case "anthropic":
		legacy.APIKeyEnv = "ANTHROPIC_API_KEY"
	case "compatible":
		legacy.Name = c.CompatibleName
		legacy.BaseURL = c.CompatibleURL
		legacy.APIKeyEnv = defaultCredentialEnv(legacy)
	}
	legacyID := ConnectionID(legacy.Provider, legacy.Name)
	if len(c.Connections) == 0 {
		c.Connections[legacyID] = legacy
	} else if active := strings.ToLower(strings.TrimSpace(c.ActiveConnection)); active != "" && active != legacyID {
		// Preserve compatibility with callers that still assign Provider and the
		// legacy endpoint fields directly before saving.
		c.Connections[legacyID] = legacy
		c.ActiveConnection = legacyID
	}

	for id, connection := range c.Connections {
		connection.Provider = strings.ToLower(strings.TrimSpace(connection.Provider))
		connection.Name = strings.ToLower(strings.TrimSpace(connection.Name))
		connection.BaseURL = strings.TrimRight(strings.TrimSpace(connection.BaseURL), "/")
		connection.Model = strings.TrimSpace(connection.Model)
		if !ValidProvider(connection.Provider) {
			delete(c.Connections, id)
			continue
		}
		if connection.Model == "" {
			connection.Model = c.Models[connection.Provider]
		}
		if connection.APIKeyEnv == "" {
			connection.APIKeyEnv = defaultCredentialEnv(connection)
		}
		normalizedID := ConnectionID(connection.Provider, connection.Name)
		if normalizedID != id {
			delete(c.Connections, id)
		}
		c.Connections[normalizedID] = connection
	}

	active := strings.ToLower(strings.TrimSpace(c.ActiveConnection))
	if _, ok := c.Connections[active]; !ok {
		if _, ok := c.Connections[legacyID]; ok {
			active = legacyID
		} else {
			for id := range c.Connections {
				active = id
				break
			}
		}
	}
	if active == "" {
		c.Connections[legacyID] = legacy
		active = legacyID
	}
	c.ActiveConnection = active
	c.applyConnection(active)
}

func normalizeStringList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
