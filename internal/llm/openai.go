package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAI drives both OpenAI itself and OpenAI-compatible chat completion APIs.
type OpenAI struct {
	name      string
	apiKey    string
	apiKeyEnv string
	baseURL   string
}

func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{name: "openai", apiKey: apiKey}
}

func NewOpenAICompatible(name, baseURL, apiKey string) *OpenAI {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "compatible"
	}
	return &OpenAI{name: name, apiKey: apiKey, apiKeyEnv: config.DefaultAPIKeyEnv(name), baseURL: strings.TrimSpace(baseURL)}
}

func (p *OpenAI) Name() string { return p.name }

func (p *OpenAI) Generate(ctx context.Context, req Request) (string, error) {
	key, err := p.resolvedAPIKey()
	if err != nil {
		return "", err
	}

	opts := []option.RequestOption{option.WithAPIKey(key)}
	if p.baseURL != "" {
		opts = append(opts, option.WithBaseURL(p.baseURL))
	}
	client := openai.NewClient(opts...)

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	// System messages are understood by OpenAI and by the broadest set of
	// OpenAI-compatible servers.
	messages = append(messages, openai.SystemMessage(req.System))
	for _, message := range req.Messages {
		switch message.Role {
		case "user":
			messages = append(messages, openai.UserMessage(message.Content))
		case "assistant":
			messages = append(messages, openai.AssistantMessage(message.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    openai.ChatModel(req.Model),
	}
	var response *openai.ChatCompletion
	if p.baseURL == "" {
		params.MaxCompletionTokens = openai.Int(req.MaxTokens)
		response, err = client.Chat.Completions.New(ctx, params)
	} else {
		// max_tokens is still the most portable field across compatible APIs.
		response, err = client.Chat.Completions.New(
			ctx,
			params,
			option.WithJSONSet("max_tokens", req.MaxTokens),
			option.WithJSONSet("temperature", req.Temperature),
		)
	}
	if err != nil {
		if p.baseURL == "" && strings.Contains(err.Error(), "insufficient_quota") {
			return "", fmt.Errorf("%w\n\nOpenAI API billing is separate from ChatGPT Plus/Pro subscriptions. A valid API key can still return insufficient_quota until API billing or credits are enabled. Enable billing at platform.openai.com, or connect a different backend with /connect openrouter, /connect groq, or /connect ollama.", err)
		}
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("%s returned no choices", p.Name())
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("%s returned an empty response", p.Name())
	}
	return text, nil
}

func (p *OpenAI) GenerateWithTools(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	if len(specs) == 0 {
		text, err := p.GenerateStream(ctx, req, onDelta)
		return ToolDecision{Text: text}, err
	}
	if p.baseURL == "" {
		return p.generateResponsesStream(ctx, req, specs, onDelta)
	}
	return p.generateChatCompletionsStream(ctx, req, specs, onDelta)
}

func (p *OpenAI) resolvedAPIKey() (string, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		if p.baseURL == "" {
			key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		} else {
			key = firstNonEmpty(os.Getenv(p.apiKeyEnv), os.Getenv("EPHEMERA_API_KEY"))
		}
	}
	if key == "" && p.baseURL == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set; run /connect openai")
	}
	if key == "" {
		// Local OpenAI-compatible servers commonly require no authentication.
		// Most ignore this placeholder header.
		key = "not-needed"
	}
	return key, nil
}

func openAIWireMessages(req Request) []map[string]string {
	messages := make([]map[string]string, 0, len(req.Messages)+1)
	messages = append(messages, map[string]string{"role": "system", "content": req.System})
	for _, message := range req.Messages {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
		}
	}
	return messages
}

func openAIWireTools(specs []ToolSpec) []map[string]any {
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        spec.Name,
				"description": spec.Description,
				"parameters":  spec.Parameters,
			},
		})
	}
	return tools
}
