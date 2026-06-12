package agent

import (
	"sort"
	"strings"
)

// executionIntent is a deterministic guardrail for requests that explicitly
// require workspace evidence or mutations. It prevents providers that ignore
// native tool schemas from claiming completion in prose before any action ran.
type executionIntent struct {
	Prompt               string
	RequiresWorkspace    bool
	RequiresWrite        bool
	RequiresGitMutation  bool
	RequiresVerification bool
}

func classifyExecutionIntent(prompt string) executionIntent {
	text := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	intent := executionIntent{Prompt: strings.TrimSpace(prompt)}
	if text == "" {
		return intent
	}

	workspaceTerms := []string{
		"workspace", "repo", "repository", "codebase", "project", "source", "harness",
		"file", "files", "folder", "directory", "git", "branch", "commit", "test suite",
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".json", ".yaml", ".yml", ".toml", ".md",
	}
	writeTerms := []string{
		"create ", "implement ", "execute ", "apply ", "edit ", "modify ", "update ", "change ",
		"fix ", "repair ", "refactor ", "rewrite ", "replace ", "remove ", "delete ", "rename ",
		"add ", "write ", "patch ", "generate ", "make sure", "set up", "setup ",
	}
	gitTerms := []string{
		"git commit", "commit ", "create branch", "checkout ", "switch branch", "merge ",
		"stash ", "stage ", "cherry-pick", "rebase ",
	}
	verificationTerms := []string{
		"run tests", "run the tests", "test this", "verify ", "validate ", "run lint", "lint ",
		"run formatter", "format ", "security audit", "benchmark ", "build and test",
	}
	inspectionTerms := []string{
		"inspect ", "analyze ", "analyse ", "review ", "debug ", "investigate ", "search ",
		"find ", "read ", "check ", "list files", "show files", "what files", "git status", "git diff",
	}

	hasWorkspace := containsAny(text, workspaceTerms)
	intent.RequiresWrite = containsAny(text, writeTerms) && hasWorkspace
	// Explanatory questions may mention words such as "fix" or "edit" without
	// authorizing a workspace mutation. Require an execution cue before turning
	// those advisory prompts into mandatory write work.
	advisory := strings.HasPrefix(text, "how ") || strings.HasPrefix(text, "why ") ||
		strings.HasPrefix(text, "what ") || strings.HasPrefix(text, "explain ") ||
		strings.HasPrefix(text, "can you explain")
	executionCue := containsAny(text, []string{
		"please fix", "fix this", "go ahead", "do it", "execute", "implement",
		"make the change", "make these changes", "edit the", "update the", "create the",
	})
	if advisory && !executionCue {
		intent.RequiresWrite = false
	}
	intent.RequiresGitMutation = containsAny(text, gitTerms)
	intent.RequiresVerification = containsAny(text, verificationTerms) && hasWorkspace
	previewOnly := containsAny(text, []string{"dry run", "dry-run", "preview ", "preview the", "show what would", "without changing", "without writing", "do not modify"})
	if previewOnly {
		intent.RequiresWrite = false
		intent.RequiresGitMutation = false
	}
	intent.RequiresWorkspace = intent.RequiresWrite || intent.RequiresGitMutation || intent.RequiresVerification ||
		(hasWorkspace && containsAny(text, inspectionTerms))

	// Explicit execution language is stronger than the noun heuristic. This
	// catches requests such as "execute the attached plan" where the prompt may
	// not repeat the words repository or file.
	if containsAny(text, []string{"execute the plan", "implement the plan", "apply the plan", "make the changes"}) {
		intent.RequiresWorkspace = true
		intent.RequiresWrite = true
	}
	return intent
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (i executionIntent) pendingEvidence(state *runState) []string {
	if state == nil || !i.RequiresWorkspace {
		return nil
	}
	var pending []string
	if state.successfulToolCount(false) == 0 {
		pending = append(pending, "at least one successful workspace tool result")
	}
	if i.RequiresWrite && !state.changed && !state.usedSuccessfulTool("apply_patch", "apply_multi_patch", "replace_in_file", "run_formatter") {
		pending = append(pending, "a successful workspace write")
	}
	if i.RequiresGitMutation && !state.usedSuccessfulTool("git_commit", "git_create_branch", "git_checkout", "git_stash", "git_merge") {
		pending = append(pending, "the requested git mutation")
	}
	if i.RequiresVerification && state.verificationAttempted && !state.verified && !state.usedSuccessfulTool("go_test", "run_linter", "run_formatter", "security_audit") {
		pending = append(pending, "a successful verification tool")
	}
	if len(pending) == 0 {
		return nil
	}
	sort.Strings(pending)
	return pending
}

func (s *runState) usedSuccessfulTool(names ...string) bool {
	if s == nil {
		return false
	}
	for _, name := range names {
		if s.successfulTools[name] > 0 {
			return true
		}
	}
	return false
}

func (s *runState) successfulToolCount(includeDelegate bool) int {
	if s == nil {
		return 0
	}
	total := 0
	for name, count := range s.successfulTools {
		if !includeDelegate && name == "delegate" {
			continue
		}
		total += count
	}
	return total
}
