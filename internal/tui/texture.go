package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// textureLine now provides a clean, stable fill. Earlier versions scattered
// decorative dots through every unused row; on real terminals those marks read
// as rendering debris and made the interface feel busier than it was.
func (m Model) textureLine(width, row, seed int, background color.Color) string {
	if width <= 0 {
		return ""
	}
	_ = row
	_ = seed
	style := lipgloss.NewStyle().Foreground(m.styles.Texture)
	if background != nil {
		style = style.Background(background)
	}
	return style.Render(strings.Repeat(" ", width))
}

func (m Model) texturedViewport() string {
	targetHeight := max(1, m.viewport.Height())
	targetWidth := max(1, m.viewport.Width())

	// Do not call viewport.View here. Bubbles pads short rows using an unstyled
	// internal Lip Gloss block; after nested ANSI resets that padding can inherit
	// the terminal's default black background. The transcript renderer already
	// produces wrapped rows, so select the visible content directly and paint its
	// final width ourselves.
	content := m.viewport.GetContent()
	allLines := []string{}
	if content != "" {
		allLines = strings.Split(content, "\n")
	}
	start := clampInt(m.viewport.YOffset(), 0, len(allLines))
	end := min(len(allLines), start+targetHeight)
	lines := append([]string(nil), allLines[start:end]...)

	fill := lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel)
	xOffset := m.viewport.XOffset()
	for index, line := range lines {
		if xOffset > 0 || lipgloss.Width(line) > targetWidth {
			line = ansi.Cut(line, xOffset, xOffset+targetWidth)
		}
		missing := targetWidth - lipgloss.Width(line)
		if missing > 0 {
			line += fill.Render(strings.Repeat(" ", missing))
		}
		lines[index] = line
	}
	for len(lines) < targetHeight {
		lines = append(lines, fill.Render(strings.Repeat(" ", targetWidth)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) insetBlock(block string) string {
	lines := strings.Split(block, "\n")
	fill := lipgloss.NewStyle().Background(m.styles.Background)
	for i, line := range lines {
		left := 1
		right := max(0, m.width-left-lipgloss.Width(line))
		lines[i] = fill.Render(strings.Repeat(" ", left)) + line + fill.Render(strings.Repeat(" ", right))
	}
	return strings.Join(lines, "\n")
}
