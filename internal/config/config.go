// Package config owns Ephemera's small, human-readable configuration file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

const fileName = "config.json"

// Config contains user preferences and connection metadata. API keys entered
// through /connect are runtime-only and deliberately excluded from JSON.
type Config struct {
	Provider  string            `json:"provider"`
	Models    map[string]string `json:"models"`
	Mode      reasoning.Mode    `json:"mode"`
	Theme     string            `json:"theme"`
	MaxTokens int64             `json:"max_tokens"`
	// ContextTokens caps the approximate prompt tokens sent with each request.
	ContextTokens int    `json:"context_tokens"`
	OllamaURL     string `json:"ollama_url"`

	AgentEnabled        bool           `json:"agent_enabled"`
	ApprovalPolicy      ApprovalPolicy `json:"approval_policy"`
	WorkspaceRoot       string         `json:"workspace_root,omitempty"`
	AutoTestCommand     string         `json:"auto_test_command,omitempty"`
	MaxToolOutputTokens int            `json:"max_tool_output_tokens"`
	ThemeDensity        string         `json:"theme_density"`
	ShowThinking        bool           `json:"show_thinking"`
	ToolDetails         bool           `json:"tool_details"`

	CompatibleName string `json:"compatible_name,omitempty"`
	CompatibleURL  string `json:"compatible_url,omitempty"`

	OpenAIKey     string `json:"-"`
	AnthropicKey  string `json:"-"`
	CompatibleKey string `json:"-"`
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
	return Config{
		Provider: "ollama",
		Models: map[string]string{
			"ollama":     "qwen3:8b",
			"openai":     "gpt-4.1-mini",
			"codex":      "gpt-5.5",
			"anthropic":  "claude-sonnet-4-6",
			"compatible": "model-name",
		},
		Mode:                reasoning.ModeNormal,
		Theme:               "rose",
		MaxTokens:           4096,
		ContextTokens:       16_000,
		OllamaURL:           "http://localhost:11434",
		AgentEnabled:        true,
		ApprovalPolicy:      ApprovalApproveWrites,
		AutoTestCommand:     "go test ./...",
		MaxToolOutputTokens: 6_000,
		ThemeDensity:        "comfortable",
		ShowThinking:        true,
		ToolDetails:         true,
		CompatibleName:      "compatible",
		CompatibleURL:       "http://localhost:1234/v1",
	}
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
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	cfg.normalize()
	return cfg, nil
}

// Save atomically writes the configuration with private permissions. Runtime
// API keys carry json:"-" tags and are never written to disk.
func Save(cfg Config) error {
	cfg.normalize()
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := filepath.Join(dir, fileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, fileName))
}

// Model returns the selected model for the active provider.
func (c Config) Model() string {
	return c.Models[c.Provider]
}

// SetModel changes the model for the active provider.
func (c *Config) SetModel(model string) {
	if c.Models == nil {
		c.Models = map[string]string{}
	}
	c.Models[c.Provider] = model
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
}
