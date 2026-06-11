package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

type commandExample struct {
	Input       string
	Description string
}

type commandSpec struct {
	Name        string
	Usage       string
	Description string
	Category    string
	Aliases     []string
	Introduced  string
	Permission  string
	Choices     []string
	Examples    []commandExample
}

type suggestion struct {
	Value       string
	Label       string
	Description string
}

var commandSpecs = []commandSpec{
	{
		Name: "/help", Usage: "[command]", Description: "show the command map and contextual help", Category: "CORE", Aliases: []string{"?"}, Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/help", "show all commands"}, {"/help connect", "inspect one command"}, {"/help mode", "see mode usage"}},
	},
	{
		Name: "/connect", Usage: "[provider]", Description: "guided provider and credential setup", Category: "ROUTE", Introduced: "v0.2.0", Permission: "credentials", Choices: config.ConnectNames(),
		Examples: []commandExample{{"/connect", "start guided setup"}, {"/connect openai", "configure OpenAI"}, {"/connect openrouter", "use a compatible preset"}},
	},
	{
		Name: "/clear", Description: "clear the current conversation", Category: "SESSION", Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/clear", "remove visible conversation"}},
	},
	{
		Name: "/new", Usage: "[name]", Description: "begin a new named session", Category: "SESSION", Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/new", "create an automatic name"}, {"/new launch-notes", "create a named session"}},
	},
	{
		Name: "/save", Usage: "[name]", Description: "save or rename this session", Category: "SESSION", Introduced: "v0.1.0", Permission: "filesystem",
		Examples: []commandExample{{"/save", "save current session"}, {"/save research", "save under a new name"}},
	},
	{
		Name: "/load", Usage: "<name>", Description: "load a saved session", Category: "SESSION", Introduced: "v0.1.0", Permission: "filesystem",
		Examples: []commandExample{{"/load research", "open a saved session"}},
	},
	{
		Name: "/sessions", Description: "list saved sessions", Category: "SESSION", Introduced: "v0.1.0", Permission: "filesystem",
		Examples: []commandExample{{"/sessions", "show session names"}},
	},
	{
		Name: "/provider", Usage: "<provider>", Description: "switch the active provider", Category: "ROUTE", Introduced: "v0.1.0", Permission: "local", Choices: config.ProviderNames(),
		Examples: []commandExample{{"/provider openai", "select OpenAI"}, {"/provider ollama", "select local Ollama"}},
	},
	{
		Name: "/model", Usage: "<model-id>", Description: "switch the active model", Category: "ROUTE", Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/model gpt-4.1-mini", "select a model by ID"}},
	},
	{
		Name: "/models", Description: "open the provider model chooser", Category: "ROUTE", Introduced: "v0.2.0", Permission: "network",
		Examples: []commandExample{{"/models", "browse available models"}},
	},
	{
		Name: "/mode", Usage: "<mode>", Description: "change the reasoning profile", Category: "RESPONSE", Introduced: "v0.1.0", Permission: "local", Choices: []string{"normal", "deep-reason", "concise", "creative"},
		Examples: []commandExample{{"/mode concise", "prefer brief responses"}, {"/mode deep-reason", "use the deeper profile"}},
	},
	{
		Name: "/usage", Description: "inspect context and message usage", Category: "CONTEXT", Introduced: "v0.3.0", Permission: "local",
		Examples: []commandExample{{"/usage", "show current context usage"}},
	},
	{
		Name: "/budget", Usage: "<tokens>", Description: "set the context token budget", Category: "CONTEXT", Introduced: "v0.3.0", Permission: "local",
		Examples: []commandExample{{"/budget 8192", "set an 8k token budget"}},
	},
	{
		Name: "/retry", Description: "retry the latest user prompt", Category: "RESPONSE", Introduced: "v0.3.0", Permission: "network",
		Examples: []commandExample{{"/retry", "regenerate the last answer"}},
	},
	{
		Name: "/undo", Description: "remove the latest message", Category: "SESSION", Introduced: "v0.3.0", Permission: "filesystem",
		Examples: []commandExample{{"/undo", "remove the latest message"}},
	},
	{
		Name: "/export", Usage: "[path]", Description: "export the transcript as Markdown", Category: "OUTPUT", Introduced: "v0.3.0", Permission: "filesystem",
		Examples: []commandExample{{"/export", "export to the default folder"}, {"/export notes.md", "export to a chosen path"}},
	},
	{
		Name: "/doctor", Description: "inspect route, credentials, and context", Category: "SYSTEM", Introduced: "v0.3.0", Permission: "local",
		Examples: []commandExample{{"/doctor", "open a diagnostic report"}},
	},
	{
		Name: "/theme", Usage: "<theme>", Description: "change the terminal palette", Category: "VIEW", Introduced: "v0.1.0", Permission: "local", Choices: []string{"rose", "mono"},
		Examples: []commandExample{{"/theme rose", "use the pink palette"}, {"/theme mono", "use monochrome"}},
	},
	{
		Name: "/copy", Description: "copy the last answer", Category: "OUTPUT", Introduced: "v0.1.0", Permission: "clipboard",
		Examples: []commandExample{{"/copy", "copy the latest answer"}},
	},
	{
		Name: "/quit", Description: "save and leave Ephemera", Category: "CORE", Aliases: []string{"/exit"}, Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/quit", "save and exit"}},
	},
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
		return append(prefixMatches, containsMatches...)
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
	case "/help":
		out := make([]suggestion, 0, len(commandSpecs)-1)
		for _, command := range commandSpecs {
			if command.Name == "/help" {
				continue
			}
			out = append(out, suggestion{
				Value:       strings.TrimPrefix(command.Name, "/"),
				Label:       command.Name + usageSuffix(command.Usage),
				Description: command.Description,
			})
		}
		return out
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
		credentialPresence(cfg.OpenAIKey),
		credentialPresence(cfg.AnthropicKey),
		credentialPresence(cfg.CompatibleKey),
		cfg.Model(),
	}, "\x00")
}

func credentialPresence(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unset"
	}
	return "set"
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
	paletteHeight := m.effectivePaletteHeight()
	if paletteHeight <= panelBorderRows {
		return 0
	}
	innerHeight := paletteHeight - panelBorderRows
	// Reserve enough detail space to explain the active field, but favor a
	// larger visible result set during provider/model selection. Seeing only
	// two choices at a time made the wizard feel unnecessarily cramped.
	detailReserve := 5
	maxVisible := 6
	if m.connect != nil {
		detailReserve = 6
		maxVisible = 7
	}
	capacity := innerHeight - detailReserve - 2 // header + divider
	if capacity < 1 {
		capacity = 1
	}
	return minInt(maxVisible, capacity)
}

func (m Model) effectivePaletteHeight() int {
	if m.paletteHeight > 0 {
		return m.paletteHeight
	}
	return m.suggestionHeight()
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
	// Keep the active row near the middle of the viewport so moving through a
	// long provider/model list preserves spatial context in both directions.
	start := m.completionIndex - limit/2
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
	return m.layoutMetrics().paletteOuterHeight
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
