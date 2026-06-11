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

func (m Model) renderHeader() string {
	banner := m.styles.Banner.Render("✦ EPHEMERA") + "  " +
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
	return m.styles.Banner.Render("✦ EPHEMERA")
}

func (m Model) panelStyle(base lipgloss.Style, offset int) lipgloss.Style {
	return base.BorderForeground(m.glowColor(offset))
}

func (m Model) renderPanel(base lipgloss.Style, offset int, content string) string {
	return m.gradientBorder(m.panelStyle(base, offset).Render(content), offset)
}

func (m Model) gradientBorder(rendered string, offset int) string {
	var b strings.Builder
	x, y := 0, 0
	for i := 0; i < len(rendered); {
		if rendered[i] == '\x1b' && i+1 < len(rendered) && rendered[i+1] == '[' {
			end := i + 2
			for end < len(rendered) && (rendered[end] < '@' || rendered[end] > '~') {
				end++
			}
			if end >= len(rendered) {
				b.WriteString(rendered[i:])
				break
			}
			b.WriteString(rendered[i : end+1])
			i = end + 1
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
		if isBorderRune(r) {
			color := m.gradientColorAt(x, y, offset)
			b.WriteString(lipgloss.NewStyle().Foreground(color).Render(string(r)))
		} else {
			b.WriteRune(r)
		}
		x += lipgloss.Width(string(r))
		i += size
	}
	return b.String()
}

func (m Model) gradientColorAt(x, y, offset int) lipgloss.Color {
	palette := m.glowPalette()
	t := 0.5 + 0.5*math.Sin(2*math.Pi*float64(m.frame+x+y)/90)
	r, g, b := hexRGB(string(fadeColor(palette[0], palette[1], t)))

	glow := float64(m.frame*2 % 180)
	d := math.Abs(float64(x+y) - glow)
	if d > 90 {
		d = 180 - d
	}
	if d < 20 {
		boost := 1.0 - d/20.0
		boost *= boost * boost
		r = lerpByte(r, 255, boost*0.8)
		g = lerpByte(g, 255, boost*0.8)
		b = lerpByte(b, 255, boost*0.8)
	}

	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", r, g, b))
}

func (m Model) glowColor(offset int) lipgloss.Color {
	palette := m.glowPalette()
	t := 0.5 + 0.5*math.Sin(2*math.Pi*float64(m.frame+offset)/90)
	return fadeColor(palette[0], palette[1], t)
}

func (m Model) glowPalette() []lipgloss.Color {
	if m.cfg.Theme == "mono" {
		return monoGlow
	}
	return roseGlow
}

func isBorderRune(r rune) bool {
	switch r {
	case '─', '│', '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘', '═', '║', '╔', '╗', '╚', '╝':
		return true
	default:
		return false
	}
}

func fadeColor(from, to lipgloss.Color, t float64) lipgloss.Color {
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
