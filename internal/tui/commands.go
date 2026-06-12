package tui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
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

const modelRouteSeparator = "::"

type modelCatalogState struct {
	Models    []string
	Err       error
	CheckedAt time.Time
}

const (
	modelCatalogSuccessTTL = 2 * time.Minute
	modelCatalogFailureTTL = 3 * time.Second
)

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
		Name: "/sessions", Usage: "[query]", Description: "list or search saved sessions", Category: "SESSION", Introduced: "v0.1.0", Permission: "filesystem",
		Examples: []commandExample{{"/sessions", "show session names"}, {"/sessions renderer", "search saved session text"}},
	},
	{
		Name: "/provider", Usage: "<connected-route>", Description: "activate a remembered provider route", Category: "ROUTE", Introduced: "v0.1.0", Permission: "local", Choices: config.ProviderNames(),
		Examples: []commandExample{{"/provider openai", "activate remembered OpenAI"}, {"/provider openrouter", "activate remembered OpenRouter"}},
	},
	{
		Name: "/model", Usage: "<model-id>", Description: "select a model across all remembered routes", Category: "ROUTE", Introduced: "v0.1.0", Permission: "local",
		Examples: []commandExample{{"/model gpt-4.1-mini", "select a model and its route"}},
	},
	{
		Name: "/models", Description: "browse models from every remembered connection", Category: "ROUTE", Introduced: "v0.2.0", Permission: "network",
		Examples: []commandExample{{"/models", "browse all connected models"}},
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
		Name: "/agent", Usage: "<on|auto|safe|read-only|status>", Description: "control project-agent mode and approval behavior", Category: "AGENT", Introduced: "v0.4.0", Permission: "local", Choices: []string{"on", "auto", "safe", "read-only", "status"},
		Examples: []commandExample{{"/agent auto", "run every requested tool immediately"}, {"/agent safe", "require approval for writes and shell"}, {"/agent status", "show agent settings"}},
	},
	{
		Name: "/approval", Usage: "<auto|safe|read-only|workspace-write|chat>", Description: "set the agent approval policy", Category: "AGENT", Introduced: "v0.4.1", Permission: "workspace", Choices: config.ApprovalPolicyChoices(),
		Examples: []commandExample{{"/approval auto", "approve and run every agent tool"}, {"/approval safe", "prompt before writes and shell"}},
	},
	{
		Name: "/approve", Description: "run the pending agent action", Category: "AGENT", Introduced: "v0.4.0", Permission: "workspace",
		Examples: []commandExample{{"/approve", "execute the pending patch or command"}},
	},
	{
		Name: "/reject", Description: "reject the pending agent action", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/reject", "skip the pending patch or command"}},
	},
	{
		Name: "/plan", Description: "show the latest agent plan", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/plan", "inspect the current agent plan"}},
	},
	{
		Name: "/surface", Description: "open the persisted Beneath the Surface trace", Category: "AGENT", Introduced: "v0.6.0", Permission: "local",
		Examples: []commandExample{{"/surface", "review the latest goal, evidence, plan, and verification after completion"}},
	},
	{
		Name: "/tools", Description: "list local agent tools", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/tools", "show tool names and risk levels"}},
	},
	{
		Name: "/eval", Description: "run deterministic local agent capability eval", Category: "AGENT", Introduced: "v0.6.0", Permission: "local",
		Examples: []commandExample{{"/eval", "check read, write, native tool, repair, and verification behavior"}},
	},
	{
		Name: "/sandbox", Usage: "<none|snapshot|docker>", Description: "set shell and workspace isolation mode", Category: "SAFETY", Introduced: "v0.7.0", Permission: "workspace", Choices: []string{"none", "snapshot", "docker"},
		Examples: []commandExample{{"/sandbox snapshot", "capture a restorable workspace snapshot before writes"}, {"/sandbox docker", "run commands in an offline Docker container"}},
	},
	{
		Name: "/dry-run", Usage: "<on|off|toggle>", Description: "preview writes and commands without executing them", Category: "SAFETY", Introduced: "v0.7.0", Permission: "local", Choices: []string{"on", "off", "toggle"},
		Examples: []commandExample{{"/dry-run on", "preview the next agent run"}, {"/dry-run off", "allow approved changes again"}},
	},
	{
		Name: "/rollback", Usage: "[now|auto|manual|status]", Description: "restore the latest snapshot or configure automatic rollback", Category: "SAFETY", Introduced: "v0.7.0", Permission: "workspace", Choices: []string{"now", "auto", "manual", "status"},
		Examples: []commandExample{{"/rollback", "restore the latest retained snapshot"}, {"/rollback auto", "restore automatically when a run fails"}, {"/rollback manual", "retain failed-run snapshots for manual restore"}},
	},
	{
		Name: "/index", Usage: "<on|off|rebuild|status>", Description: "control the persistent semantic codebase index", Category: "CONTEXT", Introduced: "v0.7.0", Permission: "filesystem", Choices: []string{"on", "off", "rebuild", "status"},
		Examples: []commandExample{{"/index rebuild", "discard and lazily rebuild the codebase index"}, {"/index off", "disable semantic codebase context"}},
	},
	{
		Name: "/tdd", Usage: "<on|off|toggle>", Description: "control test-first implementation guidance", Category: "AGENT", Introduced: "v0.7.0", Permission: "local", Choices: []string{"on", "off", "toggle"},
		Examples: []commandExample{{"/tdd on", "prefer failing tests before implementation"}, {"/tdd off", "use the normal implementation workflow"}},
	},
	{
		Name: "/learn", Usage: "<on|off|toggle>", Description: "control episodic project learning after successful runs", Category: "MEMORY", Introduced: "v0.7.0", Permission: "filesystem", Choices: []string{"on", "off", "toggle"},
		Examples: []commandExample{{"/learn on", "save compact task learnings to project memory"}, {"/learn off", "stop writing learned memories"}},
	},
	{
		Name: "/thinking", Usage: "<on|off|toggle>", Description: "show or hide Beneath the Surface decision traces", Category: "AGENT", Introduced: "v0.4.0", Permission: "local", Choices: []string{"on", "off", "toggle"},
		Examples: []commandExample{{"/thinking on", "show goal, assumptions, approach, tool rationale, and verification"}, {"/thinking off", "hide visible reasoning traces"}},
	},
	{
		Name: "/details", Description: "toggle detailed tool call output", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/details", "show or hide tool details"}},
	},
	{
		Name: "/run", Description: "continue the agent loop", Category: "AGENT", Introduced: "v0.4.0", Permission: "network",
		Examples: []commandExample{{"/run", "resume after reviewing the timeline"}},
	},
	{
		Name: "/stop", Description: "cancel the active streaming agent run", Category: "AGENT", Introduced: "v0.5.0", Permission: "local",
		Examples: []commandExample{{"/stop", "cancel the current model or tool step"}},
	},
	{
		Name: "/diff", Description: "ask the agent to inspect git diff", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/diff", "show changed files and patch context"}},
	},
	{
		Name: "/compact", Description: "compact old timeline context", Category: "AGENT", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/compact", "trim older agent events"}},
	},
	{
		Name: "/config", Description: "show saved runtime configuration", Category: "SYSTEM", Introduced: "v0.4.0", Permission: "local",
		Examples: []commandExample{{"/config", "inspect agent and token settings"}},
	},
	{
		Name: "/memory", Description: "show project memory sources", Category: "AGENT", Introduced: "v0.4.0", Permission: "filesystem",
		Examples: []commandExample{{"/memory", "inspect project memory guidance"}},
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
	case "/provider":
		routes := m.cfg.ConnectedConnections()
		out := make([]suggestion, 0, len(routes))
		for _, route := range routes {
			description := route.Connection.Provider + " · connected"
			if route.ID == m.cfg.ActiveConnection {
				description = route.Connection.Provider + " · current route"
			}
			out = append(out, suggestion{
				Value:       route.Connection.DisplayName(),
				Label:       route.Connection.DisplayName(),
				Description: description,
			})
		}
		return out
	case "/model":
		return m.connectedModelSuggestions()
	default:
		out := make([]suggestion, 0, len(spec.Choices))
		for _, choice := range spec.Choices {
			out = append(out, suggestion{Value: choice, Label: choice, Description: argumentDescription(spec.Name, choice)})
		}
		return out
	}
}

func (m *Model) connectedModelSuggestions() []suggestion {
	routes := m.cfg.ConnectedConnections()
	out := make([]suggestion, 0)
	for _, route := range routes {
		cfg, ok := m.cfg.ConfigForConnection(route.ID)
		if !ok {
			continue
		}
		catalog := m.modelCatalogForConfig(cfg, false)
		if catalog.Err != nil {
			continue
		}
		display := route.Connection.DisplayName()
		for _, model := range catalog.Models {
			description := display + " · connected"
			if route.ID == m.cfg.ActiveConnection && model == m.cfg.Model() {
				description = display + " · current selection"
			}
			out = append(out, suggestion{
				Value:       modelSelectionValue(route.ID, model),
				Label:       model,
				Description: description,
			})
		}
	}
	return out
}

func modelSelectionValue(connectionID, model string) string {
	return strings.TrimSpace(connectionID) + modelRouteSeparator + strings.TrimSpace(model)
}

func parseModelSelection(value string) (connectionID, model string, explicit bool) {
	parts := strings.SplitN(strings.TrimSpace(value), modelRouteSeparator, 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", strings.TrimSpace(value), false
	}
	return strings.ToLower(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1]), true
}

func (m *Model) findConnectedModel(model string) (string, config.Config, bool) {
	model = strings.TrimSpace(model)
	var firstID string
	var firstConfig config.Config
	for _, route := range m.cfg.ConnectedConnections() {
		cfg, ok := m.cfg.ConfigForConnection(route.ID)
		if !ok {
			continue
		}
		available, err := m.modelAvailableForConfig(cfg, model, false)
		if err != nil || !available {
			continue
		}
		if route.ID == m.cfg.ActiveConnection {
			return route.ID, cfg, true
		}
		if firstID == "" {
			firstID = route.ID
			firstConfig = cfg
		}
	}
	return firstID, firstConfig, firstID != ""
}

func (m *Model) modelSuggestionsForConfig(cfg config.Config) []suggestion {
	catalog := m.modelCatalogForConfig(cfg, false)
	if catalog.Err != nil {
		return nil
	}
	out := make([]suggestion, 0, len(catalog.Models))
	current := strings.TrimSpace(cfg.Model())
	for _, model := range catalog.Models {
		description := "available from provider"
		if model == current {
			description = "available · current selection"
		}
		out = append(out, suggestion{Value: model, Label: model, Description: description})
	}
	return out
}

func (m *Model) modelCatalogForConfig(cfg config.Config, force bool) modelCatalogState {
	key := modelCacheKey(cfg)
	if m.modelCatalogCache == nil {
		m.modelCatalogCache = map[string]modelCatalogState{}
	}
	if !force {
		if cached, ok := m.modelCatalogCache[key]; ok {
			ttl := modelCatalogSuccessTTL
			if cached.Err != nil {
				ttl = modelCatalogFailureTTL
			}
			if time.Since(cached.CheckedAt) < ttl {
				return cached
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	models, err := llm.ListModels(ctx, cfg)
	state := modelCatalogState{Models: models, Err: err, CheckedAt: time.Now()}
	if err == nil && len(models) == 0 {
		state.Err = fmt.Errorf("provider returned an empty model catalog")
	}
	m.modelCatalogCache[key] = state
	return state
}

func (m Model) cachedModelCatalogForConfig(cfg config.Config) (modelCatalogState, bool) {
	if m.modelCatalogCache == nil {
		return modelCatalogState{}, false
	}
	state, ok := m.modelCatalogCache[modelCacheKey(cfg)]
	return state, ok
}

func (m *Model) modelAvailableForConfig(cfg config.Config, model string, force bool) (bool, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return false, fmt.Errorf("model ID is empty")
	}
	catalog := m.modelCatalogForConfig(cfg, force)
	if catalog.Err != nil {
		return false, catalog.Err
	}
	for _, available := range catalog.Models {
		if available == model {
			return true, nil
		}
	}
	return false, nil
}

func modelCacheKey(cfg config.Config) string {
	return strings.Join([]string{
		cfg.Provider,
		cfg.OllamaURL,
		cfg.CompatibleName,
		cfg.CompatibleURL,
		credentialFingerprint(effectiveCatalogCredential(cfg)),
	}, "\x00")
}

func effectiveCatalogCredential(cfg config.Config) string {
	switch cfg.Provider {
	case "openai":
		return firstPresent(cfg.OpenAIKey, os.Getenv("OPENAI_API_KEY"))
	case "anthropic":
		return firstPresent(cfg.AnthropicKey, os.Getenv("ANTHROPIC_API_KEY"))
	case "compatible":
		return firstPresent(
			cfg.CompatibleKey,
			os.Getenv(config.DefaultAPIKeyEnv(cfg.CompatibleName)),
			os.Getenv("EPHEMERA_API_KEY"),
		)
	default:
		return ""
	}
}

func firstPresent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func credentialFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unset"
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:8])
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
	case "/sandbox":
		return "execution isolation mode"
	case "/dry-run", "/tdd", "/learn":
		return "feature state"
	case "/rollback":
		return "rollback behavior"
	case "/index":
		return "codebase index action"
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
