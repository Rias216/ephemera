// Package connect parses the /connect command without retaining secrets.
package connect

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
)

var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Request is a validated connection request. APIKey is deliberately separate
// from Connection so callers can keep it in memory while persisting metadata.
type Request struct {
	Provider   string
	Model      string
	APIKey     string
	Connection config.Connection
}

// Parse accepts:
//
//	/connect openai <api-key|env> <model> [base-url]
//	/connect anthropic <api-key|env> <model>
//	/connect nvidia <api-key|env> <model> [base-url]
//	/connect <name> <api-key|env> <model> <openai-compatible-base-url>
//	/connect ollama <model> [base-url]
//	/connect codex <model>
func Parse(args []string) (Request, error) {
	if len(args) == 0 {
		return Request{}, usageError()
	}

	name := strings.ToLower(strings.TrimSpace(args[0]))
	if !providerName.MatchString(name) {
		return Request{}, fmt.Errorf("invalid provider name %q", args[0])
	}

	if name == "ollama" {
		if len(args) < 2 || len(args) > 3 {
			return Request{}, fmt.Errorf("usage: /connect ollama <model> [base-url]")
		}
		connection, _ := config.Preset("ollama")
		if len(args) == 3 {
			connection.BaseURL = strings.TrimSpace(args[2])
		}
		if err := validateBaseURL(connection.BaseURL); err != nil {
			return Request{}, err
		}
		return Request{
			Provider:   name,
			Model:      strings.TrimSpace(args[1]),
			Connection: connection,
		}, nil
	}

	if name == "codex" || name == "chatgpt" {
		if len(args) != 2 {
			return Request{}, fmt.Errorf("usage: /connect codex <model>")
		}
		model := strings.TrimSpace(args[1])
		if model == "" {
			return Request{}, fmt.Errorf("model cannot be empty")
		}
		connection, _ := config.Preset("codex")
		return Request{
			Provider:   "codex",
			Model:      model,
			Connection: connection,
		}, nil
	}

	if len(args) < 3 || len(args) > 4 {
		return Request{}, usageError()
	}

	key := strings.TrimSpace(args[1])
	if key == "-" || strings.EqualFold(key, "env") {
		key = ""
	}
	model := strings.TrimSpace(args[2])
	if model == "" {
		return Request{}, fmt.Errorf("model cannot be empty")
	}

	connection, preset := config.Preset(name)
	if !preset {
		connection = config.Connection{
			Protocol:  config.ProtocolOpenAICompatible,
			APIKeyEnv: config.DefaultAPIKeyEnv(name),
		}
	}

	if len(args) == 4 {
		baseURL := strings.TrimSpace(args[3])
		if connection.Protocol == config.ProtocolAnthropic {
			return Request{}, fmt.Errorf("custom Anthropic endpoints are not supported; omit the base URL")
		}
		if connection.Protocol == config.ProtocolOpenAI {
			connection.Protocol = config.ProtocolOpenAICompatible
		}
		connection.BaseURL = baseURL
	}

	if connection.Protocol == config.ProtocolOpenAICompatible {
		if connection.BaseURL == "" {
			return Request{}, fmt.Errorf("provider %q needs an OpenAI-compatible base URL", name)
		}
		if err := validateBaseURL(connection.BaseURL); err != nil {
			return Request{}, err
		}
	}

	if connection.APIKeyEnv == "" {
		connection.APIKeyEnv = config.DefaultAPIKeyEnv(name)
	}

	return Request{
		Provider:   name,
		Model:      model,
		APIKey:     key,
		Connection: connection,
	}, nil
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid base URL %q; include http:// or https://", value)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid base URL scheme %q", parsed.Scheme)
	}
	return nil
}

func usageError() error {
	return fmt.Errorf("usage: /connect <provider> <api-key|env> <model> [base-url], or /connect ollama <model> [base-url]")
}
