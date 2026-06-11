// Package theme centralizes all terminal colors and component styles.
package theme

import "github.com/charmbracelet/lipgloss"

const (
	RosePrimary   = "#FF69B4"
	RoseSecondary = "#DB2777"
)

// Styles is a complete visual palette for the application.
type Styles struct {
	Primary        lipgloss.Color
	Secondary      lipgloss.Color
	Text           lipgloss.Color
	Muted          lipgloss.Color
	Background     lipgloss.Color
	Panel          lipgloss.Color
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
		lipgloss.Color("#171118"),
	)
}

func build(primary, secondary, text, muted, background, panel lipgloss.Color) Styles {
	return Styles{
		Primary:    primary,
		Secondary:  secondary,
		Text:       text,
		Muted:      muted,
		Background: background,
		Panel:      panel,
		Banner: lipgloss.NewStyle().
			Bold(true).
			Foreground(primary).
			Background(background),
		Subtitle: lipgloss.NewStyle().Foreground(muted).Italic(true),
		Meta: lipgloss.NewStyle().
			Foreground(muted).
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
		Prompt:         lipgloss.NewStyle().Bold(true).Foreground(primary),
		Status:         lipgloss.NewStyle().Foreground(muted).Background(background),
		UserLabel:      lipgloss.NewStyle().Bold(true).Foreground(primary),
		AssistantLabel: lipgloss.NewStyle().Bold(true).Foreground(secondary),
		NoticeLabel:    lipgloss.NewStyle().Bold(true).Foreground(muted),
		Error:          lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB7185")),
	}
}
