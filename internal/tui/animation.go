package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type animationTickMsg struct{}

func animationTick() tea.Cmd {
	return tea.Tick(160*time.Millisecond, func(time.Time) tea.Msg {
		return animationTickMsg{}
	})
}
