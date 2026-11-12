package mdview

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/op"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	// unbounded is the width of a line that never wraps; it matches Gio's
	// layout.Inf.
	unbounded = 1_000_000
	// maxCachedFlows bounds the laid-out text kept for one document: a
	// window resize creates a flow per block and width.
	maxCachedFlows = 8192
	// tabSpaces is what a stray tab becomes: the model's 4-column tab stop.
	tabSpaces = "    "
	// arrowGlyph marks an openable link.
	arrowGlyph = "↗"
)

// flowKey identifies one laid-out run of spans at one width. The first span's
// address names the run: a Doc is immutable, so it is stable.
type flowKey struct {
	first *markdown.Span
	width int
}

// flowStyle is what a run of spans is shaped with.
type flowStyle struct {
	px    fixed.Int26_6
	bold  bool
	mono  bool
	whole bool // one atom per span, spaces kept: code lines never wrap
}

// lineHeight is the distance between baselines of a run in px.
func (fs flowStyle) lineHeight() int {
	px := fs.px
	if fs.mono {
		px = px * 9 / 10
	}
	return px.Ceil() * 3 / 2
}

// atom is one shaped, unbreakable piece of text, or the gap between two. Its
// span carries the style and link; glyphs is nil for a gap.
type atom struct {
	glyphs    []text.Glyph
	span      *markdown.Span
	w, x      int
	asc, desc int
	lh        int
	arrow     bool
}

// token is one item of the stream wrapping consumes.
type token struct {
	a    atom
	gap  bool
	hard bool
}

// line is one wrapped line: its top in the flow, its baseline below the top,
// and its atoms with their x.
type line struct {
	top, base, h, w int
	asc, desc       int
	atoms           []atom
}

// flow is a wrapped run of spans: a paragraph, a heading, a cell, a code line.
type flow struct {
	lines         []line
	height, width int
	// rec, call and links are filled by the first draw: the run's static
	// drawing recorded into its own ops, replayed on every later frame, and
	// the links in it with their pointer boxes.
	rec   *op.Ops
	call  op.CallOp
	links []linkAnchor
}

// face resolves the font and size of one span: code is JetBrains Mono at
// nine tenths of the text size, bold is the font weight, and syntax roles
// bring the theme's weight and posture.
func (v *view) face(sp *markdown.Span, fs flowStyle) (font.Font, fixed.Int26_6) {
	f := font.Font{Typeface: fonts.Sans}
	px := fs.px
	if sp.Style&markdown.StyleCode != 0 || fs.mono {
		f.Typeface = fonts.Mono
		px = px * 9 / 10
	}
	sy := v.th.Syntax(theme.SyntaxRole(sp.Role))
	f.Weight = sy.Weight
	if sp.Style&markdown.StyleBold != 0 || fs.bold {
		f.Weight = font.Bold
	}
	if sy.Italic || sp.Style&markdown.StyleItalic != 0 {
		f.Style = font.Italic
	}
	return f, px
}

// shape shapes one unbreakable string on one line and measures it.
func (v *view) shape(txt string, f font.Font, px fixed.Int26_6) atom {
	// The model expands tabs; this keeps the guarantee that no tab reaches
	// the shaper whatever the caller hands over.
	txt = strings.ReplaceAll(txt, "\t", tabSpaces)
	v.shaper.LayoutString(text.Parameters{
		Font: f, PxPerEm: px, MaxWidth: unbounded, Locale: v.locale, DisableSpaceTrim: true,
	}, txt)
	a := atom{lh: px.Ceil() * 3 / 2}
	var adv fixed.Int26_6
	for g, ok := v.shaper.NextGlyph(); ok; g, ok = v.shaper.NextGlyph() {
		a.glyphs = append(a.glyphs, g)
		adv += g.Advance
		a.asc = max(a.asc, g.Ascent.Ceil())
		a.desc = max(a.desc, g.Descent.Ceil())
	}
	a.w = adv.Ceil()
	return a
}

// run is one maximal stretch of spaces or of non-spaces in a span's text.
type run struct {
	text  string
	space bool
}

// runs splits text into alternating word and space runs; a newline is a
// space. whole keeps the text as one run.
func runs(s string, whole bool) []run {
	if whole {
		return []run{{text: s}}
	}
	var out []run
	for s != "" {
		first, _ := utf8.DecodeRuneInString(s)
		space := unicode.IsSpace(first)
		end := len(s)
		for i, r := range s {
			if unicode.IsSpace(r) != space {
				end = i
				break
			}
		}
		out = append(out, run{text: s[:end], space: space})
		s = s[end:]
	}
	return out
}

// tokens shapes the spans into words and gaps. Adjacent words of different
// spans stay together, so style changes never open a break the source did
// not have; an openable link ends in the ↗ mark.
func (v *view) tokens(spans []markdown.Span, fs flowStyle) []token {
	var out []token
	for i := range spans {
		sp := &spans[i]
		f, px := v.face(sp, fs)
		for _, r := range runs(sp.Text, fs.whole) {
			if !r.space {
				a := v.shape(r.text, f, px)
				a.span = sp
				out = append(out, token{a: a})
				continue
			}
			a := v.shape(" ", f, px)
			a.glyphs, a.span = nil, sp
			a.w *= utf8.RuneCountInString(r.text)
			out = append(out, token{a: a, gap: true})
		}
		if sp.Link != nil && sp.Link.Openable && (i+1 == len(spans) || spans[i+1].Link != sp.Link) {
			a := v.shape(arrowGlyph, f, px*4/5)
			a.span, a.arrow = sp, true
			out = append(out, token{a: a})
		}
		if sp.HardBreak {
			out = append(out, token{hard: true})
		}
	}
	return out
}

// placer greedily fills lines from tokens.
type placer struct {
	width  int
	emptyH int
	f      flow
	cur    line
}

// add puts a copy of an atom at the end of the current line.
func (p *placer) add(a *atom) {
	placed := *a
	placed.x = p.cur.w
	p.cur.w += placed.w
	p.cur.asc = max(p.cur.asc, placed.asc)
	p.cur.desc = max(p.cur.desc, placed.desc)
	p.cur.h = max(p.cur.h, placed.lh)
	p.cur.atoms = append(p.cur.atoms, placed)
}

// newline closes the current line; a line with no atoms is as tall as the
// run's text.
func (p *placer) newline() {
	l := p.cur
	if len(l.atoms) == 0 {
		l.h = p.emptyH
	}
	l.h = max(l.h, l.asc+l.desc)
	l.top = p.f.height
	l.base = l.asc + (l.h-l.asc-l.desc)/2
	p.f.lines = append(p.f.lines, l)
	p.f.height += l.h
	p.f.width = max(p.f.width, l.w)
	p.cur = line{}
}

// word places one word after the gaps before it: on this line when it fits
// with them, else on a new line without them; at a line start the gaps drop.
func (p *placer) word(gaps []atom, word []token) {
	gapW, wordW := 0, 0
	for i := range gaps {
		gapW += gaps[i].w
	}
	for i := range word {
		wordW += word[i].a.w
	}
	switch {
	case p.cur.w == 0:
	case p.cur.w+gapW+wordW > p.width:
		p.newline()
	default:
		for i := range gaps {
			p.add(&gaps[i])
		}
	}
	for i := range word {
		p.add(&word[i].a)
	}
}

// wrap breaks the tokens into lines no wider than width. Spaces at a break
// are dropped and a word wider than the line overflows it; the paragraph
// clips it.
func wrap(toks []token, width, emptyH int) *flow {
	p := &placer{width: width, emptyH: emptyH}
	var gaps []atom
	for i := 0; i < len(toks); {
		switch t := &toks[i]; {
		case t.hard:
			p.newline()
			gaps = gaps[:0]
			i++
		case t.gap:
			gaps = append(gaps, t.a)
			i++
		default:
			j := i
			for j < len(toks) && !toks[j].gap && !toks[j].hard {
				j++
			}
			p.word(gaps, toks[i:j])
			gaps = gaps[:0]
			i = j
		}
	}
	if len(p.cur.atoms) > 0 || len(p.f.lines) == 0 {
		p.newline()
	}
	return &p.f
}

// flowFor returns the wrapped spans, laying them out once per width.
func (v *view) flowFor(spans []markdown.Span, width int, fs flowStyle) *flow {
	if len(spans) == 0 {
		return &flow{height: fs.lineHeight()}
	}
	key := flowKey{first: &spans[0], width: width}
	if f, ok := v.st.flows[key]; ok {
		return f
	}
	f := wrap(v.tokens(spans, fs), width, fs.lineHeight())
	v.st.flows[key] = f
	return f
}
