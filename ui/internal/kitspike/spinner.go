package kitspike

import (
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// spinner draws an indeterminate spinner inside a d×d box. It asks for
// frames only while it is drawn (UK-04, §6.5): the caller stops drawing it
// when the busy state ends, and then the phase simply freezes.
type spinner struct {
	phase float64
}

// layout draws the spinner in c inside a d×d box, advancing its angle by the
// env's frame step.
func (s *spinner) layout(env *Env, gtx *layout.Context, d int, c color.NRGBA) {
	env.Keep(true) // spinning until it is no longer drawn
	s.phase += env.DT().Seconds() / 1.1
	defer op.Affine(f32.AffineId().Rotate(f32.Point{}, float32(s.phase)*2*math.Pi)).Push(gtx.Ops).Pop()
	var p clip.Path
	p.Begin(gtx.Ops)
	center := f32.Pt(float32(d)/2, float32(d)/2)
	radius := float32(d) / 2 * 0.8
	const arc = 4.4 // radians, ~250°
	for i := 0; i <= 24; i++ {
		a := arc * float64(i) / 24
		pt := f32.Pt(center.X+radius*float32(math.Cos(a)), center.Y+radius*float32(math.Sin(a)))
		if i == 0 {
			p.MoveTo(pt)
		} else {
			p.LineTo(pt)
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(2))}.Op())
}
