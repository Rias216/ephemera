package tui

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

var roseGlow = []lipgloss.Color{
	lipgloss.Color("#FF69B4"),
	lipgloss.Color("#DDA0DD"),
}

var monoGlow = []lipgloss.Color{
	lipgloss.Color("#6B7280"),
	lipgloss.Color("#F9FAFB"),
}

const glimmerRadius = 3

func (m Model) renderHeader() string {
	banner := m.renderLogoGlow() + "  " +
		m.styles.Subtitle.Render("what vanishes may still illuminate")
	if m.width >= 72 {
		meter := m.styles.Status.Render(m.renderContextMeter(m.currentContextStats(), 10))
		if lipgloss.Width(banner)+lipgloss.Width(meter)+1 <= m.width {
			gap := max(1, m.width-lipgloss.Width(banner)-lipgloss.Width(meter)-1)
			banner += strings.Repeat(" ", gap) + meter
		}
	}
	return banner + "\n" + m.renderMetaRail()
}

func (m Model) renderMetaRail() string {
	stats := m.currentContextStats()
	items := []string{
		m.renderChip("provider", m.providerName()),
		m.renderChip("mode", string(m.cfg.Mode)),
		m.renderChip("ctx", fmt.Sprintf("%s/%s", formatTokenCount(stats.EstimatedTokens), formatTokenCount(stats.Budget))),
	}
	if m.width >= 76 {
		items = append(items, m.renderChip("model", clip(m.cfg.Model(), 28)))
	}
	if m.width >= 96 {
		items = append(items, m.renderChip("session", clip(m.session.Name, 24)))
	}
	if stats.DroppedMessages > 0 && m.width >= 64 {
		items = append(items, m.styles.Status.Render(fmt.Sprintf("trimmed %d", stats.DroppedMessages)))
	}
	return strings.Join(items, " ")
}

func (m Model) renderChip(label, value string) string {
	text := label + " " + value
	return lipgloss.NewStyle().
		Foreground(m.styles.Text).
		Background(m.styles.Panel).
		Padding(0, 1).
		Render(text)
}

func (m Model) renderContextMeter(stats contextStats, cells int) string {
	if cells <= 0 {
		return ""
	}
	ratio := 0.0
	if stats.Budget > 0 {
		ratio = float64(stats.EstimatedTokens) / float64(stats.Budget)
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(cells) + 0.5)
	if filled > cells {
		filled = cells
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", cells-filled)
	return fmt.Sprintf("ctx [%s] %s/%s", bar, formatTokenCount(stats.EstimatedTokens), formatTokenCount(stats.Budget))
}

func (m Model) renderLogoGlow() string {
	const logo = "✦ EPHEMERA"
	palette := m.glowPalette()
	if len(palette) < 2 {
		return m.styles.Banner.Render(logo)
	}

	runes := []rune(logo)
	position := positiveMod(m.frame, len(runes))
	var b strings.Builder
	for i, r := range runes {
		color := palette[0]
		distance := circularDistance(i, position, len(runes))
		if distance <= 1 {
			boost := 1.0 - float64(distance)/2.0
			color = brighten(fadeColor(palette[0], palette[1], 0.65), boost*0.75)
		}
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(color).Render(string(r)))
	}
	return b.String()
}

func (m Model) panelStyle(base lipgloss.Style, offset int) lipgloss.Style {
	return base.BorderForeground(m.glowColor(offset))
}

func (m Model) renderPanel(base lipgloss.Style, offset int, content string) string {
	rendered := m.panelStyle(base, offset).Render(content)
	return m.localizedGradientBorder(rendered, offset)
}

// localizedGradientBorder paints a stable two-tone outline and then brightens
// only a short segment that travels around its perimeter. Bubble Tea compares
// frames line-by-line, so at most a handful of rows differ on each tick instead
// of every row in a tall viewport.
func (m Model) localizedGradientBorder(rendered string, offset int) string {
	width := lipgloss.Width(rendered)
	height := lipgloss.Height(rendered)
	perimeter := borderPerimeter(width, height)
	if width < 2 || height < 2 || perimeter == 0 {
		return rendered
	}

	glimmer := positiveMod(m.frame*2+offset*max(1, perimeter/7), perimeter)
	palette := m.glowPalette()
	if len(palette) < 2 {
		return rendered
	}

	var b strings.Builder
	b.Grow(len(rendered) + perimeter*24)
	x, y := 0, 0
	for i := 0; i < len(rendered); {
		if rendered[i] == '\x1b' {
			end := ansiEscapeEnd(rendered, i)
			b.WriteString(rendered[i:end])
			i = end
			continue
		}

		r, size := utf8.DecodeRuneInString(rendered[i:])
		if r == '\n' {
			b.WriteRune(r)
			x = 0
			y++
			i += size
			continue
		}

		if isOuterBorderRune(r, x, y, width, height) {
			position := borderPosition(x, y, width, height)
			color := perimeterColor(palette[0], palette[1], position, perimeter, offset)
			distance := circularDistance(position, glimmer, perimeter)
			if distance <= glimmerRadius {
				boost := 1.0 - float64(distance)/float64(glimmerRadius+1)
				color = brighten(color, boost*boost*0.92)
			}
			b.WriteString(ansiForeground(color))
		}
		b.WriteRune(r)
		x += lipgloss.Width(string(r))
		i += size
	}
	return b.String()
}

func ansiEscapeEnd(value string, start int) int {
	if start < 0 || start >= len(value) || value[start] != '\x1b' {
		return min(len(value), start+1)
	}
	if start+1 >= len(value) {
		return len(value)
	}

	switch value[start+1] {
	case '[': // Control Sequence Introducer.
		for i := start + 2; i < len(value); i++ {
			if value[i] >= '@' && value[i] <= '~' {
				return i + 1
			}
		}
		return len(value)
	case ']': // Operating System Command, terminated by BEL or ST.
		for i := start + 2; i < len(value); i++ {
			if value[i] == '\a' {
				return i + 1
			}
			if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '\\' {
				return i + 2
			}
		}
		return len(value)
	default:
		return min(len(value), start+2)
	}
}

func (m Model) glowColor(offset int) lipgloss.Color {
	palette := m.glowPalette()
	if len(palette) == 0 {
		return lipgloss.Color("#FFFFFF")
	}
	index := offset % len(palette)
	if index < 0 {
		index += len(palette)
	}
	return palette[index]
}

func (m Model) glowPalette() []lipgloss.Color {
	if m.cfg.Theme == "mono" {
		return monoGlow
	}
	return roseGlow
}

func borderPerimeter(width, height int) int {
	if width < 2 || height < 2 {
		return 0
	}
	return 2*width + 2*height - 4
}

func borderPosition(x, y, width, height int) int {
	switch {
	case y == 0:
		return x
	case x == width-1:
		return width - 1 + y
	case y == height-1:
		return width - 1 + height - 1 + width - 1 - x
	default:
		return width - 1 + height - 1 + width - 1 + height - 1 - y
	}
}

func isOuterBorderRune(r rune, x, y, width, height int) bool {
	if y != 0 && y != height-1 && x != 0 && x != width-1 {
		return false
	}
	switch r {
	case '─', '│', '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘', '═', '║', '╔', '╗', '╚', '╝':
		return true
	default:
		return false
	}
}

func perimeterColor(from, to lipgloss.Color, position, perimeter, offset int) lipgloss.Color {
	if perimeter <= 0 {
		return from
	}
	phase := float64(position)/float64(perimeter) + float64(offset)*0.11
	t := 0.5 + 0.5*math.Sin(2*math.Pi*phase)
	return fadeColor(from, to, t)
}

func circularDistance(a, b, period int) int {
	if period <= 0 {
		return 0
	}
	distance := positiveMod(a-b, period)
	if distance > period/2 {
		distance = period - distance
	}
	return distance
}

func positiveMod(value, modulus int) int {
	if modulus <= 0 {
		return 0
	}
	value %= modulus
	if value < 0 {
		value += modulus
	}
	return value
}

func ansiForeground(color lipgloss.Color) string {
	r, g, b := hexRGB(string(color))
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

func brighten(color lipgloss.Color, amount float64) lipgloss.Color {
	if amount < 0 {
		amount = 0
	}
	if amount > 1 {
		amount = 1
	}
	r, g, b := hexRGB(string(color))
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X",
		lerpByte(r, 255, amount),
		lerpByte(g, 255, amount),
		lerpByte(b, 255, amount),
	))
}

func fadeColor(from, to lipgloss.Color, t float64) lipgloss.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	fr, fg, fb := hexRGB(string(from))
	tr, tg, tb := hexRGB(string(to))
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X",
		lerpByte(fr, tr, t),
		lerpByte(fg, tg, t),
		lerpByte(fb, tb, t),
	))
}

func hexRGB(hex string) (int, int, int) {
	if len(hex) != 7 || hex[0] != '#' {
		return 255, 255, 255
	}
	return hexPair(hex[1:3]), hexPair(hex[3:5]), hexPair(hex[5:7])
}

func hexPair(pair string) int {
	var value int
	for _, r := range pair {
		value *= 16
		switch {
		case r >= '0' && r <= '9':
			value += int(r - '0')
		case r >= 'a' && r <= 'f':
			value += int(r-'a') + 10
		case r >= 'A' && r <= 'F':
			value += int(r-'A') + 10
		}
	}
	return value
}

func lerpByte(from, to int, t float64) int {
	return from + int(float64(to-from)*t+0.5)
}

func (m Model) providerName() string {
	if m.cfg.Provider == "compatible" && strings.TrimSpace(m.cfg.CompatibleName) != "" {
		return m.cfg.CompatibleName
	}
	return m.cfg.Provider
}
