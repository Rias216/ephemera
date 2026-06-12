// Package tui implements Ephemera's Bubble Tea terminal interface.
package tui

import (
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/theme"
	"github.com/ephemera-ai/ephemera/internal/tools"
)

const helpText = `### Commands

- **/connect [provider|preset]** — guided provider setup
- **/help** — show this command map
- **/clear** — clear the current conversation
- **/new [name]** — begin a new named session
- **/save [name]** — save, optionally renaming the session
- **/load <name>** — load a saved session
- **/sessions** — list saved sessions
- **/provider <connected-route>** — activate a remembered route
- **/model <id>** / **/models** — select any connected model; its route switches automatically
- **/mode <profile>** — change response character
- **/usage** / **/budget <tokens>** — inspect or set context
- **/approval <auto|safe|read-only|workspace-write>** — set agent approval policy
- **/codex [status|budget <tokens>]** — inspect or tune the isolated Codex bridge
- **/subagent <on|off|model|status>** — configure lightweight delegation
- **/director <on|off|model|instrument|status>** — configure dual-model review
- **/debuglog [tail|export|clear]** — inspect, export, or clear session diagnostics
- **/thinking <on|off>** — show or hide Beneath the Surface traces
- **/surface** — reopen the latest persisted reasoning and verification trace
- **/eval** — run deterministic local agent capability checks
- **/sandbox**, **/dry-run**, **/rollback** — control execution safety
- **/index**, **/tdd**, **/learn** — control codebase intelligence and learning
- **/retry** / **/undo** — revise the latest exchange
- **/stop** — cancel the active streaming agent run
- **/export [path]** — export the transcript
- **/doctor** — inspect the active route
- **/theme <rose|mono>** — change palette
- **/copy** — copy the last answer
- **/quit** — leave Ephemera

Autocomplete: type **/**, use **↑/↓** or **Ctrl+N/P** to select, **PgUp/PgDn** to jump, **Enter** to run, **Tab** to complete, and **Esc** to close.

Keys: **Enter** send · **Ctrl+X** stop agent · **Ctrl+L** clear composer · **Ctrl+R** retry · **PgUp/PgDn** scroll · **Ctrl+T** timeline focus · **Alt+1..4** inspector tabs · **Ctrl+←/→** switch tabs · **Ctrl+Y** copy · **Ctrl+C** quit`

type responseMsg struct {
	text    string
	err     error
	stats   contextStats
	events  []history.Event
	pending *agent.PendingApproval
}

type approvalResultMsg struct {
	event         history.Event
	pending       agent.PendingApproval
	err           error
	continueAgent bool
}

// Model is the complete Bubble Tea state machine.
type Model struct {
	cfg              config.Config
	store            *history.Store
	session          history.Session
	styles           theme.Styles
	viewport         viewport.Model
	thinkingViewport viewport.Model
	input            textinput.Model
	spinner          spinner.Model

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
	pendingApproval     *agent.PendingApproval
	inspectorTab        int
	timelineFocus       bool
	selectedEvent       int
	expandedEvents      map[string]bool
	timelineFilter      string
	followLive          bool
	compactView         bool
	redoSession         *history.Session
	renderedTranscript  string
	renderedThinking    string
	sectionRenderCache  map[string][]string
	agentStream         <-chan agent.StreamUpdate
	agentCancel         context.CancelFunc
	liveAgent           liveAgentState
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
	loadedSession := false
	if sessionName != "" {
		if loaded, err := loadFromStore(store, sessionName); err == nil {
			session = loaded
			loadedSession = true
			applyLoadedSessionConfig(&cfg, loaded)
		} else {
			debuglog.Error("tui", "startup session load failed", err, map[string]any{
				"session": sessionName,
			})
		}
	}
	if session.Name == "" {
		session = history.New(sessionName, cfg.Provider, cfg.Model(), cfg.Mode)
	}
	if err := debuglog.EnsureSession(session.Name); err != nil {
		debuglog.Error("tui", "session diagnostics initialization failed", err, map[string]any{
			"session": session.Name,
		})
	}
	_ = debuglog.WriteSession(session.Name, "info", "tui", "session opened", "session initialized for the terminal UI", map[string]any{
		"loaded": loadedSession, "provider": cfg.Provider, "model": cfg.Model(), "workspace": cfg.WorkspaceRoot,
		"messages": len(session.Messages), "events": len(session.Events),
	})
	if store != nil {
		if err := store.Save(session); err != nil {
			debuglog.Error("tui", "startup session save failed", err, map[string]any{
				"session": session.Name,
			})
		}
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
		inspectorTab:        0,
		expandedEvents:      make(map[string]bool),
		sectionRenderCache:  make(map[string][]string),
		followLive:          true,
	}
}

func (m Model) Init() tea.Cmd {
	return animationTick(m.animationGeneration)
}

func (m Model) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if m.agentCancel != nil {
				m.agentCancel()
			}
			ctx := debuglog.WithScope(context.Background(), debuglog.Scope{
				Session: m.session.Name, RunID: m.liveAgent.RunID, Provider: firstNonEmpty(m.liveAgent.RunProvider, m.cfg.Provider),
				Model: firstNonEmpty(m.liveAgent.RunModel, m.cfg.Model()), Workspace: m.workspaceRoot(), Iteration: m.liveAgent.Iteration, Tool: m.liveAgent.Tool,
			})
			crashPath, _ := debuglog.RecordCrash(ctx, "tui.update", recovered, debug.Stack(), map[string]any{
				"message_type": fmt.Sprintf("%T", msg), "phase": m.liveAgent.Phase,
			})
			m.busy = false
			m.liveAgent.Active = false
			m.liveAgent.Phase = "recovered from UI panic"
			m.liveAgent.Err = fmt.Sprint(recovered)
			m.agentStream = nil
			m.agentCancel = nil
			m.status = "Recovered from an internal UI error. Session and crash logs were saved."
			m.notice = "**Internal UI error recovered.**\n\nCrash report: `" + escapeMarkdown(crashPath) + "`\n\nSession log: `" + escapeMarkdown(debuglog.SessionDebugPath(m.session.Name)) + "`"
			model = m
			cmd = nil
		}
	}()
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

	case agentStreamMsg:
		if !msg.ok {
			if m.busy {
				m.busy = false
				m.liveAgent.Active = false
				if m.liveAgent.Phase == "cancelling" {
					m.liveAgent.Phase = "cancelled"
					m.status = "Agent run cancelled."
				} else {
					m.recordFailure("agent stream closed unexpectedly", "agent update channel closed before a done event", nil)
					m.status = "Agent stream closed unexpectedly."
				}
				m.agentStream = nil
				m.agentCancel = nil
				m.refreshViewport(true)
			}
			return m, nil
		}
		return m, m.applyAgentStream(msg.update)

	case responseMsg:
		m.busy = false
		summary := contextSummary(msg.stats)
		if msg.err != nil {
			m.recordError("request failed", msg.err, nil)
			m.status = "The signal broke · " + summary
			m.notice = "**Request failed:** " + escapeMarkdown(msg.err.Error()) + "\n\nDebug log: `" + escapeMarkdown(debugLogPath()) + "`"
			m.refreshViewport(true)
			return m, nil
		}
		for _, event := range msg.events {
			m.session.AppendEvent(event)
		}
		if m.followLive {
			m.selectedEvent = max(0, len(m.visibleAgentEvents())-1)
		}
		m.pendingApproval = msg.pending
		if strings.TrimSpace(msg.text) != "" {
			if msg.pending == nil {
				m.session.Append("assistant", msg.text)
				m.lastAssistant = msg.text
			} else {
				// Approval prompts are control state, not assistant conversation.
				// Persisting them in the transcript makes the resumed model see
				// its own stale request and can trigger another approval loop.
				m.notice = msg.text
			}
		}
		m.session.Provider = m.cfg.Provider
		m.session.Model = m.cfg.Model()
		m.session.Mode = m.cfg.Mode
		if err := m.saveSession(); err != nil {
			m.status = "Answered, but session save failed: " + err.Error()
		} else if m.pendingApproval != nil {
			m.status = "Approval needed · /approve or /reject"
		} else {
			m.status = "Saved · " + summary
		}
		m.refreshViewport(true)
		return m, nil

	case approvalResultMsg:
		m.busy = false
		if msg.err != nil {
			m.recordError("approved action failed", msg.err, nil)
			m.status = "Approved action failed: " + msg.err.Error()
			m.refreshViewport(true)
			return m, nil
		}
		m.session.AppendEvent(msg.event)
		m.resolvePendingApproval(msg.pending, msg.event)
		m.pendingApproval = nil
		m.notice = ""
		_ = m.saveSession()
		if !msg.continueAgent {
			m.status = "Shell command captured."
			m.refreshViewport(true)
			return m, nil
		}
		m.busy = true
		if msg.event.Status == "error" {
			m.status = "Approved action failed · continuing agent to recover..."
		} else {
			m.status = "Approved action ran · continuing agent..."
		}
		m.refreshViewport(true)
		return m, tea.Batch(m.spinner.Tick, m.generateCmd())

	case spinner.TickMsg:
		if m.busy && m.focused {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case "ctrl+shift+c":
			m.copyReasoningSurface()
			return m, nil
		case "alt+1":
			m.inspectorTab = 0
			return m, nil
		case "alt+2":
			m.inspectorTab = 1
			return m, nil
		case "alt+3":
			m.inspectorTab = 2
			m.timelineFocus = false
			m.refreshThinkingViewport(false)
			return m, nil
		case "alt+4":
			m.inspectorTab = 3
			return m, nil
		case "ctrl+left":
			m.rotateInspector(-1)
			return m, nil
		case "ctrl+right":
			m.rotateInspector(1)
			return m, nil
		case "ctrl+c":
			if m.agentCancel != nil {
				m.agentCancel()
			}
			_ = m.saveSession()
			_ = config.Save(m.cfg)
			return m, tea.Quit
		case "ctrl+x":
			m.cancelGeneration()
			return m, nil
		case "ctrl+y":
			m.copyLast()
			return m, nil
		case "ctrl+r":
			cmd := m.retryLast()
			m.refreshViewport(true)
			return m, cmd
		case "enter", "a":
			if m.pendingApproval != nil && m.input.Value() == "" {
				return m, m.approvePending()
			}
			if key == "enter" && m.timelineFocus && m.input.Value() == "" {
				m.toggleSelectedEvent()
				m.refreshViewport(false)
				return m, nil
			}
		case "r":
			if m.pendingApproval != nil && m.input.Value() == "" {
				m.rejectPending()
				m.refreshViewport(true)
				return m, nil
			}
		case "ctrl+t":
			m.timelineFocus = !m.timelineFocus
			if m.timelineFocus {
				m.status = "Timeline focus · j/k select · Enter expand · f follow · t filter"
			} else {
				m.status = "Composer focus."
			}
			m.refreshViewport(false)
			return m, nil
		case "j", "down":
			if m.timelineFocus && m.input.Value() == "" {
				m.moveTimelineSelection(1)
				m.refreshViewport(false)
				return m, nil
			}
		case "k", "up":
			if m.timelineFocus && m.input.Value() == "" {
				m.moveTimelineSelection(-1)
				m.refreshViewport(false)
				return m, nil
			}
		case " ", "space":
			if m.timelineFocus && m.input.Value() == "" {
				m.toggleSelectedEvent()
				m.refreshViewport(false)
				return m, nil
			}
		case "f":
			if m.timelineFocus && m.input.Value() == "" {
				m.followLive = !m.followLive
				m.status = "Timeline follow " + onOff(m.followLive)
				m.refreshViewport(m.followLive)
				return m, nil
			}
		case "t":
			if m.timelineFocus && m.input.Value() == "" {
				m.cycleTimelineFilter()
				m.refreshViewport(false)
				return m, nil
			}
		case "y":
			if m.timelineFocus && m.input.Value() == "" {
				m.copySelectedEvent(false)
				return m, nil
			}
		case "c":
			if m.timelineFocus && m.input.Value() == "" {
				m.copySelectedEvent(true)
				return m, nil
			}
		case "u":
			if m.timelineFocus && m.input.Value() == "" && !m.busy {
				m.rewindLatestUserTurn()
				m.refreshViewport(true)
				return m, nil
			}
		case "pgup":
			if len(m.suggestions) > 0 && m.suggestionPaletteActive() {
				m.moveSuggestion(-max(1, m.suggestionCapacity()))
			} else {
				m.pageActiveViewport(-1)
			}
			return m, nil
		case "pgdown":
			if len(m.suggestions) > 0 && m.suggestionPaletteActive() {
				m.moveSuggestion(max(1, m.suggestionCapacity()))
			} else {
				m.pageActiveViewport(1)
			}
			return m, nil
		case "ctrl+u":
			if m.input.Value() == "" && !m.suggestionPaletteActive() {
				m.halfPageActiveViewport(-1)
				return m, nil
			}
		case "ctrl+d":
			if m.input.Value() == "" && !m.suggestionPaletteActive() {
				m.halfPageActiveViewport(1)
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
				if strings.HasPrefix(value, "!") {
					cmd := m.submitShellCommand(strings.TrimSpace(strings.TrimPrefix(value, "!")))
					m.refreshViewport(true)
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
		cmd := m.updateActiveViewport(msg)
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

func (m Model) View() (view tea.View) {
	defer func() {
		if recovered := recover(); recovered != nil {
			ctx := debuglog.WithScope(context.Background(), debuglog.Scope{
				Session: m.session.Name, RunID: m.liveAgent.RunID, Provider: firstNonEmpty(m.liveAgent.RunProvider, m.cfg.Provider),
				Model: firstNonEmpty(m.liveAgent.RunModel, m.cfg.Model()), Workspace: m.workspaceRoot(), Iteration: m.liveAgent.Iteration, Tool: m.liveAgent.Tool,
			})
			crashPath, _ := debuglog.RecordCrash(ctx, "tui.view", recovered, debug.Stack(), map[string]any{
				"width": m.width, "height": m.height, "phase": m.liveAgent.Phase,
			})
			fallback := fmt.Sprintf("Ephemera recovered from a rendering error.\n\nCrash report: %s\nSession log: %s", crashPath, debuglog.SessionDebugPath(m.session.Name))
			view = tea.NewView(fallback)
			view.AltScreen = true
			view.WindowTitle = "Ephemera · recovered"
		}
	}()
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

	view = tea.NewView(content)
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
	rendered := lipgloss.NewStyle().
		Foreground(m.styles.Text).
		Background(m.styles.Background).
		ColorWhitespace(true).
		Width(max(1, m.width)).
		Height(max(1, m.height)).
		Render(strings.Join(lines, "\n"))
	return reassertBackground(rendered, m.styles.Background)
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
	if m.thinkingViewport.Width() == 0 {
		m.thinkingViewport = viewport.New(viewport.WithWidth(viewportWidth), viewport.WithHeight(viewportHeight))
		m.thinkingViewport.MouseWheelEnabled = true
	} else {
		m.thinkingViewport.SetWidth(viewportWidth)
		m.thinkingViewport.SetHeight(viewportHeight)
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
	content := m.renderTranscript()
	if content != m.renderedTranscript {
		m.viewport.SetContent(content)
		m.renderedTranscript = content
	}
	if bottom {
		m.viewport.GotoBottom()
	}
	m.refreshThinkingViewport(bottom && m.inspectorTab == inspectorThinking)
}

func (m *Model) refreshThinkingViewport(bottom bool) {
	if !m.ready || m.thinkingViewport.Width() == 0 {
		return
	}
	content := m.renderThinkingPanel()
	if content != m.renderedThinking {
		wasBottom := m.thinkingViewport.AtBottom()
		m.thinkingViewport.SetContent(content)
		m.renderedThinking = content
		if bottom || (m.liveAgent.Active && wasBottom) {
			m.thinkingViewport.GotoBottom()
		}
	} else if bottom {
		m.thinkingViewport.GotoBottom()
	}
}

func (m Model) renderThinkingPanel() string {
	renderer := newCLIRenderer(m.styles, max(20, m.thinkingViewport.Width()))
	markdown := m.surfaceNotice()
	if m.liveAgent.Active {
		var live []string
		live = append(live, "### Live reasoning summary")
		if thought := strings.TrimSpace(m.liveAgent.Reasoning); thought != "" {
			live = append(live, thought)
		} else if thought := strings.TrimSpace(m.liveAgent.Thought); thought != "" {
			live = append(live, thought)
		}
		if plan := strings.TrimSpace(m.liveAgent.Plan); plan != "" {
			live = append(live, "### Live plan\n\n"+plan)
		}
		if activity := strings.TrimSpace(m.liveAgent.Activity); activity != "" {
			live = append(live, "### Current activity\n\n"+activity)
		}
		if len(live) > 1 {
			markdown = strings.Join(live, "\n\n") + "\n\n---\n\n" + markdown
		}
	}
	rows := []string{m.transcriptLine(m.styles.NoticeLabel, "surface")}
	rows = append(rows, strings.Split(renderer.Render(markdown), "\n")...)
	return strings.Join(rows, "\n")
}

func (m Model) activeViewportAtBottom() bool {
	if m.inspectorTab == inspectorThinking && m.thinkingViewport.Width() > 0 {
		return m.thinkingViewport.AtBottom()
	}
	return m.viewport.AtBottom()
}

func (m *Model) pageActiveViewport(direction int) {
	if m.inspectorTab == inspectorThinking {
		if direction < 0 {
			m.thinkingViewport.PageUp()
		} else {
			m.thinkingViewport.PageDown()
		}
		return
	}
	if direction < 0 {
		m.viewport.PageUp()
	} else {
		m.viewport.PageDown()
	}
}

func (m *Model) halfPageActiveViewport(direction int) {
	if m.inspectorTab == inspectorThinking {
		if direction < 0 {
			m.thinkingViewport.HalfPageUp()
		} else {
			m.thinkingViewport.HalfPageDown()
		}
		return
	}
	if direction < 0 {
		m.viewport.HalfPageUp()
	} else {
		m.viewport.HalfPageDown()
	}
}

func (m *Model) updateActiveViewport(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if m.inspectorTab == inspectorThinking {
		m.thinkingViewport, cmd = m.thinkingViewport.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return cmd
}

func (m *Model) renderTranscript() string {
	renderer := newCLIRenderer(m.styles, m.transcriptWidth())
	var rows []string
	appendSectionGap := func() {
		if len(rows) == 0 || rows[len(rows)-1] == renderer.blankRow() {
			return
		}
		rows = append(rows, renderer.blankRow())
	}

	if len(m.session.Messages) == 0 {
		rows = append(rows, m.transcriptLine(m.styles.NoticeLabel, "signal"))
		rows = append(rows, strings.Split(renderer.Render(fmt.Sprintf(`### The signal is open

Ask naturally, or press **/** to reveal the command palette.

- **/connect** — configure a provider
- **/models** — switch intelligence

Route: **%s** · **%s** · **%s** token context.`,
			m.providerName(),
			m.cfg.Model(),
			formatTokenCount(m.cfg.ContextTokens),
		)), "\n")...)
	}

	sections := m.buildTranscriptSections()
	selected := -1
	if m.timelineFocus {
		selected = m.clampedSelectedEvent(len(m.visibleAgentEvents()))
	}
	previous := transcriptSectionKind(-1)
	for _, section := range sections {
		if sectionNeedsGap(previous, section.kind) {
			appendSectionGap()
		}
		if section.kind == sectionAgentEvent && previous != sectionAgentEvent {
			rows = append(rows, m.renderInlineAgentHeader())
		}
		rows = append(rows, m.renderTranscriptSection(section, renderer, selected)...)
		previous = section.kind
	}
	return strings.Join(rows, "\n")
}

func (m Model) transcriptWidth() int {
	// Bubble's viewport pads short rows to its configured width with unstyled
	// spaces. Rendering two cells short therefore exposed the terminal's default
	// black background at the right edge. Paint the complete viewport width so
	// the viewport never has to append its own unstyled tail.
	return max(20, m.viewport.Width())
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

	width := m.transcriptWidth()
	labelText := glyph + " " + label + " "
	divider := strings.Repeat("─", max(0, width-lipgloss.Width(labelText)))
	labelStyle := style.Background(m.styles.Panel)
	dividerStyle := lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel)
	return labelStyle.Render(labelText) + dividerStyle.Render(divider)
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

	resolveRoleModel := func(value string) (string, string, error) {
		value = strings.TrimSpace(value)
		if strings.EqualFold(value, "inherit") || strings.EqualFold(value, "main") {
			return "", "", nil
		}
		routeID, model, explicit := parseModelSelection(value)
		if explicit {
			candidate, ok := m.cfg.ConfigForConnection(routeID)
			if !ok {
				return "", "", fmt.Errorf("unknown connected route %q", routeID)
			}
			available, err := m.modelAvailableForConfig(candidate, model, false)
			if err == nil && !available {
				return "", "", fmt.Errorf("model %q is not advertised by %s", model, candidate.Provider)
			}
			return routeID, model, nil
		}
		routeID, _, ok := m.findConnectedModel(model)
		if !ok {
			return "", "", fmt.Errorf("model %q was not found on a connected route", model)
		}
		return routeID, model, nil
	}

	parseBoundedInt := func(raw, label string, minimum, maximum int) (int, error) {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return 0, fmt.Errorf("%s must be a number", label)
		}
		if value < minimum || value > maximum {
			return 0, fmt.Errorf("%s must be between %d and %d", label, minimum, maximum)
		}
		return value, nil
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
		m.session.Events = nil
		m.lastAssistant = ""
		m.pendingApproval = nil
		m.session.Agent = history.AgentSnapshot{}
		m.liveAgent = liveAgentState{}
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
		_ = debuglog.EnsureSession(m.session.Name)
		_ = debuglog.WriteSession(m.session.Name, "info", "tui", "session created", "new interactive session created", map[string]any{
			"provider": m.cfg.Provider, "model": m.cfg.Model(), "workspace": m.workspaceRoot(),
		})
		_ = m.saveSession()
		m.notice = ""
		m.lastAssistant = ""
		m.pendingApproval = nil
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
		_ = debuglog.EnsureSession(m.session.Name)
		_ = debuglog.WriteSession(m.session.Name, "info", "tui", "session loaded", "saved interactive session loaded", map[string]any{
			"provider": m.cfg.Provider, "model": m.cfg.Model(), "workspace": m.workspaceRoot(),
			"messages": len(m.session.Messages), "events": len(m.session.Events),
		})
		_ = m.saveSession()
		m.notice = ""
		m.status = "Loaded session " + loaded.Name
		_ = config.Save(m.cfg)

	case "/sessions":
		query := strings.TrimSpace(strings.Join(args, " "))
		if query == "" {
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
			break
		}
		results, err := m.searchSessions(query)
		if err != nil {
			m.status = "Search failed: " + err.Error()
			break
		}
		if len(results) == 0 {
			m.notice = "No saved sessions match `" + escapeMarkdown(query) + "`."
		} else {
			var b strings.Builder
			fmt.Fprintf(&b, "### Saved sessions matching `%s`\n\n", escapeMarkdown(query))
			for _, result := range results {
				detail := strings.TrimSpace(result.Match)
				if detail == "" {
					detail = result.Provider + " / " + result.Model
				}
				fmt.Fprintf(&b, "- `%s` — %s\n", result.Name, escapeMarkdown(detail))
			}
			m.notice = b.String()
		}
		m.status = fmt.Sprintf("%d session match(es).", len(results))

	case "/provider":
		provider, ok := requireArg("/provider <connected-route>")
		if !ok {
			break
		}
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "chatgpt" {
			provider = "codex"
		}
		routeID, connected := m.cfg.FindConnection(provider)
		if !connected {
			if _, preset := config.Preset(provider); config.ValidProvider(provider) || preset {
				m.startConnect(provider)
				m.notice = "### Connection required\n\n`" + escapeMarkdown(provider) + "` has not been connected yet. Complete this setup once; afterward all of its models remain available from `/models`."
				break
			}
			m.status = "Unknown or unconnected provider: " + provider
			break
		}
		m.cfg.ActivateConnection(routeID)
		m.session.Provider = m.cfg.Provider
		m.session.Model = m.cfg.Model()
		m.status = fmt.Sprintf("Route → %s · model → %s", m.providerName(), m.cfg.Model())
		_ = config.Save(m.cfg)

	case "/models":
		m.openModelChooser()

	case "/model":
		if len(args) == 0 {
			m.openModelChooser()
			break
		}
		rawSelection := strings.TrimSpace(strings.Join(args, " "))
		routeID, model, explicitRoute := parseModelSelection(rawSelection)
		candidate := m.cfg
		if explicitRoute {
			var found bool
			candidate, found = m.cfg.ConfigForConnection(routeID)
			if !found {
				m.status = "Unknown connection: " + routeID
				m.notice = "### Model not changed\n\nThe selected route is no longer connected. Run `/models` to refresh the unified model list."
				break
			}
		} else if matchedID, matchedConfig, found := m.findConnectedModel(model); found {
			routeID = matchedID
			candidate = matchedConfig
		} else {
			routeID = m.cfg.ActiveConnection
		}

		activate := func() {
			if routeID != "" {
				m.cfg.ActivateConnection(routeID)
			}
			m.cfg.SetModel(model)
			m.session.Provider = m.cfg.Provider
			m.session.Model = m.cfg.Model()
		}

		available, err := m.modelAvailableForConfig(candidate, model, false)
		if err != nil {
			if candidate.Provider == "codex" {
				m.status = "Codex model change blocked: " + err.Error()
				m.notice = "### Codex model not changed\n\nThe Codex model list could not be loaded:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nOpen Codex once to refresh its login and model cache, then retry `/models`."
				break
			}
			activate()
			m.status = "Model → " + m.cfg.Model() + " via " + m.providerName() + " (unverified)"
			m.notice = "### Model and route changed without catalog verification\n\nThe saved provider catalog could not be checked:\n\n`" + escapeMarkdown(err.Error()) + "`\n\nThe selected remembered route is active."
			_ = config.Save(m.cfg)
			break
		}
		if !available {
			if candidate.Provider == "codex" {
				m.status = fmt.Sprintf("Model %q is not available from Codex.", model)
				m.notice = "### Codex model blocked\n\n`" + escapeMarkdown(model) + "` is not in the Codex ChatGPT model list. Choose one of the listed Codex models."
				break
			}
			activate()
			m.status = "Model → " + m.cfg.Model() + " via " + m.providerName() + " (not advertised)"
			m.notice = "### Model changed outside catalog\n\n`" + escapeMarkdown(model) + "` was not advertised by `" + escapeMarkdown(m.providerName()) + "`. The saved route was still selected because provider catalogs can be incomplete."
			_ = config.Save(m.cfg)
			break
		}
		activate()
		m.status = "Model → " + m.cfg.Model() + " · route → " + m.providerName()
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

	case "/agent":
		value := "status"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch value {
		case "on":
			m.cfg.AgentEnabled = true
			m.status = "Agent mode enabled · policy " + string(m.cfg.ApprovalPolicy)
			_ = config.Save(m.cfg)
		case "off":
			m.cfg.AgentEnabled = true
			m.status = "Agent mode is always on."
			_ = config.Save(m.cfg)
		case "auto", "auto-approve":
			m.cfg.AgentEnabled = true
			m.cfg.ApprovalPolicy = config.ApprovalAutoApprove
			_ = config.Save(m.cfg)
			m.notice = m.agentNotice()
			if m.pendingApproval != nil {
				return false, m.approvePending()
			}
			m.status = "Auto-approve enabled · all agent tools run immediately"
		case "safe":
			m.cfg.ApprovalPolicy = config.ApprovalApproveWrites
			m.status = "Safe approvals enabled · writes and shell require confirmation"
			_ = config.Save(m.cfg)
		case "read-only", "readonly":
			m.cfg.ApprovalPolicy = config.ApprovalReadOnly
			m.status = "Read-only agent policy enabled"
			_ = config.Save(m.cfg)
		case "status":
			m.status = "Agent status opened."
		default:
			m.status = "Usage: /agent <on|auto|safe|read-only|status>"
		}
		m.notice = m.agentNotice()

	case "/subagent":
		action := "status"
		if len(args) > 0 {
			action = strings.ToLower(args[0])
		}
		switch action {
		case "on":
			m.cfg.SubagentEnabled = true
			m.status = "Lightweight subagent enabled."
		case "off":
			m.cfg.SubagentEnabled = false
			m.status = "Lightweight subagent disabled."
		case "auto":
			if len(args) < 2 {
				m.status = fmt.Sprintf("Subagent automatic routing: %t", m.cfg.SubagentAutoRoute)
				break
			}
			switch strings.ToLower(args[1]) {
			case "on":
				m.cfg.SubagentAutoRoute = true
				m.status = "Subagent automatic routing enabled."
			case "off":
				m.cfg.SubagentAutoRoute = false
				m.status = "Subagent automatic routing disabled."
			default:
				m.status = "Usage: /subagent auto <on|off>"
			}
		case "model":
			if len(args) < 2 {
				m.status = "Choose a model from autocomplete or use /subagent model inherit"
				m.notice = m.subagentNotice()
				break
			}
			route, model, err := resolveRoleModel(strings.Join(args[1:], " "))
			if err != nil {
				m.status = "Subagent model unchanged: " + err.Error()
				break
			}
			m.cfg.SubagentProvider, m.cfg.SubagentModel = route, model
			if model == "" {
				m.status = "Subagent now inherits the main model."
			} else {
				m.status = "Subagent model → " + model
			}
		case "steps":
			if len(args) < 2 {
				m.status = fmt.Sprintf("Subagent max steps: %d", m.cfg.SubagentMaxSteps)
				break
			}
			value, err := parseBoundedInt(args[1], "subagent steps", 1, 8)
			if err != nil {
				m.status = err.Error()
				break
			}
			m.cfg.SubagentMaxSteps = value
			m.status = fmt.Sprintf("Subagent max steps → %d", value)
		case "tokens":
			if len(args) < 2 {
				m.status = fmt.Sprintf("Subagent token cap: %s", formatTokenCount(int(m.cfg.SubagentMaxTokens)))
				break
			}
			value, err := parseBoundedInt(args[1], "subagent tokens", 500, 8000)
			if err != nil {
				m.status = err.Error()
				break
			}
			m.cfg.SubagentMaxTokens = int64(value)
			m.status = fmt.Sprintf("Subagent token cap → %s", formatTokenCount(value))
		case "status":
			m.status = "Subagent status opened."
		default:
			m.status = "Usage: /subagent <on|off|status|auto|model|steps|tokens>"
		}
		_ = config.Save(m.cfg)
		m.notice = m.subagentNotice()

	case "/director":
		action := "status"
		if len(args) > 0 {
			action = strings.ToLower(args[0])
		}
		switch action {
		case "on":
			m.cfg.DirectorEnabled = true
			m.status = "Director mode enabled."
		case "off":
			m.cfg.DirectorEnabled = false
			m.status = "Director mode disabled."
		case "model", "instrument":
			if len(args) < 2 {
				m.status = "Choose a connected model from autocomplete or use inherit"
				m.notice = m.directorNotice()
				break
			}
			route, model, err := resolveRoleModel(strings.Join(args[1:], " "))
			if err != nil {
				m.status = "Director configuration unchanged: " + err.Error()
				break
			}
			if action == "model" {
				m.cfg.DirectorProvider, m.cfg.DirectorModel = route, model
				m.status = "Director model → " + firstNonEmpty(model, "inherit main")
			} else {
				m.cfg.InstrumentProvider, m.cfg.InstrumentModel = route, model
				m.status = "Instrument model → " + firstNonEmpty(model, "inherit director")
			}
		case "weight":
			if len(args) < 2 {
				m.status = fmt.Sprintf("Instrument influence: %d%%", m.cfg.InstrumentWeight)
				break
			}
			value, err := parseBoundedInt(args[1], "instrument weight", 0, 100)
			if err != nil {
				m.status = err.Error()
				break
			}
			m.cfg.InstrumentWeight = value
			m.status = fmt.Sprintf("Instrument influence → %d%%", value)
		case "steps":
			if len(args) < 2 {
				m.status = fmt.Sprintf("Director/instrument steps: %d/%d", m.cfg.DirectorMaxSteps, m.cfg.InstrumentMaxSteps)
				break
			}
			directorSteps, err := parseBoundedInt(args[1], "director steps", 4, 20)
			if err != nil {
				m.status = err.Error()
				break
			}
			instrumentSteps := m.cfg.InstrumentMaxSteps
			if len(args) > 2 {
				instrumentSteps, err = parseBoundedInt(args[2], "instrument steps", 1, 6)
				if err != nil {
					m.status = err.Error()
					break
				}
			}
			m.cfg.DirectorMaxSteps, m.cfg.InstrumentMaxSteps = directorSteps, instrumentSteps
			m.status = fmt.Sprintf("Director/instrument steps → %d/%d", directorSteps, instrumentSteps)
		case "status":
			m.status = "Director status opened."
		default:
			m.status = "Usage: /director <on|off|status|model|instrument|weight|steps>"
		}
		_ = config.Save(m.cfg)
		m.notice = m.directorNotice()

	case "/approval":
		value, ok := requireArg("/approval <auto|safe|read-only|workspace-write|chat>")
		if !ok {
			break
		}
		policy, valid := config.ParseApprovalPolicy(value)
		if !valid {
			m.status = "Unknown approval policy: " + value
			break
		}
		m.cfg.ApprovalPolicy = policy
		m.cfg.AgentEnabled = true
		_ = config.Save(m.cfg)
		m.notice = m.agentNotice()
		if policy == config.ApprovalAutoApprove && m.pendingApproval != nil {
			return false, m.approvePending()
		}
		if policy == config.ApprovalAutoApprove {
			m.status = "Auto-approve enabled · all agent tools run immediately"
		} else {
			m.status = "Approval policy → " + string(policy)
		}

	case "/approve":
		return false, m.approvePending()

	case "/reject":
		m.rejectPending()

	case "/plan":
		m.notice = m.planNotice()
		m.status = "Plan opened."

	case "/surface":
		action := "open"
		if len(args) > 0 {
			action = strings.ToLower(args[0])
		}
		switch action {
		case "open":
			m.inspectorTab = inspectorThinking
			m.timelineFocus = false
			m.refreshThinkingViewport(true)
			m.status = "Beneath the Surface opened."
		case "copy":
			m.copyReasoningSurface()
		case "export":
			target := ""
			if len(args) > 1 {
				target = strings.Join(args[1:], " ")
			}
			path, err := m.exportSurface(target)
			if err != nil {
				m.recordError("surface export failed", err, map[string]any{"target": target})
				m.status = "Surface export failed: " + err.Error()
			} else {
				m.status = "Surface exported → " + path
			}
		default:
			m.status = "Usage: /surface [copy|export [path]]"
		}

	case "/tools":
		m.notice = m.toolsNotice()
		m.status = "Tools opened."

	case "/eval":
		report, err := agent.RunDeterministicEval(context.Background())
		if err != nil {
			m.recordError("agent capability eval failed", err, nil)
			m.notice = "### Agent capability eval\n\n`" + escapeMarkdown(err.Error()) + "`"
			m.status = "Agent eval failed."
			break
		}
		m.notice = agent.FormatEvalReport(report)
		if report.Failed() > 0 {
			m.recordFailure("agent capability eval failed", fmt.Sprintf("%d deterministic agent evaluations failed", report.Failed()), map[string]any{
				"failed": report.Failed(),
				"passed": report.Passed(),
			})
			m.status = fmt.Sprintf("Agent eval failed · %d/%d passed", report.Passed(), len(report.Results))
		} else {
			m.status = fmt.Sprintf("Agent eval passed · %d/%d", report.Passed(), len(report.Results))
		}

	case "/sandbox":
		value, ok := requireArg("/sandbox <none|snapshot|docker>")
		if !ok {
			break
		}
		mode, valid := parseSandboxMode(value)
		if !valid {
			m.status = "Usage: /sandbox <none|snapshot|docker>"
			break
		}
		m.cfg.SandboxMode = mode
		_ = config.Save(m.cfg)
		m.notice = m.safetyNotice()
		m.status = "Sandbox mode → " + string(mode)

	case "/dry-run":
		value := "toggle"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		enabled, valid := toggleSetting(m.cfg.AgentDryRun, value)
		if !valid {
			m.status = "Usage: /dry-run <on|off|toggle>"
			break
		}
		m.cfg.AgentDryRun = enabled
		_ = config.Save(m.cfg)
		m.notice = m.safetyNotice()
		m.status = fmt.Sprintf("Dry run → %t", enabled)

	case "/rollback":
		value := "now"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch value {
		case "now":
			m.rollbackLatestSnapshot()
		case "auto", "on":
			m.cfg.AgentAutoRollback = true
			_ = config.Save(m.cfg)
			m.notice = m.safetyNotice()
			m.status = "Automatic rollback → enabled"
		case "manual", "off":
			m.cfg.AgentAutoRollback = false
			_ = config.Save(m.cfg)
			m.notice = m.safetyNotice()
			m.status = "Automatic rollback → disabled; failed-run snapshots are retained"
		case "status":
			m.notice = m.safetyNotice()
			m.status = "Safety status opened."
		default:
			m.status = "Usage: /rollback [now|auto|manual|status]"
		}

	case "/index":
		value, ok := requireArg("/index <on|off|rebuild|status>")
		if !ok {
			break
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "on":
			m.cfg.AgentSemanticIndex = true
			_ = config.Save(m.cfg)
			m.status = "Semantic codebase index → enabled"
		case "off":
			m.cfg.AgentSemanticIndex = false
			_ = config.Save(m.cfg)
			m.status = "Semantic codebase index → disabled"
		case "rebuild":
			m.rebuildSemanticIndex()
		case "status":
			m.status = "Codebase index status opened."
		default:
			m.status = "Usage: /index <on|off|rebuild|status>"
			break
		}
		m.notice = m.intelligenceNotice()

	case "/tdd":
		value := "toggle"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		enabled, valid := toggleSetting(m.cfg.AgentTDDMode, value)
		if !valid {
			m.status = "Usage: /tdd <on|off|toggle>"
			break
		}
		m.cfg.AgentTDDMode = enabled
		_ = config.Save(m.cfg)
		m.notice = m.intelligenceNotice()
		m.status = fmt.Sprintf("TDD mode → %t", enabled)

	case "/learn":
		value := "toggle"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		enabled, valid := toggleSetting(m.cfg.AgentLearnMemory, value)
		if !valid {
			m.status = "Usage: /learn <on|off|toggle>"
			break
		}
		m.cfg.AgentLearnMemory = enabled
		_ = config.Save(m.cfg)
		m.notice = m.intelligenceNotice()
		m.status = fmt.Sprintf("Episodic learning → %t", enabled)

	case "/thinking":
		value := "toggle"
		if len(args) > 0 {
			value = strings.ToLower(strings.TrimSpace(args[0]))
		}
		valid := true
		switch value {
		case "on", "show", "true":
			m.cfg.ShowThinking = true
		case "off", "hide", "false":
			m.cfg.ShowThinking = false
		case "toggle":
			m.cfg.ShowThinking = !m.cfg.ShowThinking
		default:
			valid = false
			m.status = "Usage: /thinking <on|off|toggle>"
		}
		if !valid {
			break
		}
		_ = config.Save(m.cfg)
		m.notice = m.agentNotice()
		if m.cfg.ShowThinking {
			m.status = "Beneath the Surface → visible"
		} else {
			m.status = "Beneath the Surface → hidden"
		}

	case "/details":
		m.cfg.ToolDetails = !m.cfg.ToolDetails
		_ = config.Save(m.cfg)
		m.status = fmt.Sprintf("Tool details → %t", m.cfg.ToolDetails)

	case "/run":
		if m.pendingApproval != nil {
			m.status = "Approval pending · /approve or /reject"
			break
		}
		if len(m.session.Messages) == 0 {
			m.status = "Nothing for the agent to run yet."
			break
		}
		m.cfg.AgentEnabled = true
		m.busy = true
		m.status = "Agent continuing..."
		return false, tea.Batch(m.spinner.Tick, m.generateCmd())

	case "/stop":
		m.cancelGeneration()

	case "/diff":
		m.notice = m.localToolNotice("git_diff")
		m.status = "Diff opened."

	case "/compact":
		m.compactAgentEvents()

	case "/compact-view":
		value := "toggle"
		if len(args) > 0 {
			value = strings.ToLower(args[0])
		}
		enabled, valid := toggleSetting(m.compactView, value)
		if !valid {
			m.status = "Usage: /compact-view [on|off|toggle]"
			break
		}
		m.compactView = enabled
		m.status = fmt.Sprintf("Compact view → %t", enabled)

	case "/config":
		m.notice = m.configNotice()
		m.status = "Config opened."

	case "/codex":
		action := "status"
		if len(args) > 0 {
			action = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch action {
		case "", "status":
			m.notice = m.codexNotice()
			m.status = "Codex bridge status opened."
		case "budget":
			if len(args) < 2 {
				m.notice = m.codexNotice()
				m.status = fmt.Sprintf("Codex bridge response target: %s tokens", formatTokenCount(int(m.cfg.CodexBridgeMaxTokens)))
				break
			}
			value, err := parseBoundedInt(args[1], "Codex bridge budget", 512, 8000)
			if err != nil {
				m.status = err.Error()
				break
			}
			m.cfg.CodexBridgeMaxTokens = int64(value)
			_ = config.Save(m.cfg)
			m.notice = m.codexNotice()
			m.status = fmt.Sprintf("Codex bridge response target → %s tokens", formatTokenCount(value))
		default:
			m.status = "Usage: /codex [status|budget <512-8000>]"
		}

	case "/debuglog", "/logs":
		action := "tail"
		if len(args) > 0 {
			action = strings.ToLower(strings.TrimSpace(args[0]))
		}
		switch action {
		case "", "status", "tail":
			m.notice = m.debugLogNotice(20)
			m.status = "Debug log opened · " + debugLogPath()
		case "export", "bundle":
			path, err := debuglog.ExportSession(m.session.Name)
			if err != nil {
				m.recordError("export debug bundle failed", err, nil)
				m.status = "Debug bundle export failed: " + err.Error()
				break
			}
			m.notice = "### Diagnostic bundle exported\n\n`" + escapeMarkdown(path) + "`\n\nThe ZIP contains the session snapshot, structured tool/debug/context logs, rotations, and the latest crash report when present."
			m.status = "Diagnostic bundle exported."
		case "clear":
			if err := clearDebugLog(m.session.Name); err != nil {
				m.recordError("clear debug log failed", err, nil)
				m.status = "Debug log clear failed: " + err.Error()
				break
			}
			m.notice = "### Debug log\n\nGlobal and current-session diagnostics were cleared. New events will be recorded automatically at:\n\n`" + escapeMarkdown(debugLogPath()) + "`"
			m.status = "Debug log cleared."
		default:
			m.status = "Usage: /debuglog [status|tail|export|clear]"
		}

	case "/memory":
		if len(args) == 0 {
			m.notice = m.memoryNotice()
			m.status = "Memory sources opened."
			break
		}
		scope := "global"
		start := 0
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "add", "global":
			start = 1
		case "project", "workspace":
			scope = "project"
			start = 1
		}
		preference := strings.TrimSpace(strings.Join(args[start:], " "))
		if preference == "" {
			m.status = "Usage: /memory [add|project] <preference>"
			break
		}
		result := tools.NewRegistry(m.cfg).Execute(context.Background(), tools.Call{Name: "prefer", Arguments: map[string]any{"preference": preference, "scope": scope}})
		if !result.OK {
			m.status = "Memory update failed: " + firstNonEmpty(result.Error, result.Output, "unknown error")
			break
		}
		m.notice = m.memoryNotice()
		m.status = "Recorded " + scope + " preference."

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

	case "/redo":
		m.redoLastUndo()

	case "/export":
		target := ""
		if len(args) > 0 {
			target = strings.Join(args, " ")
		}
		path, err := m.exportTranscript(target)
		if err != nil {
			m.recordError("transcript export failed", err, map[string]any{"target": target})
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
		Prompt:      lipgloss.NewStyle().Foreground(styles.Primary).Background(styles.Panel).ColorWhitespace(true),
		Text:        lipgloss.NewStyle().Foreground(styles.Text).Background(styles.Panel).ColorWhitespace(true),
		Placeholder: lipgloss.NewStyle().Foreground(styles.Muted).Background(styles.Panel).ColorWhitespace(true),
		Suggestion:  lipgloss.NewStyle().Foreground(styles.Secondary).Background(styles.Panel).ColorWhitespace(true),
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
	if routeID, ok := cfg.FindConnectionForModel(loaded.Provider, loaded.Model); ok {
		cfg.ActivateConnection(routeID)
	} else if config.ValidProvider(loaded.Provider) {
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
	err := m.store.Save(m.session)
	if err != nil {
		m.recordError("session save failed", err, nil)
	}
	return err
}

func (m *Model) loadSession(name string) (history.Session, error) {
	loaded, err := loadFromStore(m.store, name)
	if err != nil {
		m.recordError("session load failed", err, map[string]any{"requested_session": name})
	}
	return loaded, err
}

func (m *Model) listSessions() ([]string, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store unavailable")
	}
	names, err := m.store.List()
	if err != nil {
		m.recordError("session list failed", err, nil)
	}
	return names, err
}

func (m *Model) searchSessions(query string) ([]history.SearchResult, error) {
	if m.store == nil {
		return nil, fmt.Errorf("session store unavailable")
	}
	results, err := m.store.Search(query, 20)
	if err != nil {
		m.recordError("session search failed", err, map[string]any{"query": query})
	}
	return results, err
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
		m.recordError("clipboard write failed", err, nil)
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
