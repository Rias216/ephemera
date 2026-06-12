package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var fallbackCodexModels = []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}

// Codex uses the local Codex ChatGPT login instead of an OpenAI API key.
type Codex struct{}

func NewCodex() *Codex {
	return &Codex{}
}

func (p *Codex) Name() string { return "codex" }

func (p *Codex) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Streaming:         true,
		SupportsReasoning: true,
		MaxParallelTools:  1,
		ToolCallFormat:    "text",
		StreamingFormat:   "process",
	}
}

func (p *Codex) Generate(ctx context.Context, req Request) (string, error) {
	if _, err := loadCodexAccessToken(); err != nil {
		return "", err
	}
	exe, err := codexExecutable()
	if err != nil {
		return "", err
	}

	output, err := os.CreateTemp("", "ephemera-codex-*.txt")
	if err != nil {
		return "", err
	}
	outputPath := output.Name()
	_ = output.Close()
	defer os.Remove(outputPath)

	args := []string{
		"exec",
		"--model", req.Model,
		"--sandbox", "read-only",
		"--ephemeral",
		"--skip-git-repo-check",
		"--color", "never",
		"--output-last-message", outputPath,
		"-",
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdin = strings.NewReader(codexPrompt(req))
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codex exec failed: %w\n\n%s", err, trimCommandOutput(data))
	}
	result, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("codex did not write a final response: %w", err)
	}
	text := strings.TrimSpace(string(result))
	if text == "" {
		return "", fmt.Errorf("codex returned an empty response")
	}
	return text, nil
}

func ListCodexModels() ([]string, error) {
	if models, err := cachedCodexModels(); err == nil && len(models) > 0 {
		return models, nil
	}
	return append([]string(nil), fallbackCodexModels...), nil
}

func cachedCodexModels() ([]string, error) {
	data, err := os.ReadFile(filepath.Join(codexHome(), "models_cache.json"))
	if err != nil {
		return nil, err
	}
	var cache struct {
		Models []struct {
			Slug           string `json:"slug"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(cache.Models))
	for _, model := range cache.Models {
		if strings.TrimSpace(model.Slug) == "" || model.Visibility != "list" {
			continue
		}
		if !model.SupportedInAPI {
			continue
		}
		models = append(models, model.Slug)
	}
	models = cleanModelIDs(models)
	sort.SliceStable(models, func(i, j int) bool {
		return codexModelRank(models[i]) < codexModelRank(models[j])
	})
	return models, nil
}

func codexModelRank(model string) int {
	for i, preferred := range fallbackCodexModels {
		if model == preferred {
			return i
		}
	}
	return len(fallbackCodexModels) + 1
}

func codexHome() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func loadCodexAccessToken() (string, error) {
	path := strings.TrimSpace(os.Getenv("EPHEMERA_CODEX_AUTH_FILE"))
	if path == "" {
		path = filepath.Join(codexHome(), "auth.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Codex ChatGPT login not found; open Codex or run its login flow, then retry /connect codex")
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("Codex auth file is unreadable; open Codex and sign in again")
	}
	if strings.ToLower(strings.TrimSpace(auth.AuthMode)) != "chatgpt" {
		return "", fmt.Errorf("Codex is not signed in with ChatGPT; open Codex and choose ChatGPT login")
	}
	token := strings.TrimSpace(auth.Tokens.AccessToken)
	if token == "" {
		return "", fmt.Errorf("Codex ChatGPT access token is missing; open Codex and sign in again")
	}
	return token, nil
}

func codexExecutable() (string, error) {
	for _, value := range []string{
		os.Getenv("EPHEMERA_CODEX_CLI"),
		os.Getenv("CODEX_CLI_PATH"),
		codexCLIPathFromConfig(),
		"codex",
	} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("Codex CLI was not found")
}

func codexCLIPathFromConfig() string {
	data, err := os.ReadFile(filepath.Join(codexHome(), "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CODEX_CLI_PATH") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func codexPrompt(req Request) string {
	var b strings.Builder
	if strings.TrimSpace(req.System) != "" {
		b.WriteString("System instructions:\n")
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	b.WriteString("Conversation so far:\n")
	for _, message := range req.Messages {
		b.WriteString(strings.ToUpper(message.Role))
		b.WriteString(":\n")
		b.WriteString(message.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply only with the assistant's next response.")
	return b.String()
}

func trimCommandOutput(data []byte) string {
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 600 {
		return text[:597] + "..."
	}
	return text
}
