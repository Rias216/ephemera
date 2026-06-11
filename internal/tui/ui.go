package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var roseGlow = []lipgloss.Color{
	lipgloss.Color("#DB2777"),
	lipgloss.Color("#FF3FA4"),
	lipgloss.Color("#FF69B4"),
	lipgloss.Color("#FF9AD5"),
	lipgloss.Color("#FCE7F3"),
	lipgloss.Color("#FF69B4"),
}

var monoGlow = []lipgloss.Color{
	lipgloss.Color("#6B7280"),
	lipgloss.Color("#9CA3AF"),
	lipgloss.Color("#E5E7EB"),
	lipgloss.Color("#F9FAFB"),
	lipgloss.Color("#E5E7EB"),
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
	return base
}

func (m Model) glowColor(offset int) lipgloss.Color {
	palette := roseGlow
	if m.cfg.Theme == "mono" {
		palette = monoGlow
	}
	return palette[(m.frame+offset)%len(palette)]
}

func (m Model) providerName() string {
	if m.cfg.Provider == "compatible" && strings.TrimSpace(m.cfg.CompatibleName) != "" {
		return m.cfg.CompatibleName
	}
	return m.cfg.Provider
}
