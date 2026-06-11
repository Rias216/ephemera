package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAI uses OpenAI's official Go SDK.
type OpenAI struct{}

func NewOpenAI() *OpenAI       { return &OpenAI{} }
func (p *OpenAI) Name() string { return "openai" }

func (p *OpenAI) Generate(ctx context.Context, req Request) (string, error) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set")
	}

	client := openai.NewClient(option.WithAPIKey(key))
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	messages = append(messages, openai.DeveloperMessage(req.System))
	for _, message := range req.Messages {
		switch message.Role {
		case "user":
			messages = append(messages, openai.UserMessage(message.Content))
		case "assistant":
			messages = append(messages, openai.AssistantMessage(message.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Messages:            messages,
		Model:               openai.ChatModel(req.Model),
		MaxCompletionTokens: openai.Int(req.MaxTokens),
	}
	// Temperature is intentionally omitted. New reasoning models may reject it;
	// the reasoning mode is already encoded in the developer prompt.
	response, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", err
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("OpenAI returned no choices")
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("OpenAI returned an empty response")
	}
	return text, nil
}
