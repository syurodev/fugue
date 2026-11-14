package kit

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Spinner is the §6.5 indeterminate progress indicator for a busy control,
// drawn in a d×d box. It spins at a steady rate (no spring, §3.7)
// and asks for frames only while it is drawn: the caller stops laying it
// out when the busy state ends and the phase simply freezes. Under reduced
// motion it holds still (UK-04); a shown-alone dot paired with text carries
// the same meaning (the pairing is the caller's job).
//
// The 150 ms delay before a spinner shows lives with the busy state, not
// here: a spinner that flashes for a fast operation is worse than none.
type Spinner struct {
	// phase is the accumulated spin angle in turns, advanced by each
	// frame's time step.
	phase float64
}

// Layout draws the spinner in c inside d×d, advancing its phase by the
// frame step. Keep marks the frame as moving only while it is drawn, so an
// undrawn spinner never schedules a frame.
func (s *Spinner) Layout(env *Env, gtx *layout.Context, d int, c color.NRGBA) layout.Dimensions {
	if !env.ReducedMotion {
		env.Keep()
		s.phase += env.DT().Seconds() / 1.1
	}
	defer op.Affine(f32.AffineId().Rotate(f32.Point{}, float32(s.phase)*2*math.Pi)).Push(gtx.Ops).Pop()
	var p clip.Path
	p.Begin(gtx.Ops)
	center := f32.Pt(float32(d)/2, float32(d)/2)
	radius := float32(d) / 2 * 0.8
	const arc = 4.4 // radians, about 250 degrees
	for i := range 25 {
		a := arc * float64(i) / 24
		pt := f32.Pt(center.X+radius*float32(math.Cos(a)), center.Y+radius*float32(math.Sin(a)))
		if i == 0 {
			p.MoveTo(pt)
		} else {
			p.LineTo(pt)
		}
	}
	// The ring reads at twice a hairline; there is no dedicated token in
	// §3.4, so it is derived from one.
	width := float32(gtx.Dp(2 * theme.Hairline))
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
	return layout.Dimensions{Size: image.Pt(d, d)}
}
