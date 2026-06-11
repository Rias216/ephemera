package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

var roseGlow = []color.Color{
	lipgloss.Color("#8F164D"), // deep rose
	lipgloss.Color("#C21866"), // rich pink
	lipgloss.Color("#F02D8A"), // hot pink
	lipgloss.Color("#FF77B7"), // bright pink
	lipgloss.Color("#FFD1E6"), // pale pink glint
}

var monoGlow = []color.Color{
	lipgloss.Color("#4B5563"),
	lipgloss.Color("#9CA3AF"),
	lipgloss.Color("#F9FAFB"),
}

const (
	glimmerTrailLength   = 15.0
	glimmerLeadLength    = 2.25
	ambientFadeHalfWidth = 11.0
)

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
	period := float64(len(runes))
	head := positiveModFloat(m.animationSeconds()*logoCellsPerSecond, period)
	var b strings.Builder
	for i, r := range runes {
		delta := signedCircularDelta(float64(i), head, period)
		color := samplePalette(palette, 0.12)
		switch {
		case delta >= 0 && delta <= 0.9:
			t := 1.0 - 0.18*smootherStep(delta/0.9)
			color = samplePalette(palette, t)
		case delta < 0 && -delta <= 3.8:
			t := 1.0 - (-delta / 3.8)
			color = samplePalette(palette, smootherStep(t))
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

// localizedGradientBorder gives the complete outline a slowly shifting pink
// gradient, then layers a broad rose wave and a sharp pale-pink knife glimmer on
// top. Bubble Tea v2 diffs terminal cells and uses synchronized updates where
// supported, so this richer animation no longer depends on limiting changed rows.
func (m Model) localizedGradientBorder(rendered string, offset int) string {
	width := lipgloss.Width(rendered)
	height := lipgloss.Height(rendered)
	perimeter := borderPerimeter(width, height)
	if width < 2 || height < 2 || perimeter == 0 {
		return rendered
	}

	palette := m.glowPalette()
	if len(palette) < 2 {
		return rendered
	}

	period := float64(perimeter)
	seconds := m.animationSeconds()
	glimmerHead := positiveModFloat(
		seconds*glimmerCellsPerSecond+float64(offset)*period/7.0,
		period,
	)
	ambientHead := positiveModFloat(
		seconds*ambientCellsPerSecond+float64(offset)*period/5.0+period*0.31,
		period,
	)
	basePhase := positiveModFloat(seconds*baseGradientCyclesPS+float64(offset)*0.071, 1)

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
			position := float64(borderPosition(x, y, width, height))
			color := knifeFadeColor(palette, position, glimmerHead, ambientHead, basePhase, period, offset)
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

func (m Model) glowColor(offset int) color.Color {
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

func (m Model) glowPalette() []color.Color {
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

func knifeFadeColor(palette []color.Color, position, glimmerHead, ambientHead, basePhase, perimeter float64, offset int) color.Color {
	if perimeter <= 0 {
		return samplePalette(palette, 0)
	}

	// The resting outline is itself animated: a broad pink fade circulates slowly
	// around the entire perimeter underneath the quicker glimmer.
	baseWave := 0.5 + 0.5*math.Sin(2*math.Pi*(position/perimeter-basePhase+float64(offset)*0.11))
	baseT := 0.06 + 0.12*baseWave
	color := samplePalette(palette, baseT)

	ambientDistance := math.Abs(signedCircularDelta(position, ambientHead, perimeter))
	if ambientDistance <= ambientFadeHalfWidth {
		// This broad, low-contrast wave is the shifting color fade of the
		// outline itself. It never reaches the pale glint reserved for the knife
		// edge, so the two motions remain visually distinct.
		strength := 1.0 - smootherStep(ambientDistance/ambientFadeHalfWidth)
		ambientT := baseT + (0.58-baseT)*strength
		color = samplePalette(palette, ambientT)
	}

	delta := signedCircularDelta(position, glimmerHead, perimeter)
	switch {
	case delta >= 0 && delta <= glimmerLeadLength:
		// A short, bright leading bevel gives the glimmer a crisp knife edge.
		t := 1.0 - 0.22*smootherStep(delta/glimmerLeadLength)
		return samplePalette(palette, t)
	case delta < 0 && -delta <= glimmerTrailLength:
		// Behind the bevel, blend continuously through pink shades down into
		// the resting rose. This is a color fade, not an opacity fade.
		remaining := 1.0 - (-delta / glimmerTrailLength)
		t := baseT + (1.0-baseT)*smootherStep(remaining)
		return samplePalette(palette, t)
	default:
		return color
	}
}

func samplePalette(palette []color.Color, t float64) color.Color {
	if len(palette) == 0 {
		return lipgloss.Color("#FFFFFF")
	}
	if len(palette) == 1 || t <= 0 {
		return palette[0]
	}
	if t >= 1 {
		return palette[len(palette)-1]
	}

	scaled := t * float64(len(palette)-1)
	index := int(math.Floor(scaled))
	local := scaled - float64(index)
	return fadeColor(palette[index], palette[index+1], local)
}

func smootherStep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * t * (t*(t*6-15) + 10)
}

func signedCircularDelta(position, head, period float64) float64 {
	if period <= 0 {
		return 0
	}
	delta := positiveModFloat(position-head+period/2, period) - period/2
	return delta
}

func positiveModFloat(value, modulus float64) float64 {
	if modulus <= 0 {
		return 0
	}
	value = math.Mod(value, modulus)
	if value < 0 {
		value += modulus
	}
	return value
}

func ansiForeground(value color.Color) string {
	r, g, b := colorRGB(value)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

func fadeColor(from, to color.Color, t float64) color.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	fr, fg, fb := colorRGB(from)
	tr, tg, tb := colorRGB(to)
	return color.RGBA{
		R: uint8(lerpByte(fr, tr, t)),
		G: uint8(lerpByte(fg, tg, t)),
		B: uint8(lerpByte(fb, tb, t)),
		A: 0xFF,
	}
}

func colorRGB(value color.Color) (int, int, int) {
	if value == nil {
		return 255, 255, 255
	}
	rgba := color.NRGBAModel.Convert(value).(color.NRGBA)
	return int(rgba.R), int(rgba.G), int(rgba.B)
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
