// Package flow is the spike's rich-text engine: it flows styled runs into
// wrapped lines, keeps the per-rune x positions of every word and so can map a
// pixel to a text offset. That mapping is what Gio's widget.Selectable cannot
// give across items, and it is what a cross-item selection needs.
package flow

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/f32"
	giofont "gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"
)

// Style picks the face of a run.
type Style uint8

// Run styles.
const (
	Normal Style = iota
	Bold
	Code // inline code inside a paragraph
	Mono // block code: monospace, never wrapped
	Dim
)

// Bg is the background kind of a block.
const (
	BgNone = iota
	BgCode
	BgAdd
	BgDel
)

// Run is a span of text in one style; Tok indexes Palette.Tok (0 = default).
type Run struct {
	Text  string
	Style Style
	Tok   uint8
}

// Block is a paragraph, a list entry or a code block.
type Block struct {
	Runs   []Run
	Prefix string // selectable marker such as "- " or "1. "
	Indent int    // levels of 20 dp
	Mono   bool   // code: monospace, line breaks kept, no wrapping
	Bg     int
	Gutter string // line number, painted but not selectable
	Gap    int    // extra space above, px at 1x
}

// Item is one transcript entry (or one diff line).
type Item struct {
	Blocks []Block
	Rev    int
}

// Text is the plain text of the item, the string selection offsets index.
func (it *Item) Text() string {
	var sb strings.Builder
	for i, b := range it.Blocks {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.Prefix)
		for _, r := range b.Runs {
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

// Palette holds the colours.
type Palette struct {
	Fg, Dim, Bg, CodeBg, Sel, AddBg, DelBg color.NRGBA
	Tok                                    [8]color.NRGBA
}

// Env is what layout and paint need.
type Env struct {
	Shaper *text.Shaper
	Pal    *Palette
	Px     int // body size in px
	GutW   int
	words  map[wkey]*wm
	faces  [5]faceInfo
}

type wkey struct {
	st Style
	px int
	s  string
}

type wm struct {
	xs []int32 // x of every rune boundary, len = runes+1
}

type faceInfo struct{ asc, desc int }

// NewEnv returns an Env at the given body size.
func NewEnv(sh *text.Shaper, pal *Palette, px int) *Env {
	return &Env{Shaper: sh, Pal: pal, Px: px, GutW: px * 4, words: map[wkey]*wm{}}
}

func (e *Env) font(s Style) (giofont.Font, int) {
	switch s {
	case Bold:
		return giofont.Font{Typeface: "Inter", Weight: giofont.Bold}, e.Px
	case Code:
		return giofont.Font{Typeface: "JetBrains Mono"}, e.Px * 9 / 10
	case Mono:
		return giofont.Font{Typeface: "JetBrains Mono"}, e.Px * 9 / 10
	}
	return giofont.Font{Typeface: "Inter"}, e.Px
}

func (e *Env) params(s Style) text.Parameters {
	f, px := e.font(s)
	return text.Parameters{Font: f, PxPerEm: fixed.I(px), MaxWidth: 1 << 24, DisableSpaceTrim: true}
}

// measure shapes one word and returns its rune boundaries.
func (e *Env) measure(s Style, str string) *wm {
	k := wkey{s, e.Px, str}
	if m, ok := e.words[k]; ok {
		return m
	}
	if len(e.words) > 60000 {
		e.words = map[wkey]*wm{}
	}
	e.Shaper.LayoutString(e.params(s), str)
	xs := make([]int32, 1, len([]rune(str))+1)
	prev := fixed.Int26_6(0)
	for g, ok := e.Shaper.NextGlyph(); ok; g, ok = e.Shaper.NextGlyph() {
		if e.faces[s].asc == 0 {
			e.faces[s] = faceInfo{g.Ascent.Ceil(), g.Descent.Ceil()}
		}
		if g.Flags&text.FlagClusterBreak == 0 {
			continue
		}
		end := g.X + g.Advance
		n := max(int(g.Runes), 1)
		for i := 1; i <= n; i++ {
			xs = append(xs, int32((prev + (end-prev)*fixed.Int26_6(i)/fixed.Int26_6(n)).Round()))
		}
		prev = end
	}
	for want := len([]rune(str)) + 1; len(xs) < want; { // glyphs missing: pad
		xs = append(xs, xs[len(xs)-1])
	}
	m := &wm{xs: xs}
	e.words[k] = m
	return m
}

type seg struct {
	x      int
	text   string
	style  Style
	tok    uint8
	r0, r1 int
	xs     []int32
}

type line struct {
	top, h int
	r0, r1 int
	segs   []seg
}

type lblock struct {
	top, h int
	x0, w  int // content box
	bg     int
	gutter string
	lines  []line
}

// Laid is the layout of one item at one width.
type Laid struct {
	Rev, Width int
	Height     int
	blocks     []lblock
	lh         int
}

const pad = 8

// Layout flows the item into width pixels.
func (e *Env) Layout(it *Item, width int) *Laid {
	l := &Laid{Rev: it.Rev, Width: width}
	lh := e.Px * 3 / 2
	l.lh = lh
	y, off := 0, 0
	for bi, b := range it.Blocks {
		if bi > 0 {
			y += e.Px/2 + b.Gap
			off++
		}
		lb := lblock{top: y, bg: b.Bg, gutter: b.Gutter}
		in := 0
		if b.Bg == BgCode {
			in = pad
		}
		lb.x0 = b.Indent*e.Px*3/2 + in
		if b.Gutter != "" || b.Bg == BgAdd || b.Bg == BgDel {
			lb.x0 += e.GutW
		}
		lb.w = max(width-lb.x0-in, e.Px*4)
		runs := b.Runs
		if b.Prefix != "" {
			runs = append([]Run{{Text: b.Prefix, Style: Dim}}, runs...)
		}
		var lines []line
		off, lines = e.flowBlock(runs, lb.w, lh, off, b.Mono)
		lb.lines = lines
		by := in
		for i := range lb.lines {
			lb.lines[i].top = by
			by += lb.lines[i].h
		}
		lb.h = by + in
		y += lb.h
		l.blocks = append(l.blocks, lb)
	}
	l.Height = y
	return l
}

// flowBlock wraps runs greedily by word. Mono blocks keep their lines.
func (e *Env) flowBlock(runs []Run, avail, lh, off int, mono bool) (int, []line) {
	cur := line{h: lh, r0: off}
	var out []line
	x := 0
	brk := func(end int) {
		cur.r1 = end
		out = append(out, cur)
		cur = line{h: lh, r0: end}
		x = 0
	}
	for _, r := range runs {
		rs := []rune(r.Text)
		for i := 0; i < len(rs); {
			if rs[i] == '\n' {
				off++
				i++
				brk(off)
				continue
			}
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			if mono {
				chunk := string(rs[i:j])
				m := e.measure(r.Style, chunk)
				cur.segs = append(cur.segs, seg{x: x, text: chunk, style: r.Style, tok: r.Tok, r0: off, r1: off + j - i, xs: m.xs})
				x += int(m.xs[len(m.xs)-1])
				off += j - i
				i = j
				continue
			}
			// words with their trailing spaces
			for i < j {
				k := i
				for k < j && rs[k] != ' ' {
					k++
				}
				c := k // content end
				for k < j && rs[k] == ' ' {
					k++
				}
				m := e.measure(r.Style, string(rs[i:k]))
				cw := int(m.xs[c-i])
				if x > 0 && x+cw > avail {
					brk(off)
				}
				if x == 0 && cw > avail { // a word wider than the line: split by rune
					n := 1
					for n < c-i && int(m.xs[n+1]) <= avail {
						n++
					}
					k, c = i+n, i+n
					m = e.measure(r.Style, string(rs[i:k]))
				}
				cur.segs = append(cur.segs, seg{x: x, text: string(rs[i:k]), style: r.Style, tok: r.Tok, r0: off, r1: off + k - i, xs: m.xs})
				x += int(m.xs[len(m.xs)-1])
				off += k - i
				i = k
				if x > avail && i < j {
					brk(off)
				}
			}
		}
	}
	cur.r1 = off
	out = append(out, cur)
	return off, out
}

// Hit returns the text offset nearest to the point (x, y) of the item.
func (l *Laid) Hit(x, y int) int {
	if len(l.blocks) == 0 {
		return 0
	}
	b := &l.blocks[len(l.blocks)-1]
	for i := range l.blocks {
		if y < l.blocks[i].top+l.blocks[i].h || i == len(l.blocks)-1 {
			b = &l.blocks[i]
			break
		}
	}
	ln := &b.lines[len(b.lines)-1]
	for i := range b.lines {
		if y < b.top+b.lines[i].top+b.lines[i].h {
			ln = &b.lines[i]
			break
		}
	}
	x -= b.x0
	if len(ln.segs) == 0 || x <= 0 {
		return ln.r0
	}
	for i := range ln.segs {
		s := &ln.segs[i]
		if x < s.x+int(s.xs[len(s.xs)-1]) || i == len(ln.segs)-1 {
			lx := x - s.x
			best := 0
			for j, v := range s.xs { // nearest boundary
				if abs(int(v)-lx) < abs(int(s.xs[best])-lx) {
					best = j
				}
			}
			o := s.r0 + best
			if o == s.r1 && o > ln.r0 && i == len(ln.segs)-1 && strings.HasSuffix(s.text, " ") && o == ln.r1 {
				o-- // wrapped line: do not land after the trailing space
			}
			return o
		}
	}
	return ln.r1
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (b *lblock) xOf(ln *line, off int) int {
	for i := range ln.segs {
		s := &ln.segs[i]
		if off >= s.r0 && off <= s.r1 {
			return b.x0 + s.x + int(s.xs[off-s.r0])
		}
	}
	if len(ln.segs) == 0 {
		return b.x0
	}
	s := &ln.segs[len(ln.segs)-1]
	return b.x0 + s.x + int(s.xs[len(s.xs)-1])
}

// Paint draws the item at the current origin; [lo, hi) is the selected text
// range of this item (empty when lo >= hi).
func (e *Env) Paint(gtx layout.Context, l *Laid, lo, hi int) {
	for bi := range l.blocks {
		b := &l.blocks[bi]
		bg := color.NRGBA{}
		switch b.bg {
		case BgCode:
			bg = e.Pal.CodeBg
		case BgAdd:
			bg = e.Pal.AddBg
		case BgDel:
			bg = e.Pal.DelBg
		}
		if b.bg != BgNone {
			x0 := 0
			if b.bg == BgCode {
				x0 = b.x0 - pad
			}
			r := image.Rect(x0, b.top, l.Width, b.top+b.h)
			rr := 0
			if b.bg == BgCode {
				rr = 6
			}
			st := clip.UniformRRect(r, rr).Push(gtx.Ops)
			paint.ColorOp{Color: bg}.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			st.Pop()
		}
		if b.gutter != "" {
			e.paintText(gtx, Mono, b.gutter, e.Pal.Dim, 0, b.top+b.lines[0].top, l.lh)
		}
		for li := range b.lines {
			ln := &b.lines[li]
			ty := b.top + ln.top
			if lo < hi && lo < ln.r1+1 && hi > ln.r0 {
				a, z := max(lo, ln.r0), min(hi, ln.r1)
				x1, x2 := b.xOf(ln, a), b.xOf(ln, z)
				if hi > ln.r1 { // selection continues past the line end
					x2 += e.Px / 3
				}
				st := clip.Rect(image.Rect(x1, ty, x2, ty+ln.h)).Push(gtx.Ops)
				paint.ColorOp{Color: e.Pal.Sel}.Add(gtx.Ops)
				paint.PaintOp{}.Add(gtx.Ops)
				st.Pop()
			}
			for si := range ln.segs {
				s := &ln.segs[si]
				col := e.Pal.Fg
				switch {
				case s.style == Dim:
					col = e.Pal.Dim
				case s.tok != 0:
					col = e.Pal.Tok[s.tok]
				}
				e.paintText(gtx, s.style, s.text, col, b.x0+s.x, ty, ln.h)
			}
		}
	}
}

func (e *Env) paintText(gtx layout.Context, st Style, s string, col color.NRGBA, x, top, lh int) {
	if strings.TrimSpace(s) == "" {
		return
	}
	e.Shaper.LayoutString(e.params(st), s)
	var buf [48]text.Glyph
	gl := buf[:0]
	flush := func() {
		if len(gl) == 0 {
			return
		}
		fi := e.faces[st]
		base := top + (lh-(fi.asc+fi.desc))/2 + fi.asc
		t := op.Affine(f32.AffineId().Offset(f32.Pt(float32(x)+float32(gl[0].X)/64, float32(base)))).Push(gtx.Ops)
		cs := clip.Outline{Path: e.Shaper.Shape(gl)}.Op().Push(gtx.Ops)
		paint.ColorOp{Color: col}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		cs.Pop()
		t.Pop()
		gl = gl[:0]
	}
	for g, ok := e.Shaper.NextGlyph(); ok; g, ok = e.Shaper.NextGlyph() {
		gl = append(gl, g)
		if len(gl) == cap(gl) {
			flush()
		}
	}
	flush()
}

// Caret returns the pixel position (x, top of line y) of a text offset.
func (l *Laid) Caret(off int) (x, y int) {
	for bi := range l.blocks {
		b := &l.blocks[bi]
		for li := range b.lines {
			ln := &b.lines[li]
			if off < ln.r1 || (bi == len(l.blocks)-1 && li == len(b.lines)-1) {
				return b.xOf(ln, off), b.top + ln.top
			}
		}
	}
	return 0, 0
}

// LineH is the height of one body line.
func (l *Laid) LineH() int { return l.lh }
