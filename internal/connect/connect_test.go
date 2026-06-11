package connect

import (
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestParseOpenAI(t *testing.T) {
	t.Parallel()

	req, err := Parse([]string{"openai", "sk-test", "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Provider != "openai" || req.Model != "gpt-test" || req.APIKey != "sk-test" {
		t.Fatalf("unexpected request: %#v", req)
	}
	if req.Connection.Protocol != config.ProtocolOpenAI {
		t.Fatalf("protocol = %q", req.Connection.Protocol)
	}
}

func TestParseNVIDIA(t *testing.T) {
	t.Parallel()

	req, err := Parse([]string{"nvidia", "nvapi-test", "meta/llama-3.1-70b-instruct"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Connection.Protocol != config.ProtocolOpenAICompatible {
		t.Fatalf("protocol = %q", req.Connection.Protocol)
	}
	if req.Connection.BaseURL != config.NVIDIABaseURL {
		t.Fatalf("base URL = %q", req.Connection.BaseURL)
	}
}

func TestParseCustomProvider(t *testing.T) {
	t.Parallel()

	req, err := Parse([]string{
		"openrouter",
		"env",
		"openai/gpt-4.1",
		"https://openrouter.ai/api/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.APIKey != "" || req.Connection.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("unexpected key handling: %#v", req)
	}
}

func TestParseOllama(t *testing.T) {
	t.Parallel()

	req, err := Parse([]string{"ollama", "qwen3:8b", "http://ollama.internal:11434"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Connection.Protocol != config.ProtocolOllama {
		t.Fatalf("protocol = %q", req.Connection.Protocol)
	}
	if req.APIKey != "" {
		t.Fatal("Ollama should not have an API key")
	}
}

func TestParseCustomProviderNeedsBaseURL(t *testing.T) {
	t.Parallel()

	if _, err := Parse([]string{"custom", "key", "model"}); err == nil {
		t.Fatal("expected missing base URL error")
	}
}
