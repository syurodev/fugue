package main

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

// fade returns c with its alpha scaled by a, clamped to [0, 1].
func fade(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A) * min(max(a, 0), 1))
	return c
}

// mix blends from a to b by t in [0, 1].
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	t = min(max(t, 0), 1)
	lerp := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.NRGBA{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: lerp(a.A, b.A)}
}

// fillRRect fills a rounded rectangle.
func fillRRect(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	paint.FillShape(ops, c, clip.UniformRRect(r, radius).Op(ops))
}

// strokeRRect strokes a rounded rectangle's outline.
func strokeRRect(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	rr := clip.UniformRRect(r, radius)
	paint.FillShape(ops, c, clip.Stroke{Path: rr.Path(ops), Width: width}.Op())
}

// shadow fakes a soft drop shadow under r with a few widening translucent
// layers: cheap, and Gio has no blur.
func shadow(gtx *layout.Context, r image.Rectangle, radius int, strength float32) {
	for i, a := range []float32{0.10, 0.06, 0.035, 0.02} {
		grow := gtx.Dp(unit.Dp(2 + 3*float32(i)))
		rr := image.Rectangle{Min: r.Min.Sub(image.Pt(grow, grow-gtx.Dp(4))), Max: r.Max.Add(image.Pt(grow, grow+gtx.Dp(4)))}
		fillRRect(gtx.Ops, rr, radius+grow, color.NRGBA{A: uint8(255 * a * strength)})
	}
}

// scaleAround pushes a scale by s about the point c.
func scaleAround(ops *op.Ops, c f32.Point, s float32) op.TransformStack {
	return op.Affine(f32.AffineId().Scale(c, f32.Pt(s, s))).Push(ops)
}

// at runs fn with the origin moved to p and returns its dimensions.
func at(gtx *layout.Context, p image.Point, fn func(gtx *layout.Context) layout.Dimensions) layout.Dimensions {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	return fn(gtx)
}

// chevron strokes a downward chevron of the given size centred on c, turned
// by angle radians.
func chevron(ops *op.Ops, c f32.Point, size, angle, width float32, col color.NRGBA) {
	defer op.Affine(f32.AffineId().Rotate(f32.Point{}, angle).Offset(c)).Push(ops).Pop()
	h := size / 2
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(f32.Pt(-h, -h/2))
	p.LineTo(f32.Pt(0, h/2))
	p.LineTo(f32.Pt(h, -h/2))
	paint.FillShape(ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}

// check strokes a check mark of the given size centred on c.
func check(ops *op.Ops, c f32.Point, size, width float32, col color.NRGBA) {
	h := size / 2
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(c.Add(f32.Pt(-h, 0)))
	p.LineTo(c.Add(f32.Pt(-h/3, h*0.65)))
	p.LineTo(c.Add(f32.Pt(h, -h*0.6)))
	paint.FillShape(ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}
