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
	"github.com/ephemera-ai/ephemera/internal/debuglog"
)

// Risk describes the permissions needed to execute a tool.
type Risk string

const (
	RiskRead  Risk = "read"
	RiskWrite Risk = "write"
	RiskShell Risk = "shell"
)

// ToolProperty is a JSON-schema-compatible argument property.
type ToolProperty struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Items       *ToolSchema `json:"items,omitempty"`
}

// ToolSchema describes the JSON object accepted by a tool.
type ToolSchema struct {
	Type                 string                  `json:"type"`
	Properties           map[string]ToolProperty `json:"properties,omitempty"`
	Required             []string                `json:"required,omitempty"`
	AdditionalProperties bool                    `json:"additionalProperties"`
}

// Handler executes a tool against the active registry runtime.
type Handler func(context.Context, Registry, Call, func(string)) Result

// Tool is the single source of truth for provider schemas, policy metadata,
// and executable behavior. Provider adapters serialize Parameters while the
// registry invokes Execute through the middleware chain.
type Tool struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Risk          Risk             `json:"risk"`
	Parameters    ToolSchema       `json:"parameters"`
	Version       string           `json:"version,omitempty"`
	ProviderHints map[string]any   `json:"provider_hints,omitempty"`
	ValidateCall  func(Call) error `json:"-"`
	Execute       Handler          `json:"-"`

	// PathArguments declares arguments that address local filesystem targets.
	// Relative values resolve from WorkspaceRoot; absolute and escaping values
	// are permitted only when the exact call is explicitly approved.
	PathArguments []string            `json:"-"`
	PathExtractor func(Call) []string `json:"-"`

	// Arguments is retained as a source-compatible declaration helper. Register
	// compiles it into Parameters and validation reads only the schema.
	Arguments []ArgumentSpec `json:"-"`
}

// ArgumentSpec is the concise declaration form used by built-in tools.
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
	Summary  string
	Metadata map[string]any
	Duration time.Duration
}

// Registry owns the local tool catalog and execution policy.
type Registry struct {
	WorkspaceRoot      string
	ApprovalPolicy     config.ApprovalPolicy
	MaxOutputTokens    int
	AutoTestCommand    string
	CommandTimeout     time.Duration
	DryRun             bool
	SandboxMode        config.SandboxMode
	WebClient          HTTPDoer
	GitHubToken        string
	GitHubAPIURL       string
	middlewares        []Middleware
	catalog            *toolCatalogState
	resources          *registryResources
	allowExternalPaths bool
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
	registry := Registry{
		WorkspaceRoot:   root,
		ApprovalPolicy:  cfg.ApprovalPolicy,
		MaxOutputTokens: cfg.MaxToolOutputTokens,
		AutoTestCommand: cfg.AutoTestCommand,
		CommandTimeout:  time.Duration(cfg.AgentToolTimeoutSec) * time.Second,
		DryRun:          cfg.AgentDryRun,
		SandboxMode:     cfg.SandboxMode,
		GitHubToken:     strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		GitHubAPIURL:    "https://api.github.com",
		catalog:         defaultToolCatalog.clone(),
		resources:       &registryResources{},
	}
	registry.middlewares = defaultMiddlewareChain()
	for _, pluginErr := range registry.LoadConfiguredPlugins(context.Background(), cfg.PluginDirectories, cfg.PluginManifests) {
		debuglog.Error("plugin", "plugin discovery failed", pluginErr, map[string]any{"workspace": root})
	}
	return registry
}

func arg(name, kind, description string, required bool) ArgumentSpec {
	return ArgumentSpec{Name: name, Type: kind, Description: description, Required: required}
}

// ToolSpecs returns a deterministic snapshot of process-wide default tools.
func ToolSpecs() []Tool { return Builtins() }

// ParameterSchema compiles concise argument declarations into JSON Schema.
func (tool Tool) ParameterSchema() ToolSchema {
	if tool.Parameters.Type != "" || len(tool.Parameters.Properties) > 0 {
		return tool.Parameters
	}
	properties := make(map[string]ToolProperty, len(tool.Arguments))
	var required []string
	for _, argument := range toolArgumentSpecs(tool) {
		property := ToolProperty{Type: argument.Type, Description: argument.Description}
		if tool.Name == "apply_multi_patch" && argument.Name == "patches" {
			property.Items = &ToolSchema{
				Type: "object",
				Properties: map[string]ToolProperty{
					"path":    {Type: "string", Description: "Workspace-relative or absolute file path; external paths require approval."},
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
	return ToolSchema{Type: "object", Properties: properties, Required: required, AdditionalProperties: false}
}

// RequiresApproval reports whether the configured policy needs user approval
// based only on tool risk. Call-aware code must use RequiresApprovalCall so an
// out-of-workspace path can be routed through the same approval flow.
func (r Registry) RequiresApproval(name string) bool {
	// Auto-approve intentionally attempts every requested action immediately.
	// Unknown tools still fail safely in Execute instead of creating a useless
	// approval prompt that can never make them executable.
	if r.ApprovalPolicy == config.ApprovalAutoApprove {
		return false
	}

	tool, ok := r.Lookup(name)
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

// RequiresApprovalCall reports whether this exact call needs confirmation. In
// addition to the configured risk policy, every filesystem target outside the
// active workspace requires approval unless auto-approve is enabled.
func (r Registry) RequiresApprovalCall(call Call) bool {
	if r.ApprovalPolicy == config.ApprovalAutoApprove {
		return false
	}
	if r.RequiresApproval(call.Name) {
		return true
	}
	normalized, err := r.Normalize(call)
	if err != nil {
		return false
	}
	targets, err := r.externalTargets(normalized)
	return err == nil && len(targets) > 0
}

// ApprovalReason returns a concise call-specific explanation for the prompt.
func (r Registry) ApprovalReason(call Call) string {
	normalized, err := r.Normalize(call)
	if err != nil {
		return ""
	}
	targets, err := r.externalTargets(normalized)
	if err != nil || len(targets) == 0 {
		return ""
	}
	return "Access outside the active workspace: " + strings.Join(targets, ", ") + ". Workspace snapshots and rollback do not cover external targets."
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
		"create_directory":  {"directory": "path", "dir": "path", "folder": "path", "name": "path"},
		"apply_patch":       {"file": "path", "filename": "path", "text": "content"},
		"apply_multi_patch": {"files": "patches", "changes": "patches"},
		"replace_in_file":   {"file": "path", "filename": "path", "old_text": "old", "new_text": "new", "replace_all": "all"},
		"prefer":            {"text": "preference", "value": "preference"},
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
	tool, ok := r.Lookup(call.Name)
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
	for _, argument := range toolArgumentSpecs(tool) {
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

// RepairHint gives the model one deterministic correction after malformed tool
// arguments without guessing or executing a different write on its behalf.
func RepairHint(call Call, err error) string {
	if err == nil {
		return ""
	}
	switch call.Name {
	case "apply_patch":
		if strings.TrimSpace(argString(call, "content")) == "" {
			return `apply_patch writes a file and requires both "path" and complete "content"; to create only a folder, call create_directory with "path" instead`
		}
	case "create_directory":
		if strings.TrimSpace(argString(call, "path")) == "" {
			return `resend create_directory with a non-empty "path"`
		}
	case "shell":
		if strings.TrimSpace(argString(call, "command")) == "" {
			return `resend shell with a non-empty "command"; use create_directory for folders and apply_patch for files`
		}
	}
	return "resend the same tool once with every required argument from its schema; do not repeat the unchanged invalid call"
}

// Validate checks the shape of a tool call before execution.
func (r Registry) Validate(call Call) error {
	tool, ok := r.Lookup(call.Name)
	if !ok {
		return fmt.Errorf("unknown tool %q", call.Name)
	}
	arguments := toolArgumentSpecs(tool)
	allowed := make(map[string]ArgumentSpec, len(arguments))
	for _, argument := range arguments {
		allowed[argument.Name] = argument
	}
	for key := range call.Arguments {
		if _, ok := allowed[key]; !ok && !tool.Parameters.AdditionalProperties {
			return fmt.Errorf("%s does not accept argument %q", call.Name, key)
		}
	}
	for _, argument := range arguments {
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
	if tool.ValidateCall != nil {
		return tool.ValidateCall(call)
	}
	return nil
}

// ResolvePath returns an absolute path after enforcing the active approval scope.
func (r Registry) ResolvePath(value string) (string, error) { return r.safePath(value) }

// PathIdentity returns the stable path label used by agent state without granting
// access to the target. Workspace paths are relative; external paths are absolute.
func (r Registry) PathIdentity(value string) (string, error) {
	abs, outside, err := r.classifyPath(value)
	if err != nil {
		return "", err
	}
	if outside {
		return filepath.ToSlash(abs), nil
	}
	rel, err := filepath.Rel(r.WorkspaceRoot, abs)
	if err != nil {
		return filepath.ToSlash(abs), nil
	}
	return filepath.ToSlash(rel), nil
}

func (r Registry) displayPath(absolute string) string {
	identity, err := r.PathIdentity(absolute)
	if err != nil || strings.TrimSpace(identity) == "" {
		return filepath.ToSlash(filepath.Clean(absolute))
	}
	return identity
}

// Execute runs one approved tool call.
func (r Registry) Execute(ctx context.Context, call Call) Result {
	return r.ExecuteStream(ctx, call, nil)
}

// ExecuteStream runs a tool through the shared middleware pipeline.
func (r Registry) ExecuteStream(ctx context.Context, call Call, emit func(string)) Result {
	chain := r.middlewares
	if len(chain) == 0 {
		chain = defaultMiddlewareChain()
	}
	terminal := Handler(func(ctx context.Context, runtime Registry, call Call, emit func(string)) Result {
		definition, ok := runtime.Lookup(call.Name)
		if !ok {
			return fail(call.Name, "tool is not registered")
		}
		if definition.Execute == nil {
			return fail(call.Name, "tool has no executor")
		}
		return definition.Execute(ctx, runtime, call, emit)
	})
	for index := len(chain) - 1; index >= 0; index-- {
		terminal = chain[index](terminal)
	}
	return terminal(ctx, r, call, emit)
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
	case "create_directory":
		path, err := r.safePath(argString(call, "path"))
		if err != nil {
			return fail(call.Name, err.Error())
		}
		display := r.displayPath(path)
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && !info.IsDir():
			return fail(call.Name, "path already exists and is not a directory: "+display)
		case statErr == nil:
			result.Output = "DRY RUN: directory already exists: " + display
			result.Metadata["changed"] = false
			result.Metadata["existed"] = true
		case os.IsNotExist(statErr):
			result.Output = "DRY RUN: would create directory " + display
			result.Metadata["changed"] = true
			result.Metadata["existed"] = false
		default:
			return fail(call.Name, statErr.Error())
		}
		result.Metadata["path"] = display
		result.Metadata["directory"] = true
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
		display := r.displayPath(path)
		result.Output = renderDryRunDiff(display, string(before), content)
		result.Metadata["path"] = display
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
		display := r.displayPath(path)
		result.Output = renderDryRunDiff(display, string(before), after)
		result.Metadata["path"] = display
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

func (r Registry) listFiles(call Call) Result { return r.listFilesStream(call, nil) }

func (r Registry) listFilesStream(call Call, emit func(string)) Result {
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
		item := r.displayPath(path)
		files = append(files, item)
		if emit != nil {
			emit(item + "\n")
		}
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

func (r Registry) tree(call Call) Result { return r.treeStream(call, nil) }

func (r Registry) treeStream(call Call, emit func(string)) Result {
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
		rel := r.displayPath(path)
		if rel == "." {
			lines = append(lines, ".")
			if emit != nil {
				emit(".\n")
			}
			return nil
		}
		prefix := strings.Repeat("  ", max(0, currentDepth-1))
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			name += "/"
		}
		line := prefix + name
		lines = append(lines, line)
		if emit != nil {
			emit(line + "\n")
		}
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

func (r Registry) readFile(call Call) Result { return r.readFileStream(call, nil) }

func (r Registry) readFileStream(call Call, emit func(string)) Result {
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
	var chunk strings.Builder
	for index := startLine - 1; index < endLine; index++ {
		line := fmt.Sprintf("%d: %s", index+1, lines[index])
		out.WriteString(line)
		chunk.WriteString(line)
		if index+1 < endLine {
			out.WriteByte('\n')
			chunk.WriteByte('\n')
		}
		if emit != nil && (index-startLine+2)%50 == 0 {
			emit(chunk.String())
			chunk.Reset()
		}
	}
	if emit != nil && chunk.Len() > 0 {
		emit(chunk.String())
	}
	result := ok(call.Name, out.String())
	display := r.displayPath(path)
	hash := sha256.Sum256(data)
	result.Metadata = map[string]any{
		"path":           display,
		"start_line":     startLine,
		"end_line":       endLine,
		"content_sha256": fmt.Sprintf("%x", hash[:]),
		"file_bytes":     len(data),
	}
	return result
}

func (r Registry) search(call Call) Result { return r.searchStream(call, nil) }

func (r Registry) searchStream(call Call, emit func(string)) Result {
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
				display := r.displayPath(path)
				match := fmt.Sprintf("%s:%d: %s", display, i+1, strings.TrimSpace(line))
				matches = append(matches, match)
				if emit != nil {
					emit(match + "\n")
				}
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

func (r Registry) createDirectory(call Call) Result {
	path, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	display := r.displayPath(path)
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && !info.IsDir():
		return fail(call.Name, "path already exists and is not a directory: "+display)
	case statErr == nil:
		result := ok(call.Name, "directory already exists: "+display)
		result.Metadata = map[string]any{"path": display, "changed": false, "directory": true, "existed": true}
		return result
	case !os.IsNotExist(statErr):
		return fail(call.Name, statErr.Error())
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fail(call.Name, err.Error())
	}
	result := ok(call.Name, "created directory "+display)
	result.Metadata = map[string]any{"path": display, "changed": true, "directory": true, "existed": false}
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
	display := r.displayPath(path)
	result := ok(call.Name, "wrote "+display)
	result.Metadata = map[string]any{"path": display, "changed": true}
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
	display := r.displayPath(path)
	result := ok(call.Name, fmt.Sprintf("updated %s (%d replacement(s))", display, count))
	result.Metadata = map[string]any{"path": display, "changed": true, "replacements": count}
	return result
}

func (r Registry) runVerificationCommand(ctx context.Context, call Call, emit func(string)) Result {
	command := strings.TrimSpace(argString(call, "command"))
	configured := strings.TrimSpace(r.AutoTestCommand)
	if command == "" {
		command = configured
	}
	if command == "" {
		return fail(call.Name, "verification command is not configured")
	}
	if command != configured && (!recognizedVerificationCommand(command) || unsafeVerificationComposition(command)) {
		return fail(call.Name, "verification override is not a recognized standalone test command")
	}
	result := r.runCommand(ctx, command, emit)
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["command"] = command
	result.Metadata["verification"] = true
	return result
}

func unsafeVerificationComposition(command string) bool {
	return strings.ContainsAny(command, "\r\n;&|><`\x00") || strings.Contains(command, "$(")
}

func recognizedVerificationCommand(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	prefixes := []string{
		"go test", "npm test", "npm run test", "pnpm test", "pnpm run test", "yarn test", "yarn run test",
		"cargo test", "pytest", "python -m pytest", "python3 -m pytest", "dotnet test", "mvn test", "mvnw test",
		"gradle test", "gradlew test",
	}
	for _, prefix := range prefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
	}
	return false
}

func (r Registry) runCommand(ctx context.Context, command string, emit func(string)) Result {
	command = strings.TrimSpace(command)
	if command == "" {
		return fail("shell", "command is required")
	}
	if looksDangerousCommand(command) {
		return Result{OK: false, Error: "refusing destructive shell command"}
	}
	route, _ := ctx.Value(sandboxRouteKey{}).(sandboxRoute)
	if route.mode == "" {
		route.mode = "host"
	}
	var cmd *exec.Cmd
	sandbox := route.mode
	if route.mode == "docker" {
		mount := r.WorkspaceRoot + ":/workspace"
		cmd = exec.CommandContext(ctx, route.dockerPath,
			"run", "--rm", "--network", "none", "--read-only",
			"--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
			"-v", mount, "-w", "/workspace", route.image, "sh", "-lc", command,
		)
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
	if ctx.Err() == context.Canceled {
		return Result{OK: false, Output: text, Error: "command canceled", Metadata: map[string]any{"sandbox": sandbox}}
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
	abs, outside, err := r.classifyPath(value)
	if err != nil {
		return "", err
	}
	if outside && !r.allowExternalPaths {
		return "", fmt.Errorf("path is outside the active workspace and requires explicit approval: %s", abs)
	}
	return abs, nil
}

func (r Registry) classifyPath(value string) (absolute string, outside bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(r.WorkspaceRoot, value)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", false, err
	}
	root, err := filepath.Abs(r.WorkspaceRoot)
	if err != nil {
		return "", false, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	candidate, err := resolveForBoundary(abs)
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		rootVolume := filepath.VolumeName(root)
		candidateVolume := filepath.VolumeName(candidate)
		if rootVolume != "" && candidateVolume != "" && !strings.EqualFold(rootVolume, candidateVolume) {
			return abs, true, nil
		}
		return "", false, err
	}
	outside = rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
	return abs, outside, nil
}

func (r Registry) externalTargets(call Call) ([]string, error) {
	tool, ok := r.Lookup(call.Name)
	if !ok {
		return nil, nil
	}
	values := make([]string, 0, len(tool.PathArguments)+2)
	for _, name := range tool.PathArguments {
		value := strings.TrimSpace(argString(call, name))
		if value != "" {
			values = append(values, value)
		}
	}
	if tool.PathExtractor != nil {
		values = append(values, tool.PathExtractor(call)...)
	}
	seen := map[string]bool{}
	var external []string
	for _, value := range values {
		abs, outside, err := r.classifyPath(value)
		if err != nil {
			return nil, err
		}
		key := filepath.Clean(abs)
		if outside && !seen[key] {
			seen[key] = true
			external = append(external, key)
		}
	}
	sort.Strings(external)
	return external, nil
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
	truncated, _ := truncateApproxTokensWithSummary(value, maxTokens)
	return truncated
}

func truncateApproxTokensWithSummary(value string, maxTokens int) (string, string) {
	if maxTokens <= 0 {
		maxTokens = 6000
	}
	maxChars := maxTokens * 4
	runes := []rune(value)
	if len(runes) <= maxChars {
		return strings.TrimRight(value, "\r\n"), ""
	}
	totalLines := strings.Count(value, "\n") + 1
	head := maxChars * 2 / 3
	tail := maxChars - head
	headText := strings.TrimRight(string(runes[:head]), "\r\n")
	tailText := strings.TrimLeft(string(runes[len(runes)-tail:]), "\r\n")
	visibleLines := strings.Count(headText, "\n") + strings.Count(tailText, "\n") + 2
	summary := fmt.Sprintf("truncated output: %d lines/%d characters total; showing about %d lines from the beginning and end", totalLines, len(runes), visibleLines)
	return headText + "\n[" + summary + "]\n" + tailText, summary
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
