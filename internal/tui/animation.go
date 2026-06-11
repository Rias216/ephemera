package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// Bubble Tea v2 coalesces model updates into renderer frames and writes only
	// changed cells. Sixty frames per second keeps the gradient fluid without
	// making animation speed depend on how many ticks the terminal can display.
	AnimationFPS = 60

	glimmerCellsPerSecond = 18.0
	ambientCellsPerSecond = 4.5
	logoCellsPerSecond    = 8.5
	baseGradientCyclesPS  = 0.055
)

type animationTickMsg struct {
	generation uint64
	at         time.Time
}

func animationTick(generation uint64) tea.Cmd {
	return tea.Tick(time.Second/AnimationFPS, func(now time.Time) tea.Msg {
		return animationTickMsg{generation: generation, at: now}
	})
}

func (m Model) animationSeconds() float64 {
	return m.animationElapsed.Seconds()
}
