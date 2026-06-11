// Package config owns Ephemera's small, human-readable configuration file.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ephemera-ai/ephemera/internal/reasoning"
)

const fileName = "config.json"

// Config contains non-secret preferences. API keys are intentionally read only
// from environment variables and are never written to disk.
type Config struct {
	Provider  string            `json:"provider"`
	Models    map[string]string `json:"models"`
	Mode      reasoning.Mode    `json:"mode"`
	Theme     string            `json:"theme"`
	MaxTokens int64             `json:"max_tokens"`
	OllamaURL string            `json:"ollama_url"`
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
		Mode:      reasoning.ModeNormal,
		Theme:     "rose",
		MaxTokens: 4096,
		OllamaURL: "http://localhost:11434",
	}
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

func (c *Config) normalize() {
	defaults := Default()
	if _, supported := defaults.Models[c.Provider]; !supported {
		c.Provider = defaults.Provider
	}
	if c.Models == nil {
		c.Models = defaults.Models
	}
	for provider, model := range defaults.Models {
		if c.Models[provider] == "" {
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
	if c.OllamaURL == "" {
		c.OllamaURL = defaults.OllamaURL
	}
}
