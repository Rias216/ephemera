// Package theme centralizes all terminal colors and component styles.
package theme

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

const (
	RosePrimary   = "#F43F9A"
	RoseSecondary = "#D61F72"
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
	PanelDeep      color.Color
	Texture        color.Color
	Divider        color.Color
	Success        color.Color
	Warning        color.Color
	Banner         lipgloss.Style
	Subtitle       lipgloss.Style
	Meta           lipgloss.Style
	Viewport       lipgloss.Style
	Input          lipgloss.Style
	Prompt         lipgloss.Style
	Status         lipgloss.Style
	Footer         lipgloss.Style
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
			lipgloss.Color("#101013"),
			lipgloss.Color("#34343B"),
			lipgloss.Color("#3F4652"),
			lipgloss.Color("#D1FAE5"),
			lipgloss.Color("#FDE68A"),
		)
	}
	return build(
		lipgloss.Color("#F43F9A"),
		lipgloss.Color("#D61F72"),
		lipgloss.Color("#FF82BC"),
		lipgloss.Color("#FFE1F0"),
		lipgloss.Color("#F9EAF2"),
		lipgloss.Color("#B58DA3"),
		lipgloss.Color("#765468"),
		lipgloss.Color("#08050A"),
		lipgloss.Color("#140A12"),
		lipgloss.Color("#1E0D19"),
		lipgloss.Color("#0E0710"),
		lipgloss.Color("#3A1830"),
		lipgloss.Color("#5B2948"),
		lipgloss.Color("#86EFAC"),
		lipgloss.Color("#F5A7C7"),
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

func build(primary, secondary, accentSoft, accentBright, text, muted, faint, background, panel, panelRaised, panelDeep, texture, divider, success, warning color.Color) Styles {
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
		PanelDeep:    panelDeep,
		Texture:      texture,
		Divider:      divider,
		Success:      success,
		Warning:      warning,
		Banner:       lipgloss.NewStyle().Bold(true).Foreground(primary).Background(background),
		Subtitle:     lipgloss.NewStyle().Foreground(muted).Background(background).Italic(true),
		Meta: lipgloss.NewStyle().
			Foreground(muted).
			Background(panel).
			PaddingLeft(1),
		Viewport: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(secondary).
			Foreground(text).
			Background(panel).
			Padding(0, 1),
		Input: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primary).
			Foreground(text).
			Background(panel).
			Padding(0, 1),
		Prompt: lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panel),
		Status: lipgloss.NewStyle().Foreground(muted).Background(panelDeep),
		Footer: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(divider).
			Foreground(text).
			Background(panelDeep).
			Padding(0, 1),
		UserLabel:      lipgloss.NewStyle().Bold(true).Foreground(primary).Background(panel),
		AssistantLabel: lipgloss.NewStyle().Bold(true).Foreground(secondary).Background(panel),
		NoticeLabel:    lipgloss.NewStyle().Bold(true).Foreground(muted).Background(panel),
		Error:          lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB7185")).Background(background),
	}
}
