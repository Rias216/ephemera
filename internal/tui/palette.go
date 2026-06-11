package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m Model) renderSuggestions() string {
	paletteHeight := m.effectivePaletteHeight()
	if !m.suggestionPaletteActive() || paletteHeight <= panelBorderRows {
		return ""
	}

	innerWidth := max(8, m.width-6)
	innerHeight := max(1, paletteHeight-panelBorderRows)
	lines := make([]string, 0, innerHeight)
	lines = append(lines, m.renderPaletteHeader(innerWidth))

	capacity := m.suggestionCapacity()
	items, start := m.suggestionWindow()
	listRows := capacity
	if m.connect != nil && len(items) == 0 && (m.connect.Step == connectAPIKey || m.connect.Step == connectReview) {
		// Credential and review steps have no choice list. Use the available
		// space for field guidance and the route preview instead.
		listRows = 0
	}
	if len(items) < listRows {
		listRows = max(1, len(items))
	}
	for i := 0; i < listRows; i++ {
		if i < len(items) {
			lines = append(lines, m.renderSuggestionRow(items[i], start+i, start+i == m.completionIndex, innerWidth))
			continue
		}
		if i == 0 && len(items) == 0 {
			message := m.emptySuggestionMessage()
			lines = append(lines, m.paletteTextLine("  "+message, innerWidth, m.styles.Muted, m.styles.Panel, false))
			continue
		}
		lines = append(lines, m.textureLine(innerWidth, i, 73, m.styles.Panel))
	}

	if len(lines) < innerHeight {
		lines = append(lines, m.paletteRule(innerWidth))
	}
	detailHeight := innerHeight - len(lines)
	if detailHeight > 0 {
		lines = append(lines, m.renderPaletteDetail(innerWidth, detailHeight)...)
	}
	for len(lines) < innerHeight {
		lines = append(lines, m.textureLine(innerWidth, len(lines), 91, m.styles.Panel))
	}
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}

	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.glowColor(2)).
		Background(m.styles.Panel).
		Padding(0, 1).
		Width(max(1, m.width-2)).
		Render(strings.Join(lines, "\n"))
	return m.localizedGradientBorder(rendered, 2)
}

func (m Model) emptySuggestionMessage() string {
	if m.connect == nil {
		return "No matching commands · Ctrl+L clears this field"
	}
	if m.connect.Step == connectModel {
		state, ok := m.cachedModelCatalogForConfig(m.connectModelListConfig())
		switch {
		case !ok:
			return "Loading the provider model catalog"
		case state.Err != nil:
			return "Catalog unavailable · check endpoint and credentials"
		case strings.TrimSpace(m.input.Value()) != "":
			return "No available model matches this text · Ctrl+L clears"
		default:
			return "Provider advertised no selectable models"
		}
	}
	return "No matching choices · Ctrl+L clears this field"
}

func (m Model) renderPaletteHeader(width int) string {
	title := "✣  COMMAND PALETTE"
	if m.connect != nil {
		step, total := m.connectProgress()
		title = fmt.Sprintf("✣  CONNECTION FLOW  ·  %02d/%02d %s", step, total, strings.ToUpper(m.connectStepTitle()))
	}
	counter := "0 / 0"
	if len(m.suggestions) > 0 {
		counter = fmt.Sprintf("%d / %d", minInt(m.completionIndex+1, len(m.suggestions)), len(m.suggestions))
		_, start := m.suggestionWindow()
		end := start + min(m.suggestionCapacity(), len(m.suggestions)-start)
		more := ""
		if start > 0 {
			more += "↑"
		}
		if end < len(m.suggestions) {
			more += "↓"
		}
		if more != "" {
			counter += "  " + more
		}
	}
	if m.connect != nil && len(m.suggestions) == 0 {
		counter = "FIELD"
		if m.connect.Step == connectReview {
			counter = "READY"
		}
	}
	left := lipgloss.NewStyle().Bold(true).Foreground(m.styles.Primary).Background(m.styles.Panel).Render(title)
	right := lipgloss.NewStyle().Foreground(m.styles.AccentSoft).Background(m.styles.Panel).Render(counter)
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + m.textureGap(gap, 3, m.styles.Panel) + right
}

func (m Model) renderSuggestionRow(item suggestion, index int, selected bool, width int) string {
	background := m.styles.Panel
	markerColor := m.styles.Faint
	labelColor := m.styles.Text
	marker := "  "
	if selected {
		markerColor = m.selectionGlow()
		labelColor = m.styles.AccentBright
		marker = "◆ "
	}

	category := m.suggestionCategory(item)
	badge := ""
	if category != "" && (width >= 70 || selected && width >= 34) {
		badge = " " + category + " "
	}
	markerView := lipgloss.NewStyle().Foreground(markerColor).Background(background).Render(marker)
	indexView := lipgloss.NewStyle().Foreground(m.styles.AccentSoft).Background(background).Render(fmt.Sprintf("%02d  ", index+1))
	labelBudget := max(1, width-lipgloss.Width(markerView)-lipgloss.Width(indexView)-len([]rune(badge))-1)
	label := clip(item.Label, labelBudget)
	labelView := lipgloss.NewStyle().Bold(selected).Foreground(labelColor).Background(background).Render(label)

	description := ""
	if item.Description != "" && width >= 58 {
		description = "   " + item.Description
	}
	if m.connect != nil && m.connect.Step == connectProvider {
		description = "   " + m.connectProfileSummary(item.Value)
	}
	used := lipgloss.Width(markerView) + lipgloss.Width(indexView) + lipgloss.Width(labelView) + len([]rune(badge))
	description = clip(description, max(0, width-used))
	descriptionView := lipgloss.NewStyle().Foreground(m.styles.Muted).Background(background).Render(description)
	gap := max(0, width-lipgloss.Width(markerView)-lipgloss.Width(indexView)-lipgloss.Width(labelView)-lipgloss.Width(descriptionView)-len([]rune(badge)))
	badgeView := ""
	if badge != "" {
		badgeColor := m.styles.Muted
		if selected {
			badgeColor = m.styles.AccentSoft
		}
		badgeView = lipgloss.NewStyle().Foreground(badgeColor).Background(background).Render(badge)
	}
	gapView := lipgloss.NewStyle().Background(background).Render(strings.Repeat(" ", gap))
	return markerView + indexView + labelView + descriptionView + gapView + badgeView
}

func (m Model) suggestionCategory(item suggestion) string {
	if m.connect != nil {
		if m.connect.Step == connectProvider || m.connect.Step == connectName {
			return connectProfileFor(item.Value).Badge
		}
		return strings.ToUpper(string(m.connect.Step))
	}
	if spec, ok := specForSuggestion(item); ok {
		return spec.Category
	}
	return ""
}

func specForSuggestion(item suggestion) (commandSpec, bool) {
	fields := strings.Fields(item.Value)
	if len(fields) == 0 {
		return commandSpec{}, false
	}
	if fields[0] == "/help" && len(fields) > 1 {
		target := fields[1]
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}
		return findCommandSpec(target)
	}
	return findCommandSpec(fields[0])
}

func (m Model) renderPaletteDetail(width, height int) []string {
	if height <= 0 {
		return nil
	}
	if m.connect != nil {
		return m.renderConnectDetail(width, height)
	}
	spec, item := m.paletteDetailSpec()
	if width >= 72 && height >= 4 {
		return m.renderTwoColumnDetail(spec, item, width, height)
	}
	return m.renderCompactDetail(spec, item, width, height)
}

func (m Model) paletteDetailSpec() (commandSpec, suggestion) {
	var selected suggestion
	if m.completionIndex >= 0 && m.completionIndex < len(m.suggestions) {
		selected = m.suggestions[m.completionIndex]
	}
	if spec, ok := specForSuggestion(selected); ok {
		return spec, selected
	}
	command, _, _ := splitCommandInput(m.input.Value())
	if spec, ok := findCommandSpec(command); ok {
		return spec, selected
	}
	if spec, ok := findCommandSpec("/help"); ok {
		return spec, selected
	}
	return commandSpec{Name: "/help", Description: "show contextual command help", Category: "CORE"}, selected
}

func (m Model) renderThreeColumnDetail(spec commandSpec, item suggestion, width, height int) []string {
	separatorWidth := 1
	usable := width - 2*separatorWidth
	leftWidth := max(22, usable*28/100)
	middleWidth := max(26, usable*34/100)
	rightWidth := max(20, usable-leftWidth-middleWidth)
	leftWidth += usable - leftWidth - middleWidth - rightWidth

	left := m.summaryColumn(spec, item, leftWidth, height)
	middle := m.usageColumn(spec, middleWidth, height)
	right := m.examplesColumn(spec, rightWidth, height)
	sep := lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render("│")
	out := make([]string, height)
	for i := 0; i < height; i++ {
		out[i] = left[i] + sep + middle[i] + sep + right[i]
	}
	return out
}

func (m Model) renderTwoColumnDetail(spec commandSpec, item suggestion, width, height int) []string {
	leftWidth := max(30, width*46/100)
	rightWidth := max(20, width-leftWidth-1)
	leftWidth += width - leftWidth - rightWidth - 1
	left := m.summaryColumn(spec, item, leftWidth, height)
	right := m.combinedUsageExamplesColumn(spec, rightWidth, height)
	sep := lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render("│")
	out := make([]string, height)
	for i := 0; i < height; i++ {
		out[i] = left[i] + sep + right[i]
	}
	return out
}

func (m Model) renderCompactDetail(spec commandSpec, item suggestion, width, height int) []string {
	lines := []string{
		m.paletteTextLine("› "+spec.Name+usageSuffix(spec.Usage), width, m.styles.AccentBright, m.styles.Panel, true),
	}
	description := spec.Description
	if item.Description != "" && item.Description != spec.Description {
		description += " · " + item.Description
	}
	for _, line := range wrapPlain(description, max(8, width-2), max(0, height-2)) {
		lines = append(lines, m.paletteTextLine("  "+line, width, m.styles.Muted, m.styles.Panel, false))
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  Enter run   Tab fill   ↑↓ select", width, m.styles.Faint, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 113)
}

func (m Model) summaryColumn(spec commandSpec, item suggestion, width, height int) []string {
	lines := []string{m.paletteTextLine("  "+spec.Name, width, m.styles.AccentBright, m.styles.Panel, true)}
	description := spec.Description
	if item.Description != "" && item.Description != spec.Description {
		description += " · " + item.Description
	}
	for _, line := range wrapPlain(description, max(8, width-2), 2) {
		lines = append(lines, m.paletteTextLine("  "+line, width, m.styles.Muted, m.styles.Panel, false))
	}
	lines = append(lines, m.paletteTextLine("", width, m.styles.Muted, m.styles.Panel, false))
	metadata := [][2]string{{"Category", fallback(spec.Category, "CORE")}}
	if len(spec.Aliases) > 0 {
		metadata = append(metadata, [2]string{"Aliases", strings.Join(spec.Aliases, ", ")})
	}
	for _, pair := range metadata {
		if len(lines) >= height {
			break
		}
		left := pair[0]
		gap := max(1, width-len([]rune(left))-len([]rune(pair[1]))-2)
		raw := "  " + left + strings.Repeat(" ", gap) + pair[1]
		lines = append(lines, m.paletteTextLine(raw, width, m.styles.Muted, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 127)
}

func (m Model) usageColumn(spec commandSpec, width, height int) []string {
	lines := []string{m.paletteTextLine("  USAGE", width, m.styles.Primary, m.styles.Panel, true)}
	usage := spec.Name + usageSuffix(spec.Usage)
	lines = append(lines, m.paletteTextLine("  › "+usage, width, m.styles.AccentSoft, m.styles.Panel, false))
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("", width, m.styles.Muted, m.styles.Panel, false))
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  ARGUMENTS", width, m.styles.Primary, m.styles.Panel, true))
	}
	arguments := usageArgumentRows(spec.Usage)
	if len(arguments) == 0 {
		arguments = [][2]string{{"—", "no arguments"}}
	}
	for _, argument := range arguments {
		if len(lines) >= height {
			break
		}
		name := argument[0]
		description := argument[1]
		raw := "  " + name + "  " + description
		lines = append(lines, m.paletteTextLine(raw, width, m.styles.Muted, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 139)
}

func (m Model) examplesColumn(spec commandSpec, width, height int) []string {
	lines := []string{m.paletteTextLine("  EXAMPLES", width, m.styles.Primary, m.styles.Panel, true)}
	for _, example := range spec.Examples {
		if len(lines) >= height-1 {
			break
		}
		raw := "  " + example.Input
		if width >= 28 && example.Description != "" {
			raw += "  ·  " + example.Description
		}
		lines = append(lines, m.paletteTextLine(raw, width, m.styles.AccentSoft, m.styles.Panel, false))
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  ◉ Tip: / reopens this palette", width, m.styles.Muted, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 151)
}

func (m Model) combinedUsageExamplesColumn(spec commandSpec, width, height int) []string {
	lines := []string{
		m.paletteTextLine("  USAGE", width, m.styles.Primary, m.styles.Panel, true),
		m.paletteTextLine("  › "+spec.Name+usageSuffix(spec.Usage), width, m.styles.AccentSoft, m.styles.Panel, false),
	}
	if len(lines) < height {
		lines = append(lines, m.paletteTextLine("  EXAMPLES", width, m.styles.Primary, m.styles.Panel, true))
	}
	for _, example := range spec.Examples {
		if len(lines) >= height {
			break
		}
		lines = append(lines, m.paletteTextLine("  "+example.Input+"  ·  "+example.Description, width, m.styles.Muted, m.styles.Panel, false))
	}
	return m.fitDetailLines(lines, width, height, 163)
}

func (m Model) fitDetailLines(lines []string, width, height, seed int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, m.textureLine(width, len(lines), seed, m.styles.Panel))
	}
	return lines
}

func (m Model) paletteRule(width int) string {
	return lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render(strings.Repeat("─", max(0, width)))
}

func (m Model) smallRule(width int) string {
	return lipgloss.NewStyle().Foreground(m.styles.Divider).Background(m.styles.Panel).Render("  " + strings.Repeat("─", max(0, width-2)))
}

func (m Model) paletteTextLine(raw string, width int, foreground, background color.Color, bold bool) string {
	raw = padPlain(raw, width)
	return lipgloss.NewStyle().Bold(bold).Foreground(foreground).Background(background).Render(raw)
}

func (m Model) textureGap(width, row int, background color.Color) string {
	if width <= 0 {
		return ""
	}
	return m.textureLine(width, row, 181, background)
}

func padPlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = clip(value, width)
	used := lipgloss.Width(value)
	if used < width {
		value += strings.Repeat(" ", width-used)
	}
	return value
}

func wrapPlain(value string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, maxLines)
	current := ""
	for _, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if len([]rune(candidate)) <= width {
			current = candidate
			continue
		}
		if current != "" {
			lines = append(lines, current)
			if len(lines) >= maxLines {
				return lines
			}
		}
		current = clip(word, width)
	}
	if current != "" && len(lines) < maxLines {
		lines = append(lines, current)
	}
	return lines
}

func usageArgumentRows(usage string) [][2]string {
	fields := strings.Fields(usage)
	rows := make([][2]string, 0, len(fields))
	for _, field := range fields {
		name := strings.Trim(field, "[]<>")
		if name == "" {
			continue
		}
		kind := "optional"
		if strings.HasPrefix(field, "<") {
			kind = "required"
		}
		rows = append(rows, [2]string{name, kind})
	}
	return rows
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}

func commandHelpNotice(spec commandSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n%s\n\n", spec.Name, spec.Description)
	fmt.Fprintf(&b, "**Usage:** `%s%s`\n\n", spec.Name, usageSuffix(spec.Usage))
	if len(spec.Examples) > 0 {
		b.WriteString("**Examples:**\n\n")
		for _, example := range spec.Examples {
			fmt.Fprintf(&b, "- `%s` — %s\n", example.Input, example.Description)
		}
	}
	return strings.TrimSpace(b.String())
}
