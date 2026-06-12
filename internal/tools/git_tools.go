package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func (r Registry) gitLog(ctx context.Context, call Call, emit func(string)) Result {
	maxItems := argIntDefault(call, "max", 20)
	if maxItems > 200 {
		maxItems = 200
	}
	command := fmt.Sprintf("git log -n %d --date=short --pretty=format:'%%h %%ad %%an %%s'", maxItems)
	if path := strings.TrimSpace(argString(call, "path")); path != "" {
		resolved, err := r.safePath(path)
		if err != nil {
			return fail(call.Name, err.Error())
		}
		rel, _ := filepath.Rel(r.WorkspaceRoot, resolved)
		command += " -- " + shellQuote(filepath.ToSlash(rel))
	}
	return r.runCommand(ctx, command, emit)
}

func (r Registry) gitBlame(ctx context.Context, call Call, emit func(string)) Result {
	resolved, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	rel, _ := filepath.Rel(r.WorkspaceRoot, resolved)
	command := "git blame --date=short"
	start := argIntDefault(call, "start_line", 0)
	end := argIntDefault(call, "end_line", 0)
	if start > 0 {
		if end < start {
			end = start
		}
		command += fmt.Sprintf(" -L %d,%d", start, end)
	}
	command += " -- " + shellQuote(filepath.ToSlash(rel))
	return r.runCommand(ctx, command, emit)
}

func (r Registry) gitCreateBranch(ctx context.Context, call Call, emit func(string)) Result {
	name := strings.TrimSpace(argString(call, "name"))
	if !validGitRef(name) {
		return fail(call.Name, "invalid branch name")
	}
	return r.runCommand(ctx, "git switch -c "+shellQuote(name), emit)
}

func (r Registry) gitCheckout(ctx context.Context, call Call, emit func(string)) Result {
	name := strings.TrimSpace(argString(call, "name"))
	if !validGitRef(name) {
		return fail(call.Name, "invalid branch name")
	}
	return r.runCommand(ctx, "git switch "+shellQuote(name), emit)
}

func (r Registry) gitCommit(ctx context.Context, call Call, emit func(string)) Result {
	message := strings.TrimSpace(argString(call, "message"))
	if message == "" {
		return fail(call.Name, "message is required")
	}
	add := "git add --all"
	if raw := strings.TrimSpace(argString(call, "paths")); raw != "" {
		parts := strings.Fields(raw)
		quoted := make([]string, 0, len(parts))
		for _, part := range parts {
			resolved, err := r.safePath(part)
			if err != nil {
				return fail(call.Name, err.Error())
			}
			rel, _ := filepath.Rel(r.WorkspaceRoot, resolved)
			quoted = append(quoted, shellQuote(filepath.ToSlash(rel)))
		}
		if len(quoted) == 0 {
			return fail(call.Name, "paths did not contain any valid path")
		}
		add = "git add -- " + strings.Join(quoted, " ")
	}
	return r.runCommand(ctx, add+" && git commit -m "+shellQuote(message), emit)
}

func (r Registry) gitStash(ctx context.Context, call Call, emit func(string)) Result {
	action := strings.ToLower(strings.TrimSpace(argString(call, "action")))
	switch action {
	case "list":
		return r.runCommand(ctx, "git stash list", emit)
	case "pop":
		return r.runCommand(ctx, "git stash pop", emit)
	case "push":
		command := "git stash push -u"
		if message := strings.TrimSpace(argString(call, "message")); message != "" {
			command += " -m " + shellQuote(message)
		}
		return r.runCommand(ctx, command, emit)
	default:
		return fail(call.Name, "action must be push, pop, or list")
	}
}

func (r Registry) gitMerge(ctx context.Context, call Call, emit func(string)) Result {
	branch := strings.TrimSpace(argString(call, "branch"))
	if !validGitRef(branch) {
		return fail(call.Name, "invalid branch name")
	}
	return r.runCommand(ctx, "git merge --no-edit "+shellQuote(branch), emit)
}
