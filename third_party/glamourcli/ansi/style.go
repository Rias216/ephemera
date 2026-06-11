package ansi

// StylePrimitive is the subset of Glamour's style primitive used by Ephemera.
// Pointer fields preserve the same configuration semantics as the upstream API.
type StylePrimitive struct {
	Color           *string
	BackgroundColor *string
	Bold            *bool
	Italic          *bool
	Underline       *bool
}

// StyleBlock embeds the primitive used to style block-level nodes.
type StyleBlock struct {
	StylePrimitive
}

// ContainerStyle mirrors upstream block containers such as lists and tables.
type ContainerStyle struct {
	StyleBlock
}

// ChromaStyle keeps the fields Ephemera configures for fenced code blocks.
type ChromaStyle struct {
	Text       StylePrimitive
	Background StylePrimitive
}

// CodeBlockStyle describes a fenced code block.
type CodeBlockStyle struct {
	StyleBlock
	Chroma *ChromaStyle
}

// StyleConfig intentionally mirrors only the public surface consumed by
// Ephemera. It keeps the renderer drop-in compatible without carrying the
// document-oriented rendering machinery that made the TUI feel like a pager.
type StyleConfig struct {
	Document       StyleBlock
	Paragraph      StyleBlock
	BlockQuote     StyleBlock
	Heading        StyleBlock
	H1             StyleBlock
	H2             StyleBlock
	H3             StyleBlock
	H4             StyleBlock
	H5             StyleBlock
	H6             StyleBlock
	Code           StyleBlock
	CodeBlock      CodeBlockStyle
	List           ContainerStyle
	Table          ContainerStyle
	Text           StylePrimitive
	Item           StylePrimitive
	Enumeration    StylePrimitive
	Strong         StylePrimitive
	Emph           StylePrimitive
	Link           StylePrimitive
	LinkText       StylePrimitive
	HorizontalRule StylePrimitive
}
