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
	OllamaURL string            `json:"ollama_url"`

	CompatibleName string `json:"compatible_name,omitempty"`
	CompatibleURL  string `json:"compatible_url,omitempty"`

	OpenAIKey     string `json:"-"`
	AnthropicKey  string `json:"-"`
	CompatibleKey string `json:"-"`
}

// Default returns a useful local-first configuration.
func Default() Config {
	return Config{
		Provider: "ollama",
		Models: map[string]string{
			"ollama":     "qwen3:8b",
			"openai":     "gpt-5.4-mini",
			"anthropic":  "claude-sonnet-4-6",
			"compatible": "model-name",
		},
		Mode:           reasoning.ModeNormal,
		Theme:          "rose",
		MaxTokens:      4096,
		OllamaURL:      "http://localhost:11434",
		CompatibleName: "compatible",
		CompatibleURL:  "http://localhost:1234/v1",
	}
}

// ProviderNames returns all provider types understood by Ephemera.
func ProviderNames() []string {
	return []string{"ollama", "openai", "anthropic", "compatible"}
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
