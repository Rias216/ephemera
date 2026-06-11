package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// textureLine produces deterministic, sparse terminal texture. It is static,
// so Bubble Tea's renderer writes it once rather than repainting it every frame.
// The texture deliberately avoids long slash glyphs: those read like broken
// borders on small terminals and compete with the animated outline.
func (m Model) textureLine(width, row, seed int, background color.Color) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(width)
	for x := 0; x < width; x++ {
		n := positiveModInt(x*37+row*61+seed*97+x*row*3, 997)
		switch {
		case x > width*2/3 && positiveModInt(x-row+seed, 89) == 0:
			b.WriteRune('·')
		case n == 0 || n == 311:
			b.WriteRune('·')
		case n == 673 && x > width/2:
			b.WriteRune('∙')
		case row%11 == positiveModInt(seed, 11) && x%53 == positiveModInt(seed*5+11, 53):
			b.WriteRune('·')
		default:
			b.WriteByte(' ')
		}
	}
	style := lipgloss.NewStyle().Foreground(m.styles.Texture)
	if background != nil {
		style = style.Background(background)
	}
	return style.Render(b.String())
}

func (m Model) gapLine(seed int) string {
	return m.textureLine(max(1, m.width), seed, 41+seed*13, m.styles.Background)
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
		left := 2
		right := max(0, m.width-left-lipgloss.Width(line))
		lines[i] = strings.Repeat(" ", left) + line + strings.Repeat(" ", right)
	}
	return strings.Join(lines, "\n")
}

func positiveModInt(value, modulus int) int {
	if modulus <= 0 {
		return 0
	}
	value %= modulus
	if value < 0 {
		value += modulus
	}
	return value
}
