package tools

// Builtins returns the V1 local tool catalog.
func builtinTools() []Tool {
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
		{Name: "prefer", Description: "Record a durable user or project preference. Args: preference, optional scope (global/project).", Risk: RiskWrite, Arguments: []ArgumentSpec{
			arg("preference", "string", "Concise durable preference to remember.", true),
			arg("scope", "string", "global (default) or project.", false),
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
