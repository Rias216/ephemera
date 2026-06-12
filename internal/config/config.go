// Package config owns Ephemera's small, human-readable configuration file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

const (
	fileName            = "config.json"
	credentialsFileName = "credentials.json"
)

// Config contains user preferences and reusable connection metadata. Secrets
// are excluded from config.json and loaded from a separate private file.
type Config struct {
	Provider string            `json:"provider"`
	Models   map[string]string `json:"models"`
	// Connections keeps every route that has completed /connect. The active
	// route is mirrored into the legacy provider fields below so older code and
	// config files continue to work.
	Connections      map[string]SavedConnection `json:"connections,omitempty"`
	ActiveConnection string                     `json:"active_connection,omitempty"`
	Mode             reasoning.Mode             `json:"mode"`
	Theme            string                     `json:"theme"`
	MaxTokens        int64                      `json:"max_tokens"`
	// ContextTokens caps the approximate prompt tokens sent with each request.
	ContextTokens int    `json:"context_tokens"`
	OllamaURL     string `json:"ollama_url"`

	AgentConfigVersion    int            `json:"agent_config_version"`
	AgentEnabled          bool           `json:"agent_enabled"`
	ApprovalPolicy        ApprovalPolicy `json:"approval_policy"`
	WorkspaceRoot         string         `json:"workspace_root,omitempty"`
	AutoTestCommand       string         `json:"auto_test_command,omitempty"`
	MaxToolOutputTokens   int            `json:"max_tool_output_tokens"`
	AgentMaxSteps         int            `json:"agent_max_steps"`
	AgentLoopLimit        int            `json:"agent_loop_limit"`
	AgentAutoVerify       bool           `json:"agent_auto_verify"`
	AgentAutoReview       bool           `json:"agent_auto_review"`
	RequireReadBeforeEdit bool           `json:"require_read_before_edit"`
	ThemeDensity          string         `json:"theme_density"`
	ShowThinking          bool           `json:"show_thinking"`
	ToolDetails           bool           `json:"tool_details"`

	CompatibleName string `json:"compatible_name,omitempty"`
	CompatibleURL  string `json:"compatible_url,omitempty"`

	OpenAIKey     string `json:"-"`
	AnthropicKey  string `json:"-"`
	CompatibleKey string `json:"-"`
	// Credentials are persisted separately with mode 0600 where supported and
	// never serialized into config.json. This is a local file, not OS-keychain
	// encryption.
	Credentials map[string]string `json:"-"`
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
		Provider: "ollama",
		Models: map[string]string{
			"ollama":     "qwen3:8b",
			"openai":     "gpt-4.1-mini",
			"codex":      "gpt-5.5",
			"anthropic":  "claude-sonnet-4-6",
			"compatible": "model-name",
		},
		Mode:                  reasoning.ModeNormal,
		Theme:                 "rose",
		MaxTokens:             4096,
		ContextTokens:         16_000,
		OllamaURL:             "http://localhost:11434",
		AgentConfigVersion:    1,
		AgentEnabled:          true,
		ApprovalPolicy:        ApprovalApproveWrites,
		AutoTestCommand:       "go test ./...",
		MaxToolOutputTokens:   6_000,
		AgentMaxSteps:         10,
		AgentLoopLimit:        2,
		AgentAutoVerify:       true,
		AgentAutoReview:       true,
		RequireReadBeforeEdit: true,
		ThemeDensity:          "comfortable",
		ShowThinking:          true,
		ToolDetails:           true,
		CompatibleName:        "compatible",
		CompatibleURL:         "http://localhost:1234/v1",
		Connections:           map[string]SavedConnection{},
		Credentials:           map[string]string{},
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

// ValidProvider reports whether value names a supported provider type.
func ValidProvider(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, provider := range ProviderNames() {
		if value == provider {
			return true
		}
	}
	return false
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
	return cfg, nil
}

// Save atomically writes config.json and the separate local credential file.
// API keys carry json:"-" tags and are never embedded in config.json.
func Save(cfg Config) error {
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
	// Configs written before the advanced agent settings existed have version 0.
	// Apply the new safe defaults once, then preserve explicit false values.
	if c.AgentConfigVersion <= 0 {
		c.AgentAutoVerify = defaults.AgentAutoVerify
		c.AgentAutoReview = defaults.AgentAutoReview
		c.RequireReadBeforeEdit = defaults.RequireReadBeforeEdit
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
