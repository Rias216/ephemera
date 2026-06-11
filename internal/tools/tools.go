// Package tools contains the local, approval-aware tools used by the agent.
package tools

import (
	"context"
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
}

// Call is a structured request to execute a tool.
type Call struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Result is the normalized output from a tool.
type Result struct {
	Tool     string
	OK       bool
	Output   string
	Error    string
	Metadata map[string]any
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
		{Name: "list_files", Description: "List workspace files. Args: optional path, max.", Risk: RiskRead},
		{Name: "tree", Description: "Show a shallow workspace tree. Args: optional path, depth.", Risk: RiskRead},
		{Name: "read_file", Description: "Read a UTF-8 text file. Args: path.", Risk: RiskRead},
		{Name: "search", Description: "Search text files. Args: query, optional path, max.", Risk: RiskRead},
		{Name: "git_status", Description: "Run git status --short.", Risk: RiskRead},
		{Name: "git_diff", Description: "Run git diff.", Risk: RiskRead},
		{Name: "apply_patch", Description: "Write complete file content. Args: path, content.", Risk: RiskWrite},
		{Name: "shell", Description: "Run a PowerShell command in the workspace. Args: command.", Risk: RiskShell},
		{Name: "go_test", Description: "Run the configured Go test command.", Risk: RiskShell},
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

// Execute runs one approved tool call.
func (r Registry) Execute(ctx context.Context, call Call) Result {
	tool, ok := Lookup(call.Name)
	if !ok {
		return fail(call.Name, "unknown tool")
	}
	if r.ApprovalPolicy == config.ApprovalChat {
		return fail(call.Name, "agent tools are disabled by chat approval policy")
	}
	if r.ApprovalPolicy == config.ApprovalReadOnly && tool.Risk != RiskRead {
		return fail(call.Name, "write and shell tools are disabled by read-only policy")
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
		result = r.runCommand(ctx, "git diff")
	case "apply_patch":
		result = r.applyPatch(call)
	case "shell":
		result = r.runCommand(ctx, argString(call, "command"))
	case "go_test":
		result = r.runCommand(ctx, r.AutoTestCommand)
	default:
		result = fail(call.Name, "tool is not implemented")
	}
	result.Tool = call.Name
	result.Output = truncateApproxTokens(result.Output, r.MaxOutputTokens)
	result.Error = truncateApproxTokens(result.Error, r.MaxOutputTokens)
	return result
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
	return ok(call.Name, strings.Join(files, "\n"))
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
	return ok(call.Name, strings.Join(lines, "\n"))
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
	return ok(call.Name, string(data))
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
	return ok(call.Name, strings.Join(matches, "\n"))
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
	return ok(call.Name, "wrote "+filepath.ToSlash(rel))
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
	rel, err := filepath.Rel(r.WorkspaceRoot, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", value)
	}
	return abs, nil
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
