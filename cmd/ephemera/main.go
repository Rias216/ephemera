package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tui"
)

var version = "dev"

func main() {
	var (
		sessionName = flag.String("session", "", "load or create a named session")
		provider    = flag.String("provider", "", "activate a remembered provider route")
		model       = flag.String("model", "", "override model for the selected provider")
		mode        = flag.String("mode", "", "override mode: normal, deep-reason, concise, creative")
		agentEval   = flag.Bool("agent-eval", false, "run deterministic local agent capability eval and exit")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

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

func fatal(action string, err error) {
	fmt.Fprintf(os.Stderr, "ephemera: %s: %v\n", action, err)
	os.Exit(1)
}
