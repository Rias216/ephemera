package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/config"
)

type connectProfile struct {
	Name     string
	Badge    string
	Summary  string
	Protocol string
	Endpoint string
	Auth     string
	Env      string
}

func connectProfileFor(value string) connectProfile {
	name := strings.ToLower(strings.TrimSpace(value))
	profile := connectProfile{Name: name, Badge: "CLOUD", Protocol: "OpenAI compatible", Auth: "API key"}

	switch name {
	case "ollama":
		return connectProfile{
			Name: name, Badge: "LOCAL", Summary: "local Ollama runtime · no API key", Protocol: "Ollama", Endpoint: "http://localhost:11434", Auth: "not required",
		}
	case "openai":
		return connectProfile{
			Name: name, Badge: "CLOUD", Summary: "native OpenAI API · OPENAI_API_KEY", Protocol: "OpenAI", Endpoint: "https://api.openai.com/v1", Auth: "API key", Env: "OPENAI_API_KEY",
		}
	case "codex", "chatgpt":
		return connectProfile{
			Name: "codex", Badge: "CHATGPT", Summary: "Codex ChatGPT login · isolated Ephemera model bridge", Protocol: "Codex CLI / Responses", Endpoint: "~/.codex/auth.json", Auth: "Codex login",
		}
	case "anthropic":
		return connectProfile{
			Name: name, Badge: "CLOUD", Summary: "native Anthropic API · ANTHROPIC_API_KEY", Protocol: "Anthropic", Endpoint: "https://api.anthropic.com/v1", Auth: "API key", Env: "ANTHROPIC_API_KEY",
		}
	case "compatible":
		return connectProfile{
			Name: name, Badge: "CUSTOM", Summary: "bring any OpenAI-compatible endpoint", Protocol: "OpenAI compatible", Auth: "optional API key",
		}
	}

	if preset, ok := config.Preset(name); ok {
		profile.Endpoint = preset.BaseURL
		profile.Env = preset.APIKeyEnv
		profile.Badge = "PRESET"
		profile.Summary = "hosted OpenAI-compatible preset"
		if name == "lm-studio" {
			profile.Badge = "LOCAL"
			profile.Summary = "local LM Studio server · key usually optional"
			profile.Auth = "usually none"
		}
		return profile
	}

	if name == "" {
		profile.Name = "provider"
		profile.Badge = "SELECT"
		profile.Summary = "choose a provider or compatible preset"
		profile.Endpoint = "—"
		profile.Auth = "—"
		return profile
	}

	profile.Badge = "CUSTOM"
	profile.Summary = "custom compatible route"
	profile.Env = config.DefaultAPIKeyEnv(name)
	return profile
}

func (m Model) connectProfileSummary(value string) string {
	profile := connectProfileFor(value)
	summary := profile.Summary
	if profile.Env != "" && strings.TrimSpace(os.Getenv(profile.Env)) != "" {
		summary += " · key detected"
	}
	return summary
}

func (m Model) renderConnectDetail(width, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := []string{m.connectProgressRail(width)}
	bodyHeight := height - 1
	if bodyHeight <= 0 {
		return lines
	}

	if width >= 104 && bodyHeight >= 3 {
		usable := width - 2
		leftWidth := max(24, usable*30/100)
		middleWidth := max(28, usable*35/100)
		rightWidth := max(22, usable-leftWidth-middleWidth)
		leftWidth += usable - leftWidth - middleWidth - rightWidth

		left := m.connectSelectionColumn(leftWidth, bodyHeight)
		middle := m.connectFieldColumn(middleWidth, bodyHeight)
		right := m.connectPreviewColumn(rightWidth, bodyHeight)
		separator := lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render("│")
		for i := 0; i < bodyHeight; i++ {
			lines = append(lines, left[i]+separator+middle[i]+separator+right[i])
		}
		return lines
	}

	if width >= 70 && bodyHeight >= 3 {
		leftWidth := max(28, width*45/100)
		rightWidth := width - leftWidth - 1
		left := m.connectFieldColumn(leftWidth, bodyHeight)
		right := m.connectPreviewColumn(rightWidth, bodyHeight)
		separator := lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render("│")
		for i := 0; i < bodyHeight; i++ {
			lines = append(lines, left[i]+separator+right[i])
		}
		return lines
	}

	compact := []string{
		m.paletteTextLine("  "+m.connectStepTitle()+"  ·  "+m.connectRequirement(), width, m.styles.AccentBright, m.styles.Panel, true),
	}
	for _, line := range wrapPlain(m.connectStepGuidance(), max(8, width-4), max(0, bodyHeight-2)) {
		compact = append(compact, m.paletteTextLine("  "+line, width, m.styles.Muted, m.styles.Panel, false))
	}
	if len(compact) < bodyHeight {
		compact = append(compact, m.paletteTextLine("  Enter next  Shift+Tab back  Esc cancel", width, m.styles.Faint, m.styles.Panel, false))
	}
	return append(lines, m.fitDetailLines(compact, width, bodyHeight, 227)...)
}

func (m Model) connectProgressRail(width int) string {
	step, total := m.connectProgress()
	if width < 76 {
		return m.paletteTextLine(fmt.Sprintf("  STEP %d/%d  ·  %s", step, total, strings.ToUpper(m.connectStepTitle())), width, m.styles.AccentSoft, m.styles.PanelDeep, true)
	}

	labels := []string{"01 PROVIDER", "02 DETAILS", "03 AUTH", "04 MODEL", "05 REVIEW"}
	separatorWidth := len(labels) - 1
	usable := max(len(labels), width-separatorWidth)
	baseWidth := usable / len(labels)
	extra := usable % len(labels)

	var b strings.Builder
	for i, label := range labels {
		if i > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.PanelDeep).Render("│"))
		}

		tabWidth := baseWidth
		if i < extra {
			tabWidth++
		}
		foreground := m.styles.Faint
		background := m.styles.PanelDeep
		bold := false
		switch {
		case i+1 < step:
			foreground = m.styles.Muted
			background = m.styles.Panel
		case i+1 == step:
			foreground = m.styles.AccentBright
			background = m.styles.PanelRaised
			bold = true
		}
		b.WriteString(centerStyledText(label, tabWidth, foreground, background, bold))
	}
	return padStyledLine(b.String(), width, m.styles.PanelDeep)
}

func centerStyledText(value string, width int, foreground, background color.Color, bold bool) string {
	value = clip(value, max(1, width))
	remaining := max(0, width-lipgloss.Width(value))
	left := remaining / 2
	right := remaining - left
	content := strings.Repeat(" ", left) + value + strings.Repeat(" ", right)
	return lipgloss.NewStyle().Bold(bold).Foreground(foreground).Background(background).Render(content)
}

func (m Model) connectSelectionColumn(width, height int) []string {
	profile := m.selectedConnectProfile()
	title := "  SELECTED CHOICE"
	if m.connect != nil && (m.connect.Step == connectAPIKey || m.connect.Step == connectModel || m.connect.Step == connectReview) {
		title = "  ACTIVE ROUTE"
	}
	name := profile.Name
	if name == "" {
		name = "choose a provider"
	}
	lines := []string{
		m.paletteTextLine(title, width, m.styles.Primary, m.styles.Panel, true),
		m.paletteTextLine("  ◆ "+name+"  ["+profile.Badge+"]", width, m.styles.AccentBright, m.styles.Panel, true),
		m.paletteTextLine("  "+fallback(profile.Summary, "connection route"), width, m.styles.Muted, m.styles.Panel, false),
		m.connectLabelValueLine("Endpoint", fallback(profile.Endpoint, "custom / pending"), width),
		m.connectLabelValueLine("Auth", fallback(profile.Auth, "—"), width),
		m.connectLabelValueLine("Protocol", fallback(profile.Protocol, "—"), width),
	}
	if profile.Env != "" {
		lines = append(lines, m.connectLabelValueLine("Environment", profile.Env, width))
	}
	return m.fitDetailLines(lines, width, height, 239)
}

func (m Model) connectFieldColumn(width, height int) []string {
	requirement := m.connectRequirement()
	lines := []string{
		m.paletteTextLine("  CURRENT FIELD", width, m.styles.Primary, m.styles.Panel, true),
		m.paletteTextLine("  "+m.connectStepTitle()+"  ·  "+requirement, width, m.styles.AccentBright, m.styles.Panel, true),
	}
	for _, line := range wrapPlain(m.connectStepGuidance(), max(8, width-4), max(1, height-4)) {
		lines = append(lines, m.paletteTextLine("  "+line, width, m.styles.Muted, m.styles.Panel, false))
	}
	if len(lines) < height {
		state, stateColor := m.connectFieldState()
		lines = append(lines, m.paletteTextLine("  "+state, width, stateColor, m.styles.PanelDeep, true))
	}
	if hint := m.connectDefaultHint(); hint != "" && len(lines) < height {
		lines = append(lines, m.paletteTextLine("  DEFAULT  "+hint, width, m.styles.AccentSoft, m.styles.PanelDeep, false))
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  Enter next · Shift+Tab back · Esc cancel", width, m.styles.Faint, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 251)
}

func (m Model) connectPreviewColumn(width, height int) []string {
	lines := []string{
		m.paletteTextLine("  ROUTE PREVIEW", width, m.styles.Primary, m.styles.Panel, true),
		m.connectLabelValueLine("Provider", m.connectPreviewName(), width),
		m.connectLabelValueLine("Endpoint", m.connectEndpointPreview(), width),
		m.connectLabelValueLine("Credentials", m.connectCredentialPreview(), width),
		m.connectLabelValueLine("Catalog", m.connectCatalogPreview(), width),
		m.connectLabelValueLine("Model", m.connectModelPreview(), width),
	}
	if len(lines) < height {
		lines = append(lines, m.smallRule(width))
	}
	if len(lines) < height {
		lines = append(lines, m.connectLabelValueLine("Current route", m.providerName()+" · "+m.cfg.Model(), width))
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  Nothing changes before review", width, m.styles.Faint, m.styles.Panel, false))
	}
	if m.connect != nil && m.connect.Step == connectReview && len(lines) < height {
		lines = append(lines, m.paletteTextLine("  ENTER activates · Shift+Tab revises", width, m.styles.AccentBright, m.styles.Panel, true))
	}
	return m.fitDetailLines(lines, width, height, 263)
}

func (m Model) connectLabelValueLine(label, value string, width int) string {
	prefix := "  " + label
	value = fallback(value, "—")
	available := max(1, width-lipgloss.Width(prefix)-2)
	value = clip(value, available)
	gap := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(value))
	left := lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.Panel).Render(prefix)
	right := lipgloss.NewStyle().Foreground(m.styles.Muted).Background(m.styles.Panel).Render(value)
	spaces := lipgloss.NewStyle().Background(m.styles.Panel).Render(strings.Repeat(" ", gap))
	return left + spaces + right
}

func padStyledLine(value string, width int, background color.Color) string {
	if lipgloss.Width(value) > width {
		return clip(value, width)
	}
	return value + lipgloss.NewStyle().Background(background).Render(strings.Repeat(" ", width-lipgloss.Width(value)))
}

func (m Model) selectedConnectProfile() connectProfile {
	name := m.connectDisplayName()
	selectedValue := ""
	if m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
		selectedValue = m.suggestions[m.completionIndex].Value
	}
	if m.connect != nil && (m.connect.Step == connectProvider || m.connect.Step == connectName) && strings.TrimSpace(selectedValue) != "" {
		name = selectedValue
	}
	profile := connectProfileFor(name)
	if m.connect != nil {
		if m.connect.BaseURL != "" {
			profile.Endpoint = m.connect.BaseURL
		} else if m.connect.Step == connectBaseURL && strings.TrimSpace(selectedValue) != "" {
			profile.Endpoint = selectedValue
		}
		profile.Auth = m.connectCredentialPreview()
	}
	return profile
}

func (m Model) connectDisplayName() string {
	if m.connect == nil {
		return "—"
	}
	if m.connect.Provider == "compatible" && strings.TrimSpace(m.connect.Name) != "" {
		return m.connect.Name
	}
	if strings.TrimSpace(m.connect.Provider) == "" {
		return "pending"
	}
	return m.connect.Provider
}

func (m Model) connectPreviewName() string {
	if m.connect != nil && (m.connect.Step == connectProvider || m.connect.Step == connectName) && m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
		return m.suggestions[m.completionIndex].Value
	}
	return m.connectDisplayName()
}

func (m Model) connectEndpointPreview() string {
	if m.connect == nil {
		return "—"
	}
	if strings.TrimSpace(m.connect.BaseURL) != "" {
		return m.connect.BaseURL
	}
	if m.connect.Step == connectProvider || m.connect.Step == connectName {
		profile := connectProfileFor(m.connectPreviewName())
		if profile.Endpoint != "" {
			return profile.Endpoint
		}
	}
	if m.connect.Step == connectBaseURL && m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
		return m.suggestions[m.completionIndex].Value
	}
	switch m.connect.Provider {
	case "openai":
		return "api.openai.com/v1"
	case "codex":
		return "~/.codex/auth.json"
	case "anthropic":
		return "api.anthropic.com/v1"
	case "ollama":
		return m.cfg.OllamaURL
	case "compatible":
		return m.defaultCompatibleBaseURL()
	default:
		return "pending"
	}
}

func (m Model) connectCredentialPreview() string {
	if m.connect == nil {
		return "—"
	}
	if strings.TrimSpace(m.connect.APIKey) != "" {
		return "runtime key entered"
	}
	if m.connect.Step == connectAPIKey && strings.TrimSpace(m.input.Value()) != "" {
		return "runtime key entered"
	}
	if m.cfg.CredentialForConnection(m.connectConnectionID()) != "" {
		return "remembered key loaded"
	}
	if m.connect.Step == connectProvider || m.connect.Step == connectName {
		profile := connectProfileFor(m.connectPreviewName())
		if profile.Auth == "not required" || profile.Auth == "usually none" {
			return profile.Auth
		}
		if profile.Env != "" && strings.TrimSpace(os.Getenv(profile.Env)) != "" {
			return profile.Env + " detected"
		}
		return profile.Auth
	}
	profile := connectProfileFor(m.connectDisplayName())
	if profile.Auth == "Codex login" {
		return "Codex ChatGPT login"
	}
	if profile.Badge == "LOCAL" && (profile.Name == "ollama" || profile.Name == "lm-studio") {
		return "not required"
	}
	for _, env := range m.connectCredentialEnvs() {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return env + " detected"
		}
	}
	if m.connectKeyOptional() {
		return "optional / not entered"
	}
	if m.connect.Step == connectAPIKey {
		return "waiting for key"
	}
	return "not entered"
}

func (m Model) connectCredentialEnvs() []string {
	if m.connect == nil {
		return nil
	}
	switch m.connect.Provider {
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "codex":
		return nil
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY"}
	case "compatible":
		name := m.connect.Name
		if preset, ok := config.Preset(name); ok && preset.APIKeyEnv != "" {
			return []string{preset.APIKeyEnv, "EPHEMERA_API_KEY"}
		}
		return []string{config.DefaultAPIKeyEnv(name), "EPHEMERA_API_KEY"}
	default:
		return nil
	}
}

func (m Model) connectModelPreview() string {
	if m.connect == nil {
		return "—"
	}
	if m.connect.Step == connectModel {
		if value := strings.TrimSpace(m.input.Value()); value != "" {
			return value
		}
		if m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
			return m.suggestions[m.completionIndex].Value
		}
	}
	if strings.TrimSpace(m.connect.Model) != "" {
		return m.connect.Model
	}
	return "not selected"
}

func (m Model) connectCatalogPreview() string {
	if m.connect == nil {
		return "—"
	}
	if m.connect.Step != connectModel && m.connect.Step != connectReview {
		return "verification pending"
	}
	state, ok := m.cachedModelCatalogForConfig(m.connectModelListConfig())
	if !ok {
		return "loading"
	}
	if state.Err != nil {
		return "unavailable · check route"
	}
	return fmt.Sprintf("verified · %d available", len(state.Models))
}

func (m Model) connectRequirement() string {
	if m.connect == nil {
		return ""
	}
	switch m.connect.Step {
	case connectProvider, connectModel:
		return "REQUIRED"
	case connectName:
		return "OPTIONAL NAME"
	case connectBaseURL:
		return "DEFAULT AVAILABLE"
	case connectAPIKey:
		if m.connectKeyOptional() || m.connectCredentialPreview() == "not required" {
			return "OPTIONAL"
		}
		if strings.Contains(m.connectCredentialPreview(), " detected") {
			return "ENV DETECTED"
		}
		return "SECRET"
	case connectReview:
		return "CONFIRM"
	default:
		return ""
	}
}

func (m Model) connectDefaultHint() string {
	if m.connect == nil {
		return ""
	}
	switch m.connect.Step {
	case connectBaseURL:
		if m.connect.Provider == "ollama" {
			return m.cfg.OllamaURL
		}
		return m.defaultCompatibleBaseURL()
	case connectAPIKey:
		for _, env := range m.connectCredentialEnvs() {
			if strings.TrimSpace(os.Getenv(env)) != "" {
				return "Enter uses " + env
			}
		}
		return "kept in memory only"
	case connectModel:
		state, ok := m.cachedModelCatalogForConfig(m.connectModelListConfig())
		switch {
		case !ok:
			return "catalog loads automatically"
		case state.Err != nil:
			return "fix endpoint or credentials"
		default:
			return fmt.Sprintf("%d provider-advertised models", len(state.Models))
		}
	case connectReview:
		return "no changes until Enter"
	default:
		return ""
	}
}

func (m Model) connectFieldState() (string, color.Color) {
	if m.connect == nil {
		return "IDLE", m.styles.Faint
	}
	value := strings.TrimSpace(m.input.Value())
	switch m.connect.Step {
	case connectProvider:
		candidate := value
		if candidate == "" && m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
			candidate = m.suggestions[m.completionIndex].Value
		}
		if _, ok := config.Preset(candidate); ok || candidate == "compatible" {
			return "✓ READY TO ADVANCE", m.styles.AccentSoft
		}
		return "TYPE OR SELECT A PROVIDER", m.styles.Warning
	case connectName:
		return "✓ BLANK USES ‘compatible’", m.styles.AccentSoft
	case connectBaseURL:
		candidate := value
		if candidate == "" {
			if m.connect.Provider == "ollama" {
				candidate = m.cfg.OllamaURL
			} else {
				candidate = m.defaultCompatibleBaseURL()
			}
		}
		if err := validateEndpoint(candidate); err != nil {
			return "! " + strings.ToUpper(err.Error()), m.styles.Warning
		}
		return "✓ VALID ENDPOINT", m.styles.AccentSoft
	case connectAPIKey:
		preview := m.connectCredentialPreview()
		switch {
		case strings.Contains(preview, "entered"), strings.Contains(preview, "detected"), strings.Contains(preview, "remembered"), preview == "not required":
			return "✓ " + strings.ToUpper(preview), m.styles.AccentSoft
		case m.connectKeyOptional():
			return "✓ OPTIONAL FOR THIS ROUTE", m.styles.AccentSoft
		default:
			return "KEY NOT YET PROVIDED", m.styles.Warning
		}
	case connectModel:
		state, ok := m.cachedModelCatalogForConfig(m.connectModelListConfig())
		if !ok {
			return "LOADING PROVIDER CATALOG", m.styles.Muted
		}
		if state.Err != nil {
			return "! CATALOG UNAVAILABLE — CHECK ROUTE", m.styles.Warning
		}
		candidate := value
		if candidate == "" && m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
			candidate = m.suggestions[m.completionIndex].Value
		}
		for _, model := range state.Models {
			if candidate == model {
				return "✓ AVAILABLE FROM PROVIDER", m.styles.AccentSoft
			}
		}
		if candidate == "" {
			return fmt.Sprintf("SELECT 1 OF %d AVAILABLE MODELS", len(state.Models)), m.styles.Warning
		}
		return "! NOT IN PROVIDER CATALOG", m.styles.Warning
	case connectReview:
		return "✓ READY TO ACTIVATE", m.styles.AccentBright
	default:
		return "", m.styles.Faint
	}
}

func (m Model) connectKeyOptional() bool {
	if m.connect == nil {
		return false
	}
	return !m.connectKeyRequired()
}
