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
	"unicode/utf8"

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
	m.cfg.RememberConnection(config.SavedConnection{
		Provider: "compatible",
		Name:     "test-provider",
		BaseURL:  server.URL,
		Model:    "provider-live-a",
	}, "")

	got := m.commandSuggestions("/model ")
	if !hasSuggestion(got, "provider-live-a") || !hasSuggestion(got, "provider-live-h") {
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

func TestModelSuggestionsExcludeUnavailableSavedSelection(t *testing.T) {
	server := modelListServer(t, []string{"live-model"})
	m := Model{cfg: config.Default()}
	m.cfg.RememberConnection(config.SavedConnection{
		Provider: "compatible",
		Name:     "test-provider",
		BaseURL:  server.URL,
		Model:    "stale-model",
	}, "")

	got := m.commandSuggestions("/model ")
	if hasSuggestion(got, "stale-model") {
		t.Fatalf("model suggestions included unavailable saved model: %#v", got)
	}
	if !hasSuggestion(got, "live-model") {
		t.Fatalf("model suggestions missing live provider model: %#v", got)
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

func TestConnectCodexSkipsCredentialStep(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	m.startConnect("chatgpt")

	if m.connect == nil {
		t.Fatal("connect flow unexpectedly finished")
	}
	if m.connect.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", m.connect.Provider)
	}
	if m.connect.Step != connectModel {
		t.Fatalf("step = %q, want model", m.connect.Step)
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

func TestConnectRequiredCredentialDoesNotAdvance(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	m := Model{
		cfg:     config.Default(),
		styles:  theme.New("rose"),
		connect: &connectFlow{Provider: "openai", Step: connectAPIKey},
	}
	m.input = textInputForTest("")

	m.submitConnectStep()

	if m.connect == nil || m.connect.Step != connectAPIKey {
		t.Fatalf("connect advanced without required credential: %#v", m.connect)
	}
	if !strings.Contains(m.status, "required") {
		t.Fatalf("status = %q, want required-key guidance", m.status)
	}
}

func TestTranscriptRowsUseThemeBackgroundInsteadOfBlack(t *testing.T) {
	cfg := config.Default()
	styles := theme.New("rose")
	m := Model{cfg: cfg, styles: styles}
	m.viewport.SetWidth(60)
	m.session = history.New("current", cfg.Provider, cfg.Model(), reasoning.ModeNormal)
	m.session.Append("user", "Hello")

	got := m.renderTranscript()
	if strings.Contains(got, "\x1b[40m") || strings.Contains(got, "48;2;0;0;0") {
		t.Fatalf("transcript contains black background escape: %q", got)
	}
	panel := "48;2;20;10;18"
	if !strings.Contains(got, panel) {
		t.Fatalf("transcript did not use panel background %q: %q", panel, got)
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

func TestKnifeFadeGlimmerDoesNotUseBrightestPaletteStop(t *testing.T) {
	peak := knifeFadeColor(roseGlow, 40, 40, 82, 0.2, 100, 0)
	if theme.Hex(peak) == theme.Hex(roseGlow[len(roseGlow)-1]) {
		t.Fatalf("glimmer peak is still the brightest palette stop: %s", theme.Hex(peak))
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

func TestAgentCommandTogglesAgentMode(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/agent on")

	if !m.cfg.AgentEnabled {
		t.Fatal("/agent on did not enable agent mode")
	}
	if !strings.Contains(m.notice, "Approval policy") {
		t.Fatalf("agent notice = %q, want approval details", m.notice)
	}

	_, _ = m.handleCommand("/agent off")
	if !m.cfg.AgentEnabled {
		t.Fatal("/agent off should leave always-on agent mode enabled")
	}
}

func TestEvalCommandRunsAgentCapabilityEval(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	_, _ = m.handleCommand("/eval")

	if !strings.Contains(m.status, "Agent eval passed") {
		t.Fatalf("status = %q, want passing eval status", m.status)
	}
	if !strings.Contains(m.notice, "Agent capability eval") || !strings.Contains(m.notice, "native-tool-call") {
		t.Fatalf("eval notice = %q, want capability report", m.notice)
	}
}

func TestModelsCommandOpensInteractiveChooser(t *testing.T) {
	server := modelListServer(t, []string{"live-model"})
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "test-provider", BaseURL: server.URL, Model: "live-model"}, "")
	_, _ = m.handleCommand("/models")

	if m.input.Value() != "/model " {
		t.Fatalf("input = %q, want /model chooser", m.input.Value())
	}
	if !hasSuggestion(m.suggestions, "live-model") {
		t.Fatalf("model chooser missing live provider choice: %#v", m.suggestions)
	}
}

func TestModelCommandAcceptsUnavailableTypedID(t *testing.T) {
	server := modelListServer(t, []string{"live-model"})
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "test-provider", BaseURL: server.URL, Model: "live-model"}, "")

	_, _ = m.handleCommand("/model missing-model")

	if got := m.cfg.Model(); got != "missing-model" {
		t.Fatalf("model did not change to typed ID: %q", got)
	}
	if !strings.Contains(m.status, "not advertised") {
		t.Fatalf("status = %q, want uncataloged-model warning", m.status)
	}
}

func TestModelCommandAcceptsTypedIDWhenCatalogFails(t *testing.T) {
	server := failingModelListServer(t, http.StatusBadGateway, `{"error":{"message":"catalog down"}}`)
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "test-provider", BaseURL: server.URL, Model: "old-model"}, "")

	_, _ = m.handleCommand("/model typed-model")

	if got := m.cfg.Model(); got != "typed-model" {
		t.Fatalf("model did not change to typed ID: %q", got)
	}
	if !strings.Contains(m.status, "unverified") {
		t.Fatalf("status = %q, want unverified-model warning", m.status)
	}
}

func TestCodexModelCommandRejectsUnavailableTypedID(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.RememberConnection(config.SavedConnection{Provider: "codex", Model: "gpt-5.5"}, "")

	_, _ = m.handleCommand("/model random-api-model")

	if got := m.cfg.Model(); got != "gpt-5.5" {
		t.Fatalf("codex model changed to unavailable ID: %q", got)
	}
	if !strings.Contains(m.status, "not available from Codex") {
		t.Fatalf("status = %q, want Codex availability error", m.status)
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

func failingModelListServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected model-list path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func hasSuggestion(items []suggestion, value string) bool {
	for _, item := range items {
		if item.Value == value || item.Label == value {
			return true
		}
	}
	return false
}

func TestConnectProgressTabsSpanFullRail(t *testing.T) {
	m := Model{
		cfg:     config.Default(),
		styles:  theme.New("rose"),
		connect: &connectFlow{Step: connectProvider},
	}

	const width = 112
	got := m.connectProgressRail(width)
	if renderedWidth := lipgloss.Width(got); renderedWidth != width {
		t.Fatalf("progress rail width = %d, want %d", renderedWidth, width)
	}

	plain := stripAllANSIEscapes(got)
	if strings.Count(plain, "│") != 4 {
		t.Fatalf("progress rail separators = %d, want 4: %q", strings.Count(plain, "│"), plain)
	}
	for _, label := range []string{"01 PROVIDER", "02 DETAILS", "03 AUTH", "04 MODEL", "05 REVIEW"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("progress rail missing %q: %q", label, plain)
		}
	}

	reviewByte := strings.Index(plain, "05 REVIEW")
	if reviewByte < 0 {
		t.Fatal("review tab not found")
	}
	reviewCell := utf8.RuneCountInString(plain[:reviewByte])
	if reviewCell < width*4/5 {
		t.Fatalf("review tab starts at cell %d, want it in final fifth of %d cells: %q", reviewCell, width, plain)
	}
}

func stripAllANSIEscapes(value string) string {
	var plain strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			i = ansiEscapeEnd(value, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		plain.WriteRune(r)
		i += size
	}
	return plain.String()
}

func TestCommandPaletteUsesRemainingSpaceForInspector(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose"), height: 34, width: 120, focused: true}
	m.input = textInputForTest("/help")
	m.suggestions = m.commandSuggestions(m.input.Value())
	m.resize()

	got := m.renderSuggestions()
	for _, want := range []string{"USAGE", "EXAMPLES", "Category", "/help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("command inspector missing %q: %q", want, got)
		}
	}
}

func TestViewUsesFullTerminalHeightWithAndWithoutPalette(t *testing.T) {
	m := New(config.Default(), nil, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m = updated.(Model)
	if got := lipgloss.Height(m.View().Content); got != 36 {
		t.Fatalf("plain view height = %d, want 36", got)
	}

	m.input.SetValue("/help")
	m.rebuildSuggestions()
	m.resize()
	m.refreshViewport(true)
	if got := lipgloss.Height(m.View().Content); got != 36 {
		t.Fatalf("palette view height = %d, want 36", got)
	}
}

func TestBackgroundFillIsStaticAndClean(t *testing.T) {
	m := Model{styles: theme.New("rose")}
	first := m.textureLine(100, 4, 17, m.styles.Panel)
	second := m.textureLine(100, 4, 17, m.styles.Panel)
	if first != second {
		t.Fatal("background fill changed between identical renders")
	}
	if strings.ContainsAny(first, "·∙╱╲/\\") {
		t.Fatalf("background fill contains decorative artifacts: %q", first)
	}
	if lipgloss.Width(first) != 100 {
		t.Fatalf("background fill width = %d, want 100", lipgloss.Width(first))
	}
}

func TestConnectModelAdvancesToReviewBeforeActivating(t *testing.T) {
	server := modelListServer(t, []string{"live-model"})
	cfg := config.Default()
	originalProvider := cfg.Provider
	m := Model{
		cfg:    cfg,
		styles: theme.New("rose"),
		connect: &connectFlow{
			Provider: "compatible",
			Name:     "test-provider",
			BaseURL:  server.URL,
			Step:     connectModel,
			History:  []connectStep{connectProvider, connectAPIKey},
		},
	}
	m.input = textInputForTest("live-model")

	m.submitConnectStep()

	if m.connect == nil || m.connect.Step != connectReview {
		t.Fatalf("connect step = %#v, want review", m.connect)
	}
	if m.cfg.Provider != originalProvider {
		t.Fatalf("provider changed before review: %q -> %q", originalProvider, m.cfg.Provider)
	}
	if m.connect.Model != "live-model" {
		t.Fatalf("review model = %q, want live-model", m.connect.Model)
	}
}

func TestConnectModelAcceptsUnavailableTypedID(t *testing.T) {
	server := modelListServer(t, []string{"live-model"})
	m := Model{
		cfg:    config.Default(),
		styles: theme.New("rose"),
		connect: &connectFlow{
			Provider: "compatible",
			Name:     "test-provider",
			BaseURL:  server.URL,
			Step:     connectModel,
		},
	}
	m.input = textInputForTest("missing-model")

	m.submitConnectStep()

	if m.connect == nil || m.connect.Step != connectReview {
		t.Fatalf("connect did not advance to review with typed model: %#v", m.connect)
	}
	if m.connect.Model != "missing-model" {
		t.Fatalf("connect model = %q, want typed model", m.connect.Model)
	}
	if !strings.Contains(m.status, "not advertised") {
		t.Fatalf("status = %q, want uncataloged-model warning", m.status)
	}
}

func TestConnectCodexModelRejectsUnavailableTypedID(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	m := Model{
		cfg:    config.Default(),
		styles: theme.New("rose"),
		connect: &connectFlow{
			Provider: "codex",
			Step:     connectModel,
		},
	}
	m.input = textInputForTest("random-api-model")

	m.submitConnectStep()

	if m.connect == nil || m.connect.Step != connectModel {
		t.Fatalf("connect advanced with unavailable Codex model: %#v", m.connect)
	}
	if !strings.Contains(m.status, "not available from Codex") {
		t.Fatalf("status = %q, want Codex availability error", m.status)
	}
}

func TestConnectReviewActivatesRoute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	server := modelListServer(t, []string{"live-model"})
	m := Model{
		cfg:    config.Default(),
		styles: theme.New("rose"),
		connect: &connectFlow{
			Provider: "compatible",
			Name:     "test-provider",
			BaseURL:  server.URL,
			APIKey:   "runtime-secret",
			Model:    "live-model",
			Step:     connectReview,
		},
	}
	m.input = textInputForTest("")

	m.submitConnectStep()

	if m.connect != nil {
		t.Fatalf("connect flow still active after review: %#v", m.connect)
	}
	if m.cfg.Provider != "compatible" || m.cfg.Model() != "live-model" {
		t.Fatalf("activated route = %s/%s", m.cfg.Provider, m.cfg.Model())
	}
	if m.cfg.CompatibleKey != "runtime-secret" {
		t.Fatal("runtime API key was not applied")
	}
}

func TestConnectBackPreservesPreviouslyEnteredValues(t *testing.T) {
	m := Model{
		cfg:    config.Default(),
		styles: theme.New("rose"),
		connect: &connectFlow{
			Provider: "compatible",
			Name:     "custom-route",
			BaseURL:  "https://example.test/v1",
			Step:     connectAPIKey,
			History:  []connectStep{connectProvider, connectName, connectBaseURL},
		},
	}
	m.input = textInputForTest("")

	if !m.retreatConnect() {
		t.Fatal("retreatConnect() returned false")
	}
	if m.connect.Step != connectBaseURL {
		t.Fatalf("step = %q, want base URL", m.connect.Step)
	}
	if got := m.input.Value(); got != "https://example.test/v1" {
		t.Fatalf("restored input = %q", got)
	}
}

func TestSuggestionWindowKeepsSelectionNearCenter(t *testing.T) {
	m := Model{height: 40, width: 120, paletteHeight: 14}
	for i := 0; i < 12; i++ {
		m.suggestions = append(m.suggestions, suggestion{Value: fmt.Sprintf("item-%02d", i)})
	}
	m.completionIndex = 6

	items, start := m.suggestionWindow()
	if len(items) < 3 {
		t.Fatalf("window too small: %d", len(items))
	}
	position := m.completionIndex - start
	if position <= 0 || position >= len(items)-1 {
		t.Fatalf("selection position = %d in %d rows; want contextual rows above and below", position, len(items))
	}
}

func TestBackgroundTextureAvoidsSlashArtifacts(t *testing.T) {
	m := Model{styles: theme.New("rose")}
	for row := 0; row < 30; row++ {
		line := m.textureLine(180, row, 41, m.styles.Background)
		if strings.ContainsAny(line, "╱╲/\\") {
			t.Fatalf("texture row contains slash-like artifact: %q", line)
		}
	}
}

func TestModelCacheKeyDoesNotRetainCredentialValue(t *testing.T) {
	first := config.Default()
	first.Provider = "openai"
	first.OpenAIKey = "secret-one"
	second := first
	second.OpenAIKey = "secret-two"

	if modelCacheKey(first) == modelCacheKey(second) {
		t.Fatal("model cache key should change when account credentials change")
	}
	if strings.Contains(modelCacheKey(first), "secret-one") || strings.Contains(modelCacheKey(second), "secret-two") {
		t.Fatal("model cache key retained raw credential material")
	}
}

func TestUnifiedModelSelectionSwitchesRememberedRouteAutomatically(t *testing.T) {
	openRouter := modelListServer(t, []string{"router-model"})
	groq := modelListServer(t, []string{"groq-model"})

	cfg := config.Default()
	cfg.Connections = map[string]config.SavedConnection{}
	cfg.ActiveConnection = ""
	cfg.Credentials = map[string]string{}
	cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "openrouter", BaseURL: openRouter.URL, Model: "router-model"}, "")
	cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "groq", BaseURL: groq.URL, Model: "groq-model"}, "")
	m := Model{cfg: cfg, styles: theme.New("rose")}

	_, _ = m.handleCommand("/model compatible:openrouter::router-model")

	if m.cfg.ActiveConnection != "compatible:openrouter" || m.cfg.CompatibleName != "openrouter" {
		t.Fatalf("route not switched automatically: active=%q name=%q", m.cfg.ActiveConnection, m.cfg.CompatibleName)
	}
	if got := m.cfg.Model(); got != "router-model" {
		t.Fatalf("model = %q, want router-model", got)
	}
}

func TestUnifiedModelSuggestionsIncludeAllRememberedProviders(t *testing.T) {
	first := modelListServer(t, []string{"first-model"})
	second := modelListServer(t, []string{"second-model"})

	cfg := config.Default()
	cfg.Connections = map[string]config.SavedConnection{}
	cfg.ActiveConnection = ""
	cfg.Credentials = map[string]string{}
	cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "first", BaseURL: first.URL, Model: "first-model"}, "")
	cfg.RememberConnection(config.SavedConnection{Provider: "compatible", Name: "second", BaseURL: second.URL, Model: "second-model"}, "")
	m := Model{cfg: cfg}

	got := m.commandSuggestions("/model ")
	if !hasSuggestion(got, "first-model") || !hasSuggestion(got, "second-model") {
		t.Fatalf("unified model suggestions missing remembered routes: %#v", got)
	}
	if !hasSuggestion(got, "/model compatible:first::first-model") || !hasSuggestion(got, "/model compatible:second::second-model") {
		t.Fatalf("suggestions do not retain route identity: %#v", got)
	}
}

func TestReconnectUsesRememberedCredentialWithoutReentry(t *testing.T) {
	cfg := config.Default()
	cfg.RememberConnection(config.SavedConnection{Provider: "openai", Model: "gpt-test"}, "remembered-secret")
	m := Model{
		cfg:     cfg,
		styles:  theme.New("rose"),
		connect: &connectFlow{Provider: "openai", Step: connectAPIKey},
	}
	m.input = textInputForTest("")

	m.submitConnectStep()

	if m.connect == nil || m.connect.Step != connectModel {
		t.Fatalf("remembered credential did not advance to model selection: %#v", m.connect)
	}
	if got := m.connectModelListConfig().OpenAIKey; got != "remembered-secret" {
		t.Fatalf("model catalog config key = %q, want remembered credential", got)
	}
}

func TestCodexCommandUpdatesBridgeBudgetAndExplainsWorkspaceAuthority(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	_, _ = m.handleCommand("/codex budget 1024")

	if m.cfg.CodexBridgeMaxTokens != 1024 {
		t.Fatalf("Codex bridge budget = %d, want 1024", m.cfg.CodexBridgeMaxTokens)
	}
	for _, want := range []string{"isolated model-only bridge", "Codex-native shell", "Ephemera then reads, writes", "1.0k tokens"} {
		if !strings.Contains(m.notice, want) {
			t.Fatalf("Codex notice missing %q: %s", want, m.notice)
		}
	}
}

func TestCodexNoticeGuidesReadOnlyPolicyRecovery(t *testing.T) {
	cfg := config.Default()
	cfg.ApprovalPolicy = config.ApprovalReadOnly
	m := Model{cfg: cfg}

	notice := m.codexNotice()
	if !strings.Contains(notice, "/approval safe") || !strings.Contains(notice, "/approval workspace-write") {
		t.Fatalf("read-only Codex notice lacks recovery commands: %s", notice)
	}
}

func TestCommandSpecsExposeCodexAndDebugLogControls(t *testing.T) {
	for _, name := range []string{"/codex", "/debuglog"} {
		if _, ok := findCommandSpec(name); !ok {
			t.Fatalf("missing command spec %s", name)
		}
	}
}
