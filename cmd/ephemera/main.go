package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	evalsuite "github.com/ephemera-ai/ephemera/internal/eval"
	"github.com/ephemera-ai/ephemera/internal/history"
	llmruntime "github.com/ephemera-ai/ephemera/internal/llm"
	appmetrics "github.com/ephemera-ai/ephemera/internal/metrics"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	workruntime "github.com/ephemera-ai/ephemera/internal/runtime"
	"github.com/ephemera-ai/ephemera/internal/tools"
	apptrace "github.com/ephemera-ai/ephemera/internal/trace"
	"github.com/ephemera-ai/ephemera/internal/tui"
)

var version = "dev"

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*values = append(*values, value)
	}
	return nil
}

func main() {
	defer func() {
		if recovered := recover(); recovered != nil {
			debuglog.Failure("cli", "panic", fmt.Sprint(recovered), nil)
			panic(recovered)
		}
	}()
	var toolPlugins stringListFlag
	var (
		sessionName = flag.String("session", "", "load or create a named session")
		workspace   = flag.String("workspace", "", "use this directory as the active workspace (defaults to the launch directory)")
		provider    = flag.String("provider", "", "activate a remembered provider route")
		model       = flag.String("model", "", "override model for the selected provider")
		mode        = flag.String("mode", "", "override mode: normal, deep-reason, concise, creative")
		agentEval   = flag.Bool("agent-eval", false, "run deterministic local agent capability eval and exit")
		gradeEval   = flag.String("grade-eval", "", "grade the current workspace using an eval task JSON file")
		llmEval     = flag.Bool("llm-eval", false, "execute --grade-eval with the configured real provider before grading")
		evalHistory = flag.Bool("eval-history", false, "display evaluation regression history")
		evalDiff    = flag.String("eval-diff", "", "compare two eval run ids: --eval-diff RUN1 RUN2 or RUN1,RUN2")
		metricsOn   = flag.Bool("metrics", false, "enable structured agent metrics export")
		traceRun    = flag.String("trace", "", "display a saved run trace by run id")
		traceFormat = flag.String("trace-format", "tree", "trace display format: tree or mermaid")
		initProject = flag.Bool("init-project", false, "write .ephemera/project.json from conservative project discovery")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Var(&toolPlugins, "tool", "load a subprocess tool plugin manifest (repeatable)")
	flag.Parse()

	for _, path := range toolPlugins {
		if err := tools.LoadPlugin(path); err != nil {
			fatal("load tool plugin", err)
		}
	}
	appmetrics.Default().SetEnabled(*metricsOn)

	if *showVersion {
		fmt.Println("ephemera", version)
		return
	}
	if *agentEval {
		report, err := agent.RunDeterministicEval(context.Background())
		if err != nil {
			fatal("agent eval", err)
		}
		fmt.Println(agent.FormatEvalReport(report))
		if report.Failed() > 0 {
			debuglog.Failure("cli", "agent eval failed", fmt.Sprintf("%d deterministic agent evaluations failed", report.Failed()), map[string]any{
				"failed": report.Failed(),
			})
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("load config", err)
	}
	if *provider != "" {
		value := strings.ToLower(strings.TrimSpace(*provider))
		if routeID, ok := cfg.FindConnection(value); ok {
			cfg.ActivateConnection(routeID)
		} else if config.ValidProvider(value) {
			cfg.Provider = value
		} else {
			fatal("provider", fmt.Errorf("route %q is not connected", *provider))
		}
	}
	if *model != "" {
		cfg.SetModel(*model)
	}
	if *mode != "" {
		parsed, parseErr := reasoning.Parse(*mode)
		if parseErr != nil {
			fatal("mode", parseErr)
		}
		cfg.Mode = parsed
	}
	if err := config.Save(cfg); err != nil {
		fatal("save config", err)
	}
	workspaceRoot, err := resolveWorkspaceRoot(*workspace)
	if err != nil {
		fatal("resolve workspace", err)
	}
	cfg.WorkspaceRoot = workspaceRoot
	if *evalHistory {
		history, err := evalsuite.LoadHistory(workspaceRoot)
		if err != nil {
			fatal("load eval history", err)
		}
		fmt.Println(evalsuite.FormatHistory(history))
		return
	}
	if strings.TrimSpace(*evalDiff) != "" {
		fromID, toID, err := parseEvalDiff(*evalDiff, flag.Args())
		if err != nil {
			fatal("eval diff", err)
		}
		history, err := evalsuite.LoadHistory(workspaceRoot)
		if err != nil {
			fatal("load eval history", err)
		}
		diff, err := evalsuite.DiffHistory(history, fromID, toID)
		if err != nil {
			fatal("eval diff", err)
		}
		fmt.Println(diff)
		return
	}
	if strings.TrimSpace(*traceRun) != "" {
		run, err := apptrace.Load(workspaceRoot, *traceRun)
		if err != nil {
			fatal("load trace", err)
		}
		if strings.EqualFold(strings.TrimSpace(*traceFormat), "mermaid") {
			fmt.Println(apptrace.RenderMermaid(run))
		} else {
			fmt.Println(apptrace.RenderTree(run))
		}
		return
	}
	if *initProject {
		manifest := workruntime.DiscoverProjectManifest(workspaceRoot, cfg.AutoTestCommand)
		if err := workruntime.WriteProjectManifest(workspaceRoot, manifest); err != nil {
			fatal("initialize project manifest", err)
		}
		fmt.Println("Wrote", workruntime.ManifestPath(workspaceRoot))
		fmt.Println(manifest.Summary())
		return
	}
	if strings.TrimSpace(*gradeEval) != "" {
		task, err := evalsuite.LoadTask(*gradeEval)
		if err != nil {
			fatal("load eval task", err)
		}
		if *llmEval {
			providerClient, err := llmruntime.New(cfg)
			if err != nil {
				fatal("create eval provider", err)
			}
			result, err := evalsuite.RunLLM(context.Background(), workspaceRoot, task, cfg, providerClient, 10*time.Minute)
			if err != nil {
				fatal("run real-LLM eval", err)
			}
			entry, err := evalsuite.AppendHistory(workspaceRoot, evalsuite.HistoryEntry{
				Task: task.Name, Provider: result.Provider, Model: result.Model, Passed: result.Passed,
				Duration: result.Latency, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens,
				TokenCost: result.TokenCost, ToolCalls: result.ToolCalls, ReasoningQuality: result.ReasoningQuality,
			})
			if err != nil {
				fatal("record eval history", err)
			}
			fmt.Printf("Run ID: %s\n%s\n", entry.ID, evalsuite.FormatLLMResult(result))
			if !result.Passed {
				os.Exit(1)
			}
			return
		}
		report := evalsuite.Grade(context.Background(), workspaceRoot, task, 5*time.Minute)
		entry, err := evalsuite.AppendHistory(workspaceRoot, evalsuite.HistoryEntry{Task: task.Name, Passed: report.Passed(), Duration: report.Duration})
		if err != nil {
			fatal("record eval history", err)
		}
		fmt.Printf("Run ID: %s\n%s\n", entry.ID, evalsuite.FormatReport(report))
		if !report.Passed() {
			debuglog.Failure("cli", "workspace eval failed", "workspace evaluation did not pass", map[string]any{
				"task":      *gradeEval,
				"workspace": workspaceRoot,
			})
			os.Exit(1)
		}
		return
	}

	store, err := history.NewStore()
	if err != nil {
		fatal("open session store", err)
	}

	program := tea.NewProgram(
		tui.New(cfg, store, *sessionName),
		tea.WithFPS(tui.AnimationFPS),
	)
	if _, err := program.Run(); err != nil {
		fatal("run TUI", err)
	}
}

func resolveWorkspaceRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		value = cwd
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %s", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

func parseEvalDiff(value string, positional []string) (string, string, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ':' })
	if len(parts) == 1 && len(positional) > 0 {
		parts = append(parts, positional[0])
	}
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("expected two run ids")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func fatal(action string, err error) {
	debuglog.Error("cli", action, err, nil)
	fmt.Fprintf(os.Stderr, "ephemera: %s: %v\n", action, err)
	fmt.Fprintf(os.Stderr, "debug log: %s\n", debuglog.Path())
	os.Exit(1)
}
