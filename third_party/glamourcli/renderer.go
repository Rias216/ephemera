// Package glamour is a focused, terminal-native compatibility renderer for
// Ephemera. It intentionally implements the tiny upstream API surface the app
// uses while rendering Markdown as CLI structures instead of a document page.
package glamour

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/glamour/ansi"
)

// Option configures a TermRenderer.
type Option func(*TermRenderer)

// TermRenderer renders the Markdown patterns commonly produced by LLMs into a
// compact terminal-native transcript.
type TermRenderer struct {
	width  int
	config ansi.StyleConfig
	colors rendererStyles
}

// WithStyles applies a style configuration.
func WithStyles(config ansi.StyleConfig) Option {
	return func(renderer *TermRenderer) { renderer.config = config }
}

// WithWordWrap sets the maximum visible line width.
func WithWordWrap(width int) Option {
	return func(renderer *TermRenderer) {
		if width > 0 {
			renderer.width = width
		}
	}
}

// NewTermRenderer constructs a CLI renderer. Its signature is intentionally
// compatible with the upstream Glamour constructor used by Ephemera.
func NewTermRenderer(options ...Option) (*TermRenderer, error) {
	renderer := &TermRenderer{width: 80}
	for _, option := range options {
		if option != nil {
			option(renderer)
		}
	}
	if renderer.width < 12 {
		renderer.width = 12
	}
	renderer.colors = newRendererStyles(renderer.config)
	return renderer, nil
}

// Render converts Markdown-like model output into terminal-native blocks.
func (renderer *TermRenderer) Render(source string) (string, error) {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	lines := strings.Split(source, "\n")

	var blocks []string
	var paragraph []string
	var listLines []string
	var quoteLines []string

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		text := strings.Join(paragraph, " ")
		blocks = append(blocks, strings.Join(renderer.wrapInline(text, "", "", renderer.colors.body), "\n"))
		paragraph = paragraph[:0]
	}
	flushList := func() {
		if len(listLines) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(listLines, "\n"))
		listLines = listLines[:0]
	}
	flushQuote := func() {
		if len(quoteLines) == 0 {
			return
		}
		text := strings.Join(quoteLines, " ")
		blocks = append(blocks, strings.Join(renderer.wrapInline(text, "│ ", "│ ", renderer.colors.muted), "\n"))
		quoteLines = quoteLines[:0]
	}
	flushAll := func() {
		flushParagraph()
		flushList()
		flushQuote()
	}

	for index := 0; index < len(lines); {
		line := lines[index]
		trimmed := strings.TrimSpace(line)

		if marker, language, ok := fenceStart(trimmed); ok {
			flushAll()
			index++
			var code []string
			for index < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[index]), marker) {
				code = append(code, lines[index])
				index++
			}
			if index < len(lines) {
				index++
			}
			blocks = append(blocks, renderer.renderCodeBlock(language, code))
			continue
		}

		if index+1 < len(lines) && looksLikeTableRow(line) && isTableSeparator(lines[index+1]) {
			flushAll()
			var rows [][]string
			rows = append(rows, splitTableRow(line))
			index += 2 // skip the Markdown separator row
			for index < len(lines) && looksLikeTableRow(lines[index]) && strings.TrimSpace(lines[index]) != "" {
				rows = append(rows, splitTableRow(lines[index]))
				index++
			}
			blocks = append(blocks, renderer.renderTable(rows))
			continue
		}

		if trimmed == "" {
			flushAll()
			index++
			continue
		}

		if level, title, ok := heading(trimmed); ok {
			flushAll()
			blocks = append(blocks, renderer.renderHeading(level, title))
			index++
			continue
		}

		if isHorizontalRule(trimmed) {
			flushAll()
			blocks = append(blocks, renderer.paint(renderer.colors.muted, strings.Repeat("─", renderer.width)))
			index++
			continue
		}

		if text, ok := quote(line); ok {
			flushParagraph()
			flushList()
			quoteLines = append(quoteLines, text)
			index++
			continue
		}

		if item, ok := parseListItem(line); ok {
			flushParagraph()
			flushQuote()
			firstPrefix := item.padding + item.marker + " "
			nextPrefix := strings.Repeat(" ", cellWidth(firstPrefix))
			listLines = append(listLines, renderer.wrapInline(item.text, firstPrefix, nextPrefix, renderer.colors.accent)...)
			index++
			continue
		}

		flushList()
		flushQuote()
		paragraph = append(paragraph, trimmed)
		index++
	}
	flushAll()

	return strings.TrimSpace(strings.Join(blocks, "\n\n")) + "\n", nil
}

type rendererStyles struct {
	body     textStyle
	muted    textStyle
	accent   textStyle
	heading  textStyle
	code     textStyle
	strong   textStyle
	emphasis textStyle
	link     textStyle
}

type textStyle struct {
	color     string
	bold      bool
	italic    bool
	underline bool
	strike    bool
}

func newRendererStyles(config ansi.StyleConfig) rendererStyles {
	body := primitiveStyle(config.Text, primitiveStyle(config.Paragraph.StylePrimitive, textStyle{color: "#F5F5F5"}))
	muted := primitiveStyle(config.BlockQuote.StylePrimitive, body)
	accent := primitiveStyle(config.LinkText, primitiveStyle(config.Heading.StylePrimitive, body))
	headingStyle := primitiveStyle(config.Heading.StylePrimitive, accent)
	headingStyle.bold = true
	code := primitiveStyle(config.Code.StylePrimitive, accent)
	strong := primitiveStyle(config.Strong, body)
	strong.bold = true
	emphasis := primitiveStyle(config.Emph, body)
	emphasis.italic = true
	link := primitiveStyle(config.LinkText, accent)
	link.underline = true
	return rendererStyles{
		body: body, muted: muted, accent: accent, heading: headingStyle,
		code: code, strong: strong, emphasis: emphasis, link: link,
	}
}

func primitiveStyle(primitive ansi.StylePrimitive, fallback textStyle) textStyle {
	style := fallback
	if primitive.Color != nil && strings.TrimSpace(*primitive.Color) != "" {
		style.color = strings.TrimSpace(*primitive.Color)
	}
	if primitive.Bold != nil {
		style.bold = *primitive.Bold
	}
	if primitive.Italic != nil {
		style.italic = *primitive.Italic
	}
	if primitive.Underline != nil {
		style.underline = *primitive.Underline
	}
	return style
}

func (renderer *TermRenderer) renderHeading(level int, source string) string {
	title := plainInline(source)
	if level == 1 {
		title = strings.ToUpper(title)
	}
	prefix := "├─ "
	if level == 1 {
		prefix = "╭─ "
	} else if level >= 3 {
		prefix = "└─ "
	}
	available := maxInt(1, renderer.width-cellWidth(prefix)-1)
	title = clipCells(title, available)
	line := prefix + title + " "
	line += strings.Repeat("─", maxInt(0, renderer.width-cellWidth(line)))
	return renderer.paint(renderer.colors.heading, line)
}

func (renderer *TermRenderer) renderCodeBlock(language string, lines []string) string {
	if len(lines) == 0 {
		lines = []string{""}
	}
	language = strings.TrimSpace(strings.Fields(language + " ")[0])
	label := " code "
	if language != "" {
		label = " " + strings.ToLower(language) + " "
	}
	label = clipCells(label, maxInt(1, renderer.width-4))
	top := "╭─" + label + strings.Repeat("─", maxInt(0, renderer.width-3-cellWidth(label))) + "╮"
	bottom := "╰" + strings.Repeat("─", maxInt(0, renderer.width-2)) + "╯"

	digits := len(strconv.Itoa(len(lines)))
	innerWidth := maxInt(1, renderer.width-2)
	gutterWidth := digits + 4
	contentWidth := maxInt(1, innerWidth-gutterWidth)

	out := []string{renderer.paint(renderer.colors.accent, top)}
	for number, line := range lines {
		line = strings.ReplaceAll(line, "\t", "    ")
		line = clipCells(line, contentWidth)
		gutter := fmt.Sprintf(" %*d │ ", digits, number+1)
		content := renderer.paint(renderer.colors.muted, gutter) + renderer.paint(renderer.colors.code, line)
		padding := strings.Repeat(" ", maxInt(0, innerWidth-cellWidth(gutter)-cellWidth(line)))
		out = append(out,
			renderer.paint(renderer.colors.accent, "│")+
				content+padding+
				renderer.paint(renderer.colors.accent, "│"),
		)
	}
	out = append(out, renderer.paint(renderer.colors.accent, bottom))
	return strings.Join(out, "\n")
}

func (renderer *TermRenderer) renderTable(rows [][]string) string {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return ""
	}
	columns := 0
	for _, row := range rows {
		columns = maxInt(columns, len(row))
	}
	if columns == 0 {
		return ""
	}

	contentBudget := renderer.width - (columns + 1) - (2 * columns)
	if contentBudget < columns {
		var fallback []string
		for rowIndex, row := range rows {
			prefix := "• "
			if rowIndex == 0 {
				prefix = "› "
			}
			fallback = append(fallback, renderer.wrapInline(strings.Join(row, " · "), prefix, "  ", renderer.colors.accent)...)
		}
		return strings.Join(fallback, "\n")
	}

	desired := make([]int, columns)
	for _, row := range rows {
		for column := 0; column < columns; column++ {
			cell := ""
			if column < len(row) {
				cell = plainInline(row[column])
			}
			desired[column] = maxInt(desired[column], cellWidth(cell))
		}
	}
	widths := make([]int, columns)
	for column := range widths {
		widths[column] = maxInt(1, minInt(desired[column], maxInt(3, contentBudget/columns)))
	}
	for sumInts(widths) > contentBudget {
		widest := indexOfWidest(widths)
		if widths[widest] <= 1 {
			break
		}
		widths[widest]--
	}
	for sumInts(widths) < contentBudget {
		grew := false
		for column := range widths {
			if widths[column] < desired[column] && sumInts(widths) < contentBudget {
				widths[column]++
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	border := func(left, middle, right string) string {
		parts := make([]string, columns)
		for column, width := range widths {
			parts[column] = strings.Repeat("─", width+2)
		}
		return renderer.paint(renderer.colors.muted, left+strings.Join(parts, middle)+right)
	}

	out := []string{border("┌", "┬", "┐")}
	for rowIndex, row := range rows {
		var line strings.Builder
		line.WriteString(renderer.paint(renderer.colors.muted, "│"))
		for column, width := range widths {
			cell := ""
			if column < len(row) {
				cell = clipCells(plainInline(row[column]), width)
			}
			style := renderer.colors.body
			if rowIndex == 0 {
				style = renderer.colors.strong
			}
			line.WriteString(" ")
			line.WriteString(renderer.paint(style, cell))
			line.WriteString(strings.Repeat(" ", maxInt(0, width-cellWidth(cell))+1))
			line.WriteString(renderer.paint(renderer.colors.muted, "│"))
		}
		out = append(out, line.String())
		if rowIndex == 0 && len(rows) > 1 {
			out = append(out, border("├", "┼", "┤"))
		}
	}
	out = append(out, border("└", "┴", "┘"))
	return strings.Join(out, "\n")
}

func (renderer *TermRenderer) wrapInline(source, firstPrefix, nextPrefix string, prefixStyle textStyle) []string {
	spans := renderer.parseInline(source, renderer.colors.body)
	return renderer.wrapSpans(spans, firstPrefix, nextPrefix, prefixStyle)
}

type inlineSpan struct {
	text  string
	style textStyle
}

type inlineToken struct {
	text  string
	style textStyle
	space bool
}

func (renderer *TermRenderer) parseInline(source string, base textStyle) []inlineSpan {
	var spans []inlineSpan
	appendText := func(text string, style textStyle) {
		if text == "" {
			return
		}
		if len(spans) > 0 && spans[len(spans)-1].style == style {
			spans[len(spans)-1].text += text
			return
		}
		spans = append(spans, inlineSpan{text: text, style: style})
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
				style := mergeStyle(base, renderer.colors.strong)
				for _, span := range renderer.parseInline(inner, style) {
					appendText(span.text, span.style)
				}
				index += 4 + end
				continue
			}
		}
		if strings.HasPrefix(source[index:], "~~") {
			if end := strings.Index(source[index+2:], "~~"); end >= 0 {
				style := base
				style.strike = true
				appendText(source[index+2:index+2+end], style)
				index += 4 + end
				continue
			}
		}
		if source[index] == '`' {
			if end := strings.IndexByte(source[index+1:], '`'); end >= 0 {
				appendText(source[index+1:index+1+end], mergeStyle(base, renderer.colors.code))
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
						url := source[labelEnd+2 : labelEnd+2+closeURL]
						appendText(label, mergeStyle(base, renderer.colors.link))
						if strings.TrimSpace(url) != "" && strings.TrimSpace(url) != strings.TrimSpace(label) {
							appendText(" <"+strings.TrimSpace(url)+">", renderer.colors.muted)
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
				inner := source[index+1 : index+1+end]
				style := mergeStyle(base, renderer.colors.emphasis)
				appendText(inner, style)
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

func mergeStyle(base, overlay textStyle) textStyle {
	merged := base
	if overlay.color != "" {
		merged.color = overlay.color
	}
	merged.bold = merged.bold || overlay.bold
	merged.italic = merged.italic || overlay.italic
	merged.underline = merged.underline || overlay.underline
	merged.strike = merged.strike || overlay.strike
	return merged
}

func (renderer *TermRenderer) wrapSpans(spans []inlineSpan, firstPrefix, nextPrefix string, prefixStyle textStyle) []string {
	var tokens []inlineToken
	for _, span := range spans {
		var buffer strings.Builder
		space := false
		flush := func() {
			if buffer.Len() == 0 {
				return
			}
			tokens = append(tokens, inlineToken{text: buffer.String(), style: span.style, space: space})
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
	if prefix == "" {
		prefix = ""
	}
	var lines []string
	var line strings.Builder
	line.WriteString(renderer.paint(prefixStyle, prefix))
	lineWidth := cellWidth(prefix)
	pendingSpace := false

	flushLine := func() {
		lines = append(lines, strings.TrimRight(line.String(), " "))
		line.Reset()
		line.WriteString(renderer.paint(prefixStyle, nextPrefix))
		lineWidth = cellWidth(nextPrefix)
		pendingSpace = false
	}

	for _, token := range tokens {
		if token.space {
			if lineWidth > cellWidth(prefix) && lineWidth > cellWidth(nextPrefix) {
				pendingSpace = true
			}
			continue
		}

		available := renderer.width - lineWidth
		spaceWidth := 0
		if pendingSpace && lineWidth > cellWidth(prefix) && lineWidth > cellWidth(nextPrefix) {
			spaceWidth = 1
		}
		wordWidth := cellWidth(token.text)
		if lineWidth > cellWidth(prefix) && lineWidth > cellWidth(nextPrefix) && spaceWidth+wordWidth > available {
			flushLine()
			available = renderer.width - lineWidth
			spaceWidth = 0
		}
		if pendingSpace && spaceWidth == 1 {
			line.WriteByte(' ')
			lineWidth++
		}
		pendingSpace = false

		remaining := token.text
		for remaining != "" {
			available = maxInt(1, renderer.width-lineWidth)
			piece, rest := takeCells(remaining, available)
			if piece == "" {
				flushLine()
				continue
			}
			line.WriteString(renderer.paint(token.style, piece))
			lineWidth += cellWidth(piece)
			remaining = rest
			if remaining != "" {
				flushLine()
			}
		}
	}
	if lineWidth > cellWidth(nextPrefix) || len(lines) == 0 {
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return lines
}

func (renderer *TermRenderer) paint(style textStyle, text string) string {
	if text == "" {
		return ""
	}
	return sgr(style) + text + sgr(renderer.colors.body)
}

func sgr(style textStyle) string {
	codes := []string{"22", "23", "24", "29"}
	if red, green, blue, ok := parseHex(style.color); ok {
		codes = append(codes, "38", "2", strconv.Itoa(red), strconv.Itoa(green), strconv.Itoa(blue))
	}
	if style.bold {
		codes = append(codes, "1")
	}
	if style.italic {
		codes = append(codes, "3")
	}
	if style.underline {
		codes = append(codes, "4")
	}
	if style.strike {
		codes = append(codes, "9")
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

func parseHex(value string) (int, int, int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 {
		return 0, 0, 0, false
	}
	parsed, err := strconv.ParseUint(value, 16, 24)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(parsed >> 16), int((parsed >> 8) & 0xff), int(parsed & 0xff), true
}

type listItem struct {
	padding string
	marker  string
	text    string
}

func parseListItem(line string) (listItem, bool) {
	leading := len(line) - len(strings.TrimLeft(line, " \t"))
	trimmed := strings.TrimSpace(line)
	padding := strings.Repeat("  ", minInt(3, leading/2))

	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, marker) {
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
			glyph := "•"
			if strings.HasPrefix(strings.ToLower(text), "[x] ") {
				glyph = "✓"
				text = strings.TrimSpace(text[4:])
			} else if strings.HasPrefix(text, "[ ] ") {
				glyph = "○"
				text = strings.TrimSpace(text[4:])
			}
			return listItem{padding: padding, marker: glyph, text: text}, true
		}
	}

	index := 0
	for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
		index++
	}
	if index > 0 && index+1 < len(trimmed) && (trimmed[index] == '.' || trimmed[index] == ')') && trimmed[index+1] == ' ' {
		return listItem{padding: padding, marker: trimmed[:index+1], text: strings.TrimSpace(trimmed[index+2:])}, true
	}
	return listItem{}, false
}

func fenceStart(trimmed string) (marker, language string, ok bool) {
	if strings.HasPrefix(trimmed, "```") {
		return "```", strings.TrimSpace(trimmed[3:]), true
	}
	if strings.HasPrefix(trimmed, "~~~") {
		return "~~~", strings.TrimSpace(trimmed[3:]), true
	}
	return "", "", false
}

func heading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(strings.TrimRight(line[level+1:], "# ")), true
}

func quote(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, ">") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), true
}

func isHorizontalRule(line string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
	if len(compact) < 3 {
		return false
	}
	character := compact[0]
	if character != '-' && character != '*' && character != '_' {
		return false
	}
	for index := 1; index < len(compact); index++ {
		if compact[index] != character {
			return false
		}
	}
	return true
}

func looksLikeTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Count(trimmed, "|") >= 1 && !strings.HasPrefix(trimmed, "```")
}

func isTableSeparator(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(strings.Trim(cell, ":"))
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func plainInline(source string) string {
	var out strings.Builder
	for index := 0; index < len(source); {
		if source[index] == '\\' && index+1 < len(source) {
			index++
			out.WriteByte(source[index])
			index++
			continue
		}
		if strings.HasPrefix(source[index:], "**") || strings.HasPrefix(source[index:], "__") || strings.HasPrefix(source[index:], "~~") {
			index += 2
			continue
		}
		if strings.ContainsRune("*_`", rune(source[index])) {
			index++
			continue
		}
		if source[index] == '[' {
			closeLabel := strings.IndexByte(source[index+1:], ']')
			if closeLabel >= 0 {
				labelEnd := index + 1 + closeLabel
				if labelEnd+1 < len(source) && source[labelEnd+1] == '(' {
					closeURL := strings.IndexByte(source[labelEnd+2:], ')')
					if closeURL >= 0 {
						out.WriteString(source[index+1 : labelEnd])
						index = labelEnd + closeURL + 3
						continue
					}
				}
			}
		}
		out.WriteByte(source[index])
		index++
	}
	return strings.TrimSpace(out.String())
}

func clipCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if cellWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	piece, _ := takeCells(value, width-1)
	return piece + "…"
}

func takeCells(value string, width int) (string, string) {
	if width <= 0 || value == "" {
		return "", value
	}
	used := 0
	index := 0
	for index < len(value) {
		character, size := utf8.DecodeRuneInString(value[index:])
		characterWidth := runeCellWidth(character)
		if used+characterWidth > width {
			break
		}
		used += characterWidth
		index += size
	}
	return value[:index], value[index:]
}

func cellWidth(value string) int {
	width := 0
	for _, character := range value {
		width += runeCellWidth(character)
	}
	return width
}

func runeCellWidth(character rune) int {
	switch {
	case character == 0:
		return 0
	case character < 32 || (character >= 0x7f && character < 0xa0):
		return 0
	case unicode.Is(unicode.Mn, character), unicode.Is(unicode.Me, character), unicode.Is(unicode.Cf, character):
		return 0
	case character >= 0x1100 && (character <= 0x115f ||
		character == 0x2329 || character == 0x232a ||
		(character >= 0x2e80 && character <= 0xa4cf && character != 0x303f) ||
		(character >= 0xac00 && character <= 0xd7a3) ||
		(character >= 0xf900 && character <= 0xfaff) ||
		(character >= 0xfe10 && character <= 0xfe19) ||
		(character >= 0xfe30 && character <= 0xfe6f) ||
		(character >= 0xff00 && character <= 0xff60) ||
		(character >= 0xffe0 && character <= 0xffe6) ||
		(character >= 0x1f300 && character <= 0x1faff) ||
		(character >= 0x20000 && character <= 0x3fffd)):
		return 2
	default:
		return 1
	}
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func indexOfWidest(values []int) int {
	index := 0
	for candidate := 1; candidate < len(values); candidate++ {
		if values[candidate] > values[index] {
			index = candidate
		}
	}
	return index
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
