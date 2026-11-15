package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Drawing helpers shared by the kit's pressable controls (Button,
// IconButton): state fills, the §3.4 focus ring, and label measurement.
// Every color that reaches the screen comes from theme (UK-03); the only
// arithmetic done here is the transient alpha of a §3.7 transition.

// strokeRRect strokes a rounded rectangle's outline.
func strokeRRect(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	rr := clip.UniformRRect(r, radius)
	paint.FillShape(ops, c, clip.Stroke{Path: rr.Path(ops), Width: width}.Op())
}

// focusRing strokes the keyboard focus ring of §3.4 just inside r:
// FocusStroke wide, FocusInset in from the edge, over the control body.
func focusRing(env *Env, gtx *layout.Context, r image.Rectangle, radius int) {
	inset := gtx.Dp(theme.FocusInset)
	inner := image.Rect(r.Min.X+inset, r.Min.Y+inset, r.Max.X-inset, r.Max.Y-inset)
	strokeRRect(gtx.Ops, inner, max(radius-inset, 1), float32(gtx.Dp(theme.FocusStroke)), env.Th.Chrome().FocusBorder)
}

// fade returns c with its alpha scaled by t: the transient alpha of a
// §3.7 background transition. The spring settles exactly on 0 or 1, so a
// frame at rest always shows the token itself, never an in-between color
// (UK-03); theme owns the derivation of the tokens, the kit only fades
// between two of them while the user watches.
func fade(c color.NRGBA, t float32) color.NRGBA {
	c.A = uint8(float32(c.A) * min(max(t, 0), 1))
	return c
}

// at runs fn with the origin moved to p.
func at(gtx *layout.Context, p image.Point, fn func(*layout.Context)) {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	fn(gtx)
}

// measureLabel returns the laid-out size of txt in token t without painting:
// the number a control's width is computed from, so a label never overflows
// the box measured for it.
func measureLabel(env *Env, gtx *layout.Context, t theme.Type, txt string) image.Point {
	m := op.Record(gtx.Ops)
	// The paint color dies with the macro, so any token works; Fg is the
	// cheapest honest one.
	dims := drawText(env, gtx, t, env.Th.Chrome().Fg, txt, false)
	m.Stop()
	return dims.Size
}

// labelLine is the px height of one line of token t: its absolute §3.0
// line height, the number every control aligns to.
func labelLine(env *Env, gtx *layout.Context, t theme.Type) int {
	_, line := env.Content(t)
	return gtx.Sp(line)
}

// centerLabelY returns the y that vertically centers one line of t in a box
// of height h.
func centerLabelY(env *Env, gtx *layout.Context, t theme.Type, h int) int {
	return (h - labelLine(env, gtx, t)) / 2
}
