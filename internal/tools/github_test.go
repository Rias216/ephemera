package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ephemera-ai/ephemera/internal/config"
)

type recordingHTTPDoer struct {
	request *http.Request
	body    string
	calls   int
	status  int
	output  string
}

func (d *recordingHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	d.request = request
	if request.Body != nil {
		data, _ := io.ReadAll(request.Body)
		d.body = string(data)
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	output := d.output
	if output == "" {
		output = `{"ok":true}`
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(output)),
	}, nil
}

func TestGitHubIssueReadUsesVersionedAPIRequest(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)
	doer := &recordingHTTPDoer{output: `{"number":42,"title":"Bug"}`}
	registry.WebClient = doer
	registry.GitHubAPIURL = "https://api.github.test"

	result := registry.Execute(WithApproval(context.Background()), Call{Name: "github_issue", Arguments: map[string]any{
		"action": "get", "repository": "owner/repo", "number": 42,
	}})
	if !result.OK || !strings.Contains(result.Output, `"number": 42`) {
		t.Fatalf("result = %#v", result)
	}
	if doer.request.Method != http.MethodGet || doer.request.URL.String() != "https://api.github.test/repos/owner/repo/issues/42" {
		t.Fatalf("request = %s %s", doer.request.Method, doer.request.URL)
	}
	if doer.request.Header.Get("X-GitHub-Api-Version") == "" || doer.request.Header.Get("Authorization") != "" {
		t.Fatalf("headers = %#v", doer.request.Header)
	}
}

func TestGitHubIssueMutationRequiresTokenAndSendsPayload(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)
	doer := &recordingHTTPDoer{status: http.StatusCreated, output: `{"number":7}`}
	registry.WebClient = doer
	registry.GitHubAPIURL = "https://api.github.test"

	missing := registry.Execute(WithApproval(context.Background()), Call{Name: "github_issue", Arguments: map[string]any{
		"action": "create", "repository": "owner/repo", "title": "Fix", "body": "Details",
	}})
	if missing.OK || !strings.Contains(missing.Error, "GITHUB_TOKEN") || doer.calls != 0 {
		t.Fatalf("missing-token result = %#v, calls=%d", missing, doer.calls)
	}

	registry.GitHubToken = "secret"
	created := registry.Execute(WithApproval(context.Background()), Call{Name: "github_issue", Arguments: map[string]any{
		"action": "create", "repository": "owner/repo", "title": "Fix", "body": "Details", "labels": "bug, agent,bug",
	}})
	if !created.OK || doer.request.Method != http.MethodPost {
		t.Fatalf("created = %#v", created)
	}
	if doer.request.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("authorization header = %q", doer.request.Header.Get("Authorization"))
	}
	for _, want := range []string{`"title":"Fix"`, `"body":"Details"`, `"labels":["bug","agent"]`} {
		if !strings.Contains(doer.body, want) {
			t.Fatalf("payload missing %s: %s", want, doer.body)
		}
	}
}

func TestGitHubPullRequestReviewUsesReviewEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	registry := NewRegistry(cfg)
	registry.GitHubToken = "secret"
	doer := &recordingHTTPDoer{status: http.StatusOK, output: `{"state":"APPROVED"}`}
	registry.WebClient = doer
	registry.GitHubAPIURL = "https://api.github.test"

	result := registry.Execute(WithApproval(context.Background()), Call{Name: "github_pr", Arguments: map[string]any{
		"action": "review", "repository": "owner/repo", "number": 9, "event": "approve", "body": "Looks good",
	}})
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	if doer.request.URL.Path != "/repos/owner/repo/pulls/9/reviews" || !strings.Contains(doer.body, `"event":"APPROVE"`) {
		t.Fatalf("request = %s payload=%s", doer.request.URL, doer.body)
	}
}

func TestGitHubToolsValidateRepositoryAndRespectDryRun(t *testing.T) {
	cfg := config.Default()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.AgentDryRun = true
	registry := NewRegistry(cfg)
	doer := &recordingHTTPDoer{}
	registry.WebClient = doer

	invalid := registry.Execute(WithApproval(context.Background()), Call{Name: "github_issue", Arguments: map[string]any{
		"action": "get", "repository": "../bad", "number": 1,
	}})
	if invalid.OK {
		t.Fatalf("invalid repository accepted: %#v", invalid)
	}

	preview := registry.Execute(WithApproval(context.Background()), Call{Name: "github_pr", Arguments: map[string]any{
		"action": "create", "repository": "owner/repo", "title": "Feature", "head": "feature", "base": "main",
	}})
	if !preview.OK || preview.Metadata["dry_run"] != true || doer.calls != 0 {
		t.Fatalf("preview = %#v, calls=%d", preview, doer.calls)
	}
}
