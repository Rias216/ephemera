package llm

import (
	"errors"
	"testing"
	"time"
)

func TestCommonErrorTaxonomy(t *testing.T) {
	cases := []struct {
		message   string
		code      string
		retryable bool
	}{
		{"HTTP 429 rate limit exceeded", "rate_limit", true},
		{"maximum context length exceeded", "context_length", false},
		{"connection reset by peer", "transient", true},
		{"401 unauthorized API key", "auth", false},
	}
	for _, tc := range cases {
		got := ClassifyError(noClassifierProvider("test"), errors.New(tc.message))
		if got.Code != tc.code || got.Retryable != tc.retryable || got.Provider != "test" {
			t.Fatalf("%q => %#v", tc.message, got)
		}
	}
}

func TestProviderSpecificTaxonomy(t *testing.T) {
	anthropic := providerSpecificTaxonomy("anthropic", errors.New("529 overloaded_error"))
	if anthropic.Code != "overloaded" || !anthropic.Retryable || anthropic.Backoff != 3*time.Second {
		t.Fatalf("anthropic = %#v", anthropic)
	}
	ollama := providerSpecificTaxonomy("ollama", errors.New("connection refused"))
	if ollama.Code != "daemon_unavailable" || !ollama.Retryable {
		t.Fatalf("ollama = %#v", ollama)
	}
	codex := providerSpecificTaxonomy("codex", errors.New("workspace is read-only"))
	if codex.Code != "bridge_read_only" || codex.Retryable {
		t.Fatalf("codex = %#v", codex)
	}
}
