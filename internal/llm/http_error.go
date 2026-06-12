package llm

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPStatusError preserves status and retry metadata from provider HTTP
// responses so the agent can respect Retry-After instead of retrying blindly.
type HTTPStatusError struct {
	Provider   string
	Operation  string
	StatusCode int
	Status     string
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "provider"
	}
	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "request"
	}
	status := strings.TrimSpace(e.Status)
	if status == "" && e.StatusCode > 0 {
		status = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("%s %s failed: %s", provider, operation, status)
	}
	return fmt.Sprintf("%s %s failed: %s: %s", provider, operation, status, body)
}

func newHTTPStatusError(provider, operation string, response *http.Response, body []byte) error {
	statusCode := 0
	status := ""
	retryAfter := time.Duration(0)
	if response != nil {
		statusCode = response.StatusCode
		status = response.Status
		retryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(body), "�"))
	runes := []rune(text)
	if len(runes) > 32<<10 {
		text = string(runes[:32<<10]) + "…[TRUNCATED]"
	}
	return &HTTPStatusError{
		Provider: provider, Operation: operation, StatusCode: statusCode,
		Status: status, Body: text, RetryAfter: retryAfter,
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := when.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}
