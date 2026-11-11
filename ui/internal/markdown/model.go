package markdown

// Doc is one parsed markdown document. It is immutable once returned by
// Parse; callers must not modify any slice reachable from it.
type Doc struct {
	// Blocks are the top-level blocks in source order.
	Blocks []Block
	// Truncated reports that a resource limit was hit and some content was
	// cut: an over-long source, nesting deeper than maxNestingDepth, or a
	// table beyond the column or cell caps.
	Truncated bool
}

// BlockKind names which member of Block carries data.
type BlockKind uint8

const (
	// BlockParagraph is inline spans forming one paragraph.
	BlockParagraph BlockKind = iota
	// BlockHeading is inline spans with a level from 1 (top) to 6.
	BlockHeading
	// BlockThematicBreak is a horizontal rule.
	BlockThematicBreak
	// BlockCode is a fenced or indented code block.
	BlockCode
	// BlockHTML is literal raw HTML shown as text, never interpreted.
	BlockHTML
	// BlockQuote is a quoted run of nested blocks.
	BlockQuote
	// BlockList is an ordered or unordered list.
	BlockList
	// BlockTable is a GFM table with per-column alignment.
	BlockTable
)

// SpanStyle is the set of inline styles. Bold and italic combine freely;
// code spans carry no other style.
type SpanStyle uint8

const (
	// StyleBold is **strong emphasis**.
	StyleBold SpanStyle = 1 << iota
	// StyleItalic is *emphasis*.
	StyleItalic
	// StyleStrike is GFM ~~strikethrough~~.
	StyleStrike
	// StyleCode is `inline code`.
	StyleCode
)

// Role is a syntax role attached to code spans (F10.3a). The values match
// the theme package's SyntaxRole strings on purpose; the type is defined
// here because the theme package imports a GUI toolkit and this package
// must not. F10.3b maps between the two.
type Role string

const (
	// RolePlain is unstyled text; also the fallback for unknown tokens.
	RolePlain Role = "plain"
	// RoleKeyword is keywords and their sub-categories.
	RoleKeyword Role = "keyword"
	// RoleOperator is operators and punctuation.
	RoleOperator Role = "operator"
	// RoleVariable is names, variables, attributes and properties.
	RoleVariable Role = "variable"
	// RoleFunction is function names and their sub-categories.
	RoleFunction Role = "function"
	// RoleType is type keywords, classes, namespaces and built-in
	// pseudo-names.
	RoleType Role = "type"
	// RoleDefinition is a name at its declaration site. No token type of
	// the lexer distinguishes declarations from uses, so Parse never emits
	// it; the role exists so the palette and the layout keep a place for
	// it.
	RoleDefinition Role = "definition"
	// RoleString is strings, escapes and regular expressions.
	RoleString Role = "string"
	// RoleNumber is numbers and the constant keywords true, false, nil,
	// null.
	RoleNumber Role = "number"
	// RoleComment is comments and their sub-categories.
	RoleComment Role = "comment"
	// RoleMeta is preprocessor lines, decorators and labels.
	RoleMeta Role = "meta"
	// RoleInvalid is error tokens.
	RoleInvalid Role = "invalid"
)

// Link is the sanitized destination of a link span. The popover of F10.3b
// shows both host forms; Openable is false for every scheme other than
// http, https and mailto, and for relative destinations.
type Link struct {
	// URL is the normalized destination.
	URL string
	// Host is the display form of the host: unicode for IDN, empty for
	// mailto and hostless URLs.
	Host string
	// HostPunycode is the ASCII punycode form of the host; identical to
	// Host when the host is already ASCII.
	HostPunycode string
	// Openable reports whether the URL may be opened by the caller.
	Openable bool
}

// Image is an image reference. Images are never fetched; the model keeps
// the alt text (in the span's Text) and the destination host only.
type Image struct {
	// Alt is the alternative text, sanitized like any other text.
	Alt string
	// URL is the sanitized destination.
	URL string
	// Host is the display form of the destination host.
	Host string
}

// Span is one run of inline text with one style, one role and at most one
// link or image attachment. Text is source text with tabs expanded to the
// 4-column tab stop and unsafe characters removed; whitespace around and
// between spans is exactly the source's (DA-E03).
type Span struct {
	// Text is the span's literal text.
	Text string
	// Style is the union of Style* flags.
	Style SpanStyle
	// Role is the syntax role; only code-block lines carry roles other
	// than RolePlain.
	Role Role
	// Link is set when the span sits inside a link.
	Link *Link
	// Image is set for an image reference; Text holds the alt text.
	Image *Image
	// HardBreak reports a hard line break after this span.
	HardBreak bool
}

// Block is one block-level node; Kind picks the member that carries data.
type Block struct {
	// Kind selects the populated member.
	Kind BlockKind
	// Level is the heading level, 1 to 6.
	Level int
	// Spans are the inline spans of a paragraph, heading or HTML block.
	Spans []Span
	// Language is the fenced code block's info string, lowercased.
	Language string
	// Highlighted reports whether the code block was tokenized; false
	// means every line is plain.
	Highlighted bool
	// Lines are the code block's lines, each a slice of role-tagged spans,
	// whitespace preserved.
	Lines [][]Span
	// Blocks are the nested blocks of a blockquote.
	Blocks []Block
	// List carries the items of a list block.
	List List
	// Table carries the rows of a table block.
	Table Table
}

// List is an ordered or unordered list, nested to any depth the limits
// allow.
type List struct {
	// Ordered reports a numbered list.
	Ordered bool
	// Start is the first item's number; 1 unless the source says otherwise.
	Start int
	// Items are the list items in order.
	Items []ListItem
}

// ListItem is one list item. Task-list items carry their checked state
// read-only; the renderer never mutates it.
type ListItem struct {
	// Task reports a GFM task-list item.
	Task bool
	// Checked is the task's checkbox state.
	Checked bool
	// Blocks are the item's blocks; a tight list item has one paragraph.
	Blocks []Block
}

// Table is a GFM table. Aligns is indexed by column; rows beyond the
// column cap or the cell cap were dropped and set Doc.Truncated.
type Table struct {
	// Aligns is the per-column alignment.
	Aligns []Align
	// Header is the header row; each cell holds inline spans.
	Header []Cell
	// Rows are the body rows.
	Rows [][]Cell
}

// Cell is one table cell's inline spans.
type Cell struct {
	// Spans are the cell's inline spans.
	Spans []Span
}

// Align is a table column's alignment.
type Align uint8

const (
	// AlignNone is the source's default alignment.
	AlignNone Align = iota
	// AlignLeft is `:--`.
	AlignLeft
	// AlignCenter is `:-:`.
	AlignCenter
	// AlignRight is `--:`.
	AlignRight
)
