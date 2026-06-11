package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// Bubble Tea v2 coalesces updates into renderer frames and writes only cells
	// that changed. 60 FPS matches the refresh cadence of most terminals while
	// elapsed-time motion keeps the animation correct when frames are skipped.
	AnimationFPS = 60

	glimmerCellsPerSecond      = 19.5
	ambientCellsPerSecond      = 3.8
	logoCellsPerSecond         = 9.5
	contextGlintCellsPerSecond = 4.0
	baseGradientCyclesPS       = 0.028
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
