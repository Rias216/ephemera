package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

func TestCompatibleAPIKeyPrefersNamedProviderEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "openrouter-secret")
	t.Setenv("EPHEMERA_API_KEY", "generic-secret")

	got := compatibleAPIKey("openrouter", "")
	if got != "openrouter-secret" {
		t.Fatalf("compatibleAPIKey() = %q, want named provider key", got)
	}
}

func TestCompatibleAPIKeyPrefersExplicitValue(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "openrouter-secret")
	t.Setenv("EPHEMERA_API_KEY", "generic-secret")

	got := compatibleAPIKey("openrouter", "runtime-secret")
	if got != "runtime-secret" {
		t.Fatalf("compatibleAPIKey() = %q, want explicit runtime key", got)
	}
}

func TestListCodexModelsReadsVisibleCachedModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	data := `{"models":[
		{"slug":"gpt-5.4-mini","visibility":"list","supported_in_api":true},
		{"slug":"codex-auto-review","visibility":"hide","supported_in_api":true},
		{"slug":"gpt-5.5","visibility":"list","supported_in_api":true},
		{"slug":"not-api","visibility":"list","supported_in_api":false}
	]}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	models, err := ListCodexModels()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "gpt-5.5,gpt-5.4-mini" {
		t.Fatalf("codex models = %#v", models)
	}
}

func TestLoadCodexAccessTokenUsesLocalAuthFile(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("EPHEMERA_CODEX_AUTH_FILE", authFile)
	if err := os.WriteFile(authFile, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"test-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	token, err := loadCodexAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestCodexCLIPathFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	config := "model = \"gpt-5.5\"\nCODEX_CLI_PATH = 'C:\\\\Codex\\\\codex.exe'\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := codexCLIPathFromConfig(); got != "C:\\\\Codex\\\\codex.exe" {
		t.Fatalf("codex CLI path = %q", got)
	}
}

func TestListModelsAcceptsOpenAIDataEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.Provider = "compatible"
	cfg.CompatibleName = "test-provider"
	cfg.CompatibleURL = server.URL

	models, err := ListModels(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "model-a,model-b" {
		t.Fatalf("models = %#v, want sorted IDs", models)
	}
}

func TestListModelsAcceptsCompatibleModelsEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"named-model"},{"model":"model-field"}]}`))
	}))
	t.Cleanup(server.Close)

	models, err := listOpenAICompatibleModels(context.Background(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "model-field,named-model" {
		t.Fatalf("models = %#v", models)
	}
}

func TestListModelsAcceptsStringCatalogEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":["model-b","model-a"]}`))
	}))
	t.Cleanup(server.Close)

	models, err := listOpenAICompatibleModels(context.Background(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "model-a,model-b" {
		t.Fatalf("models = %#v", models)
	}
}

func TestListModelsRejectsEmptyCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)

	_, err := listOpenAICompatibleModels(context.Background(), server.URL, "")
	if err == nil || !strings.Contains(err.Error(), "no model IDs") {
		t.Fatalf("error = %v, want empty-catalog error", err)
	}
}

func TestListModelsIncludesProviderErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid API key"}}`))
	}))
	t.Cleanup(server.Close)

	_, err := listOpenAICompatibleModels(context.Background(), server.URL, "bad-key")
	if err == nil || !strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("error = %v, want provider detail", err)
	}
}
