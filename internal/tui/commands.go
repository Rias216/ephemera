package tui

import (
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/config"
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
	{Name: "/connect", Usage: "[provider]", Description: "guided provider connection", Choices: config.ProviderNames()},
	{Name: "/clear", Description: "clear the current conversation"},
	{Name: "/new", Usage: "[name]", Description: "begin a new session"},
	{Name: "/save", Usage: "[name]", Description: "save or rename this session"},
	{Name: "/load", Usage: "<name>", Description: "load a saved session"},
	{Name: "/sessions", Description: "list saved sessions"},
	{Name: "/provider", Usage: "<provider>", Description: "switch provider", Choices: config.ProviderNames()},
	{Name: "/model", Usage: "<model-id>", Description: "switch model"},
	{Name: "/mode", Usage: "<mode>", Description: "change reasoning mode", Choices: []string{"normal", "deep-reason", "concise", "creative"}},
	{Name: "/theme", Usage: "<theme>", Description: "change palette", Choices: []string{"rose", "mono"}},
	{Name: "/copy", Description: "copy the last answer"},
	{Name: "/quit", Description: "save and leave Ephemera"},
}

func (m *Model) rebuildSuggestions() {
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
	if m.ready {
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
	choices := append([]string(nil), spec.Choices...)
	if spec.Name == "/load" {
		if names, err := m.store.List(); err == nil {
			choices = names
		}
	}
	if len(choices) == 0 {
		return nil
	}

	query := strings.ToLower(strings.TrimSpace(argPrefix))
	var out []suggestion
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice), query) {
			continue
		}
		out = append(out, suggestion{
			Value:       spec.Name + " " + choice,
			Label:       choice,
			Description: argumentDescription(spec.Name, choice),
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
	return limitSuggestions(out, 7)
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

func (m Model) suggestionWindow() ([]suggestion, int) {
	if len(m.suggestions) == 0 {
		return nil, 0
	}
	limit := 7
	if m.height > 0 {
		// Keep at least three transcript rows. A palette row costs one line,
		// while its border and extra block separator cost three more.
		limit = minInt(limit, maxInt(0, m.height-18))
	}
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
	items, _ := m.suggestionWindow()
	if len(items) == 0 {
		return 0
	}
	return len(items) + 3
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
