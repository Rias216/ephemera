package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	"github.com/ephemera-ai/ephemera/internal/tui"
)

var version = "dev"

func main() {
	var (
		sessionName = flag.String("session", "", "load or create a named session")
		provider    = flag.String("provider", "", "override provider: ollama, openai, anthropic, compatible")
		model       = flag.String("model", "", "override model for the selected provider")
		mode        = flag.String("mode", "", "override mode: normal, deep-reason, concise, creative")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("ephemera", version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("load config", err)
	}
	if *provider != "" {
		value := strings.ToLower(strings.TrimSpace(*provider))
		if !config.ValidProvider(value) {
			fatal("provider", fmt.Errorf("unsupported provider %q", *provider))
		}
		cfg.Provider = value
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
