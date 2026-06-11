package tui

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/ephemera-ai/ephemera/internal/config"
)

type connectStep string

const (
	connectProvider connectStep = "provider"
	connectName     connectStep = "name"
	connectBaseURL  connectStep = "base-url"
	connectAPIKey   connectStep = "api-key"
	connectModel    connectStep = "model"
)

type connectFlow struct {
	Provider string
	Name     string
	BaseURL  string
	APIKey   string
	Model    string
	Step     connectStep
}

func (m *Model) startConnect(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	m.connect = &connectFlow{Step: connectProvider}
	m.notice = "### Connect\n\nChoose a provider. **Tab** completes, **↑/↓** selects, **Enter** advances, and **Esc** cancels. API keys are kept in memory only."
	m.status = "Connection wizard · choose a provider."
	m.prepareConnectInput()

	if provider != "" {
		m.input.SetValue(provider)
		m.input.CursorEnd()
		m.submitConnectStep()
		return
	}
	m.rebuildSuggestions()
}

func (m *Model) cancelConnect() {
	m.connect = nil
	m.notice = "Connection cancelled. Nothing was changed."
	m.status = "The existing connection remains."
	m.restorePromptInput()
	m.rebuildSuggestions()
}

func (m *Model) submitConnectStep() {
	if m.connect == nil {
		return
	}
	value := strings.TrimSpace(m.input.Value())

	switch m.connect.Step {
	case connectProvider:
		provider := strings.ToLower(value)
		if !config.ValidProvider(provider) {
			m.status = "Choose ollama, openai, anthropic, or compatible."
			m.rebuildSuggestions()
			return
		}
		m.connect.Provider = provider
		switch provider {
		case "ollama":
			m.connect.Step = connectBaseURL
		case "openai", "anthropic":
			m.connect.Step = connectAPIKey
		case "compatible":
			m.connect.Step = connectName
		}

	case connectName:
		if value == "" {
			value = "compatible"
		}
		m.connect.Name = value
		m.connect.Step = connectBaseURL

	case connectBaseURL:
		if value == "" {
			if m.connect.Provider == "ollama" {
				value = m.cfg.OllamaURL
			} else {
				value = m.cfg.CompatibleURL
			}
		}
		if err := validateEndpoint(value); err != nil {
			m.status = "Invalid endpoint: " + err.Error()
			return
		}
		m.connect.BaseURL = strings.TrimRight(value, "/")
		if m.connect.Provider == "ollama" {
			m.connect.Step = connectModel
		} else {
			m.connect.Step = connectAPIKey
		}

	case connectAPIKey:
		m.connect.APIKey = value
		m.connect.Step = connectModel

	case connectModel:
		if value == "" {
			value = m.defaultConnectModel()
		}
		if value == "" {
			m.status = "A model ID is required."
			return
		}
		m.connect.Model = value
		m.finishConnect()
		return
	}

	m.prepareConnectInput()
	m.rebuildSuggestions()
}

func (m *Model) finishConnect() {
	flow := m.connect
	if flow == nil {
		return
	}

	m.cfg.Provider = flow.Provider
	switch flow.Provider {
	case "ollama":
		m.cfg.OllamaURL = flow.BaseURL
	case "openai":
		m.cfg.OpenAIKey = flow.APIKey
	case "anthropic":
		m.cfg.AnthropicKey = flow.APIKey
	case "compatible":
		m.cfg.CompatibleName = flow.Name
		m.cfg.CompatibleURL = flow.BaseURL
		m.cfg.CompatibleKey = flow.APIKey
	}
	m.cfg.SetModel(flow.Model)
	m.session.Provider = m.cfg.Provider
	m.session.Model = m.cfg.Model()
	_ = config.Save(m.cfg)
	_ = m.store.Save(m.session)

	display := flow.Provider
	if flow.Provider == "compatible" {
		display = flow.Name
	}
	m.notice = fmt.Sprintf(
		"### Connected\n\nProvider: `%s`  \nModel: `%s`\n\nThe connection is active. API keys entered here remain only in this process; use environment variables for persistence.",
		display,
		flow.Model,
	)
	m.status = fmt.Sprintf("Connected → %s · %s", display, flow.Model)
	m.connect = nil
	m.restorePromptInput()
	m.rebuildSuggestions()
}

func (m *Model) prepareConnectInput() {
	if m.connect == nil {
		return
	}
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	m.input.EchoCharacter = '•'
	m.input.Placeholder = m.connectPlaceholder()
	if m.connect.Step == connectAPIKey {
		m.input.EchoMode = textinput.EchoPassword
	}
}

func (m *Model) restorePromptInput() {
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	m.input.Placeholder = "Ask what must be seen…"
}

func (m Model) connectPlaceholder() string {
	if m.connect == nil {
		return "Ask what must be seen…"
	}
	switch m.connect.Step {
	case connectProvider:
		return "Provider: ollama, openai, anthropic, compatible"
	case connectName:
		return "Connection name [compatible]"
	case connectBaseURL:
		if m.connect.Provider == "ollama" {
			return "Ollama URL [" + m.cfg.OllamaURL + "]"
		}
		return "OpenAI-compatible base URL [" + m.cfg.CompatibleURL + "]"
	case connectAPIKey:
		switch m.connect.Provider {
		case "openai":
			if os.Getenv("OPENAI_API_KEY") != "" {
				return "API key [Enter uses OPENAI_API_KEY]"
			}
			return "OpenAI API key"
		case "anthropic":
			if os.Getenv("ANTHROPIC_API_KEY") != "" {
				return "API key [Enter uses ANTHROPIC_API_KEY]"
			}
			return "Anthropic API key"
		default:
			if os.Getenv("EPHEMERA_API_KEY") != "" {
				return "API key [Enter uses EPHEMERA_API_KEY]"
			}
			return "API key [optional for local servers]"
		}
	case connectModel:
		return "Model ID [" + m.defaultConnectModel() + "]"
	default:
		return "Connection value"
	}
}

func (m Model) defaultConnectModel() string {
	if m.connect == nil {
		return ""
	}
	if model := strings.TrimSpace(m.cfg.Models[m.connect.Provider]); model != "" {
		return model
	}
	return "model-name"
}

func (m Model) connectSuggestions() []suggestion {
	if m.connect == nil || m.connect.Step == connectAPIKey {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var values []suggestion

	switch m.connect.Step {
	case connectProvider:
		for _, provider := range config.ProviderNames() {
			values = append(values, suggestion{
				Value:       provider,
				Label:       provider,
				Description: argumentDescription("/connect", provider),
			})
		}
	case connectName:
		values = []suggestion{
			{Value: "openrouter", Label: "openrouter", Description: "custom connection name"},
			{Value: "groq", Label: "groq", Description: "custom connection name"},
			{Value: "lm-studio", Label: "lm-studio", Description: "local compatible server"},
			{Value: "together", Label: "together", Description: "custom connection name"},
		}
	case connectBaseURL:
		if m.connect.Provider == "ollama" {
			values = []suggestion{{Value: m.cfg.OllamaURL, Label: m.cfg.OllamaURL, Description: "current Ollama endpoint"}}
		} else {
			values = []suggestion{
				{Value: "https://openrouter.ai/api/v1", Label: "OpenRouter", Description: "https://openrouter.ai/api/v1"},
				{Value: "https://api.groq.com/openai/v1", Label: "Groq", Description: "https://api.groq.com/openai/v1"},
				{Value: "https://api.together.xyz/v1", Label: "Together", Description: "https://api.together.xyz/v1"},
				{Value: "http://localhost:1234/v1", Label: "LM Studio", Description: "http://localhost:1234/v1"},
			}
		}
	case connectModel:
		model := m.defaultConnectModel()
		if model != "" {
			values = []suggestion{{Value: model, Label: model, Description: "current default model"}}
		}
	}

	if query == "" {
		return limitSuggestions(values, 7)
	}
	filtered := values[:0]
	for _, item := range values {
		if strings.Contains(strings.ToLower(item.Value+" "+item.Label+" "+item.Description), query) {
			filtered = append(filtered, item)
		}
	}
	return limitSuggestions(filtered, 7)
}

func validateEndpoint(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("use an http:// or https:// URL")
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	return nil
}
