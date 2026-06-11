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
	Text           color.Color
	Muted          color.Color
	Background     color.Color
	Panel          color.Color
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
			lipgloss.Color("#F9FAFB"),
			lipgloss.Color("#6B7280"),
			lipgloss.Color("#09090B"),
			lipgloss.Color("#18181B"),
		)
	}
	return build(
		lipgloss.Color(RosePrimary),
		lipgloss.Color(RoseSecondary),
		lipgloss.Color("#FCE7F3"),
		lipgloss.Color("#9D8B99"),
		lipgloss.Color("#09070A"),
		lipgloss.Color("#24111F"),
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

func build(primary, secondary, text, muted, background, panel color.Color) Styles {
	return Styles{
		Primary:    primary,
		Secondary:  secondary,
		Text:       text,
		Muted:      muted,
		Background: background,
		Panel:      panel,
		Banner:     lipgloss.NewStyle().Bold(true).Foreground(primary),
		Subtitle:   lipgloss.NewStyle().Foreground(muted).Italic(true),
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
			Background(panel).
			Padding(0, 1),
		Prompt:         lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panel),
		Status:         lipgloss.NewStyle().Foreground(muted),
		UserLabel:      lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panel),
		AssistantLabel: lipgloss.NewStyle().Bold(true).Foreground(secondary).Background(panel),
		NoticeLabel:    lipgloss.NewStyle().Bold(true).Foreground(muted).Background(panel),
		Error:          lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB7185")),
	}
}
