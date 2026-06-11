// Package tui implements Ephemera's Bubble Tea terminal interface.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourStyles "github.com/charmbracelet/glamour/styles"

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
- **/provider <provider>** — select backend
- **/model <id>** / **/models** — select intelligence
- **/mode <profile>** — change response character
- **/usage** / **/budget <tokens>** — inspect or set context
- **/retry** / **/undo** — revise the latest exchange
- **/export [path]** — export the transcript
- **/doctor** — inspect the active route
- **/theme <rose|mono>** — change palette
- **/copy** — copy the last answer
- **/quit** — leave Ephemera

Autocomplete: type **/**, use **↑/↓** or **Ctrl+N/P** to select, **PgUp/PgDn** to jump, **Enter** to run, **Tab** to complete, and **Esc** to close.

Keys: **Enter** send · **Ctrl+L** clear composer · **Ctrl+R** retry · **PgUp/PgDn** scroll · **Ctrl+Y** copy · **Ctrl+C** quit`

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

	width               int
	height              int
	focused             bool
	animationGeneration uint64
	animationLastTick   time.Time
	animationElapsed    time.Duration
	paletteHeight       int
	ready               bool
	busy                bool
	status              string
	notice              string
	lastAssistant       string
	suggestions         []suggestion
	completionIndex     int
	connect             *connectFlow
	modelCatalogCache   map[string]modelCatalogState
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
	input.SetStyles(inputComponentStyles(styles))
	// Let Bubble Tea v2 render the terminal cursor independently from the text.
	// Cursor blinking therefore no longer repaints a character in the input box.
	input.SetVirtualCursor(false)

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

	now := time.Now()

	return Model{
		cfg:                 cfg,
		store:               store,
		session:             session,
		styles:              styles,
		input:               input,
		spinner:             spin,
		status:              "Enter a prompt, or /help for the map.",
		completionIndex:     0,
		focused:             true,
		animationGeneration: 1,
		animationLastTick:   now,
	}
}

func (m Model) Init() tea.Cmd {
	return animationTick(m.animationGeneration)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case animationTickMsg:
		if !m.focused || msg.generation != m.animationGeneration {
			return m, nil
		}
		if m.animationLastTick.IsZero() {
			m.animationLastTick = msg.at
		} else {
			m.animationElapsed += maxDuration(0, msg.at.Sub(m.animationLastTick))
			m.animationLastTick = msg.at
		}
		return m, animationTick(m.animationGeneration)

	case tea.BlurMsg:
		if m.focused {
			m.focused = false
			m.animationGeneration++
			m.animationLastTick = time.Time{}
			m.input.Blur()
		}
		return m, nil

	case tea.FocusMsg:
		if !m.focused {
			m.focused = true
			m.animationGeneration++
			m.animationLastTick = time.Now()
			focusCmd := m.input.Focus()
			resume := []tea.Cmd{focusCmd, animationTick(m.animationGeneration)}
			if m.busy {
				resume = append(resume, m.spinner.Tick)
			}
			return m, tea.Batch(resume...)
		}
		return m, nil

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
		if m.busy && m.focused {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyPressMsg:
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
			if len(m.suggestions) > 0 && m.suggestionPaletteActive() {
				m.moveSuggestion(-max(1, m.suggestionCapacity()))
			} else {
				m.viewport.PageUp()
			}
			return m, nil
		case "pgdown":
			if len(m.suggestions) > 0 && m.suggestionPaletteActive() {
				m.moveSuggestion(max(1, m.suggestionCapacity()))
			} else {
				m.viewport.PageDown()
			}
			return m, nil
		case "ctrl+u":
			if m.input.Value() == "" && !m.suggestionPaletteActive() {
				m.viewport.HalfPageUp()
				return m, nil
			}
		case "ctrl+d":
			if m.input.Value() == "" && !m.suggestionPaletteActive() {
				m.viewport.HalfPageDown()
				return m, nil
			}
		}

		if m.connect != nil {
			switch key {
			case "esc":
				m.cancelConnect()
				m.refreshViewport(true)
				return m, nil
			case "shift+tab":
				if !m.retreatConnect() {
					m.status = "Already at the first connection step."
				}
				m.refreshViewport(true)
				return m, nil
			case "backspace":
				if m.input.Value() == "" && m.retreatConnect() {
					m.refreshViewport(true)
					return m, nil
				}
			case "ctrl+l":
				m.input.SetValue("")
				m.rebuildSuggestions()
				return m, nil
			case "tab":
				if m.acceptSuggestion() {
					return m, nil
				}
			case "up", "ctrl+p":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(-1)
					return m, nil
				}
			case "down", "ctrl+n":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(1)
					return m, nil
				}
			case "home":
				if len(m.suggestions) > 0 {
					m.completionIndex = 0
					return m, nil
				}
			case "end":
				if len(m.suggestions) > 0 {
					m.completionIndex = len(m.suggestions) - 1
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
			case "esc":
				if m.suggestionPaletteActive() {
					m.input.SetValue("")
					m.rebuildSuggestions()
					m.status = "Command palette closed."
					return m, nil
				}
			case "ctrl+l":
				m.input.SetValue("")
				m.rebuildSuggestions()
				m.status = "Composer cleared."
				return m, nil
			case "tab":
				if m.acceptSuggestion() {
					return m, nil
				}
			case "up", "ctrl+p":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(-1)
					return m, nil
				}
			case "down", "ctrl+n":
				if len(m.suggestions) > 0 {
					m.moveSuggestion(1)
					return m, nil
				}
			case "home":
				if len(m.suggestions) > 0 {
					m.completionIndex = 0
					return m, nil
				}
			case "end":
				if len(m.suggestions) > 0 {
					m.completionIndex = len(m.suggestions) - 1
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

func (m Model) View() tea.View {
	content := "\n  Ephemera is arranging the dark…\n"
	var cursor *tea.Cursor

	if m.ready && m.width >= 40 && m.height >= 16 {
		metrics := m.layoutMetrics()
		panelWidth := max(1, m.width-2)
		innerWidth := max(8, m.width-6)
		prompt := m.animatedPromptGlyph() + " " + m.styles.Prompt.Render(m.promptLabel())
		inputValue := m.input.View()
		composerMeta := m.renderComposerMeta()
		inputLine := prompt + inputValue
		if lipgloss.Width(inputLine)+lipgloss.Width(composerMeta)+2 <= innerWidth {
			inputLine += strings.Repeat(" ", max(1, innerWidth-lipgloss.Width(inputLine)-lipgloss.Width(composerMeta))) + composerMeta
		}
		inputBox := m.insetBlock(m.renderPanel(m.styles.Input.Width(panelWidth), 3, inputLine))

		header := m.renderHeader()
		viewportPanel := m.insetBlock(m.renderPanel(
			m.styles.Viewport.Width(panelWidth).Height(metrics.viewportOuterHeight),
			1,
			m.texturedViewport(),
		))
		footerPanel := m.insetBlock(m.renderPanel(
			m.styles.Footer.Width(panelWidth),
			4,
			m.renderFooter(),
		))

		blocks := []string{header, viewportPanel, inputBox}
		if palette := m.renderSuggestions(); palette != "" {
			blocks = append(blocks, m.insetBlock(palette))
		}
		blocks = append(blocks, footerPanel)
		content = m.fitScreen(strings.Join(blocks, "\n"))

		if m.focused && !m.busy {
			cursor = m.input.Cursor()
			if cursor != nil {
				// One-cell screen inset, panel border, panel padding, and prompt
				// precede the real terminal cursor. Vertically the panels are packed
				// without decorative spacer rows.
				cursor.X += 3 + lipgloss.Width(prompt)
				cursor.Y += lipgloss.Height(header) + lipgloss.Height(viewportPanel) + 1
			}
		}
	} else if m.ready {
		content = m.styles.Error.Render("Ephemera needs a terminal at least 40×16.")
	}

	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.ReportFocus = true
	view.BackgroundColor = m.styles.Background
	view.ForegroundColor = m.styles.Text
	view.WindowTitle = "Ephemera"
	view.Cursor = cursor
	return view
}

func (m Model) fitScreen(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for len(lines) < m.height {
		lines = append(lines, m.textureLine(max(1, m.width), len(lines), 211, m.styles.Background))
	}
	return lipgloss.NewStyle().
		Foreground(m.styles.Text).
		Background(m.styles.Background).
		Width(max(1, m.width)).
		Render(strings.Join(lines, "\n"))
}

func (m *Model) resize() {
	m.ready = true
	metrics := m.layoutMetrics()
	m.paletteHeight = metrics.paletteOuterHeight
	viewportHeight := max(3, metrics.viewportInnerHeight)
	viewportWidth := max(10, m.width-6)
	if m.viewport.Width() == 0 {
		m.viewport = viewport.New(viewport.WithWidth(viewportWidth), viewport.WithHeight(viewportHeight))
		m.viewport.MouseWheelEnabled = true
	} else {
		m.viewport.SetWidth(viewportWidth)
		m.viewport.SetHeight(viewportHeight)
	}
	// Reserve stable space for the animated prompt and composer metadata so the
	// input never pushes the right edge of its panel while typing.
	composerReserve := 18
	if m.connect != nil {
		composerReserve = max(24, lipgloss.Width(m.renderComposerMeta())+2)
	}
	promptReserve := lipgloss.Width(m.promptLabel()) + 4
	m.input.SetWidth(max(8, m.width-6-promptReserve-composerReserve))
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
		welcome, _ := renderer.Render(fmt.Sprintf(`### The signal is open

Ask naturally, or press **/** to reveal the command palette.

- **/connect** — configure a provider
- **/models** — switch intelligence

Route: **%s** · **%s** · **%s** token context.`,
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
	return max(20, m.viewport.Width()-2)
}

func (m Model) transcriptLine(style lipgloss.Style, text string) string {
	glyph := "◇"
	label := strings.ToUpper(text)
	switch text {
	case "you":
		glyph = "◆"
	case "ephemera":
		glyph = "✦"
	case "signal":
		glyph = "·"
	}
	return style.Background(m.styles.Panel).Render(glyph + " " + label)
}

func (m Model) transcriptBlock(text string) string {
	width := m.transcriptWidth()
	style := lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel).Width(width)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		clean := stripANSIBackgrounds(line)
		if strings.TrimSpace(clean) != "" {
			clean = "  " + clean
		}
		lines[i] = style.Render(clean)
	}
	return strings.Join(lines, "\n")
}

func markdownStyle(styles theme.Styles) ansi.StyleConfig {
	cfg := glamourStyles.DarkStyleConfig
	if cfg.CodeBlock.Chroma != nil {
		chroma := *cfg.CodeBlock.Chroma
		cfg.CodeBlock.Chroma = &chroma
	}

	text := theme.Hex(styles.Text)
	muted := theme.Hex(styles.Muted)
	primary := theme.Hex(styles.Primary)
	secondary := theme.Hex(styles.Secondary)
	panel := theme.Hex(styles.Panel)
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
		if len(args) > 0 {
			name := args[0]
			if !strings.HasPrefix(name, "/") {
				name = "/" + name
			}
			if spec, ok := findCommandSpec(name); ok {
				m.notice = commandHelpNotice(spec)
				m.status = "Help opened · " + spec.Name
				break
			}
			m.status = "Unknown command for help: " + name
			break
		}
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
		if provider == "chatgpt" {
			provider = "codex"
		}
		if !config.ValidProvider(provider) {
			m.status = "Unknown provider: " + provider
			break
		}
		candidate := m.cfg
		candidate.Provider = provider
		available, err := m.modelAvailableForConfig(candidate, candidate.Model(), false)
		if err != nil {
			m.cfg.Provider = provider
			m.session.Provider = provider
			m.session.Model = m.cfg.Model()
			m.status = fmt.Sprintf("Provider → %s · model → %s (unverified)", provider, m.cfg.Model())
			m.notice = "### Provider selected with unverified model\n\nThe saved route for `" + provider + "` could not be verified:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nThe saved model ID will be used anyway. If generation fails, run `/connect " + provider + "` or choose a model with `/models`."
			_ = config.Save(m.cfg)
			break
		}
		if !available {
			m.startConnect(provider)
			m.notice = "### Model selection recommended\n\nThe saved model `" + escapeMarkdown(candidate.Model()) + "` is not in this provider's live catalog. Complete the connection flow to choose an advertised model, or type the ID manually if the catalog is incomplete."
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
		model := strings.TrimSpace(strings.Join(args, " "))
		available, err := m.modelAvailableForConfig(m.cfg, model, false)
		if err != nil {
			if m.cfg.Provider == "codex" {
				m.status = "Codex model change blocked: " + err.Error()
				m.notice = "### Codex model not changed\n\nThe Codex model list could not be loaded:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nOpen Codex once to refresh its login and model cache, then retry `/models`."
				break
			}
			m.cfg.SetModel(model)
			m.session.Model = m.cfg.Model()
			m.status = "Model → " + m.cfg.Model() + " (unverified)"
			m.notice = "### Model changed without catalog verification\n\nThe provider catalog could not be checked:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nThe typed model ID is active. If the provider rejects it, run `/models` or `/model <id>` to choose another."
			_ = config.Save(m.cfg)
			break
		}
		if !available {
			if m.cfg.Provider == "codex" {
				m.status = fmt.Sprintf("Model %q is not available from Codex.", model)
				m.notice = "### Codex model blocked\n\n`" + escapeMarkdown(model) + "` is not in the Codex ChatGPT model list. Choose one of the listed Codex models so this route stays on subscription auth."
				break
			}
			m.cfg.SetModel(model)
			m.session.Model = m.cfg.Model()
			m.status = "Model → " + m.cfg.Model() + " (not advertised)"
			m.notice = "### Model changed outside catalog\n\n`" + escapeMarkdown(model) + "` was not advertised by `" + escapeMarkdown(m.providerName()) + "`. It is active anyway because provider catalogs can be incomplete."
			_ = config.Save(m.cfg)
			break
		}
		m.cfg.SetModel(model)
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
	m.input.SetStyles(inputComponentStyles(m.styles))
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.styles.Primary)
}

func inputComponentStyles(styles theme.Styles) textinput.Styles {
	state := textinput.StyleState{
		Prompt:      lipgloss.NewStyle().Foreground(styles.Primary).Background(styles.Panel),
		Text:        lipgloss.NewStyle().Foreground(styles.Text).Background(styles.Panel),
		Placeholder: lipgloss.NewStyle().Foreground(styles.Muted).Background(styles.Panel),
		Suggestion:  lipgloss.NewStyle().Foreground(styles.Secondary).Background(styles.Panel),
	}
	inputStyles := textinput.DefaultDarkStyles()
	inputStyles.Focused = state
	inputStyles.Blurred = state
	inputStyles.Cursor.Color = styles.Primary
	inputStyles.Cursor.Shape = tea.CursorBar
	inputStyles.Cursor.Blink = true
	return inputStyles
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (m Model) promptLabel() string {
	if m.connect != nil {
		switch m.connect.Step {
		case connectProvider:
			return "provider→ "
		case connectName:
			return "name→ "
		case connectBaseURL:
			return "endpoint→ "
		case connectAPIKey:
			return "secret→ "
		case connectModel:
			return "model→ "
		case connectReview:
			return "confirm→ "
		}
	}
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
