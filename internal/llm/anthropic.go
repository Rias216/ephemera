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
type Anthropic struct {
	apiKey string
}

func NewAnthropic(apiKey string) *Anthropic { return &Anthropic{apiKey: apiKey} }
func (p *Anthropic) Name() string           { return "anthropic" }

func (p *Anthropic) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Streaming: true, NativeTools: true, SupportsVision: true, SupportsReasoning: true, MaxParallelTools: 8, ToolCallFormat: "anthropic", StreamingFormat: "sse"}
}

func (p *Anthropic) Generate(ctx context.Context, req Request) (string, error) {
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not set; run /connect anthropic")
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

func (p *Anthropic) GenerateWithTools(ctx context.Context, req Request, specs []ToolSpec, onDelta DeltaFunc) (ToolDecision, error) {
	return p.generateMessageStream(ctx, req, specs, onDelta)
}

func anthropicWireTools(specs []ToolSpec) []anthropic.ToolUnionParam {
	tools := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, spec := range specs {
		param := anthropic.ToolParam{
			Name:        spec.Name,
			Description: anthropic.String(spec.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: anthropicToolProperties(spec.Parameters.Properties),
				Required:   append([]string(nil), spec.Parameters.Required...),
			},
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &param})
	}
	return tools
}

func anthropicToolProperties(properties map[string]ToolProperty) map[string]map[string]string {
	out := make(map[string]map[string]string, len(properties))
	for name, property := range properties {
		out[name] = map[string]string{
			"type":        property.Type,
			"description": property.Description,
		}
	}
	return out
}

func anthropicToolDecision(content []anthropic.ContentBlockUnion, onDelta DeltaFunc) (ToolDecision, error) {
	var text strings.Builder
	var calls []ToolCall
	for _, block := range content {
		switch value := block.AsAny().(type) {
		case anthropic.TextBlock:
			text.WriteString(value.Text)
		case anthropic.ToolUseBlock:
			args, _, decodeErr := decodeRawToolArguments(value.Input)
			if decodeErr != nil {
				return ToolDecision{}, newToolProtocolError("Anthropic", value.Name, string(value.Input), decodeErr)
			}
			calls = append(calls, ToolCall{ID: value.ID, Name: value.Name, Arguments: args})
		}
	}
	visible := strings.TrimSpace(text.String())
	if onDelta != nil && visible != "" {
		if err := onDelta(Delta{Kind: DeltaText, Text: visible}); err != nil {
			return ToolDecision{}, err
		}
	}
	if visible == "" && len(calls) == 0 {
		return ToolDecision{}, fmt.Errorf("Anthropic returned an empty tool decision")
	}
	return ToolDecision{Text: visible, ToolCalls: calls, Transport: ToolTransportNative}, nil
}
