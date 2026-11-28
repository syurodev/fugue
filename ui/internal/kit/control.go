package kit

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Plumbing shared by the kit's pressable controls (Button, IconButton): the
// §4.1 state snapshot, the §3.7 motion (color, press scale, ripple, focus
// ring) and the body painting. Every color here is a theme token (UK-03);
// the only arithmetic done on one is the alpha of a transition or of the
// disabled opacity.

// controlState is the resolved §4.1 snapshot of one control for the frame.
// Priority: a disabled control shows no interaction at all, a busy one no
// hover or press wash (§4.1); the focus ring is independent of both.
type controlState struct {
	disabled bool
	busy     bool
	pressed  bool
	focused  bool
	hovered  bool
	toggled  bool
}

// pressOpts are the caller's flags of one frame.
type pressOpts struct{ disabled, busy, toggled bool }

// resolveState derives the snapshot from the options, the clickable's real
// interaction and the gallery's Preview override (Env.Preview): one pointer
// cannot hover twelve cells of a state matrix at once, so the gallery and
// the evidence tests force the interaction states and production windows
// leave Preview zero.
func resolveState(env *Env, gtx *layout.Context, tag event.Tag, click *widget.Clickable, o pressOpts) controlState {
	live := !o.disabled && !o.busy
	return controlState{
		disabled: o.disabled,
		busy:     o.busy,
		toggled:  o.toggled,
		pressed:  live && (click.Pressed() || env.previewPress(tag)),
		hovered:  live && (click.Hovered() || env.previewHover(tag)),
		focused:  !o.disabled && (env.FocusVisible(gtx, tag) || env.previewFocus(tag)),
	}
}

// controlLook is the color pairing of one control (UK-03, UK-E04): the
// fills — the variant's rest fill, the layer that replaces it under hover
// and the layer for press — the selected-state wash, the border, and the
// label color at rest and under interaction. A zero color means none.
type controlLook struct {
	base, hover, press color.NRGBA
	sel                color.NRGBA
	border             color.NRGBA
	ring               color.NRGBA
	fg, fgHover        color.NRGBA
	ripple             color.NRGBA
}

// dim scales every color of the look by a, the whole control at
// DisabledOpacity (§4.1).
func (l controlLook) dim(a float32) controlLook {
	for _, c := range []*color.NRGBA{&l.base, &l.hover, &l.press, &l.sel, &l.border, &l.ring, &l.fg, &l.fgHover, &l.ripple} {
		*c = fade(*c, a)
	}
	return l
}

// motionVals are the animated values of one frame, 0..1: the transitions of
// §3.7 that drive fills, press scale and the focus ring.
type motionVals struct{ hover, press, focus, sel float32 }

// pressable is the state both pressable controls share: the clickable, the
// §3.7 hover/press followers (snap in, Exit out with no bounce), the Snappy
// focus/selection followers and the ripple. It must be built with
// newPressable: a zero follower has no spring.
type pressable struct {
	click                        widget.Clickable
	hoverF, pressF, focusF, selF motion.Follower
	rip                          ripple
	lastPressEnd                 time.Time
	keyClicked                   bool
	polledClick                  bool
	polledAt                     time.Time
}

func newPressable() pressable {
	s := motion.Snappy()
	ex := motion.Exit()
	return pressable{
		hoverF: motion.NewFollower(ex), pressF: motion.NewFollower(ex),
		focusF: motion.NewFollower(s), selF: motion.NewFollower(s),
		rip: newRipple(),
	}
}

// Tag is the control's input tag, for focus scripting and previews.
func (c *pressable) Tag() event.Tag { return &c.click }

// Hovered reports whether the pointer is currently over the control.
func (c *pressable) Hovered() bool { return c.click.Hovered() }

// KeyClicked reports whether the last Layout activation came from Enter or
// Space rather than a pointer click (a pointer click records a Press end).
func (c *pressable) KeyClicked() bool { return c.keyClicked }

// SnapToggled snaps the selection transition to v with no animation.
func (c *pressable) SnapToggled(v bool) {
	val := float32(0)
	if v {
		val = 1
	}
	c.selF.Snap(val)
}

// ToggledProgress returns the current selection transition progress in [0, 1].
func (c *pressable) ToggledProgress() float32 { return c.selF.Value() }

// PollClick drains any pending click before Layout so a parent can update
// state (such as Toggled) before drawing the same frame. Layout still reports
// the click on its own return value within the same frame; if Layout does not
// run in that frame, the latched click expires before the next frame. Disabled
// and busy controls never report a click (§4.1).
func (c *pressable) PollClick(gtx *layout.Context, disabled, busy bool) bool {
	if c.polledClick && !c.polledAt.Equal(gtx.Now) {
		c.polledClick = false
	}
	if disabled || busy {
		_ = c.click.Clicked(*gtx)
		c.polledClick = false
		return false
	}
	if c.click.Clicked(*gtx) {
		c.polledClick = true
		c.polledAt = gtx.Now
	}
	return c.polledClick
}

// begin runs the input and the motion of a frame at the current offset:
// it reads the activation (busy blocks it, §4.1), registers the hit area of
// box — which press scaling never touches (§3.7) — resolves the state and
// advances the followers.
func (c *pressable) begin(env *Env, gtx *layout.Context, box image.Point, o pressOpts) (clicked bool, mv motionVals) {
	ig := *gtx
	prevEnd := c.lastPressEnd
	if c.polledClick && !c.polledAt.Equal(gtx.Now) {
		c.polledClick = false
	}
	if o.disabled {
		// A disabled control consumes nothing: no hover, no focus (§4.1).
		ig = gtx.Disabled()
		c.polledClick = false
	} else {
		clicked = (c.polledClick || c.click.Clicked(ig)) && !o.busy
		c.polledClick = false
	}
	var curEnd time.Time
	if h := c.click.History(); len(h) > 0 {
		curEnd = h[len(h)-1].End
	}
	c.lastPressEnd = curEnd
	c.keyClicked = clicked && (curEnd.IsZero() || curEnd.Equal(prevEnd))
	st := resolveState(env, &ig, c.Tag(), &c.click, o)
	c.click.Layout(ig, func(layout.Context) layout.Dimensions { return layout.Dimensions{Size: box} })
	mv = motionVals{
		hover: followSnapIn(env, &c.hoverF, st.hovered),
		press: followSnapIn(env, &c.pressF, st.pressed),
		focus: followState(env, &c.focusF, st.focused),
		sel:   followState(env, &c.selF, st.toggled),
	}
	c.rip.step(env, c.click.History(), st.pressed, !o.disabled && !o.busy, box)
	return clicked, mv
}

// paint draws the control: everything inside one affine scale about the
// body's center — the press shrink of §3.7, drawing only — the body, the
// ripple clipped to it, the content in the label color of the moment, and
// the focus ring last. The ring sits outside the body (§3.4), so no parent
// may clip a control.
func (c *pressable) paint(env *Env, gtx *layout.Context, r image.Rectangle, radius int, look controlLook, mv motionVals, content func(fg color.NRGBA)) {
	c.paintWith(env, gtx, r, radius, look, mv, content, false)
}

// paintFocusWash is paint for the app chrome buttons of §3.4 (FUG-109):
// keyboard focus drives the variant's hover wash instead of the outer ring.
func (c *pressable) paintFocusWash(env *Env, gtx *layout.Context, r image.Rectangle, radius int, look controlLook, mv motionVals, content func(fg color.NRGBA)) {
	c.paintWith(env, gtx, r, radius, look, mv, content, true)
}

// paintWith is the one paint body. With focusWash the §3.4 focus transition
// drives the hover wash instead of the ring, so a focused chrome button
// reads exactly like a hovered one and nothing is drawn outside the body.
// Without it the op stream is the one paint has always emitted.
func (c *pressable) paintWith(env *Env, gtx *layout.Context, r image.Rectangle, radius int, look controlLook, mv motionVals, content func(fg color.NRGBA), focusWash bool) {
	scale := float32(1)
	if !env.ReducedMotion {
		scale = 1 - (1-theme.MotionPress)*unit01(mv.press)
	}
	center := f32.Pt(float32(r.Min.X+r.Max.X)/2, float32(r.Min.Y+r.Max.Y)/2)
	defer scaleAbout(gtx, center, scale).Pop()

	wash := mv
	if focusWash {
		// The focus transition takes the hover channel, so the wash rides
		// the same Snappy fade the ring used and layerWeights keeps its
		// replace-stronger-with-stronger rule (UK-E04).
		wash.hover = max(unit01(mv.hover), unit01(mv.focus))
	}
	base, sel, hover, press := layerWeights(wash)
	fillLayer(gtx, r, radius, look.base, base)
	fillLayer(gtx, r, radius, look.sel, sel)
	if focusWash {
		// A focused toggled button stacks the wash over the selected fill —
		// SelectedWash, or the rail's NoSelectedFill + external pill —
		// because layerWeights lets the selected wash replace hover, which
		// would leave focus invisible on it (§3.4, FUG-109). Press still
		// replaces both.
		fillLayer(gtx, r, radius, look.hover, unit01(mv.focus)*(1-unit01(mv.press))*unit01(mv.sel))
	}
	fillLayer(gtx, r, radius, look.hover, hover)
	fillLayer(gtx, r, radius, look.press, press)
	if look.border.A != 0 {
		strokeRRect(gtx.Ops, r, radius, float32(gtx.Dp(theme.Hairline)), look.border)
	}
	c.rip.draw(gtx, r, radius, look.ripple)
	fgLift := max(unit01(mv.hover), unit01(mv.press))
	if focusWash {
		fgLift = max(fgLift, unit01(mv.focus))
	}
	content(mix(look.fg, look.fgHover, fgLift))
	if !focusWash && mv.focus > 0 {
		focusRing(gtx, r, radius, look.ring, mv.focus)
	}
}

// layerWeights are the opacities of the four fill layers of a frame, from
// the transitions: the stronger state replaces the weaker one instead of
// stacking on it — press replaces the selected wash, which replaces hover,
// and hover and the selected wash replace the rest tint. Gio blends in
// linear light, where stacked light washes on a dark surface rise much
// faster than their alpha suggests, so every replacement here is a contrast
// decision (UK-E04).
func layerWeights(mv motionVals) (base, sel, hover, press float32) {
	h, p, s := unit01(mv.hover), unit01(mv.press), unit01(mv.sel)
	return 1 - max(h, p, s), s * (1 - p), h * (1 - p) * (1 - s), p
}

// ripple is the §3.7 ripple: a disc spreading from the press point on
// Smooth, clipped to the body, and faded out on Exit once it has spread
// half way or the press ended, whichever comes first — a held press does not
// keep a disc over the label. Its color is a faint perceived-11% token over
// the whole-body pressed fill (RippleWash, RippleOnAction). A reduced-motion
// window never starts one.
type ripple struct {
	at    image.Point
	start time.Time
	size  motion.Follower
	alpha motion.Presence
	age   int
	reach float32
}

func newRipple() ripple {
	return ripple{size: motion.NewFollower(motion.Smooth()), alpha: motion.NewPresence(motion.Smooth(), motion.Exit())}
}

// step starts a ripple on a new real press, at the press point — a forced
// preview (Env.Preview) has no press and shows none —, lets it fade once it has spread or the press
// ended — at least one frame later, so a tap that is down and up in one
// frame still shows — and advances it, asking for frames only while it
// moves.
func (r *ripple) step(env *Env, h []widget.Press, pressed, enabled bool, box image.Point) {
	if env.ReducedMotion || !enabled {
		return
	}
	if n := len(h); n > 0 && h[n-1].Start != r.start {
		r.begin(h[n-1].Start, h[n-1].Position, box)
	}
	if r.size.Value() >= 0.5 || (!pressed && r.age > 0) {
		r.alpha.Show(false)
	}
	r.age++
	r.size.Follow(1, env.DT())
	moving := r.alpha.Advance(env.DT())
	if moving || r.size.Moving() {
		env.Keep()
	}
}

// begin restarts the spread at p; reach is the distance to the farthest
// corner, so the disc covers the whole body when it is done.
func (r *ripple) begin(start time.Time, p, box image.Point) {
	r.start, r.at, r.age = start, p, 0
	r.reach = float32(math.Hypot(float64(max(p.X, box.X-p.X)), float64(max(p.Y, box.Y-p.Y))))
	r.size.Snap(0)
	r.alpha.Show(true)
}

// draw paints the disc in c, clipped to the rounded body.
func (r *ripple) draw(gtx *layout.Context, body image.Rectangle, radius int, c color.NRGBA) {
	a := r.alpha.Progress()
	if !r.alpha.Visible() || a <= 0 {
		return
	}
	defer clip.UniformRRect(body, radius).Push(gtx.Ops).Pop()
	rad := r.size.Value() * r.reach
	d := image.Pt(int(rad+0.5), int(rad+0.5))
	// The press point is local to the control's hit area, which sits at the
	// body's corner (a field's trailing button is not at its field's origin).
	centre := r.at.Add(body.Min)
	disc := clip.Ellipse{Min: centre.Sub(d), Max: centre.Add(d)}.Op(gtx.Ops)
	paint.FillShape(gtx.Ops, fade(c, a), disc)
}

// followSnapIn snaps to 1 on the first frame when target is true (hover enter,
// press down), and fades out on Exit when target is false with no bounce.
// Reduced motion snaps both ways.
func followSnapIn(env *Env, f *motion.Follower, target bool) float32 {
	if env.ReducedMotion || target {
		v := float32(0)
		if target {
			v = 1
		}
		f.Snap(v)
		return v
	}
	f.Use(motion.Exit())
	v := f.Follow(0, env.DT())
	if f.Moving() {
		env.Keep()
	}
	return v
}

// followState moves one §3.7 transition toward target on Snappy and asks
// for the next frame while it still moves (the kit's frame discipline,
// §6.5): a page of settled controls schedules nothing. Reduced motion snaps
// the value, the instant change of UK-04.
func followState(env *Env, f *motion.Follower, target bool) float32 {
	v := float32(0)
	if target {
		v = 1
	}
	return followTo(env, f, v)
}

// followTo is followState for any target value, such as the place a menu's
// highlight slides to.
func followTo(env *Env, f *motion.Follower, v float32) float32 {
	if env.ReducedMotion {
		f.Snap(v)
		return v
	}
	v = f.Follow(v, env.DT())
	if f.Moving() {
		env.Keep()
	}
	return v
}

// drawIconAt draws one icon in a size×size slot at p, validating the name
// at the draw site so an unknown icon surfaces as the control's error.
func drawIconAt(env *Env, gtx *layout.Context, p image.Point, name IconName, size unit.Dp, c color.NRGBA) error {
	var err error
	at(gtx, p, func(gtx *layout.Context) {
		_, err = env.Icon(gtx, name, size, c)
	})
	return err
}

// drawIconScaled is drawIconAt scaled by s about the slot's center: the
// 0.8 entry and exit of an icon that cross-fades with another (§3.7).
func drawIconScaled(env *Env, gtx *layout.Context, p image.Point, name IconName, size unit.Dp, c color.NRGBA, s float32) error {
	half := float32(gtx.Dp(size)) / 2
	defer scaleAbout(gtx, f32.Pt(float32(p.X)+half, float32(p.Y)+half), s).Pop()
	return drawIconAt(env, gtx, p, name, size, c)
}

// labelClose and labelPin are the accessible names of the kit's own chrome
// controls: the header close button of a popover and its pin toggle (§6.16),
// the toast's close button (§6.18).
const (
	labelClose = "Close"
	labelPin   = "Pin"
)
