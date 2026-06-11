package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAI drives both OpenAI itself and OpenAI-compatible chat completion APIs.
type OpenAI struct {
	name    string
	apiKey  string
	baseURL string
}

func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{name: "openai", apiKey: apiKey}
}

func NewOpenAICompatible(name, baseURL, apiKey string) *OpenAI {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "compatible"
	}
	return &OpenAI{name: name, apiKey: apiKey, baseURL: strings.TrimSpace(baseURL)}
}

func (p *OpenAI) Name() string { return p.name }

func (p *OpenAI) Generate(ctx context.Context, req Request) (string, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		if p.baseURL == "" {
			key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		} else {
			key = strings.TrimSpace(os.Getenv("EPHEMERA_API_KEY"))
		}
	}
	if key == "" && p.baseURL == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set; run /connect openai")
	}
	// Local OpenAI-compatible servers commonly require no authentication, but
	// the SDK expects a value. Most such servers ignore this placeholder header.
	if key == "" {
		key = "not-needed"
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
	var err error
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
