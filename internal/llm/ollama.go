package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	ollama "github.com/ollama/ollama/api"
)

// Ollama uses the official Go client shipped in the Ollama repository.
type Ollama struct {
	baseURL string
	client  *http.Client
}

func NewOllama(baseURL string) *Ollama {
	if env := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); env != "" {
		baseURL = env
	}
	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

func (p *Ollama) Name() string { return "ollama" }

func (p *Ollama) Generate(ctx context.Context, req Request) (string, error) {
	base, err := url.Parse(p.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		if err == nil {
			err = fmt.Errorf("URL must include scheme and host")
		}
		return "", fmt.Errorf("invalid Ollama URL %q: %w", p.baseURL, err)
	}
	client := ollama.NewClient(base, p.client)

	messages := make([]ollama.Message, 0, len(req.Messages)+1)
	messages = append(messages, ollama.Message{Role: "system", Content: req.System})
	for _, message := range req.Messages {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, ollama.Message{
				Role:    message.Role,
				Content: message.Content,
			})
		}
	}

	stream := false
	request := &ollama.ChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   &stream,
		Options: map[string]interface{}{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
		},
	}

	var out strings.Builder
	if err := client.Chat(ctx, request, func(response ollama.ChatResponse) error {
		out.WriteString(response.Message.Content)
		return nil
	}); err != nil {
		return "", fmt.Errorf("Ollama request failed: %w", err)
	}

	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("Ollama returned an empty response")
	}
	return text, nil
}

func (p *Ollama) GenerateWithTools(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	return p.generateChatStream(ctx, req, specs, onDelta)
}

func (p *Ollama) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Streaming: true, NativeTools: true}
}

func ollamaWireTools(specs []ToolSpec) ollama.Tools {
	out := make(ollama.Tools, 0, len(specs))
	for _, spec := range specs {
		tool := ollama.Tool{Type: "function"}
		tool.Function.Name = spec.Name
		tool.Function.Description = spec.Description
		tool.Function.Parameters.Type = firstNonEmpty(spec.Parameters.Type, "object")
		tool.Function.Parameters.Required = append([]string(nil), spec.Parameters.Required...)
		tool.Function.Parameters.Properties = make(map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum,omitempty"`
		}, len(spec.Parameters.Properties))
		for name, property := range spec.Parameters.Properties {
			tool.Function.Parameters.Properties[name] = struct {
				Type        string   `json:"type"`
				Description string   `json:"description"`
				Enum        []string `json:"enum,omitempty"`
			}{
				Type:        property.Type,
				Description: property.Description,
			}
		}
		out = append(out, tool)
	}
	return out
}
