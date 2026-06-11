package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestModelsCommandOpensInteractiveChooser(t *testing.T) {
	m := Model{cfg: config.Default(), styles: theme.New("rose")}
	m.cfg.Provider = "openai"
	m.handleCommand("/models")

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

	m.handleCommand("/sessions")
	if m.status != "List failed: session store unavailable" {
		t.Fatalf("status = %q, want missing-store message", m.status)
	}

	m.handleCommand("/save draft")
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
