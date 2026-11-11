package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// Inline conversion. Spans keep their source text verbatim — the
// whitespace around inline code and bold, and between adjacent spans, is
// exactly the source's (DA-E03) — so text comes from the source segments
// goldmark records, never from a re-serialization. Tabs expand on the
// 4-column tab stop with a column that runs across spans of one line.

// inlineBuilder folds one container's inline children into spans.
type inlineBuilder struct {
	source []byte
	spans  []Span
	// col is the rune column of the current line; a newline in emitted
	// text resets it so tab stops stay absolute per line.
	col int
}

// inlines converts a container's inline children into sanitized spans.
func (p *parseState) inlines(container ast.Node) []Span {
	b := &inlineBuilder{source: p.source}
	for n := container.FirstChild(); n != nil; n = n.NextSibling() {
		b.walk(n, 0, nil)
	}
	return b.spans
}

// literal renders raw text (raw HTML, auto-link bodies) as one plain span.
func (p *parseState) literal(raw []byte) []Span {
	b := &inlineBuilder{source: p.source}
	b.emit(string(raw), 0, nil)
	return b.spans
}

// walk converts one inline node, carrying the enclosing style and link.
func (b *inlineBuilder) walk(n ast.Node, style SpanStyle, link *Link) {
	switch n.Kind() {
	case ast.KindText:
		b.walkTextNode(n, style, link)
	case ast.KindString:
		b.walkStringNode(n, style, link)
	case ast.KindCodeSpan:
		b.emit(codeSpanText(n, b.source), style|StyleCode, nil)
	case ast.KindEmphasis:
		b.walkEmphasis(n, style, link)
	case extast.KindStrikethrough:
		b.walkChildren(n, style|StyleStrike, link)
	case ast.KindLink:
		b.walkLink(n, style)
	case ast.KindAutoLink:
		b.walkAutoLink(n, style)
	case ast.KindImage:
		b.walkImage(n)
	case ast.KindRawHTML:
		b.emit(rawHTMLText(n, b.source), style, nil)
	case extast.KindTaskCheckBox:
		// The item records the checkbox state; nothing renders inline.
	default:
		b.walkChildren(n, style, link)
	}
}

// walkTextNode emits a source text segment and records a trailing hard
// break.
func (b *inlineBuilder) walkTextNode(n ast.Node, style SpanStyle, link *Link) {
	t, ok := n.(*ast.Text)
	if !ok {
		b.walkChildren(n, style, link)
		return
	}
	b.emit(string(t.Segment.Value(b.source)), style, link)
	if t.HardLineBreak() {
		b.markHardBreak()
	}
}

// walkStringNode emits a resolved value node (escaped characters,
// entities).
func (b *inlineBuilder) walkStringNode(n ast.Node, style SpanStyle, link *Link) {
	s, ok := n.(*ast.String)
	if !ok {
		b.walkChildren(n, style, link)
		return
	}
	b.emit(string(s.Value), style, link)
}

// walkEmphasis maps emphasis levels onto bold and italic.
func (b *inlineBuilder) walkEmphasis(n ast.Node, style SpanStyle, link *Link) {
	e, ok := n.(*ast.Emphasis)
	if !ok {
		b.walkChildren(n, style, link)
		return
	}
	if e.Level == 2 {
		style |= StyleBold
	} else {
		style |= StyleItalic
	}
	b.walkChildren(n, style, link)
}

// walkLink converts a link's children with the sanitized destination
// attached to every span.
func (b *inlineBuilder) walkLink(n ast.Node, style SpanStyle) {
	l, ok := n.(*ast.Link)
	if !ok {
		b.walkChildren(n, style, nil)
		return
	}
	link := newLink(l.Destination)
	b.walkChildren(n, style, &link)
}

// walkAutoLink converts <...> autolinks, which hold their URL outside the
// text children.
func (b *inlineBuilder) walkAutoLink(n ast.Node, style SpanStyle) {
	a, ok := n.(*ast.AutoLink)
	if !ok {
		b.walkChildren(n, style, nil)
		return
	}
	l := newLink(a.URL(b.source))
	b.walkChildren(n, style, &l)
}

// walkImage converts an image to one span holding the alt text and the
// destination host; images are never fetched and have no openable state.
// An empty alt still yields a span, so the image marker survives.
func (b *inlineBuilder) walkImage(n ast.Node) {
	node, ok := n.(*ast.Image)
	if !ok {
		return
	}
	img := newImage(node.Destination, childText(n, b.source))
	alt, _ := expandTabs(img.Alt, b.col)
	b.col = columnAfter(alt, b.col)
	b.spans = append(b.spans, Span{Text: sanitizeText(alt), Image: &img})
}

// walkChildren recurses into a container's inline children.
func (b *inlineBuilder) walkChildren(n ast.Node, style SpanStyle, link *Link) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.walk(c, style, link)
	}
}

// emit appends one span: tabs expand at the running column, unsafe runes
// drop, and a span identical in style, link and role to its predecessor
// merges into it so styling never splits source runs needlessly.
func (b *inlineBuilder) emit(raw string, style SpanStyle, link *Link) {
	text, col := expandTabs(raw, b.col)
	b.col = columnAfter(text, col)
	text = sanitizeText(text)
	if text == "" {
		return
	}
	if last := len(b.spans) - 1; last >= 0 && sameRun(b.spans[last], style, link) {
		b.spans[last].Text += text
		return
	}
	b.spans = append(b.spans, Span{Text: text, Style: style, Link: link})
}

// markHardBreak flags a hard line break on the last emitted span.
func (b *inlineBuilder) markHardBreak() {
	if last := len(b.spans) - 1; last >= 0 {
		b.spans[last].HardBreak = true
	}
}

// sameRun reports whether text can join the previous span.
func sameRun(s Span, style SpanStyle, link *Link) bool {
	return !s.HardBreak && s.Style == style && s.Role == RolePlain &&
		(s.Link == nil) == (link == nil) && equalLinks(s.Link, link)
}

// equalLinks compares two nilable links by identity of their contents.
func equalLinks(a, b *Link) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// columnAfter resets the column when emitted text ends a line, so the
// next span's tab stops count from the new line.
func columnAfter(text string, col int) int {
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		return len([]rune(text)) - i - 1
	}
	return col
}

// codeSpanText concatenates a code span's raw source segments, keeping
// interior whitespace as written.
func codeSpanText(n ast.Node, source []byte) string {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			sb.WriteString(string(t.Segment.Value(source)))
		}
	}
	return sb.String()
}

// rawHTMLText concatenates a raw HTML node's source segments; the result
// is shown as literal text.
func rawHTMLText(n ast.Node, source []byte) string {
	var sb strings.Builder
	raw, ok := n.(*ast.RawHTML)
	if !ok {
		return ""
	}
	for i := range raw.Segments.Len() {
		segment := raw.Segments.At(i)
		sb.WriteString(string(segment.Value(source)))
	}
	return sb.String()
}

// childText concatenates a node's descendant text values (an image's alt
// text).
func childText(n ast.Node, source []byte) []byte {
	var sb strings.Builder
	err := ast.Walk(n, func(inner ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			appendNodeText(&sb, inner, source)
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil
	}
	return []byte(sb.String())
}

// appendNodeText adds one node's own text to sb.
func appendNodeText(sb *strings.Builder, n ast.Node, source []byte) {
	switch t := n.(type) {
	case *ast.Text:
		sb.WriteString(string(t.Segment.Value(source)))
	case *ast.String:
		sb.WriteString(string(t.Value))
	}
}
