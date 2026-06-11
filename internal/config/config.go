// Package config owns Ephemera's small, human-readable configuration file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

const (
	fileName = "config.json"

	ProtocolOllama           = "ollama"
	ProtocolOpenAI           = "openai"
	ProtocolAnthropic        = "anthropic"
	ProtocolOpenAICompatible = "openai-compatible"

	DefaultOllamaURL = "http://localhost:11434"
	NVIDIABaseURL    = "https://integrate.api.nvidia.com/v1"
)

// Connection describes how to reach a provider. It contains metadata only;
// API keys are read from the environment or supplied in memory by /connect.
type Connection struct {
	Protocol  string `json:"protocol"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// Config contains non-secret preferences. API keys are intentionally read only
// from environment variables or held in memory and are never written to disk.
type Config struct {
	Provider    string                `json:"provider"`
	Models      map[string]string     `json:"models"`
	Connections map[string]Connection `json:"connections,omitempty"`
	Mode        reasoning.Mode        `json:"mode"`
	Theme       string                `json:"theme"`
	MaxTokens   int64                 `json:"max_tokens"`
	OllamaURL   string                `json:"ollama_url"` // retained for older config files
}

// Default returns a useful local-first configuration.
func Default() Config {
	return Config{
		Provider: "ollama",
		Models: map[string]string{
			"ollama":    "qwen3:8b",
			"openai":    "gpt-5.4-mini",
			"anthropic": "claude-sonnet-4-6",
		},
		Connections: map[string]Connection{
			"ollama": {
				Protocol: ProtocolOllama,
				BaseURL:  DefaultOllamaURL,
			},
			"openai": {
				Protocol:  ProtocolOpenAI,
				APIKeyEnv: "OPENAI_API_KEY",
			},
			"anthropic": {
				Protocol:  ProtocolAnthropic,
				APIKeyEnv: "ANTHROPIC_API_KEY",
			},
			"nvidia": {
				Protocol:  ProtocolOpenAICompatible,
				BaseURL:   NVIDIABaseURL,
				APIKeyEnv: "NVIDIA_API_KEY",
			},
		},
		Mode:      reasoning.ModeNormal,
		Theme:     "rose",
		MaxTokens: 4096,
		OllamaURL: DefaultOllamaURL,
	}
}

// Preset returns built-in connection metadata for a known provider.
func Preset(provider string) (Connection, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	connection, ok := Default().Connections[provider]
	return connection, ok
}

// DefaultAPIKeyEnv returns a predictable environment variable for a provider.
func DefaultAPIKeyEnv(provider string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(provider)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "PROVIDER"
	}
	return name + "_API_KEY"
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

// Save atomically writes the configuration with private permissions.
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

// Connection returns metadata for a configured provider.
func (c Config) Connection(provider string) (Connection, bool) {
	connection, ok := c.Connections[strings.ToLower(strings.TrimSpace(provider))]
	return connection, ok
}

// ActiveConnection returns metadata for the selected provider.
func (c Config) ActiveConnection() (Connection, bool) {
	return c.Connection(c.Provider)
}

// SetConnection adds or replaces a provider without storing its credential.
func (c *Config) SetConnection(provider string, connection Connection) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if c.Connections == nil {
		c.Connections = map[string]Connection{}
	}
	c.Connections[provider] = connection
}

func (c *Config) normalize() {
	defaults := Default()

	if c.Models == nil {
		c.Models = map[string]string{}
	}
	for provider, model := range defaults.Models {
		if c.Models[provider] == "" {
			c.Models[provider] = model
		}
	}

	if c.Connections == nil {
		c.Connections = map[string]Connection{}
	}
	_, hadOllamaConnection := c.Connections["ollama"]
	for provider, connection := range defaults.Connections {
		if _, exists := c.Connections[provider]; !exists {
			c.Connections[provider] = connection
		}
	}

	if c.OllamaURL == "" {
		c.OllamaURL = defaults.OllamaURL
	}
	ollama := c.Connections["ollama"]
	if !hadOllamaConnection {
		ollama.BaseURL = c.OllamaURL
	}
	if ollama.Protocol == "" {
		ollama.Protocol = ProtocolOllama
	}
	if ollama.BaseURL == "" {
		ollama.BaseURL = c.OllamaURL
	}
	c.OllamaURL = ollama.BaseURL
	c.Connections["ollama"] = ollama

	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = defaults.Provider
	}
	if connection, ok := c.Connections[c.Provider]; !ok || !validProtocol(connection.Protocol) {
		c.Provider = defaults.Provider
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
}

func validProtocol(protocol string) bool {
	switch protocol {
	case ProtocolOllama, ProtocolOpenAI, ProtocolAnthropic, ProtocolOpenAICompatible:
		return true
	default:
		return false
	}
}
