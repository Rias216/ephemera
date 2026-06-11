package tui

import (
	"image/color"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/ephemera-ai/ephemera/internal/theme"
)

// cliRenderer renders the small Markdown subset commonly produced by models
// into fixed-width terminal rows. Every visible cell receives an explicit
// foreground and background, so embedded formatting can never reveal the
// terminal's default (usually black) background.
type cliRenderer struct {
	width  int
	styles theme.Styles
	body   cliStyle
	muted  cliStyle
	accent cliStyle
	strong cliStyle
	code   cliStyle
}

type cliStyle struct {
	foreground color.Color
	bold       bool
	italic     bool
	underline  bool
	strike     bool
}

type cliSpan struct {
	text  string
	style cliStyle
}

type cliLine []cliSpan

type cliToken struct {
	text  string
	style cliStyle
	space bool
}

func newCLIRenderer(styles theme.Styles, width int) cliRenderer {
	return cliRenderer{
		width:  max(12, width),
		styles: styles,
		body:   cliStyle{foreground: styles.Text},
		muted:  cliStyle{foreground: styles.Muted},
		accent: cliStyle{foreground: styles.Primary, bold: true},
		strong: cliStyle{foreground: styles.Text, bold: true},
		code:   cliStyle{foreground: styles.AccentSoft},
	}
}

// Render converts Markdown-like text to fully painted, viewport-safe rows.
func (r cliRenderer) Render(source string) string {
	rows := r.renderRows(source)
	painted := make([]string, 0, len(rows))
	for _, row := range rows {
		painted = append(painted, r.paintRow(row))
	}
	return strings.Join(painted, "\n")
}

func (r cliRenderer) renderRows(source string) []cliLine {
	source = sanitizeTerminalText(source)
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	lines := strings.Split(source, "\n")

	var rows []cliLine
	appendBlank := func() {
		if len(rows) == 0 || len(rows[len(rows)-1]) == 0 {
			return
		}
		rows = append(rows, cliLine{})
	}

	for index := 0; index < len(lines); {
		line := lines[index]
		trimmed := strings.TrimSpace(line)

		if marker, language, ok := cliFenceStart(trimmed); ok {
			var code []string
			index++
			for index < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[index]), marker) {
				code = append(code, lines[index])
				index++
			}
			if index < len(lines) {
				index++
			}
			rows = append(rows, r.renderCodeBlock(language, code)...)
			continue
		}

		if index+1 < len(lines) && cliLooksLikeTableRow(line) && cliIsTableSeparator(lines[index+1]) {
			var table [][]string
			table = append(table, cliSplitTableRow(line))
			index += 2
			for index < len(lines) && cliLooksLikeTableRow(lines[index]) && strings.TrimSpace(lines[index]) != "" {
				table = append(table, cliSplitTableRow(lines[index]))
				index++
			}
			rows = append(rows, r.renderTable(table)...)
			continue
		}

		if trimmed == "" {
			appendBlank()
			index++
			continue
		}

		if level, title, ok := cliHeading(trimmed); ok {
			rows = append(rows, r.renderHeading(level, title))
			index++
			continue
		}

		if cliHorizontalRule(trimmed) {
			rows = append(rows, cliLine{{text: strings.Repeat("─", r.width), style: r.muted}})
			index++
			continue
		}

		if text, ok := cliQuote(line); ok {
			rows = append(rows, r.wrapInline(text, "  │ ", "  │ ", r.muted, r.muted)...)
			index++
			continue
		}

		if item, ok := cliParseListItem(line); ok {
			prefix := "  " + item.padding + item.marker + " "
			continuation := strings.Repeat(" ", lipgloss.Width(prefix))
			rows = append(rows, r.wrapInline(item.text, prefix, continuation, r.accent, r.body)...)
			index++
			continue
		}

		// Preserve deliberate line breaks. This is especially important for tool
		// output and provider errors, where folding adjacent lines into a Markdown
		// paragraph destroys the original terminal structure.
		rows = append(rows, r.wrapInline(trimmed, "  ", "  ", r.body, r.body)...)
		index++
	}

	for len(rows) > 0 && len(rows[len(rows)-1]) == 0 {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return []cliLine{{}}
	}
	return rows
}

func (r cliRenderer) renderHeading(level int, source string) cliLine {
	title := cliPlainInline(source)
	if level == 1 {
		title = strings.ToUpper(title)
	}
	prefix := "  ├─ "
	if level == 1 {
		prefix = "  ╭─ "
	} else if level >= 3 {
		prefix = "  └─ "
	}
	available := max(1, r.width-lipgloss.Width(prefix)-1)
	title = cliClipCells(title, available)
	line := prefix + title + " "
	line += strings.Repeat("─", max(0, r.width-lipgloss.Width(line)))
	return cliLine{{text: line, style: r.accent}}
}

func (r cliRenderer) renderCodeBlock(language string, lines []string) []cliLine {
	if len(lines) == 0 {
		lines = []string{""}
	}
	language = strings.TrimSpace(strings.Fields(language + " ")[0])
	label := " code "
	if language != "" {
		label = " " + strings.ToLower(language) + " "
	}

	indent := "  "
	frameWidth := max(10, r.width-lipgloss.Width(indent))
	label = cliClipCells(label, max(1, frameWidth-4))
	top := "╭─" + label + strings.Repeat("─", max(0, frameWidth-3-lipgloss.Width(label))) + "╮"
	bottom := "╰" + strings.Repeat("─", max(0, frameWidth-2)) + "╯"

	digits := len(strconv.Itoa(len(lines)))
	innerWidth := frameWidth - 2
	gutterWidth := digits + 4
	contentWidth := max(1, innerWidth-gutterWidth)
	rows := []cliLine{{{text: indent + top, style: r.accent}}}

	for number, raw := range lines {
		raw = strings.ReplaceAll(raw, "\t", "    ")
		parts := cliWrapHard(raw, contentWidth)
		if len(parts) == 0 {
			parts = []string{""}
		}
		for partIndex, part := range parts {
			gutter := strings.Repeat(" ", gutterWidth)
			if partIndex == 0 {
				gutter = " " + strings.Repeat(" ", digits-len(strconv.Itoa(number+1))) + strconv.Itoa(number+1) + " │ "
			} else {
				gutter = " " + strings.Repeat(" ", digits) + " │ "
			}
			padding := strings.Repeat(" ", max(0, contentWidth-lipgloss.Width(part)))
			rows = append(rows, cliLine{
				{text: indent + "│", style: r.accent},
				{text: gutter, style: r.muted},
				{text: part + padding, style: r.code},
				{text: "│", style: r.accent},
			})
		}
	}
	rows = append(rows, cliLine{{text: indent + bottom, style: r.accent}})
	return rows
}

func (r cliRenderer) renderTable(rows [][]string) []cliLine {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil
	}
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	indent := "  "
	frameWidth := max(8, r.width-lipgloss.Width(indent))
	contentBudget := frameWidth - (columns + 1) - 2*columns
	if columns == 0 || contentBudget < columns {
		var fallback []cliLine
		for rowIndex, row := range rows {
			marker := "  • "
			if rowIndex == 0 {
				marker = "  › "
			}
			fallback = append(fallback, r.wrapInline(strings.Join(row, " · "), marker, "    ", r.accent, r.body)...)
		}
		return fallback
	}

	desired := make([]int, columns)
	for _, row := range rows {
		for column := 0; column < columns; column++ {
			cell := ""
			if column < len(row) {
				cell = cliPlainInline(row[column])
			}
			desired[column] = max(desired[column], lipgloss.Width(cell))
		}
	}
	widths := make([]int, columns)
	for column := range widths {
		widths[column] = max(1, min(desired[column], max(3, contentBudget/columns)))
	}
	for cliSum(widths) > contentBudget {
		widest := cliWidest(widths)
		if widths[widest] <= 1 {
			break
		}
		widths[widest]--
	}
	for cliSum(widths) < contentBudget {
		grew := false
		for column := range widths {
			if widths[column] < desired[column] && cliSum(widths) < contentBudget {
				widths[column]++
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	border := func(left, middle, right string) cliLine {
		parts := make([]string, columns)
		for column, width := range widths {
			parts[column] = strings.Repeat("─", width+2)
		}
		return cliLine{{text: indent + left + strings.Join(parts, middle) + right, style: r.muted}}
	}

	out := []cliLine{border("┌", "┬", "┐")}
	for rowIndex, row := range rows {
		line := cliLine{{text: indent + "│", style: r.muted}}
		for column, width := range widths {
			cell := ""
			if column < len(row) {
				cell = cliClipCells(cliPlainInline(row[column]), width)
			}
			style := r.body
			if rowIndex == 0 {
				style = r.strong
			}
			line = append(line,
				cliSpan{text: " ", style: r.body},
				cliSpan{text: cell, style: style},
				cliSpan{text: strings.Repeat(" ", max(0, width-lipgloss.Width(cell))+1), style: r.body},
				cliSpan{text: "│", style: r.muted},
			)
		}
		out = append(out, line)
		if rowIndex == 0 && len(rows) > 1 {
			out = append(out, border("├", "┼", "┤"))
		}
	}
	out = append(out, border("└", "┴", "┘"))
	return out
}

func (r cliRenderer) wrapInline(source, firstPrefix, nextPrefix string, prefixStyle, baseStyle cliStyle) []cliLine {
	spans := r.parseInline(source, baseStyle)
	return r.wrapSpans(spans, firstPrefix, nextPrefix, prefixStyle)
}

func (r cliRenderer) parseInline(source string, base cliStyle) []cliSpan {
	var spans []cliSpan
	appendText := func(text string, style cliStyle) {
		if text == "" {
			return
		}
		if len(spans) > 0 && cliStylesEqual(spans[len(spans)-1].style, style) {
			spans[len(spans)-1].text += text
			return
		}
		spans = append(spans, cliSpan{text: text, style: style})
	}

	for index := 0; index < len(source); {
		if source[index] == '\\' && index+1 < len(source) {
			_, size := utf8.DecodeRuneInString(source[index+1:])
			appendText(source[index+1:index+1+size], base)
			index += size + 1
			continue
		}
		if strings.HasPrefix(source[index:], "**") || strings.HasPrefix(source[index:], "__") {
			marker := source[index : index+2]
			if end := strings.Index(source[index+2:], marker); end >= 0 {
				inner := source[index+2 : index+2+end]
				style := cliMergeStyle(base, r.strong)
				for _, span := range r.parseInline(inner, style) {
					appendText(span.text, span.style)
				}
				index += end + 4
				continue
			}
		}
		if strings.HasPrefix(source[index:], "~~") {
			if end := strings.Index(source[index+2:], "~~"); end >= 0 {
				style := base
				style.strike = true
				appendText(source[index+2:index+2+end], style)
				index += end + 4
				continue
			}
		}
		if source[index] == '`' {
			if end := strings.IndexByte(source[index+1:], '`'); end >= 0 {
				appendText(source[index+1:index+1+end], cliMergeStyle(base, r.code))
				index += end + 2
				continue
			}
		}
		if source[index] == '[' {
			closeLabel := strings.IndexByte(source[index+1:], ']')
			if closeLabel >= 0 {
				labelEnd := index + 1 + closeLabel
				if labelEnd+1 < len(source) && source[labelEnd+1] == '(' {
					closeURL := strings.IndexByte(source[labelEnd+2:], ')')
					if closeURL >= 0 {
						label := source[index+1 : labelEnd]
						url := strings.TrimSpace(source[labelEnd+2 : labelEnd+2+closeURL])
						linkStyle := r.accent
						linkStyle.underline = true
						appendText(label, cliMergeStyle(base, linkStyle))
						if url != "" && url != strings.TrimSpace(label) {
							appendText(" <"+url+">", r.muted)
						}
						index = labelEnd + closeURL + 3
						continue
					}
				}
			}
		}
		if source[index] == '*' || source[index] == '_' {
			marker := source[index]
			if end := strings.IndexByte(source[index+1:], marker); end >= 0 {
				style := base
				style.italic = true
				appendText(source[index+1:index+1+end], style)
				index += end + 2
				continue
			}
		}

		next := index + 1
		for next < len(source) && !strings.ContainsRune("\\*_`[~", rune(source[next])) {
			next++
		}
		appendText(source[index:next], base)
		index = next
	}
	return spans
}

func (r cliRenderer) wrapSpans(spans []cliSpan, firstPrefix, nextPrefix string, prefixStyle cliStyle) []cliLine {
	var tokens []cliToken
	for _, span := range spans {
		var buffer strings.Builder
		space := false
		flush := func() {
			if buffer.Len() == 0 {
				return
			}
			tokens = append(tokens, cliToken{text: buffer.String(), style: span.style, space: space})
			buffer.Reset()
		}
		for _, character := range span.text {
			isSpace := unicode.IsSpace(character)
			if buffer.Len() > 0 && isSpace != space {
				flush()
			}
			space = isSpace
			buffer.WriteRune(character)
		}
		flush()
	}

	prefix := firstPrefix
	line := cliLine{{text: prefix, style: prefixStyle}}
	lineWidth := lipgloss.Width(prefix)
	contentStart := lineWidth
	pendingSpace := false
	var lines []cliLine

	flushLine := func() {
		lines = append(lines, line)
		line = cliLine{{text: nextPrefix, style: prefixStyle}}
		lineWidth = lipgloss.Width(nextPrefix)
		contentStart = lineWidth
		pendingSpace = false
	}

	for _, token := range tokens {
		if token.space {
			if lineWidth > contentStart {
				pendingSpace = true
			}
			continue
		}

		spaceWidth := 0
		if pendingSpace && lineWidth > contentStart {
			spaceWidth = 1
		}
		wordWidth := lipgloss.Width(token.text)
		if lineWidth > contentStart && lineWidth+spaceWidth+wordWidth > r.width {
			flushLine()
			spaceWidth = 0
		}
		if spaceWidth == 1 {
			line = append(line, cliSpan{text: " ", style: token.style})
			lineWidth++
		}
		pendingSpace = false

		remaining := token.text
		for remaining != "" {
			available := max(1, r.width-lineWidth)
			piece, rest := cliTakeCells(remaining, available)
			if piece == "" {
				flushLine()
				continue
			}
			line = append(line, cliSpan{text: piece, style: token.style})
			lineWidth += lipgloss.Width(piece)
			remaining = rest
			if remaining != "" {
				flushLine()
			}
		}
	}
	if lineWidth > contentStart || len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// paintRow is the renderer's critical invariant: every row is exactly width
// cells and every span, including trailing padding, explicitly sets Panel as
// its background. Terminal defaults and nested ANSI resets cannot leak through.
func (r cliRenderer) paintRow(line cliLine) string {
	var out strings.Builder
	used := 0
	for _, span := range line {
		if used >= r.width || span.text == "" {
			continue
		}
		piece := cliClipCells(span.text, r.width-used)
		if piece == "" {
			continue
		}
		out.WriteString(r.lipStyle(span.style).Render(piece))
		used += lipgloss.Width(piece)
	}
	if used < r.width {
		out.WriteString(r.lipStyle(r.body).Render(strings.Repeat(" ", r.width-used)))
	}
	return out.String()
}

func (r cliRenderer) lipStyle(style cliStyle) lipgloss.Style {
	foreground := style.foreground
	if foreground == nil {
		foreground = r.styles.Text
	}
	return lipgloss.NewStyle().
		Foreground(foreground).
		Background(r.styles.Panel).
		Bold(style.bold).
		Italic(style.italic).
		Underline(style.underline)
}

func (r cliRenderer) blankRow() string { return r.paintRow(nil) }

func sanitizeTerminalText(value string) string {
	var out strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' {
			index = ansiEscapeEnd(value, index)
			continue
		}
		character, size := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && size == 1 {
			index++
			continue
		}
		if character == '\t' || character == '\n' || character == '\r' || !unicode.IsControl(character) {
			out.WriteRune(character)
		}
		index += size
	}
	return out.String()
}

func cliMergeStyle(base, overlay cliStyle) cliStyle {
	merged := base
	if overlay.foreground != nil {
		merged.foreground = overlay.foreground
	}
	merged.bold = merged.bold || overlay.bold
	merged.italic = merged.italic || overlay.italic
	merged.underline = merged.underline || overlay.underline
	merged.strike = merged.strike || overlay.strike
	return merged
}

func cliStylesEqual(left, right cliStyle) bool {
	return theme.Hex(left.foreground) == theme.Hex(right.foreground) &&
		left.bold == right.bold && left.italic == right.italic &&
		left.underline == right.underline && left.strike == right.strike
}

type cliListItem struct {
	padding string
	marker  string
	text    string
}

func cliParseListItem(line string) (cliListItem, bool) {
	leading := len(line) - len(strings.TrimLeft(line, " \t"))
	trimmed := strings.TrimSpace(line)
	padding := strings.Repeat("  ", min(3, leading/2))
	for _, marker := range []string{"- [x] ", "- [X] ", "- [ ] ", "* [x] ", "* [X] ", "* [ ] "} {
		if strings.HasPrefix(trimmed, marker) {
			glyph := "○"
			if strings.Contains(strings.ToLower(marker), "x") {
				glyph = "✓"
			}
			return cliListItem{padding: padding, marker: glyph, text: strings.TrimSpace(trimmed[len(marker):])}, true
		}
	}
	if len(trimmed) >= 2 && (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ")) {
		return cliListItem{padding: padding, marker: "•", text: strings.TrimSpace(trimmed[2:])}, true
	}
	for index := 0; index < len(trimmed); index++ {
		if trimmed[index] < '0' || trimmed[index] > '9' {
			if index > 0 && index+1 < len(trimmed) && (trimmed[index] == '.' || trimmed[index] == ')') && trimmed[index+1] == ' ' {
				return cliListItem{padding: padding, marker: trimmed[:index+1], text: strings.TrimSpace(trimmed[index+2:])}, true
			}
			break
		}
	}
	return cliListItem{}, false
}

func cliFenceStart(line string) (marker, language string, ok bool) {
	for _, candidate := range []string{"```", "~~~"} {
		if strings.HasPrefix(line, candidate) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(line, candidate)), true
		}
	}
	return "", "", false
}

func cliHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level+1:]), true
}

func cliQuote(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, ">") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), true
}

func cliHorizontalRule(line string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
	return len(compact) >= 3 && (strings.Trim(compact, "-") == "" || strings.Trim(compact, "*") == "" || strings.Trim(compact, "_") == "")
}

func cliLooksLikeTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Contains(trimmed, "|") && trimmed != "|"
}

func cliIsTableSeparator(line string) bool {
	cells := cliSplitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		value := strings.Trim(strings.TrimSpace(cell), ":")
		if len(value) < 3 || strings.Trim(value, "-") != "" {
			return false
		}
	}
	return true
}

func cliSplitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func cliPlainInline(value string) string {
	for _, marker := range []string{"**", "__", "~~", "`", "*", "_"} {
		value = strings.ReplaceAll(value, marker, "")
	}
	return strings.TrimSpace(value)
}

func cliWrapHard(value string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	if value == "" {
		return []string{""}
	}
	var lines []string
	remaining := value
	for remaining != "" {
		piece, rest := cliTakeCells(remaining, width)
		if piece == "" {
			_, size := utf8.DecodeRuneInString(remaining)
			piece, rest = remaining[:size], remaining[size:]
		}
		lines = append(lines, piece)
		remaining = rest
	}
	return lines
}

func cliClipCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	piece, _ := cliTakeCells(value, width)
	return piece
}

func cliTakeCells(value string, width int) (string, string) {
	if width <= 0 || value == "" {
		return "", value
	}
	used := 0
	for index, character := range value {
		cellWidth := lipgloss.Width(string(character))
		if used+cellWidth > width {
			return value[:index], value[index:]
		}
		used += cellWidth
	}
	return value, ""
}

func cliSum(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func cliWidest(values []int) int {
	index := 0
	for candidate := 1; candidate < len(values); candidate++ {
		if values[candidate] > values[index] {
			index = candidate
		}
	}
	return index
}
