package tools

import "context"

var builtinExecutorCatalog = map[string]Handler{
	"list_files": func(_ context.Context, r Registry, call Call, emit func(string)) Result {
		return r.listFilesStream(call, emit)
	},
	"tree": func(_ context.Context, r Registry, call Call, emit func(string)) Result {
		return r.treeStream(call, emit)
	},
	"read_file": func(_ context.Context, r Registry, call Call, emit func(string)) Result {
		return r.readFileStream(call, emit)
	},
	"search": func(_ context.Context, r Registry, call Call, emit func(string)) Result {
		return r.searchStream(call, emit)
	},
	"grep_regex": func(_ context.Context, r Registry, call Call, emit func(string)) Result {
		return r.grepRegexStream(call, emit)
	},
	"find_symbol":      func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.findSymbol(call) },
	"find_refs":        func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.findRefs(call) },
	"file_summary":     func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.fileSummary(call) },
	"dependency_graph": func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.dependencyGraph(call) },
	"detect_project_type": func(_ context.Context, r Registry, call Call, _ func(string)) Result {
		return r.detectProjectType(call)
	},
	"list_dependencies": func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.listDependencies(call) },
	"web_fetch":         func(ctx context.Context, r Registry, call Call, _ func(string)) Result { return r.webFetch(ctx, call) },
	"github_issue": func(ctx context.Context, r Registry, call Call, _ func(string)) Result {
		return r.githubIssue(ctx, call)
	},
	"github_pr": func(ctx context.Context, r Registry, call Call, _ func(string)) Result {
		return r.githubPullRequest(ctx, call)
	},
	"git_status": func(ctx context.Context, r Registry, _ Call, emit func(string)) Result {
		return r.runCommand(ctx, "git status --short", emit)
	},
	"git_diff": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		command := "git diff"
		if path := argString(call, "path"); path != "" {
			command += " -- " + shellQuote(path)
		}
		return r.runCommand(ctx, command, emit)
	},
	"git_log": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitLog(ctx, call, emit)
	},
	"git_blame": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitBlame(ctx, call, emit)
	},
	"git_create_branch": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitCreateBranch(ctx, call, emit)
	},
	"git_checkout": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitCheckout(ctx, call, emit)
	},
	"git_commit": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitCommit(ctx, call, emit)
	},
	"git_stash": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitStash(ctx, call, emit)
	},
	"git_merge": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.gitMerge(ctx, call, emit)
	},
	"apply_patch":       func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.applyPatch(call) },
	"apply_multi_patch": func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.applyMultiPatch(call) },
	"replace_in_file":   func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.replaceInFile(call) },
	"prefer":            func(_ context.Context, r Registry, call Call, _ func(string)) Result { return r.recordPreference(call) },
	"shell": func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		return r.runCommand(ctx, argString(call, "command"), emit)
	},
	"go_test": func(ctx context.Context, r Registry, _ Call, emit func(string)) Result {
		return r.runCommand(ctx, r.AutoTestCommand, emit)
	},
	"run_linter":     projectCommandExecutor("lint"),
	"run_formatter":  projectCommandExecutor("format"),
	"security_audit": projectCommandExecutor("audit"),
	"delegate": func(_ context.Context, _ Registry, call Call, _ func(string)) Result {
		return fail(call.Name, "delegate is executed by the agent orchestrator")
	},
}

func builtinExecutor(name string) Handler { return builtinExecutorCatalog[name] }

func projectCommandExecutor(kind string) Handler {
	return func(ctx context.Context, r Registry, call Call, emit func(string)) Result {
		command, err := r.projectCommand(kind)
		if err != nil {
			return fail(call.Name, err.Error())
		}
		return r.runCommand(ctx, command, emit)
	}
}
