package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
)

var knownModels = map[string][]string{
	"openai": {
		"gpt-4.1-mini",
	},
	"anthropic": {
		"claude-opus-4-8",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	},
}

// KnownModelIDs returns curated model IDs used when a live catalog is not
// reachable. Live provider results still take precedence.
func KnownModelIDs(provider string) []string {
	models := knownModels[strings.ToLower(strings.TrimSpace(provider))]
	out := make([]string, len(models))
	copy(out, models)
	return out
}

// ListModels asks the active provider for its currently available model IDs.
func ListModels(ctx context.Context, cfg config.Config) ([]string, error) {
	switch cfg.Provider {
	case "ollama":
		return listOllamaModels(ctx, cfg.OllamaURL)
	case "openai":
		key := firstNonEmpty(cfg.OpenAIKey, os.Getenv("OPENAI_API_KEY"))
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		return listOpenAICompatibleModels(ctx, "https://api.openai.com/v1", key)
	case "anthropic":
		key := firstNonEmpty(cfg.AnthropicKey, os.Getenv("ANTHROPIC_API_KEY"))
		if key == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
		}
		return listAnthropicModels(ctx, key)
	case "compatible":
		key := compatibleAPIKey(cfg.CompatibleName, cfg.CompatibleKey)
		return listOpenAICompatibleModels(ctx, cfg.CompatibleURL, key)
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}

func compatibleAPIKey(name, explicit string) string {
	return firstNonEmpty(
		explicit,
		os.Getenv(config.DefaultAPIKeyEnv(name)),
		os.Getenv("EPHEMERA_API_KEY"),
	)
}

func listOpenAICompatibleModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint, err := joinEndpoint(baseURL, "models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := doJSON(req, &body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Data))
	for _, item := range body.Data {
		models = append(models, item.ID)
	}
	return cleanModelIDs(models), nil
}

func listAnthropicModels(ctx context.Context, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
	req.Header.Set("anthropic-version", "2023-06-01")

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := doJSON(req, &body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Data))
	for _, item := range body.Data {
		models = append(models, item.ID)
	}
	return cleanModelIDs(models), nil
}

func listOllamaModels(ctx context.Context, baseURL string) ([]string, error) {
	endpoint, err := joinEndpoint(baseURL, "api/tags")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := doJSON(req, &body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Models))
	for _, item := range body.Models {
		models = append(models, item.Name)
	}
	return cleanModelIDs(models), nil
}

func doJSON(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s", req.URL.Host, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func joinEndpoint(baseURL, path string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("base URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("base URL must use http:// or https://")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func cleanModelIDs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
