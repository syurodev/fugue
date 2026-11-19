package kit

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// CheckState is the three-state value of a Checkbox (§6.10).
type CheckState int

// The three §6.10 checkbox states.
const (
	CheckOff CheckState = iota
	CheckOn
	CheckMixed
)

// Next cycles the checkbox state: off -> on -> mixed -> off (§6.10).
func (s CheckState) Next() CheckState {
	return (s + 1) % 3
}

// CheckboxProps configures a Checkbox for one frame (§6.10).
type CheckboxProps struct {
	Label    string
	Value    CheckState
	Disabled bool
}

// CheckboxState holds the long-lived interaction and motion state of one
// Checkbox (§6.10).
type CheckboxState struct {
	click  widget.Clickable
	hoverF motion.Follower
	pressF motion.Follower
	focusF motion.Follower
	fillF  motion.Follower
	checkF motion.Follower
	mixedF motion.Follower
}

// NewCheckboxState builds a CheckboxState at rest.
func NewCheckboxState() *CheckboxState {
	s := motion.Snappy()
	return &CheckboxState{
		hoverF: motion.NewFollower(s),
		pressF: motion.NewFollower(s),
		focusF: motion.NewFollower(s),
		fillF:  motion.NewFollower(s),
		checkF: motion.NewFollower(s),
		mixedF: motion.NewFollower(s),
	}
}

// Tag returns the control's input tag for focus scripting and Preview.
func (s *CheckboxState) Tag() event.Tag { return &s.click }

// Measure returns the Checkbox's layout dimensions without drawing or reading
// input.
func (s *CheckboxState) Measure(env *Env, gtx *layout.Context, pr CheckboxProps) layout.Dimensions {
	return layout.Dimensions{Size: s.measureSize(env, gtx, pr)}
}

func (s *CheckboxState) measureSize(env *Env, gtx *layout.Context, pr CheckboxProps) image.Point {
	box := gtx.Dp(theme.IconMD) // 16 dp (§6.10)
	h := max(box, gtx.Dp(theme.HitMin))
	w := max(box, gtx.Dp(theme.HitMin))
	if pr.Label != "" {
		lbl := measureLabel(env, gtx, theme.TypeBody(), pr.Label)
		w = box + gtx.Dp(theme.SpaceMD) + lbl.X
		h = max(h, lbl.Y)
	}
	return image.Pt(w, h)
}

// Layout lays out the Checkbox, handles click/Space activation over both the
// box and its label, and returns whether it was activated and the next state.
func (s *CheckboxState) Layout(env *Env, gtx *layout.Context, pr CheckboxProps) (changed bool, next CheckState, err error) {
	size := s.measureSize(env, gtx, pr)
	ig := *gtx
	if pr.Disabled {
		ig = gtx.Disabled()
	} else {
		changed = s.click.Clicked(ig)
	}
	next = pr.Value
	if changed {
		next = pr.Value.Next()
	}
	st := resolveState(env, &ig, s.Tag(), &s.click, pressOpts{disabled: pr.Disabled})
	s.click.Layout(ig, func(layout.Context) layout.Dimensions { return layout.Dimensions{Size: size} })

	hover := followState(env, &s.hoverF, st.hovered)
	press := followState(env, &s.pressF, st.pressed)
	focus := followState(env, &s.focusF, st.focused)
	filled := pr.Value == CheckOn || pr.Value == CheckMixed
	fillT := followState(env, &s.fillF, filled)
	checkT := followState(env, &s.checkF, pr.Value == CheckOn)
	mixedT := followState(env, &s.mixedF, pr.Value == CheckMixed)

	s.draw(env, gtx, size, pr, hover, press, focus, fillT, checkT, mixedT)
	return changed, next, nil
}

func (s *CheckboxState) draw(env *Env, gtx *layout.Context, size image.Point, pr CheckboxProps, hover, press, focus, fillT, checkT, mixedT float32) {
	c := env.Th.Chrome()
	opScale := float32(1)
	if pr.Disabled {
		opScale = theme.DisabledOpacity
	}
	boxPx := gtx.Dp(theme.IconMD)
	boxY := (size.Y - boxPx) / 2
	boxR := image.Rect(0, boxY, boxPx, boxY+boxPx)
	rad := gtx.Dp(theme.RadiusSM)

	s.drawBox(env, gtx, boxR, rad, &c, opScale, hover, press, focus, fillT, checkT, mixedT)

	if pr.Label != "" {
		lx := boxPx + gtx.Dp(theme.SpaceMD)
		ly := centerLabelY(env, gtx, theme.TypeBody(), size.Y)
		ink := fade(c.Fg, opScale)
		drawRunAt(env, gtx, image.Pt(lx, ly), size.X-lx, size.Y-ly, theme.TypeBody(), ink, pr.Label, false)
	}
}

func (s *CheckboxState) drawBox(env *Env, gtx *layout.Context, r image.Rectangle, rad int, c *theme.Chrome, opScale, hover, press, focus, fillT, checkT, mixedT float32) {
	scale := float32(1)
	if !env.ReducedMotion {
		scale = 1 - (1-theme.MotionPress)*unit01(press)
	}
	center := f32.Pt(float32(r.Min.X+r.Max.X)/2, float32(r.Min.Y+r.Max.Y)/2)
	defer scaleAbout(gtx, center, scale).Pop()

	// Unchecked rest/hover/press layers.
	fillLayer(gtx, r, rad, fade(c.FillSoft, opScale), (1-unit01(fillT))*(1-unit01(hover)))
	fillLayer(gtx, r, rad, fade(c.HoverWash, opScale), (1-unit01(fillT))*unit01(hover)*(1-unit01(press)))
	fillLayer(gtx, r, rad, fade(c.PressedWash, opScale), (1-unit01(fillT))*unit01(press))
	if fillT < 1 {
		strokeRRect(gtx.Ops, r, rad, float32(gtx.Dp(theme.Hairline)), fade(c.BorderSubtle, opScale*(1-unit01(fillT))))
	}

	// Filled on/mixed layer.
	actionFill := mix(c.Action, c.ActionHover, unit01(hover))
	actionFill = mix(actionFill, c.ActionPressed, unit01(press))
	fillLayer(gtx, r, rad, fade(actionFill, opScale), unit01(fillT))

	glyphColor := fade(c.ActionFg, opScale)
	if checkT > 0 {
		drawCheckStroke(gtx.Ops, r, glyphColor, float32(gtx.Dp(theme.IconMD))*theme.IconStroke(theme.IconMD)/iconGrid, unit01(checkT))
	}
	if mixedT > 0 {
		drawMixedStroke(gtx.Ops, r, glyphColor, float32(gtx.Dp(theme.IconMD))*theme.IconStroke(theme.IconMD)/iconGrid, unit01(mixedT))
	}
	if focus > 0 {
		focusRing(gtx, r, rad, fade(c.FocusBorder, opScale), focus)
	}
}

// drawCheckStroke draws the check mark progressively along its two segments as
// t runs from 0 to 1 (§6.10).
func drawCheckStroke(ops *op.Ops, r image.Rectangle, c color.NRGBA, strokeW, t float32) {
	if t <= 0 || c.A == 0 {
		return
	}
	w, h := float32(r.Dx()), float32(r.Dy())
	p0 := f32.Pt(float32(r.Min.X)+w*5/24, float32(r.Min.Y)+h*12.5/24)
	p1 := f32.Pt(float32(r.Min.X)+w*10/24, float32(r.Min.Y)+h*17.5/24)
	p2 := f32.Pt(float32(r.Min.X)+w*19/24, float32(r.Min.Y)+h*7.5/24)

	const split = float32(0.38)
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(p0)
	if t <= split {
		u := t / split
		path.LineTo(f32.Pt(p0.X+(p1.X-p0.X)*u, p0.Y+(p1.Y-p0.Y)*u))
	} else {
		path.LineTo(p1)
		u := (t - split) / (1 - split)
		path.LineTo(f32.Pt(p1.X+(p2.X-p1.X)*u, p1.Y+(p2.Y-p1.Y)*u))
	}
	paint.FillShape(ops, c, clip.Stroke{Path: path.End(), Width: strokeW}.Op())
}

// drawMixedStroke draws the indeterminate minus bar expanding from the center.
func drawMixedStroke(ops *op.Ops, r image.Rectangle, c color.NRGBA, strokeW, t float32) {
	if t <= 0 || c.A == 0 {
		return
	}
	cx := float32(r.Min.X+r.Max.X) / 2
	cy := float32(r.Min.Y+r.Max.Y) / 2
	half := float32(r.Dx()) * 6 / 24 * t
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(cx-half, cy))
	path.LineTo(f32.Pt(cx+half, cy))
	paint.FillShape(ops, c, clip.Stroke{Path: path.End(), Width: strokeW}.Op())
}
