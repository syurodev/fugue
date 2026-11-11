package markdown

import (
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/yuin/goldmark/ast"
)

// Clock reads the current time; Parse uses it only for the highlight
// budget. It is injected (WithClock) so tests drive the budget without
// real time and the caller owns the production clock; the package reads
// no clock of its own.
type Clock interface {
	// Now returns the current reading.
	Now() time.Time
}

// Option configures Parse.
type Option func(*parseState)

// WithClock injects the time source of the highlight budget. Without a
// clock the size and line caps still bound the lexer, but no total time
// budget runs; callers that parse untrusted input should always pass one
// (the layout layer of F10.3b wires the app clock).
func WithClock(c Clock) Option {
	return func(p *parseState) { p.clock = c }
}

// Parse parses CommonMark plus the GFM tables, strikethrough and task
// lists (transcript TR-07) into an immutable document. It never fails and
// never panics: hostile input is bounded by the named limits, and anything
// it cannot render — raw HTML, blocked links, unsafe characters — degrades
// to literal text. It is CPU-bound: run it off the window goroutine
// (F10.4 does, with a cache).
func Parse(src string, opts ...Option) *Doc {
	cut, truncated := capSource(src)
	p := &parseState{source: []byte(cut), doc: &Doc{Truncated: truncated}}
	for _, opt := range opts {
		opt(p)
	}
	root := newEngine().Parser().Parse(text.NewReader(p.source))
	p.doc.Blocks = p.blocks(root, 0)
	return p.doc
}

// newEngine builds a fresh parser on every call: the package keeps no
// mutable globals, and a parse is not on a frame path.
func newEngine() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

// parseState carries the source, the truncation flag and the injected
// clock of the highlight budget through the walk.
type parseState struct {
	source []byte
	doc    *Doc
	clock  Clock
}

// blocks converts a container's children, skipping nesting beyond the
// depth cap.
func (p *parseState) blocks(container ast.Node, depth int) []Block {
	var out []Block
	for n := container.FirstChild(); n != nil; n = n.NextSibling() {
		if depth > maxNestingDepth {
			p.doc.Truncated = true
			return out
		}
		if b, ok := p.block(n, depth); ok {
			out = append(out, b)
		}
	}
	return out
}

// block converts one block-level node.
func (p *parseState) block(n ast.Node, depth int) (Block, bool) {
	switch n.Kind() {
	case ast.KindParagraph, ast.KindTextBlock:
		return p.paragraph(n), true
	case ast.KindHeading:
		return p.heading(n), true
	case ast.KindThematicBreak:
		return Block{Kind: BlockThematicBreak}, true
	case ast.KindCodeBlock, ast.KindFencedCodeBlock:
		return p.code(n), true
	case ast.KindHTMLBlock:
		return p.html(n), true
	case ast.KindBlockquote:
		return Block{Kind: BlockQuote, Blocks: p.blocks(n, depth+1)}, true
	case ast.KindList:
		return p.list(n, depth), true
	case extast.KindTable:
		return p.table(n), true
	default:
		return Block{}, false
	}
}

// paragraph converts a paragraph or the text block of a tight list item.
func (p *parseState) paragraph(n ast.Node) Block {
	return Block{Kind: BlockParagraph, Spans: p.inlines(n)}
}

// heading converts a heading with its level.
func (p *parseState) heading(n ast.Node) Block {
	level := 0
	if h, ok := n.(*ast.Heading); ok {
		level = h.Level
	}
	return Block{Kind: BlockHeading, Level: level, Spans: p.inlines(n)}
}

// html turns a raw HTML block into literal text spans; nothing of it is
// ever interpreted.
func (p *parseState) html(n ast.Node) Block {
	return Block{Kind: BlockHTML, Spans: p.literal(n.Lines().Value(p.source))}
}

// code converts a fenced or indented code block. Interior whitespace is
// preserved line for line; tabs expand on the 4-column tab stop; a fenced
// block with a known language is tokenized into roles.
func (p *parseState) code(n ast.Node) Block {
	b := Block{Kind: BlockCode}
	if fenced, ok := n.(*ast.FencedCodeBlock); ok {
		b.Language = strings.ToLower(string(fenced.Language(p.source)))
	}
	raw := string(n.Lines().Value(p.source))
	expanded := make([]string, 0, 16)
	for _, line := range splitLines(raw) {
		text, _ := expandTabs(line, 0)
		text = sanitizeText(text)
		expanded = append(expanded, text)
		b.Lines = append(b.Lines, []Span{{Text: text}})
	}
	joined := strings.Join(expanded, "\n")
	if !highlightable(b.Language, joined, len(b.Lines)) {
		return b
	}
	if lines, ok := highlight(b.Language, joined, len(b.Lines), p.clock); ok {
		b.Lines, b.Highlighted = lines, true
	}
	return b
}

// splitLines cuts a code block into lines without their newlines.
func splitLines(raw string) []string {
	return strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
}

// highlightable reports the cheap pre-checks before the lexer runs.
func highlightable(language, raw string, lineCount int) bool {
	return language != "" && len(raw) <= maxHighlightBytes && lineCount <= maxHighlightLines
}

// list converts a list and its items; task-list items carry their
// read-only checked state.
func (p *parseState) list(n ast.Node, depth int) Block {
	l := List{Start: 1}
	if src, ok := n.(*ast.List); ok {
		l.Ordered = src.IsOrdered()
		if src.Start != 0 {
			l.Start = src.Start
		}
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		l.Items = append(l.Items, p.listItem(item, depth))
	}
	return Block{Kind: BlockList, List: l}
}

// listItem converts one item, looking for the task checkbox in its first
// inline child.
func (p *parseState) listItem(n ast.Node, depth int) ListItem {
	var item ListItem
	if first := n.FirstChild(); first != nil {
		if box, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
			item.Task, item.Checked = true, box.IsChecked
		}
	}
	item.Blocks = p.blocks(n, depth+1)
	return item
}

// table converts a GFM table, keeping per-column alignment and capping
// columns and total cells.
func (p *parseState) table(n ast.Node) Block {
	aligns := []extast.Alignment(nil)
	if src, ok := n.(*extast.Table); ok {
		aligns = src.Alignments
	}
	t := Table{Aligns: trimAligns(aligns, maxTableColumns)}
	if len(aligns) > maxTableColumns {
		p.doc.Truncated = true
	}
	cells := 0
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		if cells >= maxTableCells {
			p.doc.Truncated = true
			break
		}
		cells = p.tableRow(row, &t, cells)
	}
	return Block{Kind: BlockTable, Table: t}
}

// tableRow appends one header or body row and returns the running cell
// count.
func (p *parseState) tableRow(row ast.Node, t *Table, cells int) int {
	var out []Cell
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		if cells >= maxTableCells || len(out) >= maxTableColumns {
			p.doc.Truncated = true
			break
		}
		out = append(out, Cell{Spans: p.inlines(cell)})
		cells++
	}
	if row.Kind() == extast.KindTableHeader {
		t.Header = out
	} else {
		t.Rows = append(t.Rows, out)
	}
	return cells
}

// trimAligns converts goldmark alignments up to the column cap.
func trimAligns(aligns []extast.Alignment, cap int) []Align {
	var out []Align
	for _, a := range aligns {
		if len(out) == cap {
			break
		}
		out = append(out, convertAlign(a))
	}
	return out
}

// convertAlign maps one goldmark table alignment.
func convertAlign(a extast.Alignment) Align {
	switch a {
	case extast.AlignLeft:
		return AlignLeft
	case extast.AlignCenter:
		return AlignCenter
	case extast.AlignRight:
		return AlignRight
	case extast.AlignNone:
		return AlignNone
	default:
		return AlignNone
	}
}
