// Package tools contains the local, approval-aware tools used by the agent.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
		{Name: "git_status", Description: "Run git status --short.", Risk: RiskRead},
		{Name: "git_diff", Description: "Run git diff. Args: optional path.", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("path", "string", "Optional workspace-relative path to diff.", false),
		}},
		{Name: "apply_patch", Description: "Write complete file content. Args: path, content. Prefer replace_in_file for small edits.", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file path to create or replace.", true),
			arg("content", "string", "Complete UTF-8 file content.", true),
		}},
		{Name: "replace_in_file", Description: "Replace one exact text occurrence. Args: path, old, new, optional all.", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("path", "string", "Workspace-relative file path.", true),
			arg("old", "string", "Exact existing text to replace.", true),
			arg("new", "string", "Replacement text. Empty string deletes the old text.", false),
			arg("all", "boolean", "Replace all occurrences instead of requiring one unique match.", false),
		}},
		{Name: "shell", Description: "Run a PowerShell command in the workspace. Args: command.", Risk: RiskShell, Arguments: []ArgumentSpec{
			arg("command", "string", "PowerShell command to run from the workspace root.", true),
		}},
		{Name: "go_test", Description: "Run the configured test command.", Risk: RiskShell},
		{Name: "delegate", Description: "Spawn an isolated read-only specialist. Args: task, optional role (explore/review/debug).", Risk: RiskRead, Arguments: []ArgumentSpec{
			arg("task", "string", "Focused read-only task for the specialist.", true),
			arg("role", "string", "Specialist role: explore, review, or debug.", false),
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
		})
	}
	return specs
}

// ParameterSchema converts a tool argument catalog to a JSON-object schema.
func (tool Tool) ParameterSchema() llm.ToolSchema {
	properties := make(map[string]llm.ToolProperty, len(tool.Arguments))
	var required []string
	for _, argument := range tool.Arguments {
		properties[argument.Name] = llm.ToolProperty{Type: argument.Type, Description: argument.Description}
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
	tool, ok := Lookup(call.Name)
	if !ok {
		return finish(fail(call.Name, "unknown tool"), "")
	}
	if err := r.Validate(call); err != nil {
		return finish(fail(call.Name, err.Error()), tool.Risk)
	}
	if r.ApprovalPolicy == config.ApprovalChat {
		return finish(fail(call.Name, "agent tools are disabled by chat approval policy"), tool.Risk)
	}
	if r.ApprovalPolicy == config.ApprovalReadOnly && tool.Risk != RiskRead {
		return finish(fail(call.Name, "write and shell tools are disabled by read-only policy"), tool.Risk)
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
	case "git_status":
		result = r.runCommand(ctx, "git status --short")
	case "git_diff":
		command := "git diff"
		if path := strings.TrimSpace(argString(call, "path")); path != "" {
			command += " -- " + shellQuote(path)
		}
		result = r.runCommand(ctx, command)
	case "apply_patch":
		result = r.applyPatch(call)
	case "replace_in_file":
		result = r.replaceInFile(call)
	case "shell":
		result = r.runCommand(ctx, argString(call, "command"))
	case "go_test":
		result = r.runCommand(ctx, r.AutoTestCommand)
	case "delegate":
		result = fail(call.Name, "delegate is executed by the agent orchestrator")
	default:
		result = fail(call.Name, "tool is not implemented")
	}
	return finish(result, tool.Risk)
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
	result.Metadata = map[string]any{"path": filepath.ToSlash(rel), "start_line": startLine, "end_line": endLine}
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

func (r Registry) runCommand(ctx context.Context, command string) Result {
	command = strings.TrimSpace(command)
	if command == "" {
		return fail("shell", "command is required")
	}
	if looksDangerousCommand(command) {
		return Result{OK: false, Error: "refusing destructive shell command"}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	cmd.Dir = r.WorkspaceRoot
	data, err := cmd.CombinedOutput()
	output := string(data)
	if ctx.Err() == context.DeadlineExceeded {
		return Result{OK: false, Output: output, Error: "command timed out"}
	}
	if err != nil {
		return Result{OK: false, Output: output, Error: err.Error()}
	}
	return Result{OK: true, Output: output}
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
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
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
	if len(value) <= maxChars {
		return strings.TrimRight(value, "\r\n")
	}
	return strings.TrimRight(value[:maxChars], "\r\n") + "\n... truncated ..."
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
