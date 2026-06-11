package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// animationTickMsg advances only the small glimmer segments painted over the
// otherwise stable panel outlines. Keeping the cadence modest gives the
// renderer time to coalesce writes on slower terminals.
type animationTickMsg struct{}

func animationTick() tea.Cmd {
	return tea.Tick(140*time.Millisecond, func(time.Time) tea.Msg {
		return animationTickMsg{}
	})
}
