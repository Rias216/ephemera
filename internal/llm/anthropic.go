package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic uses Anthropic's official Go SDK.
type Anthropic struct{}

func NewAnthropic() *Anthropic    { return &Anthropic{} }
func (p *Anthropic) Name() string { return "anthropic" }

func (p *Anthropic) Generate(ctx context.Context, req Request) (string, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}

	client := anthropic.NewClient(option.WithAPIKey(key))
	messages := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, message := range req.Messages {
		block := anthropic.NewTextBlock(message.Content)
		switch message.Role {
		case "user":
			messages = append(messages, anthropic.NewUserMessage(block))
		case "assistant":
			messages = append(messages, anthropic.NewAssistantMessage(block))
		}
	}

	response, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: req.System}},
		Messages:  messages,
	})
	if err != nil {
		return "", err
	}

	var out strings.Builder
	for _, block := range response.Content {
		// Deliberately extract only final text blocks. Thinking blocks, when a model
		// produces them, remain private and are not displayed or persisted.
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("Anthropic returned no text content")
	}
	return text, nil
}
