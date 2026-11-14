package kitspike

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/fonts"
)

// font returns the Font of a type token: Inter for text, JetBrains Mono for
// code (§3.3).
func (t Type) font() font.Font {
	if t.Mono {
		return font.Font{Typeface: fonts.Mono, Style: font.Regular, Weight: t.Weight}
	}
	return font.Font{Typeface: fonts.Sans, Style: font.Regular, Weight: t.Weight}
}

// drawText lays txt out wrapped at maxWidth with the token's absolute line
// height (LineHeightScale = 1, §3.0) and paints it in c.
func drawText(gtx *layout.Context, sh *text.Shaper, t Type, maxWidth int, txt string, c color.NRGBA) layout.Dimensions {
	g := *gtx
	g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(maxWidth, 1<<20)}
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	l := widget.Label{Alignment: text.Start, MaxLines: 0, WrapPolicy: text.WrapWords, LineHeight: t.Line, LineHeightScale: 1}
	return l.Layout(g, sh, t.font(), t.Size, txt, m.Stop())
}

// measureText returns the laid-out size of txt with no painting.
func measureText(gtx *layout.Context, sh *text.Shaper, t Type, maxWidth int, txt string) image.Point {
	m := op.Record(gtx.Ops)
	dims := drawText(gtx, sh, t, maxWidth, txt, color.NRGBA{})
	m.Stop()
	return dims.Size
}

// lineTextH returns the height of a single line of the token: its absolute
// line height in px, the number every control aligns to.
func lineTextH(gtx *layout.Context, t Type) int {
	return gtx.Sp(t.Line)
}

// verticalCenter returns the y offset that centers a line of token t inside
// height h.
func verticalCenter(gtx *layout.Context, t Type, h int) int {
	return (h - lineTextH(gtx, t)) / 2
}
