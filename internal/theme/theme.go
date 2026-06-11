// Package theme centralizes all terminal colors and component styles.
package theme

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

const (
	RosePrimary   = "#FF69B4"
	RoseSecondary = "#DB2777"
)

// Styles is a complete visual palette for the application.
type Styles struct {
	Primary        color.Color
	Secondary      color.Color
	AccentSoft     color.Color
	AccentBright   color.Color
	Text           color.Color
	Muted          color.Color
	Faint          color.Color
	Background     color.Color
	Panel          color.Color
	PanelRaised    color.Color
	Success        color.Color
	Warning        color.Color
	Banner         lipgloss.Style
	Subtitle       lipgloss.Style
	Meta           lipgloss.Style
	Viewport       lipgloss.Style
	Input          lipgloss.Style
	Prompt         lipgloss.Style
	Status         lipgloss.Style
	UserLabel      lipgloss.Style
	AssistantLabel lipgloss.Style
	NoticeLabel    lipgloss.Style
	Error          lipgloss.Style
}

// Names lists supported themes.
func Names() []string { return []string{"rose", "mono"} }

// New creates a theme. Unknown names fall back to rose.
func New(name string) Styles {
	if name == "mono" {
		return build(
			lipgloss.Color("#E5E7EB"),
			lipgloss.Color("#9CA3AF"),
			lipgloss.Color("#D1D5DB"),
			lipgloss.Color("#FFFFFF"),
			lipgloss.Color("#F9FAFB"),
			lipgloss.Color("#8B93A1"),
			lipgloss.Color("#525866"),
			lipgloss.Color("#09090B"),
			lipgloss.Color("#18181B"),
			lipgloss.Color("#222226"),
			lipgloss.Color("#D1FAE5"),
			lipgloss.Color("#FDE68A"),
		)
	}
	return build(
		lipgloss.Color(RosePrimary),
		lipgloss.Color(RoseSecondary),
		lipgloss.Color("#FF9ACB"),
		lipgloss.Color("#FFE3F0"),
		lipgloss.Color("#FCE7F3"),
		lipgloss.Color("#A88E9F"),
		lipgloss.Color("#705B69"),
		lipgloss.Color("#09070A"),
		lipgloss.Color("#21101D"),
		lipgloss.Color("#2B1425"),
		lipgloss.Color("#86EFAC"),
		lipgloss.Color("#FBCFE8"),
	)
}

// Hex converts a palette color to the canonical RGB form expected by Glamour
// and by Ephemera's gradient interpolator.
func Hex(value color.Color) string {
	if value == nil {
		return ""
	}
	rgba := color.NRGBAModel.Convert(value).(color.NRGBA)
	return fmt.Sprintf("#%02X%02X%02X", rgba.R, rgba.G, rgba.B)
}

func build(primary, secondary, accentSoft, accentBright, text, muted, faint, background, panel, panelRaised, success, warning color.Color) Styles {
	return Styles{
		Primary:      primary,
		Secondary:    secondary,
		AccentSoft:   accentSoft,
		AccentBright: accentBright,
		Text:         text,
		Muted:        muted,
		Faint:        faint,
		Background:   background,
		Panel:        panel,
		PanelRaised:  panelRaised,
		Success:      success,
		Warning:      warning,
		Banner:       lipgloss.NewStyle().Bold(true).Foreground(primary),
		Subtitle:     lipgloss.NewStyle().Foreground(muted).Italic(true),
		Meta: lipgloss.NewStyle().
			Foreground(muted).
			Background(panel).
			PaddingLeft(1),
		Viewport: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(secondary).
			Background(panel).
			Padding(0, 1),
		Input: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primary).
			Background(panelRaised).
			Padding(0, 1),
		Prompt:         lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panelRaised),
		Status:         lipgloss.NewStyle().Foreground(muted),
		UserLabel:      lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panel),
		AssistantLabel: lipgloss.NewStyle().Bold(true).Foreground(secondary).Background(panel),
		NoticeLabel:    lipgloss.NewStyle().Bold(true).Foreground(muted).Background(panel),
		Error:          lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB7185")),
	}
}
