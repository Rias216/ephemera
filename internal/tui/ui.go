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
	lipgloss.Color("#5A0A2F"), // black cherry
	lipgloss.Color("#74103D"), // wine rose
	lipgloss.Color("#92144D"), // deep rose
	lipgloss.Color("#B31A60"), // saturated rose
	lipgloss.Color("#D82178"), // vivid magenta-pink
	lipgloss.Color("#F23C91"), // hot pink
	lipgloss.Color("#FF69AE"), // luminous pink
	lipgloss.Color("#FF91C4"), // soft highlight
	lipgloss.Color("#FFC4DF"), // pale pink knife glint
}

var monoGlow = []color.Color{
	lipgloss.Color("#3F4652"),
	lipgloss.Color("#697180"),
	lipgloss.Color("#AAB0BA"),
	lipgloss.Color("#F9FAFB"),
}

const (
	glimmerTrailLength   = 15.0
	glimmerLeadLength    = 2.2
	ambientFadeHalfWidth = 15.0
)

func (m Model) renderHeader() string {
	left := m.renderLogoGlow()
	if m.width >= 58 {
		left += "  " + m.styles.Subtitle.Render("what vanishes may still illuminate")
	}

	right := m.renderActivityBadge()
	if m.width >= 78 {
		right += "  " + m.renderContextMeter(m.currentContextStats(), 12)
	}

	line := left
	if right != "" && lipgloss.Width(left)+lipgloss.Width(right)+2 <= m.width {
		line += strings.Repeat(" ", max(2, m.width-lipgloss.Width(left)-lipgloss.Width(right))) + right
	}

	return line + "\n" + m.renderMetaRail()
}

func (m Model) renderActivityBadge() string {
	label := "ready"
	base := m.styles.Muted
	if !m.focused {
		label = "paused"
		base = m.styles.Faint
	} else if m.connect != nil {
		label = "setup"
		base = m.styles.AccentSoft
	} else if m.busy {
		label = "thinking"
		base = m.styles.Primary
	}

	pulse := 0.34 + 0.66*(0.5+0.5*math.Sin(m.animationSeconds()*math.Pi*2.0*1.25))
	dot := fadeColor(m.styles.Faint, base, pulse)
	return lipgloss.NewStyle().Foreground(dot).Render("●") + " " +
		lipgloss.NewStyle().Foreground(m.styles.Muted).Render(label)
}

func (m Model) renderMetaRail() string {
	stats := m.currentContextStats()
	items := []string{
		m.renderChip("route", clip(m.providerName(), 16)),
		m.renderChip("mode", clip(string(m.cfg.Mode), 14)),
	}
	candidates := []string{
		m.renderChip("model", clip(m.cfg.Model(), 26)),
		m.renderChip("session", clip(m.session.Name, 22)),
	}
	if m.connect != nil {
		step, total := m.connectProgress()
		candidates = append(candidates, m.renderChip("setup", fmt.Sprintf("%d/%d %s", step, total, strings.ToLower(m.connectStepTitle()))))
	}
	if stats.DroppedMessages > 0 {
		candidates = append(candidates, lipgloss.NewStyle().Foreground(m.styles.Warning).Render(fmt.Sprintf("trimmed %d", stats.DroppedMessages)))
	}
	for _, candidate := range candidates {
		current := strings.Join(items, "  ·  ")
		if lipgloss.Width(current)+5+lipgloss.Width(candidate) > m.width {
			continue
		}
		items = append(items, candidate)
	}
	return strings.Join(items, "  ·  ")
}

func (m Model) renderChip(label, value string) string {
	labelStyle := lipgloss.NewStyle().Foreground(m.styles.Faint)
	valueStyle := lipgloss.NewStyle().Bold(true).Foreground(m.styles.Text)
	return labelStyle.Render(strings.ToUpper(label)) + " " + valueStyle.Render(value)
}

func (m Model) renderContextMeter(stats contextStats, cells int) string {
	if cells <= 0 {
		return ""
	}
	ratio := 0.0
	if stats.Budget > 0 {
		ratio = float64(stats.EstimatedTokens) / float64(stats.Budget)
	}
	ratio = math.Max(0, math.Min(1, ratio))
	filled := int(ratio*float64(cells) + 0.5)
	accent := m.styles.Primary
	if ratio >= 0.84 {
		accent = m.styles.Warning
	}

	var bar strings.Builder
	glint := int(positiveModFloat(m.animationSeconds()*contextGlintCellsPerSecond, float64(max(1, cells))))
	for i := 0; i < cells; i++ {
		if i < filled {
			c := accent
			if i == glint {
				c = fadeColor(accent, m.styles.AccentBright, 0.35)
			}
			bar.WriteString(lipgloss.NewStyle().Foreground(c).Render("━"))
		} else {
			bar.WriteString(lipgloss.NewStyle().Foreground(m.styles.Faint).Render("─"))
		}
	}
	return lipgloss.NewStyle().Foreground(m.styles.Muted).Render("ctx ") + bar.String() +
		lipgloss.NewStyle().Foreground(m.styles.Muted).Render(" "+formatTokenCount(stats.EstimatedTokens)+"/"+formatTokenCount(stats.Budget))
}

func (m Model) renderLogoGlow() string {
	const logo = "✦ EPHEMERA"
	palette := m.glowPalette()
	if len(palette) < 2 {
		return m.styles.Banner.Render(logo)
	}

	runes := []rune(logo)
	period := float64(len(runes))
	head := positiveModFloat(organicMotion(m.animationSeconds(), logoCellsPerSecond, 0.62, 0.9), period)
	breath := 0.5 + 0.5*math.Sin(m.animationSeconds()*math.Pi*2*0.18)
	var b strings.Builder
	for i, r := range runes {
		baseT := 0.16 + 0.13*(0.5+0.5*math.Sin(float64(i)*0.72-m.animationSeconds()*0.48))
		baseT += breath * 0.035
		delta := signedCircularDelta(float64(i), head, period)
		c := samplePalette(palette, baseT)
		switch {
		case delta >= 0 && delta <= 1.05:
			c = samplePalette(palette, 0.79-0.09*smootherStep(delta/1.05))
		case delta < 0 && -delta <= 4.6:
			remaining := 1.0 - (-delta / 4.6)
			c = samplePalette(palette, baseT+(0.74-baseT)*smootherStep(remaining))
		}
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Render(string(r)))
	}
	return b.String()
}

func (m Model) panelStyle(base lipgloss.Style, offset int) lipgloss.Style {
	return base.BorderForeground(m.glowColor(offset))
}

func (m Model) renderPanel(base lipgloss.Style, offset int, content string) string {
	background := base.GetBackground()
	if background == nil {
		background = m.styles.Panel
	}
	rendered := m.panelStyle(base, offset).Render(content)
	rendered = m.localizedGradientBorder(rendered, offset)
	rendered = reassertBackground(rendered, background)
	return m.paintBlockWidth(rendered, lipgloss.Width(rendered), background)
}

// localizedGradientBorder gives the complete outline a slowly shifting pink
// gradient, then layers a restrained rose wave and a soft glimmer on top. The
// motion is elapsed-time based, so dropped renderer frames never alter animation
// speed.
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
	// Keep all panel gradients in the same visual current. Small fixed offsets
	// prevent exact synchronization without making adjacent panels look as if
	// they belong to unrelated palettes.
	glimmerHead := positiveModFloat(
		organicMotion(seconds, glimmerCellsPerSecond, 1.15, 0.72)+float64(offset)*5.0,
		period,
	)
	ambientHead := positiveModFloat(
		organicMotion(seconds, ambientCellsPerSecond, 1.45, 0.27)+float64(offset)*7.0+period*0.31,
		period,
	)
	basePhase := positiveModFloat(seconds*baseGradientCyclesPS+float64(offset)*0.018, 1)

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
			c := knifeFadeColor(palette, position, glimmerHead, ambientHead, basePhase, period, offset)
			b.WriteString(ansiForeground(c))
		}
		b.WriteRune(r)
		x += lipgloss.Width(string(r))
		i += size
	}
	return b.String()
}

func (m Model) animatedPromptGlyph() string {
	palette := m.glowPalette()
	pulse := 0.40 + 0.30*(0.5+0.5*math.Sin(m.animationSeconds()*math.Pi*2*0.82))
	if m.busy {
		pulse = 0.55 + 0.23*(0.5+0.5*math.Sin(m.animationSeconds()*math.Pi*2*1.8))
	}
	return lipgloss.NewStyle().Bold(true).Foreground(samplePalette(palette, pulse)).Background(m.styles.Panel).Render("◆")
}

func (m Model) selectionGlow() color.Color {
	palette := m.glowPalette()
	pulse := 0.45 + 0.23*(0.5+0.5*math.Sin(m.animationSeconds()*math.Pi*2*1.15))
	return samplePalette(palette, pulse)
}

func organicMotion(seconds, cellsPerSecond, wobbleCells, wobbleHz float64) float64 {
	return seconds*cellsPerSecond + math.Sin(seconds*math.Pi*2*wobbleHz)*wobbleCells
}

func ansiEscapeEnd(value string, start int) int {
	if start < 0 || start >= len(value) || value[start] != '\x1b' {
		return min(len(value), start+1)
	}
	if start+1 >= len(value) {
		return len(value)
	}

	switch value[start+1] {
	case '[':
		for i := start + 2; i < len(value); i++ {
			if value[i] >= '@' && value[i] <= '~' {
				return i + 1
			}
		}
		return len(value)
	case ']':
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

	// Two slow waves create the ambient color drift. Their slightly different
	// frequencies keep the outline from feeling like a simple rotating stripe.
	waveA := 0.5 + 0.5*math.Sin(2*math.Pi*(position/perimeter-basePhase+float64(offset)*0.11))
	waveB := 0.5 + 0.5*math.Sin(4*math.Pi*(position/perimeter+basePhase*0.43+float64(offset)*0.037))
	baseT := 0.10 + 0.18*(waveA*0.74+waveB*0.26)
	c := samplePalette(palette, baseT)

	ambientDistance := math.Abs(signedCircularDelta(position, ambientHead, perimeter))
	if ambientDistance <= ambientFadeHalfWidth {
		strength := 1.0 - smootherStep(ambientDistance/ambientFadeHalfWidth)
		ambientT := baseT + (0.48-baseT)*strength
		c = samplePalette(palette, ambientT)
	}

	delta := signedCircularDelta(position, glimmerHead, perimeter)
	switch {
	case delta >= 0 && delta <= glimmerLeadLength:
		t := 0.78 - 0.10*smootherStep(delta/glimmerLeadLength)
		return samplePalette(palette, t)
	case delta < 0 && -delta <= glimmerTrailLength:
		remaining := 1.0 - (-delta / glimmerTrailLength)
		t := baseT + (0.72-baseT)*smootherStep(remaining)
		return samplePalette(palette, t)
	default:
		return c
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
	local := smootherStep(scaled - float64(index))
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
	return positiveModFloat(position-head+period/2, period) - period/2
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
	t = math.Max(0, math.Min(1, t))
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

func (m Model) renderComposerMeta() string {
	value := m.input.Value()
	label := string(m.cfg.Mode)
	if m.connect != nil {
		step, total := m.connectProgress()
		text := fmt.Sprintf("%d/%d %s  ·  %s", step, total, strings.ToLower(m.connectStepTitle()), strings.ToLower(m.connectRequirement()))
		if m.connect.Step == connectAPIKey && utf8.RuneCountInString(value) > 0 {
			text = fmt.Sprintf("%d/%d credentials  ·  secret entered", step, total)
		}
		return lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.Panel).Render(text)
	} else if strings.HasPrefix(value, "/") {
		label = "command"
	}
	count := utf8.RuneCountInString(value)
	text := fmt.Sprintf("%s  ·  %d", label, count)
	return lipgloss.NewStyle().Foreground(m.styles.Faint).Background(m.styles.Panel).Render(text)
}

func (m Model) renderFooter() string {
	available := max(8, m.width-6)
	lines := []string{
		m.footerTabsLine(available),
		m.footerPrimaryLine(available),
		m.footerSecondaryLine(available),
	}
	return strings.Join(lines, "\n")
}

func (m Model) statusPresentation() (color.Color, string) {
	if m.busy {
		return m.selectionGlow(), "◆"
	}
	if !m.focused {
		return m.styles.Faint, "○"
	}

	lower := strings.ToLower(m.status)
	switch {
	case strings.Contains(lower, "failed"), strings.Contains(lower, "broke"), strings.Contains(lower, "invalid"), strings.Contains(lower, "required"):
		return m.styles.Warning, "!"
	case strings.Contains(lower, "connected"), strings.Contains(lower, "saved"), strings.Contains(lower, "copied"), strings.Contains(lower, "exported"):
		return m.styles.AccentSoft, "✓"
	case m.connect != nil:
		return m.styles.AccentSoft, "◆"
	default:
		return m.styles.Muted, "◇"
	}
}

func (m Model) providerName() string {
	if m.cfg.Provider == "compatible" && strings.TrimSpace(m.cfg.CompatibleName) != "" {
		return m.cfg.CompatibleName
	}
	return m.cfg.Provider
}
