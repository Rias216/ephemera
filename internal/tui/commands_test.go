package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/theme"
)

func TestModelCommandPullsCatalogFromActiveProvider(t *testing.T) {
	server := modelListServer(t, []string{"provider-live-a", "provider-live-b", "provider-live-c", "provider-live-d", "provider-live-e", "provider-live-f", "provider-live-g", "provider-live-h"})

	m := Model{cfg: config.Default()}
	m.cfg.Provider = "compatible"
	m.cfg.CompatibleURL = server.URL

	got := m.commandSuggestions("/model ")
	if !hasSuggestion(got, "/model provider-live-a") || !hasSuggestion(got, "/model provider-live-h") {
		t.Fatalf("/model suggestions missing provider models: %#v", got)
	}
}

func TestConnectModelStepPullsCatalogFromChosenProvider(t *testing.T) {
	server := modelListServer(t, []string{"connect-live-model"})
	m := Model{
		cfg:     config.Default(),
		connect: &connectFlow{Provider: "compatible", BaseURL: server.URL, Step: connectModel},
	}

	got := m.connectSuggestions()
	if !hasSuggestion(got, "connect-live-model") {
		t.Fatalf("connect model suggestions missing provider model: %#v", got)
	}
}

func TestConnectPresetUsesCompatibleProviderAndBaseURL(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	m.startConnect("nvidia")

	if m.connect == nil {
		t.Fatal("connect flow unexpectedly finished")
	}
	if m.connect.Provider != "compatible" {
		t.Fatalf("provider = %q, want compatible", m.connect.Provider)
	}
	if m.connect.Name != "nvidia" {
		t.Fatalf("name = %q, want nvidia", m.connect.Name)
	}
	if m.connect.BaseURL != config.NVIDIABaseURL {
		t.Fatalf("base URL = %q, want %q", m.connect.BaseURL, config.NVIDIABaseURL)
	}
	if m.connect.Step != connectAPIKey {
		t.Fatalf("step = %q, want API key", m.connect.Step)
	}
}

func TestConnectProviderSuggestionsIncludeCompatiblePresets(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose"), connect: &connectFlow{Step: connectProvider}}

	got := m.connectSuggestions()
	for _, want := range []string{"openrouter", "groq", "nvidia", "lm-studio"} {
		if !hasSuggestion(got, want) {
			t.Fatalf("connect provider suggestions missing %q: %#v", want, got)
		}
	}
}

func TestConnectCompatibleNamePresetSkipsBaseURLStep(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose"), connect: &connectFlow{Provider: "compatible", Step: connectName}}
	m.input = textInputForTest("openrouter")

	m.submitConnectStep()

	if m.connect.Name != "openrouter" {
		t.Fatalf("name = %q, want openrouter", m.connect.Name)
	}
	if m.connect.BaseURL != config.OpenRouterBaseURL {
		t.Fatalf("base URL = %q, want %q", m.connect.BaseURL, config.OpenRouterBaseURL)
	}
	if m.connect.Step != connectAPIKey {
		t.Fatalf("step = %q, want API key", m.connect.Step)
	}
}

func TestConnectBlankCompatibleBaseURLUsesNamedPresetDefault(t *testing.T) {
	m := Model{
		cfg:     config.Default(),
		styles:  theme.New("rose"),
		connect: &connectFlow{Provider: "compatible", Name: "groq", Step: connectBaseURL},
	}
	m.input = textInputForTest("")

	m.submitConnectStep()

	if m.connect.BaseURL != config.GroqBaseURL {
		t.Fatalf("base URL = %q, want %q", m.connect.BaseURL, config.GroqBaseURL)
	}
	if m.connect.Step != connectAPIKey {
		t.Fatalf("step = %q, want API key", m.connect.Step)
	}
}

func TestMarkdownStyleUsesPanelBackgroundForInlineText(t *testing.T) {
	styles := theme.New("rose")
	got := markdownStyle(styles)

	panelBackgrounds := map[string]**string{
		"document":  &got.Document.BackgroundColor,
		"paragraph": &got.Paragraph.BackgroundColor,
		"heading":   &got.Heading.BackgroundColor,
		"h1":        &got.H1.BackgroundColor,
		"code":      &got.Code.BackgroundColor,
	}
	want := theme.Hex(styles.Panel)
	for name, background := range panelBackgrounds {
		if background == nil || *background == nil || **background != want {
			t.Fatalf("%s markdown background = %v, want %q", name, backgroundValue(background), want)
		}
	}

	if got.CodeBlock.Chroma == nil || got.CodeBlock.Chroma.Background.BackgroundColor == nil || *got.CodeBlock.Chroma.Background.BackgroundColor != want {
		t.Fatalf("code block chroma background = %v, want %q", got.CodeBlock.Chroma, want)
	}
}

func TestTranscriptRowsDoNotPaintInlineBackgroundBoxes(t *testing.T) {
	cfg := config.Default()
	styles := theme.New("rose")
	m := Model{cfg: cfg, styles: styles}
	m.viewport.SetWidth(60)
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "Hello")

	got := m.renderTranscript()
	if strings.Contains(got, "48;2;") || strings.Contains(got, "\x1b[40m") {
		t.Fatalf("transcript contains inline background escape boxes: %q", got)
	}
}

func TestStripANSIBackgroundsRemovesResetsAndBackgrounds(t *testing.T) {
	input := "\x1b[0;38;2;252;231;243;48;2;0;0;0mHello\x1b[0m"

	got := stripANSIBackgrounds(input)

	if strings.Contains(got, "48;2") || strings.Contains(got, "\x1b[0") {
		t.Fatalf("stripANSIBackgrounds() = %q, want no background or reset escapes", got)
	}
	if !strings.Contains(got, "38;2;252;231;243") {
		t.Fatalf("stripANSIBackgrounds() = %q, want foreground preserved", got)
	}
}

func TestGradientBorderAnimatesWithoutChangingGeometry(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	base := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(72).Height(48)
	rendered := base.Render("content")

	m.animationElapsed = 1500 * time.Millisecond
	first := m.localizedGradientBorder(rendered, 0)
	m.animationElapsed = 1500*time.Millisecond + time.Second/AnimationFPS
	second := m.localizedGradientBorder(rendered, 0)

	if first == second {
		t.Fatal("gradient outline did not animate")
	}
	if lipgloss.Width(first) != lipgloss.Width(second) || lipgloss.Height(first) != lipgloss.Height(second) {
		t.Fatalf("animation changed panel geometry: %dx%d -> %dx%d",
			lipgloss.Width(first), lipgloss.Height(first), lipgloss.Width(second), lipgloss.Height(second))
	}
}

func TestKnifeFadeUsesSmoothPinkGradient(t *testing.T) {
	palette := roseGlow
	const perimeter = 100.0
	head := 40.25

	colors := make(map[string]bool)
	for position := 28.0; position <= 42.0; position++ {
		shade := knifeFadeColor(palette, position, head, 82, 0.2, perimeter, 0)
		colors[theme.Hex(shade)] = true
		r, g, b := colorRGB(shade)
		if r < b || g > b {
			t.Fatalf("knife fade emitted non-pink color %s", theme.Hex(shade))
		}
	}
	if len(colors) < 8 {
		t.Fatalf("knife fade produced only %d distinct shades; want a smooth gradient", len(colors))
	}
}

func TestKnifeFadeMovesFractionallyBetweenCells(t *testing.T) {
	const position = 34.0
	first := knifeFadeColor(roseGlow, position, 40.00, 82, 0.2, 100, 0)
	second := knifeFadeColor(roseGlow, position, 40.25, 82, 0.2, 100, 0)
	if first == second {
		t.Fatalf("fractional movement did not change color: %s", theme.Hex(first))
	}
}

func TestAmbientFadeShiftsRestingOutlineThroughPinkShades(t *testing.T) {
	const position = 24.0
	near := knifeFadeColor(roseGlow, position, 80, position, 0.2, 120, 0)
	far := knifeFadeColor(roseGlow, position, 80, position+40, 0.2, 120, 0)
	if near == far {
		t.Fatalf("ambient outline fade did not alter the resting color: %s", theme.Hex(near))
	}
	r, g, b := colorRGB(near)
	if r < b || g > b {
		t.Fatalf("ambient outline fade emitted non-pink color %s", theme.Hex(near))
	}
}

func TestBaseOutlineGradientShiftsIndependentlyOfGlimmer(t *testing.T) {
	const position = 7.0
	first := knifeFadeColor(roseGlow, position, 70, 50, 0.10, 120, 0)
	second := knifeFadeColor(roseGlow, position, 70, 50, 0.35, 120, 0)
	if theme.Hex(first) == theme.Hex(second) {
		t.Fatalf("base gradient phase did not shift color: %s", theme.Hex(first))
	}
}

func TestSuggestionPaletteHeightStaysStableWhileTypingCommand(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose"), height: 32, width: 100}
	m.input = textInputForTest("/")
	m.suggestions = m.commandSuggestions(m.input.Value())
	firstHeight := m.suggestionHeight()
	firstRenderedHeight := lipgloss.Height(m.renderSuggestions())

	m.input.SetValue("/help")
	m.suggestions = m.commandSuggestions(m.input.Value())
	secondHeight := m.suggestionHeight()
	secondRenderedHeight := lipgloss.Height(m.renderSuggestions())

	if firstHeight == 0 || firstHeight != secondHeight {
		t.Fatalf("suggestion layout heights = %d and %d, want one stable non-zero height", firstHeight, secondHeight)
	}
	if firstRenderedHeight != secondRenderedHeight {
		t.Fatalf("rendered palette heights = %d and %d, want stable height", firstRenderedHeight, secondRenderedHeight)
	}
}

func TestBlurStopsAnimationAndFocusStartsFreshGeneration(t *testing.T) {
	m := Model{focused: true, animationGeneration: 7, input: textInputForTest("")}
	updated, cmd := m.Update(tea.BlurMsg{})
	blurred := updated.(Model)
	if blurred.focused {
		t.Fatal("blur did not pause the model")
	}
	if blurred.animationGeneration != 8 {
		t.Fatalf("blur generation = %d, want 8", blurred.animationGeneration)
	}
	if cmd != nil {
		t.Fatal("blur unexpectedly scheduled another animation frame")
	}

	updated, cmd = blurred.Update(tea.FocusMsg{})
	focused := updated.(Model)
	if !focused.focused {
		t.Fatal("focus did not resume the model")
	}
	if focused.animationGeneration != 9 {
		t.Fatalf("focus generation = %d, want 9", focused.animationGeneration)
	}
	if cmd == nil {
		t.Fatal("focus did not schedule animation resumption")
	}
}

func TestAnimationUsesElapsedTimeAndKeepsRunningWhileTyping(t *testing.T) {
	start := time.Now()
	m := Model{
		focused:             true,
		animationGeneration: 3,
		animationLastTick:   start,
		input:               textInputForTest(""),
	}

	updated, cmd := m.Update(animationTickMsg{generation: 3, at: start.Add(250 * time.Millisecond)})
	got := updated.(Model)
	if got.animationElapsed != 250*time.Millisecond {
		t.Fatalf("animation elapsed = %s, want 250ms", got.animationElapsed)
	}
	if cmd == nil {
		t.Fatal("animation tick did not schedule the next frame")
	}

	updated, _ = got.Update(tea.KeyPressMsg{})
	typed := updated.(Model)
	if typed.animationElapsed != got.animationElapsed {
		t.Fatalf("typing changed animation time: %s -> %s", got.animationElapsed, typed.animationElapsed)
	}
}

func TestViewDeclaresV2ScreenFeaturesAndRealCursor(t *testing.T) {
	m := New(config.Default(), nil, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !view.AltScreen || !view.ReportFocus {
		t.Fatalf("view flags: AltScreen=%v ReportFocus=%v", view.AltScreen, view.ReportFocus)
	}
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion", view.MouseMode)
	}
	if view.BackgroundColor == nil || view.ForegroundColor == nil {
		t.Fatal("view did not declare terminal foreground/background colors")
	}
	if view.Cursor == nil {
		t.Fatal("focused input did not expose a real terminal cursor")
	}
}

func TestBorderPositionsCoverPerimeterOnce(t *testing.T) {
	const width, height = 17, 9
	want := borderPerimeter(width, height)
	seen := make(map[int]bool, want)

	for x := 0; x < width; x++ {
		seen[borderPosition(x, 0, width, height)] = true
		seen[borderPosition(x, height-1, width, height)] = true
	}
	for y := 1; y < height-1; y++ {
		seen[borderPosition(0, y, width, height)] = true
		seen[borderPosition(width-1, y, width, height)] = true
	}

	if len(seen) != want {
		t.Fatalf("unique perimeter positions = %d, want %d", len(seen), want)
	}
	for position := 0; position < want; position++ {
		if !seen[position] {
			t.Fatalf("perimeter position %d was not assigned", position)
		}
	}
}

func TestInteriorBorderLikeRuneIsNotAnimated(t *testing.T) {
	if isOuterBorderRune('│', 5, 2, 20, 6) {
		t.Fatal("interior border-like rune was treated as the panel outline")
	}
	if !isOuterBorderRune('│', 0, 2, 20, 6) {
		t.Fatal("outer vertical border was not recognized")
	}
}

func TestRenderLogoGlowPreservesBrandWidth(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	got := m.renderLogoGlow()

	if lipgloss.Width(got) != lipgloss.Width("✦ EPHEMERA") {
		t.Fatalf("renderLogoGlow() width = %d, want %d", lipgloss.Width(got), lipgloss.Width("✦ EPHEMERA"))
	}
}

func TestBuildRequestMessagesTrimsOldestContext(t *testing.T) {
	messages := []history.Message{
		{Role: "user", Content: strings.Repeat("old ", 400)},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "new ask"},
	}

	got, stats := buildRequestMessages(messages, "system", 64)

	if len(got) != 1 || got[0].Role != "user" || got[0].Content != "new ask" {
		t.Fatalf("request messages = %#v, want only latest user prompt", got)
	}
	if stats.SentMessages != 1 || stats.TotalMessages != 3 || stats.DroppedMessages != 2 {
		t.Fatalf("stats = %#v, want 1 sent / 3 total / 2 dropped", stats)
	}
}

func TestBudgetCommandUpdatesContextTokens(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/budget 8192")

	if m.cfg.ContextTokens != 8192 {
		t.Fatalf("context budget = %d, want 8192", m.cfg.ContextTokens)
	}
	if !strings.Contains(m.status, "8.2k") {
		t.Fatalf("status = %q, want formatted budget", m.status)
	}
}

func TestModelsCommandOpensInteractiveChooser(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.Provider = "openai"
	_, _ = m.handleCommand("/models")

	if m.input.Value() != "/model " {
		t.Fatalf("input = %q, want /model chooser", m.input.Value())
	}
	if !hasSuggestion(m.suggestions, "gpt-4.1-mini") {
		t.Fatalf("model chooser missing OpenAI/GPT fallback or current choices: %#v", m.suggestions)
	}
}

func TestEnterChoosesHighlightedModelSuggestion(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.input = textInputForTest("/model ")
	m.suggestions = []suggestion{{Value: "/model gpt-4.1-mini", Label: "gpt-4.1-mini", Description: "suggested openai model"}}

	if !m.acceptCommandSuggestionForEnter() {
		t.Fatal("Enter did not accept command suggestion")
	}
	if m.input.Value() != "/model gpt-4.1-mini" {
		t.Fatalf("input = %q, want selected model command", m.input.Value())
	}
}

func TestSessionCommandsHandleMissingStore(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/sessions")
	if m.status != "List failed: session store unavailable" {
		t.Fatalf("status = %q, want missing-store message", m.status)
	}

	_, _ = m.handleCommand("/save draft")
	if m.status != "Saved session draft" {
		t.Fatalf("save status = %q, want no-op save to succeed without store", m.status)
	}
}

func TestPromptLabelFollowsTheme(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg}
	if got := m.promptLabel(); got != "rose→ " {
		t.Fatalf("promptLabel() = %q, want rose prompt", got)
	}

	m.cfg.Theme = "mono"
	if got := m.promptLabel(); got != "mono→ " {
		t.Fatalf("promptLabel() = %q, want mono prompt", got)
	}
}

func TestLoadSessionDoesNotOverwriteModelWithEmptyValue(t *testing.T) {
	cfg := config.Default()
	cfg.Provider = "openai"
	cfg.SetModel("gpt-4.1-mini")
	m := Model{cfg: cfg, styles: theme.New("rose")}
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)

	loaded := history.New("loaded", "openai", "", reasoning.ModeConcise)
	m.applyLoadedSession(loaded)

	if got := m.cfg.Model(); got != "gpt-4.1-mini" {
		t.Fatalf("loaded empty model overwrote config model: %q", got)
	}
	if m.session.Model != "" {
		t.Fatalf("session model = %q, want original loaded empty value preserved", m.session.Model)
	}
}

func TestRetryCommandRemovesPreviousAssistantAndStartsRequest(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, styles: theme.New("rose")}
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "try this")
	m.session.Append("assistant", "old answer")

	_, cmd := m.handleCommand("/retry")

	if cmd == nil || !m.busy {
		t.Fatal("retry did not start a request")
	}
	if len(m.session.Messages) != 1 || m.session.Messages[0].Role != "user" {
		t.Fatalf("messages after retry = %#v, want only latest user prompt", m.session.Messages)
	}
	if m.lastAssistant != "" {
		t.Fatalf("lastAssistant = %q, want cleared", m.lastAssistant)
	}
}

func TestUndoCommandRemovesLatestMessage(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, styles: theme.New("rose")}
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "hello")
	m.session.Append("assistant", "answer")

	_, _ = m.handleCommand("/undo")

	if len(m.session.Messages) != 1 || m.session.Messages[0].Role != "user" {
		t.Fatalf("messages after undo = %#v, want only user message", m.session.Messages)
	}
}

func TestExportCommandWritesMarkdown(t *testing.T) {
	cfg := config.Default()
	m := Model{cfg: cfg, styles: theme.New("rose")}
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "hello")
	target := filepath.Join(t.TempDir(), "chat")

	_, _ = m.handleCommand("/export " + target)

	data, err := os.ReadFile(target + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## You") || !strings.Contains(string(data), "hello") {
		t.Fatalf("export content = %q, want transcript markdown", data)
	}
}

func TestDoctorCommandShowsProviderState(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/doctor")

	if !strings.Contains(m.notice, "### Doctor") || !strings.Contains(m.notice, "Provider") {
		t.Fatalf("doctor notice = %q, want provider report", m.notice)
	}
}

func textInputForTest(value string) textinput.Model {
	input := textinput.New()
	input.SetVirtualCursor(false)
	input.Focus()
	input.SetValue(value)
	input.CursorEnd()
	return input
}

func modelListServer(t *testing.T, ids []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected model-list path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{\"data\":[")
		for i, id := range ids {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, "{\"id\":%q}", id)
		}
		fmt.Fprint(w, "]}")
	}))
	t.Cleanup(server.Close)
	return server
}

func backgroundValue(value **string) string {
	if value == nil || *value == nil {
		return "<nil>"
	}
	return **value
}

func hasSuggestion(items []suggestion, value string) bool {
	for _, item := range items {
		if item.Value == value || item.Label == value {
			return true
		}
	}
	return false
}
