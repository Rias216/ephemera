package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const maxGitHubResponseBytes = 2 << 20

var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (r Registry) githubIssue(ctx context.Context, call Call) Result {
	action := strings.ToLower(strings.TrimSpace(argString(call, "action")))
	repository, err := githubRepository(argString(call, "repository"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	base := "/repos/" + repository + "/issues"
	var method, endpoint string
	var payload map[string]any
	mutation := false

	switch action {
	case "get":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		method, endpoint = http.MethodGet, base+"/"+strconv.Itoa(number)
	case "list":
		method, endpoint = http.MethodGet, base+githubListQuery(call)
	case "create":
		title := strings.TrimSpace(argString(call, "title"))
		if title == "" {
			return fail(call.Name, "github_issue create requires title")
		}
		method, endpoint, mutation = http.MethodPost, base, true
		payload = map[string]any{"title": title}
		optionalString(payload, "body", argString(call, "body"))
		if labels := splitCSV(argString(call, "labels")); len(labels) > 0 {
			payload["labels"] = labels
		}
	case "update":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		method, endpoint, mutation = http.MethodPatch, base+"/"+strconv.Itoa(number), true
		payload = map[string]any{}
		optionalString(payload, "title", argString(call, "title"))
		optionalString(payload, "body", argString(call, "body"))
		if state := strings.ToLower(strings.TrimSpace(argString(call, "state"))); state != "" {
			if state != "open" && state != "closed" {
				return fail(call.Name, "state must be open or closed")
			}
			payload["state"] = state
		}
		if labels := splitCSV(argString(call, "labels")); len(labels) > 0 {
			payload["labels"] = labels
		}
		if len(payload) == 0 {
			return fail(call.Name, "github_issue update requires title, body, state, or labels")
		}
	case "comment":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		body := strings.TrimSpace(argString(call, "body"))
		if body == "" {
			return fail(call.Name, "github_issue comment requires body")
		}
		method, endpoint, mutation = http.MethodPost, base+"/"+strconv.Itoa(number)+"/comments", true
		payload = map[string]any{"body": body}
	default:
		return fail(call.Name, "action must be get, list, create, update, or comment")
	}
	return r.githubRequest(ctx, call.Name, action, repository, method, endpoint, payload, mutation)
}

func (r Registry) githubPullRequest(ctx context.Context, call Call) Result {
	action := strings.ToLower(strings.TrimSpace(argString(call, "action")))
	repository, err := githubRepository(argString(call, "repository"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	base := "/repos/" + repository + "/pulls"
	var method, endpoint string
	var payload map[string]any
	mutation := false

	switch action {
	case "get":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		method, endpoint = http.MethodGet, base+"/"+strconv.Itoa(number)
	case "list":
		method, endpoint = http.MethodGet, base+githubListQuery(call)
	case "create":
		title := strings.TrimSpace(argString(call, "title"))
		head := strings.TrimSpace(argString(call, "head"))
		baseBranch := strings.TrimSpace(argString(call, "base"))
		if title == "" || head == "" || baseBranch == "" {
			return fail(call.Name, "github_pr create requires title, head, and base")
		}
		method, endpoint, mutation = http.MethodPost, base, true
		payload = map[string]any{"title": title, "head": head, "base": baseBranch}
		optionalString(payload, "body", argString(call, "body"))
	case "update":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		method, endpoint, mutation = http.MethodPatch, base+"/"+strconv.Itoa(number), true
		payload = map[string]any{}
		optionalString(payload, "title", argString(call, "title"))
		optionalString(payload, "body", argString(call, "body"))
		optionalString(payload, "base", argString(call, "base"))
		if state := strings.ToLower(strings.TrimSpace(argString(call, "state"))); state != "" {
			if state != "open" && state != "closed" {
				return fail(call.Name, "state must be open or closed")
			}
			payload["state"] = state
		}
		if len(payload) == 0 {
			return fail(call.Name, "github_pr update requires title, body, state, or base")
		}
	case "review":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		event := strings.ToUpper(strings.TrimSpace(argString(call, "event")))
		if event != "APPROVE" && event != "REQUEST_CHANGES" && event != "COMMENT" {
			return fail(call.Name, "event must be APPROVE, REQUEST_CHANGES, or COMMENT")
		}
		method, endpoint, mutation = http.MethodPost, base+"/"+strconv.Itoa(number)+"/reviews", true
		payload = map[string]any{"event": event}
		optionalString(payload, "body", argString(call, "body"))
	case "comment":
		number, err := positiveGitHubNumber(call, "number")
		if err != nil {
			return fail(call.Name, err.Error())
		}
		body := strings.TrimSpace(argString(call, "body"))
		if body == "" {
			return fail(call.Name, "github_pr comment requires body")
		}
		method, endpoint, mutation = http.MethodPost, "/repos/"+repository+"/issues/"+strconv.Itoa(number)+"/comments", true
		payload = map[string]any{"body": body}
	default:
		return fail(call.Name, "action must be get, list, create, update, review, or comment")
	}
	return r.githubRequest(ctx, call.Name, action, repository, method, endpoint, payload, mutation)
}

func (r Registry) githubRequest(ctx context.Context, tool, action, repository, method, endpoint string, payload map[string]any, mutation bool) Result {
	if mutation && strings.TrimSpace(r.GitHubToken) == "" {
		return fail(tool, "GitHub mutation requires GITHUB_TOKEN")
	}
	base := strings.TrimRight(strings.TrimSpace(r.GitHubAPIURL), "/")
	if base == "" {
		base = "https://api.github.com"
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fail(tool, err.Error())
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+endpoint, body)
	if err != nil {
		return fail(tool, err.Error())
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Ephemera/1.0 (+local coding agent)")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(r.GitHubToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := r.WebClient
	if client == nil {
		client = newSafeWebClient(r.CommandTimeout)
	}
	response, err := client.Do(req)
	if err != nil {
		return fail(tool, err.Error())
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubResponseBytes+1))
	if err != nil {
		return fail(tool, err.Error())
	}
	truncated := len(data) > maxGitHubResponseBytes
	if truncated {
		data = data[:maxGitHubResponseBytes]
	}
	output := strings.TrimSpace(string(data))
	var decoded any
	if json.Unmarshal(data, &decoded) == nil {
		if pretty, marshalErr := json.MarshalIndent(decoded, "", "  "); marshalErr == nil {
			output = string(pretty)
		}
	}
	if output == "" {
		output = response.Status
	}
	result := ok(tool, output)
	result.Metadata = map[string]any{
		"repository": repository,
		"action":     action,
		"status":     response.StatusCode,
		"mutation":   mutation,
		"truncated":  truncated,
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.OK = false
		result.Error = fmt.Sprintf("GitHub API HTTP %d", response.StatusCode)
	}
	return result
}

func githubRepository(value string) (string, error) {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if !githubRepositoryPattern.MatchString(value) || strings.Contains(value, "..") {
		return "", fmt.Errorf("repository must be owner/name")
	}
	parts := strings.SplitN(value, "/", 2)
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

func positiveGitHubNumber(call Call, name string) (int, error) {
	value := argIntDefault(call, name, 0)
	if value < 1 {
		return 0, fmt.Errorf("%s requires a positive %s", call.Name, name)
	}
	return value, nil
}

func githubListQuery(call Call) string {
	values := url.Values{}
	state := strings.ToLower(strings.TrimSpace(argString(call, "state")))
	if state == "open" || state == "closed" || state == "all" {
		values.Set("state", state)
	}
	maxItems := argIntDefault(call, "max", 30)
	if maxItems < 1 {
		maxItems = 1
	}
	if maxItems > 100 {
		maxItems = 100
	}
	values.Set("per_page", strconv.Itoa(maxItems))
	return "?" + values.Encode()
}

func optionalString(payload map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		payload[key] = value
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func validateGitHubPreview(call Call) error {
	if _, err := githubRepository(argString(call, "repository")); err != nil {
		return err
	}
	action := strings.ToLower(strings.TrimSpace(argString(call, "action")))
	requireNumber := func() error {
		_, err := positiveGitHubNumber(call, "number")
		return err
	}
	switch call.Name {
	case "github_issue":
		switch action {
		case "get":
			return requireNumber()
		case "list":
			return nil
		case "create":
			if strings.TrimSpace(argString(call, "title")) == "" {
				return fmt.Errorf("github_issue create requires title")
			}
			return nil
		case "update":
			if err := requireNumber(); err != nil {
				return err
			}
			if strings.TrimSpace(argString(call, "title")) == "" && strings.TrimSpace(argString(call, "body")) == "" && strings.TrimSpace(argString(call, "state")) == "" && len(splitCSV(argString(call, "labels"))) == 0 {
				return fmt.Errorf("github_issue update requires title, body, state, or labels")
			}
			return nil
		case "comment":
			if err := requireNumber(); err != nil {
				return err
			}
			if strings.TrimSpace(argString(call, "body")) == "" {
				return fmt.Errorf("github_issue comment requires body")
			}
			return nil
		default:
			return fmt.Errorf("action must be get, list, create, update, or comment")
		}
	case "github_pr":
		switch action {
		case "get":
			return requireNumber()
		case "list":
			return nil
		case "create":
			if strings.TrimSpace(argString(call, "title")) == "" || strings.TrimSpace(argString(call, "head")) == "" || strings.TrimSpace(argString(call, "base")) == "" {
				return fmt.Errorf("github_pr create requires title, head, and base")
			}
			return nil
		case "update":
			if err := requireNumber(); err != nil {
				return err
			}
			if strings.TrimSpace(argString(call, "title")) == "" && strings.TrimSpace(argString(call, "body")) == "" && strings.TrimSpace(argString(call, "state")) == "" && strings.TrimSpace(argString(call, "base")) == "" {
				return fmt.Errorf("github_pr update requires title, body, state, or base")
			}
			return nil
		case "review":
			if err := requireNumber(); err != nil {
				return err
			}
			event := strings.ToUpper(strings.TrimSpace(argString(call, "event")))
			if event != "APPROVE" && event != "REQUEST_CHANGES" && event != "COMMENT" {
				return fmt.Errorf("event must be APPROVE, REQUEST_CHANGES, or COMMENT")
			}
			return nil
		case "comment":
			if err := requireNumber(); err != nil {
				return err
			}
			if strings.TrimSpace(argString(call, "body")) == "" {
				return fmt.Errorf("github_pr comment requires body")
			}
			return nil
		default:
			return fmt.Errorf("action must be get, list, create, update, review, or comment")
		}
	default:
		return fmt.Errorf("unknown GitHub tool %q", call.Name)
	}
}
