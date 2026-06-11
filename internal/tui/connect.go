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
		if !m.applyConnectProvider(value) {
			m.status = "Choose a provider or preset from the suggestions."
			m.rebuildSuggestions()
			return
		}

	case connectName:
		m.applyConnectName(value)

	case connectBaseURL:
		if value == "" {
			if m.connect.Provider == "ollama" {
				value = m.cfg.OllamaURL
			} else {
				value = m.defaultCompatibleBaseURL()
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

func (m *Model) applyConnectProvider(value string) bool {
	name := strings.ToLower(strings.TrimSpace(value))
	switch name {
	case "ollama":
		m.connect.Provider = "ollama"
		m.connect.Step = connectBaseURL
		return true
	case "openai":
		m.connect.Provider = "openai"
		m.connect.Step = connectAPIKey
		return true
	case "anthropic":
		m.connect.Provider = "anthropic"
		m.connect.Step = connectAPIKey
		return true
	case "compatible":
		m.connect.Provider = "compatible"
		m.connect.Step = connectName
		return true
	default:
		preset, ok := config.Preset(name)
		if !ok || preset.Protocol != config.ProtocolOpenAICompatible {
			return false
		}
		m.connect.Provider = "compatible"
		m.connect.Name = name
		m.connect.BaseURL = strings.TrimRight(preset.BaseURL, "/")
		m.connect.Step = connectAPIKey
		return true
	}
}

func (m *Model) applyConnectName(value string) {
	name := strings.ToLower(strings.TrimSpace(value))
	if name == "" {
		name = "compatible"
	}
	m.connect.Name = name
	if preset, ok := config.Preset(name); ok && preset.Protocol == config.ProtocolOpenAICompatible && strings.TrimSpace(preset.BaseURL) != "" {
		m.connect.BaseURL = strings.TrimRight(preset.BaseURL, "/")
		m.connect.Step = connectAPIKey
		return
	}
	m.connect.Step = connectBaseURL
}

func (m *Model) acceptConnectSuggestionForEnter() bool {
	if m.connect == nil || len(m.suggestions) == 0 {
		return false
	}
	if strings.TrimSpace(m.input.Value()) == "" && m.connect.Step != connectProvider && m.connect.Step != connectModel {
		return false
	}
	return m.acceptSuggestion()
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
	_ = m.saveSession()

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
		return "Provider or preset: ollama, openai, anthropic, openrouter, groq, nvidia"
	case connectName:
		return "Connection name [compatible]"
	case connectBaseURL:
		if m.connect.Provider == "ollama" {
			return "Ollama URL [" + m.cfg.OllamaURL + "]"
		}
		return "OpenAI-compatible base URL [" + m.defaultCompatibleBaseURL() + "]"
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
			envName := config.DefaultAPIKeyEnv(m.connect.Name)
			switch {
			case m.connect.Name != "" && os.Getenv(envName) != "":
				return "API key [Enter uses " + envName + "]"
			case os.Getenv("EPHEMERA_API_KEY") != "":
				return "API key [Enter uses EPHEMERA_API_KEY]"
			case m.connect.Name != "":
				return "API key [" + envName + " or optional for local servers]"
			}
			return "API key [optional for local servers]"
		}
	case connectModel:
		return "Model ID [" + m.defaultConnectModel() + "]"
	default:
		return "Connection value"
	}
}

func (m Model) defaultCompatibleBaseURL() string {
	if m.connect != nil && strings.TrimSpace(m.connect.Name) != "" {
		if preset, ok := config.Preset(m.connect.Name); ok && strings.TrimSpace(preset.BaseURL) != "" {
			return preset.BaseURL
		}
	}
	return m.cfg.CompatibleURL
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

func (m *Model) openModelChooser() {
	m.input.SetValue("/model ")
	m.input.CursorEnd()
	m.rebuildSuggestions()
	provider := m.cfg.Provider
	if provider == "compatible" && strings.TrimSpace(m.cfg.CompatibleName) != "" {
		provider = m.cfg.CompatibleName
	}
	if len(m.suggestions) == 0 {
		m.notice = "### Models\n\nNo model catalog is available for the active provider. Type `/model <model-id>` to set one manually."
		m.status = "No model suggestions available."
		return
	}
	m.notice = fmt.Sprintf(
		"### Models\n\nChoose a model for `%s` from the palette below. Use **↑/↓** and **Enter** to activate the highlighted model, or **Tab** to only fill the input.",
		provider,
	)
	m.status = fmt.Sprintf("Choose a model for %s.", provider)
}

func (m *Model) connectModelListConfig() config.Config {
	cfg := m.cfg
	if m.connect == nil {
		return cfg
	}
	cfg.Provider = m.connect.Provider
	switch m.connect.Provider {
	case "ollama":
		if strings.TrimSpace(m.connect.BaseURL) != "" {
			cfg.OllamaURL = m.connect.BaseURL
		}
	case "openai":
		if strings.TrimSpace(m.connect.APIKey) != "" {
			cfg.OpenAIKey = m.connect.APIKey
		}
	case "anthropic":
		if strings.TrimSpace(m.connect.APIKey) != "" {
			cfg.AnthropicKey = m.connect.APIKey
		}
	case "compatible":
		if strings.TrimSpace(m.connect.Name) != "" {
			cfg.CompatibleName = m.connect.Name
		}
		if strings.TrimSpace(m.connect.BaseURL) != "" {
			cfg.CompatibleURL = m.connect.BaseURL
		}
		if strings.TrimSpace(m.connect.APIKey) != "" {
			cfg.CompatibleKey = m.connect.APIKey
		}
	}
	return cfg
}

func (m *Model) connectSuggestions() []suggestion {
	if m.connect == nil || m.connect.Step == connectAPIKey {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.input.Value()))
	var values []suggestion

	switch m.connect.Step {
	case connectProvider:
		for _, provider := range config.ConnectNames() {
			values = append(values, suggestion{
				Value:       provider,
				Label:       provider,
				Description: argumentDescription("/connect", provider),
			})
		}
	case connectName:
		values = []suggestion{
			{Value: "openrouter", Label: "openrouter", Description: config.OpenRouterBaseURL},
			{Value: "groq", Label: "groq", Description: config.GroqBaseURL},
			{Value: "nvidia", Label: "nvidia", Description: config.NVIDIABaseURL},
			{Value: "lm-studio", Label: "lm-studio", Description: "local compatible server"},
			{Value: "together", Label: "together", Description: config.TogetherBaseURL},
		}
	case connectBaseURL:
		if m.connect.Provider == "ollama" {
			values = []suggestion{{Value: m.cfg.OllamaURL, Label: m.cfg.OllamaURL, Description: "current Ollama endpoint"}}
		} else {
			values = []suggestion{
				{Value: m.defaultCompatibleBaseURL(), Label: "Current default", Description: m.defaultCompatibleBaseURL()},
				{Value: config.OpenRouterBaseURL, Label: "OpenRouter", Description: config.OpenRouterBaseURL},
				{Value: config.GroqBaseURL, Label: "Groq", Description: config.GroqBaseURL},
				{Value: config.NVIDIABaseURL, Label: "NVIDIA", Description: config.NVIDIABaseURL},
				{Value: config.TogetherBaseURL, Label: "Together", Description: config.TogetherBaseURL},
				{Value: config.LMStudioBaseURL, Label: "LM Studio", Description: config.LMStudioBaseURL},
			}
		}
	case connectModel:
		values = m.modelSuggestionsForConfig(m.connectModelListConfig())
		model := m.defaultConnectModel()
		if model != "" && len(values) == 0 {
			values = append(values, suggestion{Value: model, Label: model, Description: "current default model"})
		}
	}

	if query == "" {
		return values
	}
	filtered := values[:0]
	for _, item := range values {
		if strings.Contains(strings.ToLower(item.Value+" "+item.Label+" "+item.Description), query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
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
