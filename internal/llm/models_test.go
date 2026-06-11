package llm

import "testing"

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
