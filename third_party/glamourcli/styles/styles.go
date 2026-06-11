package styles

import "github.com/charmbracelet/glamour/ansi"

func ptr[T any](value T) *T { return &value }

var (
	white = "#F5F5F5"
	gray  = "#A3A3A3"
	cyan  = "#67E8F9"
)

// DarkStyleConfig provides sensible defaults and the same symbol used by the
// upstream package. Ephemera overwrites these values with its active theme.
var DarkStyleConfig = ansi.StyleConfig{
	Document:       ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &white}},
	Paragraph:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &white}},
	BlockQuote:     ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &gray}},
	Heading:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H1:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H2:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H3:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H4:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H5:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	H6:             ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan, Bold: ptr(true)}},
	Code:           ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &cyan}},
	CodeBlock:      ansi.CodeBlockStyle{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &white}}, Chroma: &ansi.ChromaStyle{}},
	List:           ansi.ContainerStyle{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &white}}},
	Table:          ansi.ContainerStyle{StyleBlock: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: &white}}},
	Text:           ansi.StylePrimitive{Color: &white},
	Item:           ansi.StylePrimitive{Color: &white},
	Enumeration:    ansi.StylePrimitive{Color: &gray},
	Strong:         ansi.StylePrimitive{Color: &white, Bold: ptr(true)},
	Emph:           ansi.StylePrimitive{Color: &white, Italic: ptr(true)},
	Link:           ansi.StylePrimitive{Color: &cyan, Underline: ptr(true)},
	LinkText:       ansi.StylePrimitive{Color: &cyan, Underline: ptr(true)},
	HorizontalRule: ansi.StylePrimitive{Color: &gray},
}
