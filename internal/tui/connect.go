package tui

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"

	"github.com/ephemera-ai/ephemera/internal/config"
)

type connectStep string

const (
	connectProvider connectStep = "provider"
	connectName     connectStep = "name"
	connectBaseURL  connectStep = "base-url"
	connectAPIKey   connectStep = "api-key"
	connectModel    connectStep = "model"
	connectReview   connectStep = "review"
)

type connectFlow struct {
	Provider string
	Name     string
	BaseURL  string
	APIKey   string
	Model    string
	Step     connectStep
	History  []connectStep
}

func (m *Model) startConnect(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	m.connect = &connectFlow{Step: connectProvider}
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
			m.status = "Choose one of the provider or preset options below."
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
		value = strings.TrimRight(value, "/")
		if m.connect.BaseURL != value {
			m.connect.Model = ""
		}
		m.connect.BaseURL = value
		if m.connect.Provider == "ollama" {
			m.advanceConnect(connectModel)
		} else {
			m.advanceConnect(connectAPIKey)
		}

	case connectAPIKey:
		if m.connect.APIKey != value {
			m.connect.Model = ""
		}
		m.connect.APIKey = value
		if m.connectKeyRequired() && !m.connectHasCredential() {
			m.status = "An API key is required for this provider. Enter one or set " + m.connectPrimaryCredentialEnv() + "."
			return
		}
		m.advanceConnect(connectModel)

	case connectModel:
		if value == "" {
			m.status = "Choose a model from the provider catalog below."
			return
		}
		available, err := m.modelAvailableForConfig(m.connectModelListConfig(), value, false)
		if err != nil {
			if m.connect.Provider == "codex" {
				m.status = "Codex model list unavailable: " + err.Error()
				m.notice = "### Codex model not changed\n\nThe Codex model list could not be loaded:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nOpen Codex once to refresh its login and model cache, then retry `/connect codex`."
				return
			}
			m.connect.Model = value
			m.advanceConnect(connectReview)
			m.prepareConnectInput()
			m.rebuildSuggestions()
			m.status = "Model accepted without catalog verification."
			m.notice = "### Model not verified\n\nThe provider catalog could not be checked:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nThe typed model ID will be used anyway. If the provider rejects it, choose another model with `/models` or `/model <id>`."
			return
		}
		if !available {
			if m.connect.Provider == "codex" {
				m.status = fmt.Sprintf("Model %q is not available from Codex.", value)
				m.notice = "### Codex model blocked\n\n`" + escapeMarkdown(value) + "` is not in the Codex ChatGPT model list. Choose one of the listed Codex models so this route stays on subscription auth."
				return
			}
			m.connect.Model = value
			m.advanceConnect(connectReview)
			m.prepareConnectInput()
			m.rebuildSuggestions()
			m.status = "Model accepted even though it was not advertised."
			m.notice = "### Model not in catalog\n\n`" + escapeMarkdown(value) + "` was not advertised by this provider's catalog. It will still be used because some providers expose incomplete model lists."
			return
		}
		m.connect.Model = value
		m.advanceConnect(connectReview)

	case connectReview:
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
		m.advanceConnect(connectBaseURL)
		return true
	case "openai":
		m.connect.Provider = "openai"
		m.advanceConnect(connectAPIKey)
		return true
	case "codex", "chatgpt":
		m.connect.Provider = "codex"
		m.advanceConnect(connectModel)
		return true
	case "anthropic":
		m.connect.Provider = "anthropic"
		m.advanceConnect(connectAPIKey)
		return true
	case "compatible":
		m.connect.Provider = "compatible"
		m.advanceConnect(connectName)
		return true
	default:
		preset, ok := config.Preset(name)
		if !ok || preset.Protocol != config.ProtocolOpenAICompatible {
			return false
		}
		m.connect.Provider = "compatible"
		m.connect.Name = name
		m.connect.BaseURL = strings.TrimRight(preset.BaseURL, "/")
		m.advanceConnect(connectAPIKey)
		return true
	}
}

func (m *Model) applyConnectName(value string) {
	name := strings.ToLower(strings.TrimSpace(value))
	if name == "" {
		name = "compatible"
	}
	if m.connect.Name != name {
		m.connect.Model = ""
	}
	m.connect.Name = name
	if preset, ok := config.Preset(name); ok && preset.Protocol == config.ProtocolOpenAICompatible && strings.TrimSpace(preset.BaseURL) != "" {
		m.connect.BaseURL = strings.TrimRight(preset.BaseURL, "/")
		m.advanceConnect(connectAPIKey)
		return
	}
	m.advanceConnect(connectBaseURL)
}

func (m *Model) advanceConnect(next connectStep) {
	if m.connect == nil || next == "" || m.connect.Step == next {
		return
	}
	m.connect.History = append(m.connect.History, m.connect.Step)
	m.connect.Step = next
}

func (m *Model) retreatConnect() bool {
	if m.connect == nil || len(m.connect.History) == 0 {
		return false
	}
	last := len(m.connect.History) - 1
	m.connect.Step = m.connect.History[last]
	m.connect.History = m.connect.History[:last]
	m.prepareConnectInput()
	m.rebuildSuggestions()
	m.status = "Back to " + strings.ToLower(m.connectStepTitle()) + "."
	return true
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
	available, err := m.modelAvailableForConfig(m.connectModelListConfig(), flow.Model, true)
	if err != nil {
		if flow.Provider == "codex" {
			m.status = "Codex connection check failed: " + err.Error()
			m.notice = "### Codex connection not activated\n\nThe Codex model list could not be verified:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nNo settings were changed."
			return
		}
		m.notice = "### Connected with unverified model\n\nThe provider catalog could not be verified:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nThe route is active with the typed model ID. If generation fails, run `/models` or `/model <id>` to adjust it."
	} else if !available {
		if flow.Provider == "codex" {
			m.status = fmt.Sprintf("Codex model %q is not available.", flow.Model)
			m.notice = "### Codex connection not activated\n\n`" + escapeMarkdown(flow.Model) + "` is not in the Codex ChatGPT model list. No settings were changed."
			return
		}
		m.notice = "### Connected with uncataloged model\n\n`" + escapeMarkdown(flow.Model) + "` was not advertised by this provider's catalog. The route is active because some providers expose incomplete model lists."
	} else {
		display := flow.Provider
		if flow.Provider == "compatible" {
			display = flow.Name
		}
		m.notice = fmt.Sprintf(
			"### Connected\n\nProvider: `%s`  \nModel: `%s`\n\nThe connection is active. API keys entered here remain only in this process; use environment variables for persistence.",
			display,
			flow.Model,
		)
	}

	m.cfg.Provider = flow.Provider
	switch flow.Provider {
	case "ollama":
		m.cfg.OllamaURL = flow.BaseURL
	case "openai":
		m.cfg.OpenAIKey = flow.APIKey
	case "codex":
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
	m.status = fmt.Sprintf("Connected → %s · %s", display, flow.Model)
	m.connect = nil
	m.restorePromptInput()
	m.rebuildSuggestions()
}

func (m *Model) prepareConnectInput() {
	if m.connect == nil {
		return
	}
	m.input.SetValue(m.connectStepValue())
	m.input.EchoMode = textinput.EchoNormal
	m.input.EchoCharacter = '•'
	m.input.Placeholder = m.connectPlaceholder()
	if m.connect.Step == connectAPIKey {
		m.input.EchoMode = textinput.EchoPassword
	}
	m.input.CursorEnd()
	m.syncConnectNotice()
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
		case "codex":
			return "Codex ChatGPT login [uses ~/.codex/auth.json]"
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
		return "Select an available model from the provider catalog"
	case connectReview:
		return "Press Enter to activate · Shift+Tab returns to model"
	default:
		return "Connection value"
	}
}

func (m Model) connectStepValue() string {
	if m.connect == nil {
		return ""
	}
	switch m.connect.Step {
	case connectProvider:
		if m.connect.Provider == "compatible" && m.connect.Name != "" {
			return m.connect.Name
		}
		return m.connect.Provider
	case connectName:
		return m.connect.Name
	case connectBaseURL:
		return m.connect.BaseURL
	case connectAPIKey:
		return m.connect.APIKey
	case connectModel:
		return m.connect.Model
	default:
		return ""
	}
}

func (m *Model) syncConnectNotice() {
	if m.connect == nil {
		return
	}
	step, total := m.connectProgress()
	title := m.connectStepTitle()
	body := m.connectStepGuidance()
	m.notice = fmt.Sprintf("### Connect · %d/%d · %s\n\n%s\n\nThe active route is not changed until the review step is confirmed.", step, total, title, body)
	m.status = fmt.Sprintf("Connection setup · %s · step %d of %d", strings.ToLower(title), step, total)
}

func (m Model) connectProgress() (int, int) {
	if m.connect == nil {
		return 0, 0
	}
	switch m.connect.Step {
	case connectProvider:
		return 1, 5
	case connectName, connectBaseURL:
		return 2, 5
	case connectAPIKey:
		return 3, 5
	case connectModel:
		return 4, 5
	case connectReview:
		return 5, 5
	default:
		return 1, 5
	}
}

func (m Model) connectStepTitle() string {
	if m.connect == nil {
		return "Connection"
	}
	switch m.connect.Step {
	case connectProvider:
		return "Provider"
	case connectName:
		return "Connection name"
	case connectBaseURL:
		return "Endpoint"
	case connectAPIKey:
		return "Credentials"
	case connectModel:
		return "Model"
	case connectReview:
		return "Review"
	default:
		return "Connection"
	}
}

func (m Model) connectStepGuidance() string {
	if m.connect == nil {
		return ""
	}
	switch m.connect.Step {
	case connectProvider:
		return "Choose a direct provider, a hosted preset, or a custom OpenAI-compatible endpoint."
	case connectName:
		return "Name this compatible route. Selecting a known preset fills its endpoint automatically."
	case connectBaseURL:
		return "Enter the API base URL. Press Enter to accept the suggested default shown in brackets."
	case connectAPIKey:
		return "Enter an API key, or press Enter to use the detected environment variable when available. The value is never written to config.json."
	case connectModel:
		return "Choose an advertised model, or type a model ID manually when the provider catalog is incomplete."
	case connectReview:
		return "Review the route summary below. Press Enter to activate it, or Shift+Tab to revise the model."
	default:
		return "Complete the current connection field."
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

func (m *Model) openModelChooser() {
	m.input.SetValue("/model ")
	m.input.CursorEnd()
	m.rebuildSuggestions()
	catalog := m.modelCatalogForConfig(m.cfg, false)
	provider := m.cfg.Provider
	if provider == "compatible" && strings.TrimSpace(m.cfg.CompatibleName) != "" {
		provider = m.cfg.CompatibleName
	}
	if catalog.Err != nil {
		m.notice = "### Models unavailable\n\nThe live catalog for `" + provider + "` could not be loaded:\n\n`" + escapeMarkdown(catalog.Err.Error()) + "`\n\nCheck the active endpoint and credentials, or type a model ID directly with `/model <id>`."
		m.status = "Model catalog unavailable: " + catalog.Err.Error()
		return
	}
	if len(catalog.Models) == 0 {
		m.notice = "### Models unavailable\n\nThe provider returned an empty catalog. Type a model ID directly with `/model <id>` if you know one."
		m.status = "Provider returned no available models."
		return
	}
	m.notice = fmt.Sprintf(
		"### Available models\n\n`%s` advertised **%d** model(s). Choose one from the live catalog below. Use **↑/↓** and **Enter** to activate the highlighted model, or **Tab** to only fill the input.",
		provider,
		len(catalog.Models),
	)
	m.status = fmt.Sprintf("Choose one of %d available models for %s.", len(catalog.Models), provider)
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
		cfg.OpenAIKey = m.connect.APIKey
	case "codex":
	case "anthropic":
		cfg.AnthropicKey = m.connect.APIKey
	case "compatible":
		cfg.CompatibleName = m.connect.Name
		cfg.CompatibleURL = m.connect.BaseURL
		cfg.CompatibleKey = m.connect.APIKey
	}
	return cfg
}

func (m *Model) connectSuggestions() []suggestion {
	if m.connect == nil || m.connect.Step == connectAPIKey || m.connect.Step == connectReview {
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

func (m Model) connectKeyRequired() bool {
	if m.connect == nil {
		return false
	}
	switch m.connect.Provider {
	case "openai", "anthropic":
		return true
	case "codex":
		return false
	case "compatible":
		preset, ok := config.Preset(m.connect.Name)
		return ok && preset.APIKeyEnv != "" && !strings.EqualFold(m.connect.Name, "lm-studio")
	default:
		return false
	}
}

func (m Model) connectHasCredential() bool {
	if m.connect == nil {
		return false
	}
	if strings.TrimSpace(m.connect.APIKey) != "" {
		return true
	}
	for _, env := range m.connectCredentialEnvs() {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return true
		}
	}
	return false
}

func (m Model) connectPrimaryCredentialEnv() string {
	envs := m.connectCredentialEnvs()
	if len(envs) > 0 {
		return envs[0]
	}
	return "the provider credential environment variable"
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
