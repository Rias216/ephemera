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
	style := lipgloss.NewStyle().Foreground(m.styles.Texture).ColorWhitespace(true)
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

	fill := lipgloss.NewStyle().Foreground(m.styles.Text).Background(m.styles.Panel).ColorWhitespace(true)
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
	return m.paintBlockWidth(strings.Join(lines, "\n"), targetWidth, m.styles.Panel)
}

func (m Model) paintBlockWidth(block string, width int, background color.Color) string {
	if width <= 0 {
		return block
	}
	fill := lipgloss.NewStyle().Foreground(m.styles.Text).Background(background).ColorWhitespace(true)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		missing := width - lipgloss.Width(line)
		if missing > 0 {
			line += fill.Render(strings.Repeat(" ", missing))
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func reassertBackground(block string, background color.Color) string {
	if background == nil || block == "" {
		return block
	}
	prefix := "\x1b[48;2;" + rgbParams(background) + "m"
	var out strings.Builder
	out.Grow(len(block) + strings.Count(block, "\n")*len(prefix) + 32)
	out.WriteString(prefix)
	for index := 0; index < len(block); {
		if block[index] == '\n' {
			out.WriteByte('\n')
			out.WriteString(prefix)
			index++
			continue
		}
		if block[index] != '\x1b' {
			out.WriteByte(block[index])
			index++
			continue
		}
		end := ansiEscapeEnd(block, index)
		sequence := block[index:end]
		out.WriteString(sequence)
		if sgrResetsBackground(sequence) {
			out.WriteString(prefix)
		}
		index = end
	}
	return out.String()
}

func sgrResetsBackground(sequence string) bool {
	if len(sequence) < 3 || !strings.HasPrefix(sequence, "\x1b[") || sequence[len(sequence)-1] != 'm' {
		return false
	}
	params := sequence[2 : len(sequence)-1]
	if params == "" {
		return true
	}
	for _, part := range strings.Split(params, ";") {
		if part == "" || part == "0" || part == "49" {
			return true
		}
	}
	return false
}

func (m Model) insetBlock(block string) string {
	lines := strings.Split(block, "\n")
	fill := lipgloss.NewStyle().Background(m.styles.Background).ColorWhitespace(true)
	for i, line := range lines {
		left := 1
		right := max(0, m.width-left-lipgloss.Width(line))
		lines[i] = fill.Render(strings.Repeat(" ", left)) + line + fill.Render(strings.Repeat(" ", right))
	}
	return m.paintBlockWidth(strings.Join(lines, "\n"), m.width, m.styles.Background)
}
