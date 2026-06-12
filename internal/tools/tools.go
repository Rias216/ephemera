// Package tools contains the local, approval-aware tools used by the agent.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/llm"
)

// Risk describes the permissions needed to execute a tool.
type Risk string

const (
	RiskRead  Risk = "read"
	RiskWrite Risk = "write"
	RiskShell Risk = "shell"
)

// Tool describes a local capability available to the agent.
type Tool struct {
	Name        string
	Description string
	Risk        Risk
	Arguments   []ArgumentSpec
}

// ArgumentSpec describes one JSON argument accepted by a tool.
type ArgumentSpec struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// Call is a structured request to execute a tool.
type Call struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Fingerprint returns a stable, compact identity for an exact tool call.
// Persist only the digest so large patch contents and shell commands are not
// duplicated into every timeline event.
func Fingerprint(call Call) string {
	data, _ := json.Marshal(call)
	sum := sha256.Sum256(data)
	return call.Name + ":" + fmt.Sprintf("%x", sum[:])
}

// Result is the normalized output from a tool.
type Result struct {
	Tool     string
	OK       bool
	Output   string
	Error    string
	Metadata map[string]any
	Duration time.Duration
}

// Registry owns the local tool catalog and execution policy.
type Registry struct {
	WorkspaceRoot   string
	ApprovalPolicy  config.ApprovalPolicy
	MaxOutputTokens int
	AutoTestCommand string
	CommandTimeout  time.Duration
	DryRun          bool
	SandboxMode     config.SandboxMode
	WebClient       HTTPDoer
	GitHubToken     string
	GitHubAPIURL    string
}

// NewRegistry creates a tool registry rooted in cfg.WorkspaceRoot or cwd.
func NewRegistry(cfg config.Config) Registry {
	root := strings.TrimSpace(cfg.WorkspaceRoot)
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	return Registry{
		WorkspaceRoot:   root,
		ApprovalPolicy:  cfg.ApprovalPolicy,
		MaxOutputTokens: cfg.MaxToolOutputTokens,
		AutoTestCommand: cfg.AutoTestCommand,
		CommandTimeout:  time.Duration(cfg.AgentToolTimeoutSec) * time.Second,
		DryRun:          cfg.AgentDryRun,
		SandboxMode:     cfg.SandboxMode,
		GitHubToken:     strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		GitHubAPIURL:    "https://api.github.com",
	}
}

// Builtins returns the V1 local tool catalog.
func Builtins() []Tool {
	return []Tool{
		{Name: "list_files", Description: "List workspace files. Args: optional path, max.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative directory to list.", false),
			arg("max", "integer", "Maximum number of files to return.", false),
		}},
		{Name: "tree", Description: "Show a shallow workspace tree. Args: optional path, depth.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative directory to inspect.", false),
			arg("depth", "integer", "Maximum directory depth.", false),
		}},
		{Name: "read_file", Description: "Read a UTF-8 text file. Args: path, optional start_line/end_line.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file path.", true),
			arg("start_line", "integer", "1-based first line to read.", false),
			arg("end_line", "integer", "1-based final line to read.", false),
		}},
		{Name: "search", Description: "Search text files. Args: query, optional path, max.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("query", "string", "Case-insensitive text query.", true),
			arg("path", "string", "Workspace-relative directory to search.", false),
			arg("max", "integer", "Maximum matches to return.", false),
		}},
		{Name: "grep_regex", Description: "Regex search across text files. Args: pattern, optional path, max, case_sensitive.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("pattern", "string", "Go-compatible regular expression.", true),
			arg("path", "string", "Workspace-relative directory to search.", false),
			arg("max", "integer", "Maximum matches to return.", false),
			arg("case_sensitive", "boolean", "Use case-sensitive matching.", false),
		}},
		{Name: "find_symbol", Description: "Find likely definitions of a function, type, class, or variable. Args: symbol, optional path, max.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("symbol", "string", "Exact symbol name.", true),
			arg("path", "string", "Workspace-relative directory to search.", false),
			arg("max", "integer", "Maximum matches to return.", false),
		}},
		{Name: "find_refs", Description: "Find references to an exact symbol. Args: symbol, optional path, max.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("symbol", "string", "Exact symbol name.", true),
			arg("path", "string", "Workspace-relative directory to search.", false),
			arg("max", "integer", "Maximum matches to return.", false),
		}},
		{Name: "file_summary", Description: "Summarize a source file's package, imports, and top-level definitions. Args: path.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative source file.", true),
		}},
		{Name: "dependency_graph", Description: "Show source import relationships for the workspace or a subdirectory. Args: optional path, max.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative directory.", false),
			arg("max", "integer", "Maximum files to inspect.", false),
		}},
		{Name: "detect_project_type", Description: "Detect languages, frameworks, build systems, and test commands from project markers.", Risk: RiskRead},
		{Name: "list_dependencies", Description: "List declared project dependencies and versions from common manifests.", Risk: RiskRead},
		{Name: "git_status", Description: "Run git status --short.", Risk: RiskRead},
		{Name: "git_diff", Description: "Run git diff. Args: optional path.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Optional workspace-relative path to diff.", false),
		}},
		{Name: "git_log", Description: "View concise git history. Args: optional max, path.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("max", "integer", "Maximum commits.", false),
			arg("path", "string", "Optional workspace-relative path.", false),
		}},
		{Name: "git_blame", Description: "Show line attribution for a file. Args: path, optional start_line/end_line.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file.", true),
			arg("start_line", "integer", "Optional first line.", false),
			arg("end_line", "integer", "Optional final line.", false),
		}},
		{Name: "git_create_branch", Description: "Create and checkout a git branch. Args: name.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("name", "string", "New branch name.", true),
		}},
		{Name: "git_checkout", Description: "Checkout an existing branch. Args: name.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("name", "string", "Branch name.", true),
		}},
		{Name: "git_commit", Description: "Stage selected paths and commit. Args: message, optional paths.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("message", "string", "Commit message.", true),
			arg("paths", "string", "Space-separated workspace-relative paths; defaults to all changes.", false),
		}},
		{Name: "git_stash", Description: "Push, pop, or list stashes. Args: action, optional message.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("action", "string", "push, pop, or list.", true),
			arg("message", "string", "Optional stash message for push.", false),
		}},
		{Name: "git_merge", Description: "Merge a branch with conflict detection. Args: branch.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("branch", "string", "Branch to merge.", true),
		}},
		{Name: "web_fetch", Description: "Fetch a public HTTP(S) URL as bounded readable text. Args: url, optional max_chars.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("url", "string", "Public HTTP or HTTPS URL.", true),
			arg("max_chars", "integer", "Maximum returned characters (1000-200000).", false),
		}},
		{Name: "github_issue", Description: "Read, create, update, or comment on a GitHub issue. Args: action, repository, and action-specific fields.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("action", "string", "get, list, create, update, or comment.", true),
			arg("repository", "string", "GitHub repository as owner/name.", true),
			arg("number", "integer", "Issue number for get, update, or comment.", false),
			arg("title", "string", "Issue title for create or update.", false),
			arg("body", "string", "Issue body or comment text.", false),
			arg("state", "string", "open or closed.", false),
			arg("labels", "string", "Comma-separated label names.", false),
			arg("max", "integer", "Maximum list results, up to 100.", false),
		}},
		{Name: "github_pr", Description: "Read, create, update, review, or comment on a GitHub pull request. Args: action, repository, and action-specific fields.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("action", "string", "get, list, create, update, review, or comment.", true),
			arg("repository", "string", "GitHub repository as owner/name.", true),
			arg("number", "integer", "Pull request number for get, update, review, or comment.", false),
			arg("title", "string", "Pull request title for create or update.", false),
			arg("body", "string", "Pull request body, review body, or comment text.", false),
			arg("state", "string", "open or closed.", false),
			arg("head", "string", "Source branch for create.", false),
			arg("base", "string", "Target branch for create or update.", false),
			arg("event", "string", "APPROVE, REQUEST_CHANGES, or COMMENT for review.", false),
			arg("max", "integer", "Maximum list results, up to 100.", false),
		}},
		{Name: "apply_patch", Description: "Write complete file content. Args: path, content. Prefer replace_in_file for small edits.", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file path to create or replace.", true),
			arg("content", "string", "Complete UTF-8 file content.", true),
		}},
		{Name: "apply_multi_patch", Description: "Atomically write multiple complete files. Args: patches array of {path, content}. If any write fails, every target is rolled back.", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("patches", "array", "Two or more objects with workspace-relative path and complete UTF-8 content fields.", true),
		}},
		{Name: "replace_in_file", Description: "Replace one exact text occurrence. Args: path, old, new, optional all.", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file path.", true),
			arg("old", "string", "Exact existing text to replace.", true),
			arg("new", "string", "Replacement text. Empty string deletes the old text.", false),
			arg("all", "boolean", "Replace all occurrences instead of requiring one unique match.", false),
		}},
		{Name: "shell", Description: "Run a shell command in the workspace. Args: command.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("command", "string", "Shell command to run from the workspace root.", true),
		}},
		{Name: "go_test", Description: "Run the configured test command.", Risk: RiskShell},
		{Name: "run_linter", Description: "Detect and run the project's configured linter.", Risk: RiskShell},
		{Name: "run_formatter", Description: "Detect and run the project's formatter. This may rewrite source files.", Risk: RiskShell},
		{Name: "security_audit", Description: "Run the ecosystem's available dependency security audit.", Risk: RiskShell},
		{Name: "delegate", Description: "Spawn an isolated read-only specialist. Args: task, optional role (explore/review/debug/critic).", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("task", "string", "Focused read-only task for the specialist.", true),
			arg("role", "string", "Specialist role: explore, review, debug, or critic.", false),
		}},
	}
}

func arg(name, kind, description string, required bool) ArgumentSpec {
	return ArgumentSpec{Name: name, Type: kind, Description: description, Required: required}
}

// ToolSpecs returns provider-neutral schemas for all built-in tools.
func ToolSpecs() []llm.ToolSpec {
	builtins := Builtins()
	specs := make([]llm.ToolSpec, 0, len(builtins))
	for _, tool := range builtins {
		specs = append(specs, llm.ToolSpec{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.ParameterSchema(),
			Version:     "1.0.0",
		})
	}
	return specs
}

// ParameterSchema converts a tool argument catalog to a JSON-object schema.
func (tool Tool) ParameterSchema() llm.ToolSchema {
	properties := make(map[string]llm.ToolProperty, len(tool.Arguments))
	var required []string
	for _, argument := range tool.Arguments {
		property := llm.ToolProperty{Type: argument.Type, Description: argument.Description}
		if tool.Name == "apply_multi_patch" && argument.Name == "patches" {
			property.Items = &llm.ToolSchema{
				Type: "object",
				Properties: map[string]llm.ToolProperty{
					"path":    {Type: "string", Description: "Workspace-relative file path."},
					"content": {Type: "string", Description: "Complete UTF-8 file content."},
				},
				Required:             []string{"path", "content"},
				AdditionalProperties: false,
			}
		}
		properties[argument.Name] = property
		if argument.Required {
			required = append(required, argument.Name)
		}
	}
	return llm.ToolSchema{
		Type:                 "object",
		Properties:           properties,
		Required:             required,
		AdditionalProperties: false,
	}
}

// Lookup finds a builtin tool.
func Lookup(name string) (Tool, bool) {
	for _, tool := range Builtins() {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// RequiresApproval reports whether the configured policy needs user approval.
func (r Registry) RequiresApproval(name string) bool {
	// Auto-approve intentionally attempts every requested action immediately.
	// Unknown tools still fail safely in Execute instead of creating a useless
	// approval prompt that can never make them executable.
	if r.ApprovalPolicy == config.ApprovalAutoApprove {
		return false
	}

	tool, ok := Lookup(name)
	if !ok {
		return true
	}
	switch r.ApprovalPolicy {
	case config.ApprovalWorkspaceWrite:
		return false
	case config.ApprovalApproveWrites:
		return tool.Risk == RiskWrite || tool.Risk == RiskShell
	default:
		return tool.Risk != RiskRead
	}
}

// Normalize applies conservative provider-output repairs and then validates the call.
// It never guesses between conflicting values.
func (r Registry) Normalize(call Call) (Call, error) {
	call.Name = strings.TrimSpace(call.Name)
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	aliases := map[string]map[string]string{
		"list_files":        {"directory": "path", "dir": "path", "limit": "max"},
		"tree":              {"directory": "path", "dir": "path", "max_depth": "depth"},
		"read_file":         {"file": "path", "filename": "path", "start": "start_line", "end": "end_line"},
		"search":            {"pattern": "query", "text": "query", "directory": "path", "dir": "path", "limit": "max"},
		"grep_regex":        {"query": "pattern", "regex": "pattern", "directory": "path", "dir": "path", "limit": "max"},
		"find_symbol":       {"name": "symbol", "query": "symbol", "directory": "path", "dir": "path", "limit": "max"},
		"find_refs":         {"name": "symbol", "query": "symbol", "directory": "path", "dir": "path", "limit": "max"},
		"file_summary":      {"file": "path", "filename": "path"},
		"dependency_graph":  {"directory": "path", "dir": "path", "limit": "max"},
		"web_fetch":         {"link": "url", "uri": "url", "limit": "max_chars"},
		"github_issue":      {"repo": "repository", "issue": "number", "limit": "max"},
		"github_pr":         {"repo": "repository", "pr": "number", "limit": "max"},
		"git_diff":          {"file": "path"},
		"git_log":           {"file": "path", "limit": "max"},
		"git_blame":         {"file": "path", "filename": "path", "start": "start_line", "end": "end_line"},
		"git_create_branch": {"branch": "name"},
		"git_checkout":      {"branch": "name"},
		"git_commit":        {"msg": "message", "files": "paths"},
		"git_merge":         {"name": "branch"},
		"apply_patch":       {"file": "path", "filename": "path", "text": "content"},
		"apply_multi_patch": {"files": "patches", "changes": "patches"},
		"replace_in_file":   {"file": "path", "filename": "path", "old_text": "old", "new_text": "new", "replace_all": "all"},
		"shell":             {"cmd": "command"},
		"delegate":          {"prompt": "task", "specialist": "role"},
	}
	for alias, canonical := range aliases[call.Name] {
		value, exists := call.Arguments[alias]
		if !exists {
			continue
		}
		if current, conflict := call.Arguments[canonical]; conflict && fmt.Sprint(current) != fmt.Sprint(value) {
			return call, fmt.Errorf("%s received conflicting %q and %q arguments", call.Name, canonical, alias)
		}
		if _, exists := call.Arguments[canonical]; !exists {
			call.Arguments[canonical] = value
		}
		delete(call.Arguments, alias)
	}
	tool, ok := Lookup(call.Name)
	if !ok {
		return call, fmt.Errorf("unknown tool %q", call.Name)
	}
	if call.Name == "apply_multi_patch" {
		if value, exists := call.Arguments["patches"]; exists {
			normalized, err := normalizeMultiPatchArgument(value)
			if err != nil {
				return call, err
			}
			call.Arguments["patches"] = normalized
		}
	}
	for _, argument := range tool.Arguments {
		value, exists := call.Arguments[argument.Name]
		if !exists {
			continue
		}
		if value == nil && !argument.Required {
			delete(call.Arguments, argument.Name)
			continue
		}
		switch argument.Type {
		case "integer":
			switch item := value.(type) {
			case string:
				parsed, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
				if err == nil {
					call.Arguments[argument.Name] = parsed
				}
			case float64:
				if item == float64(int64(item)) {
					call.Arguments[argument.Name] = int64(item)
				}
			}
		case "boolean":
			if item, ok := value.(string); ok {
				parsed, err := strconv.ParseBool(strings.TrimSpace(item))
				if err == nil {
					call.Arguments[argument.Name] = parsed
				}
			}
		}
	}
	return call, r.Validate(call)
}

// Validate checks the shape of a tool call before execution.
func (r Registry) Validate(call Call) error {
	tool, ok := Lookup(call.Name)
	if !ok {
		return fmt.Errorf("unknown tool %q", call.Name)
	}
	allowed := make(map[string]ArgumentSpec, len(tool.Arguments))
	for _, argument := range tool.Arguments {
		allowed[argument.Name] = argument
	}
	for key := range call.Arguments {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s does not accept argument %q", call.Name, key)
		}
	}
	for _, argument := range tool.Arguments {
		if argument.Required && strings.TrimSpace(argString(call, argument.Name)) == "" {
			return fmt.Errorf("%s requires %q", call.Name, argument.Name)
		}
		if !hasArgument(call, argument.Name) {
			continue
		}
		if err := validateArgumentType(call, argument); err != nil {
			return err
		}
	}
	return nil
}

// ResolvePath returns a workspace-confined absolute path.
func (r Registry) ResolvePath(value string) (string, error) { return r.safePath(value) }

// Execute runs one approved tool call.
func (r Registry) Execute(ctx context.Context, call Call) Result {
	return r.ExecuteStream(ctx, call, nil)
}

// ExecuteStream runs a tool and publishes safe incremental command output.
func (r Registry) ExecuteStream(ctx context.Context, call Call, emit func(string)) Result {
	started := time.Now()
	finish := func(result Result, risk Risk) Result {
		result.Tool = call.Name
		result.Duration = time.Since(started)
		if result.OK && strings.TrimSpace(result.Output) == "" {
			result.Output = emptySuccessOutput(call.Name)
		}
		result.Output = truncateApproxTokens(result.Output, r.MaxOutputTokens)
		result.Error = truncateApproxTokens(result.Error, r.MaxOutputTokens)
		if result.Metadata == nil {
			result.Metadata = map[string]any{}
		}
		result.Metadata["ok"] = result.OK
		result.Metadata["risk"] = string(risk)
		result.Metadata["duration_ms"] = result.Duration.Milliseconds()
		return result
	}
	normalized, err := r.Normalize(call)
	if err != nil {
		tool, _ := Lookup(call.Name)
		return finish(fail(call.Name, err.Error()), tool.Risk)
	}
	call = normalized
	tool, _ := Lookup(call.Name)
	if r.ApprovalPolicy == config.ApprovalChat {
		return finish(fail(call.Name, "agent tools are disabled by chat approval policy"), tool.Risk)
	}
	if r.ApprovalPolicy == config.ApprovalReadOnly && tool.Risk != RiskRead {
		return finish(fail(call.Name, "write and shell tools are disabled by read-only policy"), tool.Risk)
	}
	if r.DryRun && tool.Risk != RiskRead {
		return finish(r.preview(call), tool.Risk)
	}

	var result Result
	switch call.Name {
	case "list_files":
		result = r.listFiles(call)
	case "tree":
		result = r.tree(call)
	case "read_file":
		result = r.readFile(call)
	case "search":
		result = r.search(call)
	case "grep_regex":
		result = r.grepRegex(call)
	case "find_symbol":
		result = r.findSymbol(call)
	case "find_refs":
		result = r.findRefs(call)
	case "file_summary":
		result = r.fileSummary(call)
	case "dependency_graph":
		result = r.dependencyGraph(call)
	case "detect_project_type":
		result = r.detectProjectType(call)
	case "list_dependencies":
		result = r.listDependencies(call)
	case "web_fetch":
		result = r.webFetch(ctx, call)
	case "github_issue":
		result = r.githubIssue(ctx, call)
	case "github_pr":
		result = r.githubPullRequest(ctx, call)
	case "git_status":
		result = r.runCommand(ctx, "git status --short", emit)
	case "git_diff":
		command := "git diff"
		if path := strings.TrimSpace(argString(call, "path")); path != "" {
			command += " -- " + shellQuote(path)
		}
		result = r.runCommand(ctx, command, emit)
	case "git_log":
		result = r.gitLog(ctx, call, emit)
	case "git_blame":
		result = r.gitBlame(ctx, call, emit)
	case "git_create_branch":
		result = r.gitCreateBranch(ctx, call, emit)
	case "git_checkout":
		result = r.gitCheckout(ctx, call, emit)
	case "git_commit":
		result = r.gitCommit(ctx, call, emit)
	case "git_stash":
		result = r.gitStash(ctx, call, emit)
	case "git_merge":
		result = r.gitMerge(ctx, call, emit)
	case "apply_patch":
		result = r.applyPatch(call)
	case "apply_multi_patch":
		result = r.applyMultiPatch(call)
	case "replace_in_file":
		result = r.replaceInFile(call)
	case "shell":
		result = r.runCommand(ctx, argString(call, "command"), emit)
	case "go_test":
		result = r.runCommand(ctx, r.AutoTestCommand, emit)
	case "run_linter":
		if command, commandErr := r.projectCommand("lint"); commandErr != nil {
			result = fail(call.Name, commandErr.Error())
		} else {
			result = r.runCommand(ctx, command, emit)
		}
	case "run_formatter":
		if command, commandErr := r.projectCommand("format"); commandErr != nil {
			result = fail(call.Name, commandErr.Error())
		} else {
			result = r.runCommand(ctx, command, emit)
		}
	case "security_audit":
		if command, commandErr := r.projectCommand("audit"); commandErr != nil {
			result = fail(call.Name, commandErr.Error())
		} else {
			result = r.runCommand(ctx, command, emit)
		}
	case "delegate":
		result = fail(call.Name, "delegate is executed by the agent orchestrator")
	default:
		result = fail(call.Name, "tool is not implemented")
	}
	return finish(result, tool.Risk)
}

// Preview simulates a normalized tool call without mutating the workspace.
func (r Registry) Preview(call Call) Result {
	normalized, err := r.Normalize(call)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	return r.preview(normalized)
}

func (r Registry) preview(call Call) Result {
	result := Result{Tool: call.Name, OK: true, Metadata: map[string]any{"dry_run": true, "changed": false}}
	switch call.Name {
	case "apply_patch":
		path, err := r.safePath(argString(call, "path"))
		if err != nil {
			return fail(call.Name, err.Error())
		}
		content, ok := call.Arguments["content"].(string)
		if !ok {
			return fail(call.Name, "content is required")
		}
		before, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(r.WorkspaceRoot, path)
		result.Output = renderDryRunDiff(filepath.ToSlash(rel), string(before), content)
		result.Metadata["path"] = filepath.ToSlash(rel)
	case "apply_multi_patch":
		result = r.previewMultiPatch(call)
	case "github_issue", "github_pr":
		if err := validateGitHubPreview(call); err != nil {
			return fail(call.Name, err.Error())
		}
		result.Output = "DRY RUN: would execute " + call.Name + " " + marshalPreviewArgs(call.Arguments)
	case "replace_in_file":
		path, err := r.safePath(argString(call, "path"))
		if err != nil {
			return fail(call.Name, err.Error())
		}
		before, err := os.ReadFile(path)
		if err != nil {
			return fail(call.Name, err.Error())
		}
		oldText := argString(call, "old")
		count := strings.Count(string(before), oldText)
		if oldText == "" || count == 0 {
			return fail(call.Name, "old text was not found")
		}
		if count > 1 && !argBoolDefault(call, "all", false) {
			return fail(call.Name, fmt.Sprintf("old text matched %d times; make it unique or set all=true", count))
		}
		limit := 1
		if argBoolDefault(call, "all", false) {
			limit = -1
		}
		after := strings.Replace(string(before), oldText, argString(call, "new"), limit)
		rel, _ := filepath.Rel(r.WorkspaceRoot, path)
		result.Output = renderDryRunDiff(filepath.ToSlash(rel), string(before), after)
		result.Metadata["path"] = filepath.ToSlash(rel)
		result.Metadata["replacements"] = count
	default:
		command := strings.TrimSpace(argString(call, "command"))
		if command == "" {
			switch call.Name {
			case "go_test":
				command = r.AutoTestCommand
			case "run_linter":
				command, _ = r.projectCommand("lint")
			case "run_formatter":
				command, _ = r.projectCommand("format")
			case "security_audit":
				command, _ = r.projectCommand("audit")
			default:
				command = call.Name + " " + marshalPreviewArgs(call.Arguments)
			}
		}
		result.Output = "DRY RUN: would execute " + strings.TrimSpace(command)
	}
	return result
}

func renderDryRunDiff(path, before, after string) string {
	if before == after {
		return "DRY RUN: no content change for " + path
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DRY RUN diff for %s\n--- a/%s\n+++ b/%s\n@@ complete-file preview @@\n", path, path, path)
	for _, line := range strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n") {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n") {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func marshalPreviewArgs(arguments map[string]any) string {
	data, _ := json.Marshal(arguments)
	return string(data)
}

func (r Registry) listFiles(call Call) Result {
	root, err := r.safePath(argStringDefault(call, "path", "."))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	maxItems := argIntDefault(call, "max", 200)
	files := make([]string, 0, maxItems)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(r.WorkspaceRoot, path)
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxItems {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fail(call.Name, err.Error())
	}
	sort.Strings(files)
	result := ok(call.Name, strings.Join(files, "\n"))
	requested := filepath.ToSlash(filepath.Clean(argStringDefault(call, "path", ".")))
	result.Metadata = map[string]any{"path": requested, "count": len(files), "max": maxItems}
	return result
}

func (r Registry) tree(call Call) Result {
	root, err := r.safePath(argStringDefault(call, "path", "."))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	depth := argIntDefault(call, "depth", 2)
	var lines []string
	baseDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) && path != root {
			return filepath.SkipDir
		}
		currentDepth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - baseDepth
		if currentDepth > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(r.WorkspaceRoot, path)
		if rel == "." {
			lines = append(lines, ".")
			return nil
		}
		prefix := strings.Repeat("  ", max(0, currentDepth-1))
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			name += "/"
		}
		lines = append(lines, prefix+name)
		return nil
	})
	if err != nil {
		return fail(call.Name, err.Error())
	}
	result := ok(call.Name, strings.Join(lines, "\n"))
	requested := filepath.ToSlash(filepath.Clean(argStringDefault(call, "path", ".")))
	result.Metadata = map[string]any{"path": requested, "entries": len(lines), "depth": depth}
	return result
}

func (r Registry) readFile(call Call) Result {
	path, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	startLine := argIntDefault(call, "start_line", 1)
	endLine := argIntDefault(call, "end_line", len(lines))
	startLine = max(1, min(startLine, len(lines)+1))
	endLine = max(startLine-1, min(endLine, len(lines)))
	if startLine > len(lines) || endLine < startLine {
		return ok(call.Name, "")
	}
	var out strings.Builder
	for index := startLine - 1; index < endLine; index++ {
		fmt.Fprintf(&out, "%d: %s", index+1, lines[index])
		if index+1 < endLine {
			out.WriteByte('\n')
		}
	}
	result := ok(call.Name, out.String())
	rel, _ := filepath.Rel(r.WorkspaceRoot, path)
	hash := sha256.Sum256(data)
	result.Metadata = map[string]any{
		"path":           filepath.ToSlash(rel),
		"start_line":     startLine,
		"end_line":       endLine,
		"content_sha256": fmt.Sprintf("%x", hash[:]),
		"file_bytes":     len(data),
	}
	return result
}

func (r Registry) search(call Call) Result {
	query := strings.TrimSpace(argString(call, "query"))
	if query == "" {
		return fail(call.Name, "query is required")
	}
	root, err := r.safePath(argStringDefault(call, "path", "."))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	maxItems := argIntDefault(call, "max", 80)
	var matches []string
	lower := strings.ToLower(query)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(matches) >= maxItems {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || looksBinary(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), lower) {
				rel, _ := filepath.Rel(r.WorkspaceRoot, path)
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
				if len(matches) >= maxItems {
					break
				}
			}
		}
		return nil
	})
	result := ok(call.Name, strings.Join(matches, "\n"))
	requested := filepath.ToSlash(filepath.Clean(argStringDefault(call, "path", ".")))
	result.Metadata = map[string]any{"path": requested, "query": query, "matches": len(matches), "max": maxItems}
	return result
}

func (r Registry) applyPatch(call Call) Result {
	path, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	content, okArg := call.Arguments["content"].(string)
	if !okArg {
		return fail(call.Name, "content is required; V1 apply_patch writes complete file content")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fail(call.Name, err.Error())
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fail(call.Name, err.Error())
	}
	rel, _ := filepath.Rel(r.WorkspaceRoot, path)
	result := ok(call.Name, "wrote "+filepath.ToSlash(rel))
	result.Metadata = map[string]any{"path": filepath.ToSlash(rel), "changed": true}
	return result
}

func (r Registry) replaceInFile(call Call) Result {
	path, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	oldText := argString(call, "old")
	newText := argString(call, "new")
	if oldText == "" {
		return fail(call.Name, "old is required")
	}
	count := strings.Count(string(data), oldText)
	if count == 0 {
		return fail(call.Name, "old text was not found")
	}
	replaceAll := argBoolDefault(call, "all", false)
	if count > 1 && !replaceAll {
		return fail(call.Name, fmt.Sprintf("old text matched %d times; make it unique or set all=true", count))
	}
	limit := 1
	if replaceAll {
		limit = -1
	}
	updated := strings.Replace(string(data), oldText, newText, limit)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return fail(call.Name, err.Error())
	}
	rel, _ := filepath.Rel(r.WorkspaceRoot, path)
	result := ok(call.Name, fmt.Sprintf("updated %s (%d replacement(s))", filepath.ToSlash(rel), count))
	result.Metadata = map[string]any{"path": filepath.ToSlash(rel), "changed": true, "replacements": count}
	return result
}

func (r Registry) runCommand(ctx context.Context, command string, emit func(string)) Result {
	command = strings.TrimSpace(command)
	if command == "" {
		return fail("shell", "command is required")
	}
	if looksDangerousCommand(command) {
		return Result{OK: false, Error: "refusing destructive shell command"}
	}
	timeout := r.CommandTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	sandbox := "host"
	if r.SandboxMode == config.SandboxDocker {
		dockerPath, err := exec.LookPath("docker")
		if err != nil {
			return Result{OK: false, Error: "docker sandbox requested, but docker is not installed or not on PATH", Metadata: map[string]any{"sandbox": "docker"}}
		}
		image := r.dockerSandboxImage()
		inspect := exec.CommandContext(ctx, dockerPath, "image", "inspect", image)
		if err := inspect.Run(); err != nil {
			return Result{OK: false, Error: "docker sandbox image is unavailable locally: " + image + " (pull it before running with network isolation)", Metadata: map[string]any{"sandbox": "docker", "image": image}}
		}
		mount := r.WorkspaceRoot + ":/workspace"
		cmd = exec.CommandContext(ctx, dockerPath,
			"run", "--rm", "--network", "none", "--read-only",
			"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
			"-v", mount, "-w", "/workspace", image, "sh", "-lc", command,
		)
		sandbox = "docker"
	} else if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
		cmd.Dir = r.WorkspaceRoot
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", command)
		cmd.Dir = r.WorkspaceRoot
	}
	var output synchronizedBuffer
	writer := io.Writer(&output)
	if emit != nil {
		writer = io.MultiWriter(&output, streamWriter{emit: emit})
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	text := output.String()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{OK: false, Output: text, Error: "command timed out", Metadata: map[string]any{"sandbox": sandbox}}
	}
	if err != nil {
		return Result{OK: false, Output: text, Error: err.Error(), Metadata: map[string]any{"sandbox": sandbox}}
	}
	return Result{OK: true, Output: text, Metadata: map[string]any{"sandbox": sandbox}}
}

func (r Registry) dockerSandboxImage() string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(r.WorkspaceRoot, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return "golang:1.25-alpine"
	case exists("package.json"):
		return "node:24-alpine"
	case exists("pyproject.toml") || exists("requirements.txt"):
		return "python:3.13-alpine"
	case exists("Cargo.toml"):
		return "rust:1.88-alpine"
	default:
		return "alpine:3.21"
	}
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type streamWriter struct{ emit func(string) }

func (w streamWriter) Write(data []byte) (int, error) {
	if len(data) > 0 && w.emit != nil {
		w.emit(string(append([]byte(nil), data...)))
	}
	return len(data), nil
}

func looksDangerousCommand(command string) bool {
	lower := strings.ToLower(command)
	dangerous := []string{
		"remove-item",
		"rm -r",
		"rm -rf",
		"rmdir /s",
		"del /s",
		"git reset --hard",
		"git clean -fd",
	}
	for _, token := range dangerous {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func (r Registry) safePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(r.WorkspaceRoot, value)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}

	root, err := filepath.EvalSymlinks(r.WorkspaceRoot)
	if err != nil {
		root = r.WorkspaceRoot
	}
	candidate, err := resolveForBoundary(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", value)
	}
	return abs, nil
}

func resolveForBoundary(path string) (string, error) {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}

	current := path
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{resolvedParent}, suffix...)
			return filepath.Join(parts...), nil
		}
		current = parent
	}
}

func hasArgument(call Call, key string) bool {
	if call.Arguments == nil {
		return false
	}
	_, ok := call.Arguments[key]
	return ok
}

func validateArgumentType(call Call, argument ArgumentSpec) error {
	value := call.Arguments[argument.Name]
	if value == nil {
		if argument.Required {
			return fmt.Errorf("%s requires %q", call.Name, argument.Name)
		}
		return nil
	}
	switch argument.Type {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s argument %q must be a string", call.Name, argument.Name)
		}
	case "integer":
		switch value := value.(type) {
		case int:
			return nil
		case int64:
			return nil
		case float64:
			if value == float64(int(value)) {
				return nil
			}
		case json.Number:
			if _, err := value.Int64(); err == nil {
				return nil
			}
		default:
		}
		return fmt.Errorf("%s argument %q must be an integer", call.Name, argument.Name)
	case "boolean":
		switch value := value.(type) {
		case bool:
			return nil
		case string:
			if strings.EqualFold(value, "true") || strings.EqualFold(value, "false") {
				return nil
			}
		default:
		}
		return fmt.Errorf("%s argument %q must be a boolean", call.Name, argument.Name)
	case "array":
		switch value.(type) {
		case []any, []map[string]any:
			return nil
		default:
			return fmt.Errorf("%s argument %q must be an array", call.Name, argument.Name)
		}
	}
	return nil
}

func argString(call Call, key string) string {
	if call.Arguments == nil {
		return ""
	}
	switch value := call.Arguments[key].(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		if value == nil {
			return ""
		}
		return fmt.Sprint(value)
	}
}

func argStringDefault(call Call, key, fallback string) string {
	if value := argString(call, key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func argIntDefault(call Call, key string, fallback int) int {
	if call.Arguments == nil {
		return fallback
	}
	switch value := call.Arguments[key].(type) {
	case float64:
		if value > 0 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed > 0 {
			return int(parsed)
		}
	}
	return fallback
}

func argBoolDefault(call Call, key string, fallback bool) bool {
	if call.Arguments == nil {
		return fallback
	}
	switch value := call.Arguments[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return fallback
	}
}

func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "bin", "node_modules", "vendor", ".gocache":
		return true
	default:
		return false
	}
}

func looksBinary(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".rar", ".zip", ".png", ".jpg", ".jpeg", ".gif", ".pdf":
		return true
	default:
		return false
	}
}

func truncateApproxTokens(value string, maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = 6000
	}
	maxChars := maxTokens * 4
	runes := []rune(value)
	if len(runes) <= maxChars {
		return strings.TrimRight(value, "\r\n")
	}
	head := maxChars * 2 / 3
	tail := maxChars - head
	return strings.TrimRight(string(runes[:head]), "\r\n") + "\n... truncated; tail preserved ...\n" + strings.TrimLeft(string(runes[len(runes)-tail:]), "\r\n")
}

func emptySuccessOutput(tool string) string {
	switch tool {
	case "list_files":
		return "No files found in the requested path. The directory exists and is empty or contains only ignored directories."
	case "tree":
		return "The requested path contains no visible entries."
	case "search":
		return "No matches found for the requested query."
	case "read_file":
		return "The requested file or line range is empty."
	case "git_status":
		return "Working tree clean; git status returned no changes."
	case "git_diff":
		return "No unstaged diff; git diff returned no changes."
	case "go_test":
		return "Verification command completed successfully with no output."
	case "shell":
		return "Command completed successfully with no output."
	default:
		return "Tool completed successfully with no output."
	}
}

func ok(tool, output string) Result {
	return Result{Tool: tool, OK: true, Output: output}
}

func fail(tool, message string) Result {
	return Result{Tool: tool, OK: false, Error: message}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
