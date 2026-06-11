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
	targetHeight := max(1, m.viewport.Height())
	targetWidth := max(1, m.viewport.Width())
	value := m.viewport.View()
	lines := []string{}
	if value != "" {
		lines = strings.Split(value, "\n")
	}
	if len(lines) > targetHeight {
		lines = lines[:targetHeight]
	}

	// Viewport content can contain many independently styled spans. Never rely
	// on the terminal or the enclosing panel to paint the unused tail of a row:
	// explicitly fill it with Panel after the final ANSI reset.
	fill := lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel)
	for index, line := range lines {
		missing := targetWidth - lipgloss.Width(line)
		if missing > 0 {
			lines[index] = line + fill.Render(strings.Repeat(" ", missing))
		}
	}
	for len(lines) < targetHeight {
		lines = append(lines, m.textureLine(targetWidth, len(lines), 17, m.styles.Panel))
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
