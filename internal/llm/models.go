package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
)

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
	case "codex":
		return ListCodexModels()
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

	var body json.RawMessage
	if err := doJSON(req, &body); err != nil {
		return nil, err
	}
	models, err := modelIDsFromPayload(body)
	if err != nil {
		return nil, fmt.Errorf("%s model catalog: %w", req.URL.Host, err)
	}
	return cleanModelIDs(models), nil
}

func listAnthropicModels(ctx context.Context, apiKey string) ([]string, error) {
	const endpoint = "https://api.anthropic.com/v1/models"
	models := make([]string, 0, 32)
	afterID := ""
	for page := 0; page < 20; page++ {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		query := parsed.Query()
		query.Set("limit", "1000")
		if afterID != "" {
			query.Set("after_id", afterID)
		}
		parsed.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
		req.Header.Set("anthropic-version", "2023-06-01")

		var body struct {
			Data    []modelListItem `json:"data"`
			HasMore bool            `json:"has_more"`
			LastID  string          `json:"last_id"`
		}
		if err := doJSON(req, &body); err != nil {
			return nil, err
		}
		for _, item := range body.Data {
			models = append(models, item.value())
		}
		if !body.HasMore {
			break
		}
		next := strings.TrimSpace(body.LastID)
		if next == "" || next == afterID {
			return nil, fmt.Errorf("Anthropic model catalog pagination did not advance")
		}
		afterID = next
	}
	models = cleanModelIDs(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("Anthropic model catalog contained no model IDs")
	}
	return models, nil
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
		Models []modelListItem `json:"models"`
	}
	if err := doJSON(req, &body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Models))
	for _, item := range body.Models {
		models = append(models, item.value())
	}
	models = cleanModelIDs(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("Ollama returned an empty model catalog")
	}
	return models, nil
}

func doJSON(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerHTTPError(req.URL.Host, resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s returned invalid JSON: %w", req.URL.Host, err)
	}
	return nil
}

type modelListItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func (m modelListItem) value() string {
	return firstNonEmpty(m.ID, m.Name, m.Model)
}

func modelIDsFromPayload(raw json.RawMessage) ([]string, error) {
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		for _, collection := range []json.RawMessage{envelope.Data, envelope.Models} {
			if models := modelIDsFromCollection(collection); len(models) > 0 {
				return models, nil
			}
		}
	}
	if models := modelIDsFromCollection(raw); len(models) > 0 {
		return models, nil
	}

	return nil, fmt.Errorf("response contained no model IDs")
}

func modelIDsFromCollection(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []modelListItem
	if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
		models := make([]string, 0, len(items))
		for _, item := range items {
			models = append(models, item.value())
		}
		models = cleanModelIDs(models)
		if len(models) > 0 {
			return models
		}
	}

	var stringsOnly []string
	if err := json.Unmarshal(raw, &stringsOnly); err == nil {
		stringsOnly = cleanModelIDs(stringsOnly)
		if len(stringsOnly) > 0 {
			return stringsOnly
		}
	}
	return nil
}

func providerHTTPError(host string, resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	message := strings.TrimSpace(string(data))
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if len(data) > 0 && json.Unmarshal(data, &body) == nil {
		message = firstNonEmpty(body.Error.Message, body.Message, message)
	}
	if message == "" {
		return fmt.Errorf("%s returned %s", host, resp.Status)
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 240 {
		message = message[:237] + "..."
	}
	return fmt.Errorf("%s returned %s: %s", host, resp.Status, message)
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
