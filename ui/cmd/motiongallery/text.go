package main

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
)

// style is one text style.
type style struct {
	size   unit.Sp
	weight font.Weight
}

func body() style    { return style{size: 13, weight: font.Normal} }
func small() style   { return style{size: 11, weight: font.Normal} }
func label() style   { return style{size: 13, weight: font.Medium} }
func heading() style { return style{size: 20, weight: font.SemiBold} }

func (st style) font() font.Font {
	return font.Font{Typeface: fonts.Sans, Style: font.Regular, Weight: st.weight}
}

// textAt lays txt out at the origin, wrapped at maxWidth, in colour c.
func textAt(gtx *layout.Context, sh *text.Shaper, st style, maxWidth int, txt string, c color.NRGBA) layout.Dimensions {
	g := *gtx
	g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(maxWidth, 1<<20)}
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	l := widget.Label{Alignment: text.Start, MaxLines: 0, Truncator: "", WrapPolicy: text.WrapWords, LineHeight: 0, LineHeightScale: 1.3}
	return l.Layout(g, sh, st.font(), st.size, txt, m.Stop())
}

// textWidth measures txt on one line.
func textWidth(gtx *layout.Context, sh *text.Shaper, st style, txt string) int {
	m := op.Record(gtx.Ops)
	dims := textAt(gtx, sh, st, 1<<20, txt, color.NRGBA{})
	m.Stop()
	return dims.Size.X
}

// streamText lays out the first reveal runes of txt (reveal may be
// fractional) so the newest characters fade in: every glyph is painted with
// an alpha that rises over the last tail runes before the reveal point. It
// shapes once and paints runs of glyphs that share an alpha step, the same
// way a label paints, so wrapping is identical to a plain label.
func streamText(gtx *layout.Context, sh *text.Shaper, st style, maxWidth int, txt string, reveal, tail float32, c color.NRGBA) layout.Dimensions {
	sh.LayoutString(text.Parameters{
		Font: st.font(), Alignment: text.Start, PxPerEm: fixed.I(gtx.Sp(st.size)), MaxLines: 0, Truncator: "",
		WrapPolicy: text.WrapWords, MinWidth: 0, MaxWidth: maxWidth, LineHeightScale: 1.3, LineHeight: 0, DisableSpaceTrim: false,
	}, txt)
	var (
		run      []text.Glyph
		runAlpha = -1
		runeAt   float32
		size     image.Point
	)
	flush := func() {
		if len(run) > 0 && runAlpha > 0 {
			// Shape draws a run relative to its first glyph, as a label does.
			off := op.Affine(f32.AffineId().Offset(f32.Pt(float32(run[0].X)/64, float32(run[0].Y)))).Push(gtx.Ops)
			stack := clip.Outline{Path: sh.Shape(run)}.Op().Push(gtx.Ops)
			paint.ColorOp{Color: fade(c, float32(runAlpha)/16)}.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			stack.Pop()
			off.Pop()
		}
		run = run[:0]
	}
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		if runeAt < reveal {
			// Only revealed text takes space, so the block grows as it streams.
			size.X = max(size.X, (g.X + g.Advance).Ceil())
			size.Y = max(size.Y, int(g.Y)+g.Descent.Ceil())
		}
		alpha := int(16 * min(max((reveal-runeAt)/tail, 0), 1))
		runeAt += float32(g.Runes)
		if alpha != runAlpha {
			flush()
			runAlpha = alpha
		}
		run = append(run, g)
		if g.Flags&text.FlagLineBreak != 0 {
			flush()
		}
	}
	flush()
	return layout.Dimensions{Size: size, Baseline: 0}
}
