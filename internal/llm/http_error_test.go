package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type plainHTTPTestProvider string

func (p plainHTTPTestProvider) Name() string                                      { return string(p) }
func (p plainHTTPTestProvider) Generate(context.Context, Request) (string, error) { return "", nil }

func TestHTTPStatusErrorPreservesRetryAfter(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{"7"}},
	}
	err := newHTTPStatusError("nvidia", "streaming request", response, []byte(`{"status":429}`))
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T", err)
	}
	if statusErr.RetryAfter != 7*time.Second {
		t.Fatalf("retry after = %s", statusErr.RetryAfter)
	}
	classified := ClassifyError(plainHTTPTestProvider("nvidia"), err)
	if classified.Class != "rate_limit" || classified.Backoff != 7*time.Second || !classified.Retryable {
		t.Fatalf("classification = %#v", classified)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, time.June, 13, 10, 0, 0, 0, time.UTC)
	got := parseRetryAfter(now.Add(4*time.Second).Format(http.TimeFormat), now)
	if got != 4*time.Second {
		t.Fatalf("retry after = %s", got)
	}
}
