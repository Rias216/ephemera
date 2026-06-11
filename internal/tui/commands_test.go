package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

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
	want := string(styles.Panel)
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
	m.viewport.Width = 60
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

func TestGlowColorChangesWithFrame(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	first := m.glowColor(0)
	m.frame = 1

	if got := m.glowColor(0); got == first {
		t.Fatalf("glowColor did not shift with frame: %q", got)
	}
}

func TestRenderLogoGlowPreservesBrandText(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}

	got := m.renderLogoGlow()

	if !strings.Contains(got, "EPHEMERA") {
		t.Fatalf("renderLogoGlow() = %q, want brand text", got)
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
