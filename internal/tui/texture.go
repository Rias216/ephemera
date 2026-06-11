package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
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
	target := max(1, m.viewport.Height())
	value := m.viewport.View()
	lines := []string{}
	if value != "" {
		lines = strings.Split(value, "\n")
	}
	if len(lines) > target {
		lines = lines[:target]
	}
	for len(lines) < target {
		lines = append(lines, m.textureLine(max(1, m.viewport.Width()), len(lines), 17, m.styles.Panel))
	}
	return strings.Join(lines, "\n")
}

func (m Model) insetBlock(block string) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		left := 1
		right := max(0, m.width-left-lipgloss.Width(line))
		lines[i] = strings.Repeat(" ", left) + line + strings.Repeat(" ", right)
	}
	return strings.Join(lines, "\n")
}
