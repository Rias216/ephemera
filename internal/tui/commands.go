package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type commandSpec struct {
	Name        string
	Usage       string
	Description string
	Choices     []string
}

type suggestion struct {
	Value       string
	Label       string
	Description string
}

var commandSpecs = []commandSpec{
	{Name: "/help", Description: "show the command map"},
	{Name: "/connect", Usage: "[provider]", Description: "guided provider connection", Choices: config.ConnectNames()},
	{Name: "/clear", Description: "clear the current conversation"},
	{Name: "/new", Usage: "[name]", Description: "begin a new session"},
	{Name: "/save", Usage: "[name]", Description: "save or rename this session"},
	{Name: "/load", Usage: "<name>", Description: "load a saved session"},
	{Name: "/sessions", Description: "list saved sessions"},
	{Name: "/provider", Usage: "<provider>", Description: "switch provider", Choices: config.ProviderNames()},
	{Name: "/model", Usage: "<model-id>", Description: "switch model"},
	{Name: "/models", Description: "open model chooser"},
	{Name: "/mode", Usage: "<mode>", Description: "change reasoning mode", Choices: []string{"normal", "deep-reason", "concise", "creative"}},
	{Name: "/theme", Usage: "<theme>", Description: "change palette", Choices: []string{"rose", "mono"}},
	{Name: "/copy", Description: "copy the last answer"},
	{Name: "/quit", Description: "save and leave Ephemera"},
}

func (m *Model) rebuildSuggestions() {
	previousHeight := m.paletteHeight
	previous := ""
	if m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
		previous = m.suggestions[m.completionIndex].Value
	}

	if m.connect != nil {
		m.suggestions = m.connectSuggestions()
	} else {
		m.suggestions = m.commandSuggestions(m.input.Value())
	}

	m.completionIndex = 0
	if previous != "" {
		for i, item := range m.suggestions {
			if item.Value == previous {
				m.completionIndex = i
				break
			}
		}
	}
	if m.ready && m.suggestionHeight() != previousHeight {
		m.resize()
	}
}

func (m *Model) commandSuggestions(raw string) []suggestion {
	if !strings.HasPrefix(raw, "/") {
		return nil
	}

	command, argPrefix, hasArgs := splitCommandInput(raw)
	if !hasArgs {
		query := strings.ToLower(command)
		var prefixMatches []suggestion
		var containsMatches []suggestion
		for _, spec := range commandSpecs {
			item := suggestion{
				Value:       completionValue(spec),
				Label:       spec.Name + usageSuffix(spec.Usage),
				Description: spec.Description,
			}
			name := strings.ToLower(spec.Name)
			switch {
			case strings.HasPrefix(name, query):
				prefixMatches = append(prefixMatches, item)
			case strings.Contains(name, strings.TrimPrefix(query, "/")):
				containsMatches = append(containsMatches, item)
			}
		}
		return limitSuggestions(append(prefixMatches, containsMatches...), 7)
	}

	spec, ok := findCommandSpec(command)
	if !ok {
		return nil
	}
	choices := m.commandChoiceSuggestions(spec)
	if len(choices) == 0 {
		return nil
	}

	query := strings.ToLower(strings.TrimSpace(argPrefix))
	var out []suggestion
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice.Value+" "+choice.Label+" "+choice.Description), query) {
			continue
		}
		out = append(out, suggestion{
			Value:       spec.Name + " " + choice.Value,
			Label:       choice.Label,
			Description: choice.Description,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		iPrefix := strings.HasPrefix(strings.ToLower(out[i].Label), query)
		jPrefix := strings.HasPrefix(strings.ToLower(out[j].Label), query)
		if iPrefix != jPrefix {
			return iPrefix
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func (m *Model) commandChoiceSuggestions(spec commandSpec) []suggestion {
	switch spec.Name {
	case "/load":
		names, err := m.listSessions()
		if err != nil {
			return nil
		}
		out := make([]suggestion, 0, len(names))
		for _, name := range names {
			out = append(out, suggestion{Value: name, Label: name, Description: argumentDescription(spec.Name, name)})
		}
		return out
	case "/model":
		return m.modelSuggestionsForConfig(m.cfg)
	default:
		out := make([]suggestion, 0, len(spec.Choices))
		for _, choice := range spec.Choices {
			out = append(out, suggestion{Value: choice, Label: choice, Description: argumentDescription(spec.Name, choice)})
		}
		return out
	}
}

func (m *Model) modelSuggestionsForConfig(cfg config.Config) []suggestion {
	key := modelCacheKey(cfg)
	if m.modelSuggestionCache != nil {
		if cached, ok := m.modelSuggestionCache[key]; ok {
			return cached
		}
	} else {
		m.modelSuggestionCache = map[string][]suggestion{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	models, err := llm.ListModels(ctx, cfg)
	fallback := false
	if err != nil || len(models) == 0 {
		models = llm.KnownModelIDs(cfg.Provider)
		fallback = true
	}
	out := make([]suggestion, 0, len(models)+1)
	seen := map[string]struct{}{}
	add := func(model, description string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		out = append(out, suggestion{Value: model, Label: model, Description: description})
	}
	add(cfg.Model(), "current selection")
	description := "provider model"
	if fallback {
		description = "suggested " + cfg.Provider + " model"
	}
	for _, model := range models {
		add(model, description)
	}
	m.modelSuggestionCache[key] = out
	return out
}

func modelCacheKey(cfg config.Config) string {
	return strings.Join([]string{
		cfg.Provider,
		cfg.OllamaURL,
		cfg.CompatibleName,
		cfg.CompatibleURL,
		cfg.OpenAIKey,
		cfg.AnthropicKey,
		cfg.CompatibleKey,
		cfg.Model(),
	}, "\x00")
}

func (m *Model) acceptCommandSuggestionForEnter() bool {
	if len(m.suggestions) == 0 {
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		return false
	}
	return m.acceptSuggestion()
}

func (m Model) commandNeedsMoreInput() bool {
	command, argPrefix, hasArgs := splitCommandInput(m.input.Value())
	spec, ok := findCommandSpec(command)
	if !ok || !usageRequiresValue(spec.Usage) {
		return false
	}
	return !hasArgs || strings.TrimSpace(argPrefix) == ""
}

func usageRequiresValue(usage string) bool {
	return strings.HasPrefix(strings.TrimSpace(usage), "<")
}

func splitCommandInput(raw string) (command, argPrefix string, hasArgs bool) {
	index := strings.IndexRune(raw, ' ')
	if index < 0 {
		return strings.TrimSpace(raw), "", false
	}
	return strings.TrimSpace(raw[:index]), raw[index+1:], true
}

func findCommandSpec(name string) (commandSpec, bool) {
	for _, spec := range commandSpecs {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func completionValue(spec commandSpec) string {
	if spec.Usage != "" {
		return spec.Name + " "
	}
	return spec.Name
}

func usageSuffix(usage string) string {
	if usage == "" {
		return ""
	}
	return " " + usage
}

func argumentDescription(command, choice string) string {
	switch command {
	case "/provider", "/connect":
		if choice == "compatible" {
			return "custom OpenAI-compatible API"
		}
		if preset, ok := config.Preset(choice); ok && preset.Protocol == config.ProtocolOpenAICompatible {
			return "OpenAI-compatible preset"
		}
		return "connect to " + choice
	case "/mode":
		return "reasoning profile"
	case "/theme":
		return "terminal palette"
	case "/load":
		return "saved session"
	default:
		return ""
	}
}

func limitSuggestions(items []suggestion, limit int) []suggestion {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func (m *Model) moveSuggestion(delta int) {
	if len(m.suggestions) == 0 {
		return
	}
	m.completionIndex = (m.completionIndex + delta + len(m.suggestions)) % len(m.suggestions)
}

func (m *Model) acceptSuggestion() bool {
	if len(m.suggestions) == 0 {
		return false
	}
	if m.completionIndex < 0 || m.completionIndex >= len(m.suggestions) {
		m.completionIndex = 0
	}
	m.input.SetValue(m.suggestions[m.completionIndex].Value)
	m.input.CursorEnd()
	m.rebuildSuggestions()
	return true
}

func (m Model) suggestionPaletteActive() bool {
	return m.connect != nil || strings.HasPrefix(m.input.Value(), "/")
}

func (m Model) suggestionCapacity() int {
	limit := 7
	if m.height > 0 {
		// Keep at least three transcript rows. A palette row costs one line,
		// while its border and extra block separator cost three more.
		limit = minInt(limit, maxInt(0, m.height-18))
	}
	return limit
}

func (m Model) suggestionWindow() ([]suggestion, int) {
	if len(m.suggestions) == 0 {
		return nil, 0
	}
	limit := m.suggestionCapacity()
	if limit <= 0 {
		return nil, 0
	}
	if len(m.suggestions) <= limit {
		return m.suggestions, 0
	}
	start := m.completionIndex - limit + 1
	if start < 0 {
		start = 0
	}
	if start+limit > len(m.suggestions) {
		start = len(m.suggestions) - limit
	}
	return m.suggestions[start : start+limit], start
}

func (m Model) suggestionHeight() int {
	if !m.suggestionPaletteActive() {
		return 0
	}
	capacity := m.suggestionCapacity()
	if capacity <= 0 {
		return 0
	}
	// Reserve a fixed palette height for the entire command entry. Narrowing
	// from seven matches to one no longer grows the viewport by six rows and
	// forces a large terminal repaint on every keystroke.
	return capacity + 3
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
