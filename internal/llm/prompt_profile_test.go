package llm

import (
	"context"
	"testing"
)

type profileProvider struct {
	name string
	caps ProviderCapabilities
}

func (p profileProvider) Name() string                                      { return p.name }
func (p profileProvider) Generate(context.Context, Request) (string, error) { return "", nil }
func (p profileProvider) Capabilities() ProviderCapabilities                { return p.caps }

func TestProfileForNegotiatedToolFormats(t *testing.T) {
	tests := []struct {
		format  string
		name    string
		want    string
		compact bool
	}{
		{format: "openai", name: "compatible", want: "openai"},
		{format: "anthropic", name: "anthropic", want: "anthropic"},
		{format: "ollama", name: "ollama", want: "ollama", compact: true},
		{format: "text", name: "custom", want: "portable"},
	}
	for _, tt := range tests {
		profile := ProfileFor(profileProvider{name: tt.name, caps: ProviderCapabilities{ToolCallFormat: tt.format}})
		if profile.Name != tt.want || profile.Compact != tt.compact {
			t.Fatalf("format %q profile = %#v, want name=%q compact=%t", tt.format, profile, tt.want, tt.compact)
		}
		if profile.SystemGuidance == "" || profile.ReasoningGuidance == "" {
			t.Fatalf("format %q returned incomplete profile: %#v", tt.format, profile)
		}
	}
}
