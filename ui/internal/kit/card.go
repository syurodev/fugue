package kit

import (
	"image"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const cardPressScale = float32(0.99)

// CardProps configures a Card container for one frame (§6.19).
type CardProps struct {
	Clickable bool
	Disabled  bool
	Padding   unit.Dp // defaults to theme.SpaceLG (12 dp)
	Radius    unit.Dp // defaults to theme.RadiusLG (14 dp)
}

// CardState holds the long-lived interaction and motion state of a Card
// (§6.19).
type CardState struct {
	click  widget.Clickable
	hoverF motion.Follower
	pressF motion.Follower
	focusF motion.Follower
}

// NewCardState builds a CardState at rest.
func NewCardState() *CardState {
	sn := motion.Snappy()
	ex := motion.Exit()
	return &CardState{
		hoverF: motion.NewFollower(ex),
		pressF: motion.NewFollower(ex),
		focusF: motion.NewFollower(sn),
	}
}

// Tag returns the card's input tag when Clickable, for focus scripting and
// Preview.
func (s *CardState) Tag() event.Tag { return &s.click }

// Layout lays out the Card with SurfaceRaised, BorderSubtle, radius.lg,
// space.lg padding and no shadow (§6.19). When Clickable, hover and press
// appear on the first frame and fade out on Exit with no bounce, and press
// scales to 0.99.
func (s *CardState) Layout(env *Env, gtx *layout.Context, pr CardProps, body layout.Widget) (dims layout.Dimensions, clicked bool, err error) {
	padDp := pr.Padding
	if padDp <= 0 {
		padDp = theme.SpaceLG
	}
	radDp := pr.Radius
	if radDp <= 0 {
		radDp = theme.RadiusLG
	}
	pad := gtx.Dp(padDp)
	rad := gtx.Dp(radDp)

	// Measure/record body first so the card wraps its content tightly.
	rec := op.Record(gtx.Ops)
	innerGtx := *gtx
	innerGtx.Constraints.Min = image.Point{}
	innerGtx.Constraints.Max = image.Pt(max(gtx.Constraints.Max.X-2*pad, 0), max(gtx.Constraints.Max.Y-2*pad, 0))
	bodyDims := body(innerGtx)
	call := rec.Stop()

	size := image.Pt(
		max(bodyDims.Size.X+2*pad, gtx.Constraints.Min.X),
		max(bodyDims.Size.Y+2*pad, gtx.Constraints.Min.Y),
	)
	r := image.Rectangle{Max: size}

	var hover, press, focus float32
	if pr.Clickable {
		ig := *gtx
		if pr.Disabled {
			ig = gtx.Disabled()
		} else {
			clicked = s.click.Clicked(ig)
		}
		st := resolveState(env, &ig, s.Tag(), &s.click, pressOpts{disabled: pr.Disabled})
		s.click.Layout(ig, func(layout.Context) layout.Dimensions { return layout.Dimensions{Size: size} })
		hover = followSnapIn(env, &s.hoverF, st.hovered)
		press = followSnapIn(env, &s.pressF, st.pressed)
		focus = followState(env, &s.focusF, st.focused)
	}

	s.paint(env, gtx, r, rad, pad, pr, hover, press, focus, call)
	return layout.Dimensions{Size: size}, clicked, nil
}

func (s *CardState) paint(env *Env, gtx *layout.Context, r image.Rectangle, rad, pad int, pr CardProps, hover, press, focus float32, body op.CallOp) {
	c := env.Th.Chrome()
	opScale := float32(1)
	if pr.Disabled {
		opScale = theme.DisabledOpacity
	}
	scale := float32(1)
	if pr.Clickable && !env.ReducedMotion {
		scale = 1 - (1-cardPressScale)*unit01(press)
	}
	center := f32.Pt(float32(r.Min.X+r.Max.X)/2, float32(r.Min.Y+r.Max.Y)/2)
	defer scaleAbout(gtx, center, scale).Pop()

	fillLayer(gtx, r, rad, fade(c.SurfaceBase, opScale), 1)
	if hover > 0 {
		fillLayer(gtx, r, rad, fade(c.HoverWash, opScale), unit01(hover)*(1-unit01(press)))
	}
	if press > 0 {
		fillLayer(gtx, r, rad, fade(c.PressedWash, opScale), unit01(press))
	}
	strokeRRect(gtx.Ops, r, rad, float32(gtx.Dp(theme.Hairline)), fade(c.BorderSubtle, opScale))

	at(gtx, image.Pt(pad, pad), func(gtx *layout.Context) {
		body.Add(gtx.Ops)
	})
	if focus > 0 {
		focusRing(gtx, r, rad, fade(c.FocusBorder, opScale), focus)
	}
}
