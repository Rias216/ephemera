package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type verificationPlan struct {
	Command    string
	Scope      string
	Applicable bool
	Reason     string
}

func (r Runner) buildVerificationPlan(state *runState) verificationPlan {
	command := strings.TrimSpace(r.Config.AutoTestCommand)
	if command == "" {
		return verificationPlan{Scope: "none", Reason: "no verification command configured"}
	}
	if state == nil {
		return verificationPlan{Command: command, Scope: "full", Applicable: r.verificationCommandApplicableCommand(command), Reason: "run state unavailable"}
	}
	if state.projectManifestSource == "file" {
		return verificationPlan{Command: command, Scope: "manifest", Applicable: r.verificationCommandApplicableCommand(command), Reason: "explicit project manifest requires the configured command"}
	}
	lower := strings.ToLower(command)
	if !strings.HasPrefix(lower, "go test") || !containsCommandToken(command, "./...") {
		return verificationPlan{Command: command, Scope: "full", Applicable: r.verificationCommandApplicableCommand(command), Reason: "configured command cannot be narrowed safely"}
	}
	if _, err := os.Stat(filepath.Join(r.Tools.WorkspaceRoot, "go.mod")); err != nil {
		return verificationPlan{Command: command, Scope: "full", Applicable: false, Reason: "go.mod is unavailable"}
	}

	packages := map[string]bool{}
	hasGoChange := false
	for _, path := range sortedKeys(state.changedPaths) {
		path = normalizePath(path)
		if path == "" || runtimeArtifactPath(path) {
			continue
		}
		base := strings.ToLower(filepath.Base(filepath.FromSlash(path)))
		if base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" {
			return verificationPlan{Command: command, Scope: "full", Applicable: true, Reason: "module metadata changed"}
		}
		if !strings.HasSuffix(base, ".go") {
			continue
		}
		hasGoChange = true
		dir := normalizePath(filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))))
		if dir == "." || dir == "" {
			return verificationPlan{Command: command, Scope: "full", Applicable: true, Reason: "root package changed"}
		}
		if !safeGoPackagePath(dir) {
			return verificationPlan{Command: command, Scope: "full", Applicable: true, Reason: "changed package path requires shell-specific quoting"}
		}
		packages["./"+strings.TrimPrefix(dir, "./")+"/..."] = true
	}
	if !hasGoChange {
		return verificationPlan{Scope: "not-applicable", Applicable: false, Reason: "no Go source or module metadata changed"}
	}
	if len(packages) == 0 || len(packages) > 8 {
		return verificationPlan{Command: command, Scope: "full", Applicable: true, Reason: "task spans too many packages to narrow safely"}
	}
	patterns := make([]string, 0, len(packages))
	for pattern := range packages {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return verificationPlan{
		Command:    replaceCommandToken(command, "./...", patterns),
		Scope:      "task",
		Applicable: true,
		Reason:     "verification narrowed to packages changed by this run",
	}
}

func containsCommandToken(command, target string) bool {
	for _, token := range strings.Fields(command) {
		if token == target {
			return true
		}
	}
	return false
}

func replaceCommandToken(command, target string, replacements []string) string {
	fields := strings.Fields(command)
	out := make([]string, 0, len(fields)+len(replacements))
	replaced := false
	for _, field := range fields {
		if field == target && !replaced {
			out = append(out, replacements...)
			replaced = true
			continue
		}
		out = append(out, field)
	}
	if !replaced {
		return command
	}
	return strings.Join(out, " ")
}

func safeGoPackagePath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, r := range path {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '/', '-', '_', '.':
			continue
		default:
			return false
		}
	}
	return true
}
