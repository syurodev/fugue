package kit

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Drawing helpers shared by the kit's pressable controls (Button,
// IconButton): fills, the §3.4 focus ring, the press scale and label
// measurement. Every color that reaches the screen comes from theme
// (UK-03); the only arithmetic done on one is the alpha of a §3.7
// transition and the mix between two tokens while the user watches.

// strokeRRect strokes a rounded rectangle's outline.
func strokeRRect(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	rr := clip.UniformRRect(r, radius)
	paint.FillShape(ops, c, clip.Stroke{Path: rr.Path(ops), Width: width}.Op())
}

// fillLayer fills the rounded body with c at opacity a; a transparent color
// or a zero opacity paints nothing.
func fillLayer(gtx *layout.Context, r image.Rectangle, radius int, c color.NRGBA, a float32) {
	if c.A != 0 && a > 0 {
		fillRRect(gtx.Ops, r, radius, fade(c, a))
	}
}

// focusRing strokes the keyboard focus ring of §3.4: FocusStroke wide, its
// inner edge FocusOffset outside the control's body, faded in by a (the
// §3.7 ring fade). The stroke is centered on its path, so the path sits half
// a stroke further out. c is the control's own solid tone (§3.8).
func focusRing(gtx *layout.Context, r image.Rectangle, radius int, c color.NRGBA, a float32) {
	out := gtx.Dp(theme.FocusOffset) + gtx.Dp(theme.FocusStroke)/2
	strokeRRect(gtx.Ops, r.Inset(-out), radius+out, float32(gtx.Dp(theme.FocusStroke)), fade(c, a))
}

// scaleAbout scales everything pushed after it by s about center: the
// affine transform of the §3.7 press shrink, which moves drawing and no
// event area.
func scaleAbout(gtx *layout.Context, center f32.Point, s float32) op.TransformStack {
	return op.Affine(f32.AffineId().Scale(center, f32.Pt(s, s))).Push(gtx.Ops)
}

// fade returns c with its alpha scaled by t: the transient alpha of a
// §3.7 transition. The springs settle exactly on 0 or 1, so a frame at rest
// always shows the token itself, never an in-between color (UK-03).
func fade(c color.NRGBA, t float32) color.NRGBA {
	c.A = uint8(float32(c.A)*unit01(t) + 0.5)
	return c
}

// mix blends a toward b by t in premultiplied space, so a transparent end
// brings no stray hue.
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	t = unit01(t)
	if t == 0 {
		return a
	}
	if t == 1 {
		return b
	}
	pa, pb := premul(a), premul(b)
	var out [4]float32
	for i := range out {
		out[i] = pa[i] + (pb[i]-pa[i])*t
	}
	c := a
	if out[3] <= 0 {
		c.A = 0
		return c
	}
	c.R, c.G, c.B, c.A = uint8(out[0]/out[3]+0.5), uint8(out[1]/out[3]+0.5), uint8(out[2]/out[3]+0.5), uint8(out[3]*255+0.5)
	return c
}

// premul is c as premultiplied 0..255 channels and alpha 0..1.
func premul(c color.NRGBA) [4]float32 {
	a := float32(c.A) / 255
	return [4]float32{float32(c.R) * a, float32(c.G) * a, float32(c.B) * a, a}
}

// unit01 clamps t to [0, 1].
func unit01(t float32) float32 { return min(max(t, 0), 1) }

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
