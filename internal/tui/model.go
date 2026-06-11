// Package tui implements Ephemera's Bubble Tea terminal interface.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourStyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/llm"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/theme"
)

const helpText = `### Commands

- **/connect [provider|preset]** — guided provider setup
- **/help** — show this command map
- **/clear** — clear the current conversation
- **/new [name]** — begin a new named session
- **/save [name]** — save, optionally renaming the session
- **/load <name>** — load a saved session
- **/sessions** — list saved sessions
- **/provider <ollama|openai|anthropic|compatible>** — select backend
- **/model <id>** — select the active provider's model
- **/models** — open the model chooser
- **/mode <normal|deep-reason|concise|creative>** — change reasoning mode
- **/theme <rose|mono>** — change palette
- **/copy** — copy the last answer
- **/quit** — leave Ephemera

Autocomplete: type **/**, use **↑/↓** to select, and press **Enter** to run or **Tab** to complete.

Keys: **Enter** send · **Ctrl+R** retry · **PgUp/PgDn** scroll · **Ctrl+Y** copy · **Ctrl+C** quit`

type responseMsg struct {
	text  string
	err   error
	stats contextStats
}

// Model is the complete Bubble Tea state machine.
type Model struct {
	cfg      config.Config
	store    *history.Store
	session  history.Session
	styles   theme.Styles
	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	width                int
	height               int
	frame                int
	ready                bool
	busy                 bool
	status               string
	notice               string
	lastAssistant        string
	suggestions          []suggestion
	completionIndex      int
	connect              *connectFlow
	modelSuggestionCache map[string][]suggestion
}

// New creates a TUI model. When sessionName exists it is loaded; otherwise a
// new session with that name is created.
func New(cfg config.Config, store *history.Store, sessionName string) Model {
	styles := theme.New(cfg.Theme)

	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "Ask what must be seen…"
	input.CharLimit = 32_768
	input.ShowSuggestions = false
	input.Focus()
	input.TextStyle = lipgloss.NewStyle().Foreground(styles.Text).Background(styles.Panel)
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(styles.Muted).Background(styles.Panel)
	input.CompletionStyle = lipgloss.NewStyle().Foreground(styles.Secondary).Background(styles.Panel)
	input.Cursor.Style = lipgloss.NewStyle().Foreground(styles.Primary).Background(styles.Panel)

	spin := spinner.New()
	spin.Spinner = spinner.MiniDot
	spin.Style = lipgloss.NewStyle().Foreground(styles.Primary)

	var session history.Session
	if sessionName != "" {
		if loaded, err := loadFromStore(store, sessionName); err == nil {
			session = loaded
			applyLoadedSessionConfig(&cfg, loaded)
		}
	}
	if session.Name == "" {
		session = history.New(sessionName, cfg.Provider, cfg.Model(), cfg.Mode)
	}

	return Model{
		cfg:             cfg,
		store:           store,
		session:         session,
		styles:          styles,
		input:           input,
		spinner:         spin,
		status:          "Enter a prompt, or /help for the map.",
		completionIndex: 0,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, animationTick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case animationTickMsg:
		m.frame++
		return m, animationTick()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.refreshViewport(true)
		return m, nil

	case responseMsg:
		m.busy = false
		summary := contextSummary(msg.stats)
		if msg.err != nil {
			m.status = "The signal broke · " + summary
			m.notice = "**Request failed:** " + escapeMarkdown(msg.err.Error())
			m.refreshViewport(true)
			return m, nil
		}
		m.session.Append("assistant", msg.text)
		m.lastAssistant = msg.text
		m.session.Provider = m.cfg.Provider
		m.session.Model = m.cfg.Model()
		m.session.Mode = m.cfg.Mode
		if err := m.saveSession(); err != nil {
			m.status = "Answered, but session save failed: " + err.Error()
		} else {
			m.status = "Saved · " + summary
		}
		m.refreshViewport(true)
		return m, nil

	case spinner.TickMsg:
		if m.busy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "ctrl+c":
			_ = m.saveSession()
			_ = config.Save(m.cfg)
			return m, tea.Quit
		case "ctrl+y":
			m.copyLast()
			return m, nil
		case "ctrl+r":
			cmd := m.retryLast()
			m.refreshViewport(true)
			return m, cmd
		case "pgup":
			m.viewport.ViewUp()
			return m, nil
		case "pgdown":
			m.viewport.ViewDown()
			return m, nil
		case "ctrl+u":
			m.viewport.HalfViewUp()
			return m, nil
		case "ctrl+d":
			m.viewport.HalfViewDown()
			return m, nil
		}

		if m.connect != nil {
			switch key {
			case "esc":
				m.cancelConnect()
				m.refreshViewport(true)
				return m, nil
			case "tab":
				if m.acceptSuggestion() {
					return m, nil
				}
			case "up":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(-1)
					return m, nil
				}
			case "down":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(1)
					return m, nil
				}
			case "enter":
				m.acceptConnectSuggestionForEnter()
				m.submitConnectStep()
				m.refreshViewport(true)
				return m, nil
			}
		} else {
			switch key {
			case "tab":
				if m.acceptSuggestion() {
					return m, nil
				}
			case "up":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(-1)
					return m, nil
				}
			case "down":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(1)
					return m, nil
				}
			case "enter":
				if m.busy {
					m.status = "One thought at a time."
					return m, nil
				}
				if m.acceptCommandSuggestionForEnter() && m.commandNeedsMoreInput() {
					return m, nil
				}
				value := strings.TrimSpace(m.input.Value())
				if value == "" {
					return m, nil
				}
				m.input.SetValue("")
				m.rebuildSuggestions()
				if strings.HasPrefix(value, "/") {
					quit, cmd := m.handleCommand(value)
					m.refreshViewport(true)
					if quit {
						return m, tea.Quit
					}
					return m, cmd
				}
				m.notice = ""
				m.session.Append("user", value)
				// Persist the prompt before starting a potentially long network call.
				// A crash or interrupted request should not erase the user's thought.
				_ = m.saveSession()
				m.busy = true
				m.status = "Reasoning beneath the surface…"
				m.refreshViewport(true)
				return m, tea.Batch(m.spinner.Tick, m.generateCmd())
			}
		}
	}

	if _, ok := msg.(tea.MouseMsg); ok && m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}
	if !m.busy {
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		if m.input.Value() != before {
			m.rebuildSuggestions()
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "\n  Ephemera is arranging the dark…\n"
	}
	if m.width < 40 || m.height < 15 {
		return m.styles.Error.Render("Ephemera needs a terminal at least 40×15.")
	}

	prompt := m.styles.Prompt.Render(m.promptLabel())
	inputLine := lipgloss.JoinHorizontal(lipgloss.Top, prompt, m.input.View())
	inputBox := m.renderPanel(m.styles.Input.Width(max(1, m.width-4)), 3, inputLine)

	leftStatus := m.status
	if m.busy {
		leftStatus = m.spinner.View() + " " + m.status
	}
	rightStatus := "Ctrl+R retry · Ctrl+Y copy · Ctrl+C quit"
	if m.connect != nil {
		rightStatus = "Enter next · Esc cancel · Tab complete · ↑/↓ select"
	} else if len(m.suggestions) > 0 {
		rightStatus = "Enter accept · Tab complete · ↑/↓ select"
	} else if m.width < 88 {
		rightStatus = "Ctrl+C quit"
	}
	if lipgloss.Width(leftStatus)+lipgloss.Width(rightStatus)+3 > m.width {
		rightStatus = ""
	}
	leftStatus = clip(leftStatus, max(8, m.width-lipgloss.Width(rightStatus)-3))
	space := max(0, m.width-lipgloss.Width(leftStatus)-lipgloss.Width(rightStatus)-2)
	status := m.styles.Status.Render(leftStatus + strings.Repeat(" ", space) + rightStatus)

	blocks := []string{
		m.renderHeader(),
		m.renderPanel(m.styles.Viewport.Width(max(1, m.width-4)).Height(m.viewport.Height), 1, m.viewport.View()),
		inputBox,
	}
	if palette := m.renderSuggestions(); palette != "" {
		blocks = append(blocks, palette)
	}
	blocks = append(blocks, status)

	return lipgloss.NewStyle().
		Background(m.styles.Background).
		Foreground(m.styles.Text).
		Width(m.width).
		Render(strings.Join(blocks, "\n"))
}

func (m Model) renderSuggestions() string {
	items, start := m.suggestionWindow()
	if len(items) == 0 {
		return ""
	}
	var lines []string
	for i, item := range items {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(m.styles.Muted).Background(m.styles.Panel)
		if start+i == m.completionIndex {
			marker = "› "
			style = lipgloss.NewStyle().Bold(true).Foreground(m.styles.Primary).Background(m.styles.Panel)
		}
		line := marker + item.Label
		if item.Description != "" && m.width >= 68 {
			line += "  " + item.Description
		}
		lines = append(lines, style.Render(clip(line, max(8, m.width-8))))
	}
	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.glowColor(2)).
		Background(m.styles.Panel).
		Padding(0, 1).
		Width(max(1, m.width-4)).
		Render(strings.Join(lines, "\n"))
	return m.gradientBorder(rendered, 2)
}

func (m *Model) resize() {
	m.ready = true
	// Header+meta (2), viewport borders (2), input borders (2), status (1),
	// separators (4), and the optional autocomplete palette leave the rest for
	// scrollable content.
	viewportHeight := max(3, m.height-12-m.suggestionHeight())
	viewportWidth := max(10, m.width-6)
	if m.viewport.Width == 0 {
		m.viewport = viewport.New(viewportWidth, viewportHeight)
		m.viewport.MouseWheelEnabled = true
	} else {
		m.viewport.Width = viewportWidth
		m.viewport.Height = viewportHeight
	}
	m.input.Width = max(8, m.width-12)
}

func (m *Model) refreshViewport(bottom bool) {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderTranscript())
	if bottom {
		m.viewport.GotoBottom()
	}
}

func (m Model) renderTranscript() string {
	width := m.transcriptWidth()
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle(m.styles)),
		glamour.WithWordWrap(max(1, width-2)),
	)
	if err != nil {
		return "renderer error: " + err.Error()
	}

	var out strings.Builder
	if len(m.session.Messages) == 0 {
		out.WriteString(m.transcriptLine(m.styles.NoticeLabel, "signal"))
		out.WriteString("\n")
		welcome, _ := renderer.Render(fmt.Sprintf(`### Ready

- Ask normally, or type **/** for the command palette.
- Start fast with **/connect**, **/models**, **/mode concise**, or **/doctor**.
- Current route: **%s** · **%s** · context **%s** tokens.`,
			m.providerName(),
			m.cfg.Model(),
			formatTokenCount(m.cfg.ContextTokens),
		))
		out.WriteString(m.transcriptBlock(strings.TrimSpace(welcome)))
		out.WriteString("\n")
	}

	for _, message := range m.session.Messages {
		switch message.Role {
		case "user":
			out.WriteString(m.transcriptLine(m.styles.UserLabel, "you"))
		case "assistant":
			out.WriteString(m.transcriptLine(m.styles.AssistantLabel, "ephemera"))
		default:
			continue
		}
		out.WriteString("\n")
		rendered, renderErr := renderer.Render(message.Content)
		if renderErr != nil {
			out.WriteString(m.transcriptBlock(message.Content))
		} else {
			out.WriteString(m.transcriptBlock(strings.TrimSpace(rendered)))
		}
		out.WriteString("\n\n")
	}

	if m.notice != "" {
		out.WriteString(m.transcriptLine(m.styles.NoticeLabel, "signal"))
		out.WriteString("\n")
		rendered, renderErr := renderer.Render(m.notice)
		if renderErr != nil {
			out.WriteString(m.transcriptBlock(m.notice))
		} else {
			out.WriteString(m.transcriptBlock(strings.TrimSpace(rendered)))
		}
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}

func (m Model) transcriptWidth() int {
	return max(20, m.viewport.Width-2)
}

func (m Model) transcriptLine(style lipgloss.Style, text string) string {
	return style.Background(m.styles.Panel).Width(m.transcriptWidth()).Render(text)
}

func (m Model) transcriptBlock(text string) string {
	width := m.transcriptWidth()
	style := lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel).Width(width)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = style.Render(stripANSIBackgrounds(line))
	}
	return strings.Join(lines, "\n")
}

func markdownStyle(styles theme.Styles) ansi.StyleConfig {
	cfg := glamourStyles.DarkStyleConfig
	if cfg.CodeBlock.Chroma != nil {
		chroma := *cfg.CodeBlock.Chroma
		cfg.CodeBlock.Chroma = &chroma
	}

	text := string(styles.Text)
	muted := string(styles.Muted)
	primary := string(styles.Primary)
	secondary := string(styles.Secondary)
	panel := string(styles.Panel)
	bold := true

	setPrimitive := func(primitive *ansi.StylePrimitive, color string) {
		primitive.Color = stringPtr(color)
		primitive.BackgroundColor = stringPtr(panel)
	}
	setBlock := func(block *ansi.StyleBlock, color string) {
		setPrimitive(&block.StylePrimitive, color)
	}

	setBlock(&cfg.Document, text)
	setBlock(&cfg.Paragraph, text)
	setBlock(&cfg.BlockQuote, muted)
	setBlock(&cfg.Heading, primary)
	setBlock(&cfg.H1, primary)
	setBlock(&cfg.H2, primary)
	setBlock(&cfg.H3, primary)
	setBlock(&cfg.H4, primary)
	setBlock(&cfg.H5, primary)
	setBlock(&cfg.H6, primary)
	setBlock(&cfg.Code, secondary)
	setBlock(&cfg.CodeBlock.StyleBlock, text)
	setBlock(&cfg.List.StyleBlock, text)
	setBlock(&cfg.Table.StyleBlock, text)
	setPrimitive(&cfg.Text, text)
	setPrimitive(&cfg.Item, text)
	setPrimitive(&cfg.Enumeration, text)
	setPrimitive(&cfg.Strong, text)
	setPrimitive(&cfg.Emph, text)
	setPrimitive(&cfg.Link, secondary)
	setPrimitive(&cfg.LinkText, secondary)
	setPrimitive(&cfg.HorizontalRule, muted)
	cfg.Heading.Bold = &bold
	cfg.H1.Bold = &bold
	cfg.H2.Bold = &bold
	cfg.H3.Bold = &bold
	if cfg.CodeBlock.Chroma != nil {
		cfg.CodeBlock.Chroma.Text.Color = stringPtr(text)
		cfg.CodeBlock.Chroma.Text.BackgroundColor = stringPtr(panel)
		cfg.CodeBlock.Chroma.Background.BackgroundColor = stringPtr(panel)
	}
	return cfg
}

func stringPtr(value string) *string { return &value }

func (m Model) generateCmd() tea.Cmd {
	cfg := m.cfg
	session := m.session
	return func() tea.Msg {
		system := reasoning.SystemPrompt(cfg.Mode)
		messages, stats := buildRequestMessages(session.Messages, system, cfg.ContextTokens)

		provider, err := llm.New(cfg)
		if err != nil {
			return responseMsg{err: err, stats: stats}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		text, err := provider.Generate(ctx, llm.Request{
			Model:       cfg.Model(),
			System:      system,
			Messages:    messages,
			MaxTokens:   cfg.MaxTokens,
			Temperature: cfg.Mode.Temperature(),
		})
		return responseMsg{text: text, err: err, stats: stats}
	}
}

func (m *Model) handleCommand(raw string) (bool, tea.Cmd) {
	parts := strings.Fields(raw)
	command := strings.ToLower(parts[0])
	args := parts[1:]

	requireArg := func(usage string) (string, bool) {
		if len(args) == 0 {
			m.status = "Usage: " + usage
			return "", false
		}
		return strings.Join(args, " "), true
	}

	switch command {
	case "/help":
		m.notice = helpText
		m.status = "Command map opened."

	case "/connect":
		provider := ""
		if len(args) > 0 {
			provider = args[0]
		}
		m.startConnect(provider)

	case "/clear":
		m.session.Messages = nil
		m.lastAssistant = ""
		m.notice = ""
		m.status = "The surface is clear."
		_ = m.saveSession()

	case "/new":
		name := ""
		if len(args) > 0 {
			name = strings.Join(args, " ")
		}
		_ = m.saveSession()
		m.session = history.New(name, m.cfg.Provider, m.cfg.Model(), m.cfg.Mode)
		_ = m.saveSession()
		m.notice = ""
		m.lastAssistant = ""
		m.status = "New session: " + m.session.Name

	case "/save":
		if len(args) > 0 {
			m.session.Name = history.Sanitize(strings.Join(args, " "))
		}
		if err := m.saveSession(); err != nil {
			m.status = "Save failed: " + err.Error()
		} else {
			m.status = "Saved session " + m.session.Name
		}

	case "/load":
		name, ok := requireArg("/load <name>")
		if !ok {
			break
		}
		loaded, err := m.loadSession(name)
		if err != nil {
			m.status = err.Error()
			break
		}
		_ = m.saveSession()
		m.applyLoadedSession(loaded)
		m.notice = ""
		m.status = "Loaded session " + loaded.Name
		_ = config.Save(m.cfg)

	case "/sessions":
		names, err := m.listSessions()
		if err != nil {
			m.status = "List failed: " + err.Error()
			break
		}
		if len(names) == 0 {
			m.notice = "No saved sessions yet."
		} else {
			var b strings.Builder
			b.WriteString("### Saved sessions\n\n")
			for _, name := range names {
				fmt.Fprintf(&b, "- `%s`\n", name)
			}
			m.notice = b.String()
		}
		m.status = fmt.Sprintf("%d session(s).", len(names))

	case "/provider":
		provider, ok := requireArg("/provider <ollama|openai|anthropic|compatible>")
		if !ok {
			break
		}
		provider = strings.ToLower(strings.TrimSpace(provider))
		if !config.ValidProvider(provider) {
			m.status = "Unknown provider: " + provider
			break
		}
		m.cfg.Provider = provider
		m.session.Provider = provider
		m.session.Model = m.cfg.Model()
		m.status = fmt.Sprintf("Provider → %s · model → %s", provider, m.cfg.Model())
		_ = config.Save(m.cfg)

	case "/models":
		m.openModelChooser()

	case "/model":
		if len(args) == 0 {
			m.openModelChooser()
			break
		}
		model := strings.Join(args, " ")
		m.cfg.SetModel(strings.TrimSpace(model))
		m.session.Model = m.cfg.Model()
		m.status = "Model → " + m.cfg.Model()
		_ = config.Save(m.cfg)

	case "/mode":
		value, ok := requireArg("/mode <normal|deep-reason|concise|creative>")
		if !ok {
			break
		}
		mode, err := reasoning.Parse(value)
		if err != nil {
			m.status = err.Error()
			break
		}
		m.cfg.Mode = mode
		m.session.Mode = mode
		m.status = "Mode → " + string(mode)
		_ = config.Save(m.cfg)

	case "/usage":
		stats := m.currentContextStats()
		m.notice = m.usageNotice(stats)
		m.status = "Usage · " + contextSummary(stats)

	case "/budget":
		value, ok := requireArg("/budget <tokens>")
		if !ok {
			break
		}
		budget, err := parseContextBudget(value)
		if err != nil {
			m.status = err.Error()
			break
		}
		m.cfg.ContextTokens = budget
		m.status = "Context budget → " + formatTokenCount(budget) + " tokens"
		_ = config.Save(m.cfg)

	case "/retry":
		return false, m.retryLast()

	case "/undo":
		m.undoLastMessage()

	case "/export":
		target := ""
		if len(args) > 0 {
			target = strings.Join(args, " ")
		}
		path, err := m.exportTranscript(target)
		if err != nil {
			m.status = "Export failed: " + err.Error()
			break
		}
		m.status = "Exported → " + path

	case "/doctor":
		m.notice = m.doctorNotice()
		m.status = "Doctor report opened."

	case "/theme":
		value, ok := requireArg("/theme <rose|mono>")
		if !ok {
			break
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "rose" && value != "mono" {
			m.status = "Unknown theme: " + value
			break
		}
		m.cfg.Theme = value
		m.styles = theme.New(value)
		m.applyThemeToComponents()
		m.status = "Theme → " + value
		_ = config.Save(m.cfg)

	case "/copy":
		m.copyLast()

	case "/quit", "/exit":
		_ = m.saveSession()
		_ = config.Save(m.cfg)
		return true, nil

	default:
		m.status = "Unknown command. /help reveals the map."
	}
	return false, nil
}

func (m *Model) applyThemeToComponents() {
	m.input.TextStyle = lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel)
	m.input.PlaceholderStyle = lipgloss.NewStyle().Foreground(m.styles.Muted).Background(m.styles.Panel)
	m.input.CompletionStyle = lipgloss.NewStyle().Foreground(m.styles.Secondary).Background(m.styles.Panel)
	m.input.Cursor.Style = lipgloss.NewStyle().Foreground(m.styles.Primary).Background(m.styles.Panel)
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.styles.Primary)
}

func (m Model) promptLabel() string {
	switch m.cfg.Theme {
	case "mono":
		return "mono→ "
	default:
		return "rose→ "
	}
}

func (m *Model) applyLoadedSession(loaded history.Session) {
	m.session = loaded
	applyLoadedSessionConfig(&m.cfg, loaded)
	m.lastAssistant = findLastAssistant(loaded.Messages)
}

func applyLoadedSessionConfig(cfg *config.Config, loaded history.Session) {
	if config.ValidProvider(loaded.Provider) {
		cfg.Provider = loaded.Provider
	}
	if strings.TrimSpace(loaded.Model) != "" {
		cfg.SetModel(loaded.Model)
	}
	if loaded.Mode.Valid() {
		cfg.Mode = loaded.Mode
	}
}

func (m *Model) saveSession() error {
	if m.store == nil {
		return nil
	}
	return m.store.Save(m.session)
}

func (m *Model) loadSession(name string) (history.Session, error) {
	return loadFromStore(m.store, name)
}

func (m *Model) listSessions() ([]string, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store unavailable")
	}
	return m.store.List()
}

func loadFromStore(store *history.Store, name string) (history.Session, error) {
	if store == nil {
		return history.Session{}, fmt.Errorf("session store unavailable")
	}
	return store.Load(name)
}

func (m *Model) copyLast() {
	if strings.TrimSpace(m.lastAssistant) == "" {
		m.status = "Nothing to copy yet."
		return
	}
	if err := clipboard.WriteAll(m.lastAssistant); err != nil {
		m.status = "Clipboard unavailable: " + err.Error()
		return
	}
	m.status = "Last answer copied."
}

func findLastAssistant(messages []history.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].Content
		}
	}
	return ""
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer("`", "'", "*", "\\*", "_", "\\_")
	return replacer.Replace(s)
}

func clip(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
