package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func providerSpecificTaxonomy(provider string, err error) ErrorTaxonomy {
	result := ClassifyError(noClassifierProvider(provider), err)
	result.Provider = provider
	text := ""
	if err != nil {
		text = strings.ToLower(err.Error())
	}
	if provider == "anthropic" && (strings.Contains(text, "overloaded_error") || strings.Contains(text, "529")) {
		result.Code, result.Class, result.Retryable, result.Backoff = "overloaded", "overloaded", true, 3*time.Second
	}
	if provider == "ollama" && strings.Contains(text, "connection refused") {
		result.Code, result.Class, result.Retryable, result.Backoff = "daemon_unavailable", "transient", true, time.Second
	}
	if provider == "codex" && strings.Contains(text, "read-only") {
		result.Code, result.Class = "bridge_read_only", "permanent"
	}
	return result
}

type noClassifierProvider string

func (p noClassifierProvider) Name() string                                      { return string(p) }
func (p noClassifierProvider) Generate(context.Context, Request) (string, error) { return "", nil }

func (p *OpenAI) ClassifyError(err error) ErrorTaxonomy {
	return providerSpecificTaxonomy(p.Name(), err)
}
func (p *Anthropic) ClassifyError(err error) ErrorTaxonomy {
	return providerSpecificTaxonomy(p.Name(), err)
}
func (p *Ollama) ClassifyError(err error) ErrorTaxonomy {
	return providerSpecificTaxonomy(p.Name(), err)
}
func (p *Codex) ClassifyError(err error) ErrorTaxonomy {
	return providerSpecificTaxonomy(p.Name(), err)
}

func (p *OpenAI) HealthCheck(context.Context) error {
	_, err := p.resolvedAPIKey()
	return err
}

func (p *Anthropic) HealthCheck(context.Context) error {
	if strings.TrimSpace(p.apiKey) == "" && strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	return nil
}

func (p *Ollama) HealthCheck(ctx context.Context) error {
	base, err := url.Parse(p.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("invalid Ollama URL %q", p.baseURL)
	}
	endpoint := strings.TrimRight(p.baseURL, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Ollama health check returned %s", resp.Status)
	}
	return nil
}

func (p *Codex) HealthCheck(context.Context) error {
	if _, err := loadCodexAccessToken(); err != nil {
		return err
	}
	_, err := codexExecutable()
	return err
}

func containsAny(text string, markers ...string) bool {
	text = strings.ToLower(text)
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (p *OpenAI) IsNativeToolCompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"tool_choice", "tool choice", "tools is not supported", "tools are not supported",
		"function calling", "function_call", "tool calls are not supported",
		"invalid tool schema", "unsupported schema", "invalid function schema",
		"does not support tools", "doesn't support tools", "unknown field \"tools\"",
	)
}

func (p *Anthropic) IsNativeToolCompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"tools: extra inputs are not permitted", "invalid tool schema",
		"tool_use is not supported", "tools are not supported",
	)
}

func (p *Ollama) IsNativeToolCompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"does not support tools", "does not support tool calling", "tools are not supported",
	)
}

func (p *Codex) IsNativeToolCompatibilityError(error) bool { return false }
