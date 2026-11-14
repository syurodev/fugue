package kitspike

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// fillRRect fills a rounded rectangle.
func fillRRect(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	paint.FillShape(ops, c, clip.UniformRRect(r, radius).Op(ops))
}

// strokeRRect strokes a rounded rectangle's outline.
func strokeRRect(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	rr := clip.UniformRRect(r, radius)
	paint.FillShape(ops, c, clip.Stroke{Path: rr.Path(ops), Width: width}.Op())
}

// fillRect fills a rectangle.
func fillRect(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

// at runs fn with the origin moved to p and returns its dimensions.
func at(gtx *layout.Context, p image.Point, fn func(gtx *layout.Context) layout.Dimensions) layout.Dimensions {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	return fn(gtx)
}

// dp converts a dp token.
func dp(gtx *layout.Context, v unit.Dp) int { return gtx.Dp(v) }

// scaleAround pushes a scale by s about the point c.
func scaleAround(ops *op.Ops, c f32.Point, s float32) op.TransformStack {
	return op.Affine(f32.AffineId().Scale(c, f32.Pt(s, s))).Push(ops)
}

// focusRing strokes the keyboard focus ring of §3.4 around r: FocusStroke
// wide, FocusInset inside the edge. Draw it last, after the control body.
func focusRing(env *Env, gtx *layout.Context, r image.Rectangle, radius int) {
	inset := dp(gtx, FocusInset)
	inner := image.Rect(r.Min.X+inset, r.Min.Y+inset, r.Max.X-inset, r.Max.Y-inset)
	strokeRRect(gtx.Ops, inner, max(radius-inset, 1), float32(dp(gtx, FocusStroke)), env.Th.Chrome().FocusBorder)
}
