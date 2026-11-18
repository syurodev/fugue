package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// SwitchStatus is the async confirmation status of a Switch (§6.11).
type SwitchStatus int

// The §6.11 switch statuses.
const (
	SwitchIdle SwitchStatus = iota
	SwitchPending
	SwitchError
)

// SwitchProps configures a Switch for one frame (§6.11).
type SwitchProps struct {
	Label    string
	Checked  bool
	Status   SwitchStatus
	Disabled bool
}

// SwitchState holds the long-lived interaction and motion state of one Switch
// (§6.11).
type SwitchState struct {
	click    widget.Clickable
	thumbF   motion.Follower // Bouncy thumb position (0..1)
	stretchF motion.Follower // Bouncy horizontal stretch on press (0..1)
	trackF   motion.Follower // Snappy track color (0..1)
	hoverF   motion.Follower // Snappy hover
	pressF   motion.Follower // Snappy press
	focusF   motion.Follower // Snappy focus ring
	errF     motion.Follower // Snappy error highlight
	spin     Spinner
}

// NewSwitchState builds a SwitchState at rest.
func NewSwitchState() *SwitchState {
	sn := motion.Snappy()
	bc := motion.Bouncy()
	return &SwitchState{
		thumbF:   motion.NewFollower(bc),
		stretchF: motion.NewFollower(bc),
		trackF:   motion.NewFollower(sn),
		hoverF:   motion.NewFollower(sn),
		pressF:   motion.NewFollower(sn),
		focusF:   motion.NewFollower(sn),
		errF:     motion.NewFollower(sn),
	}
}

// Tag returns the control's input tag for focus scripting and Preview.
func (s *SwitchState) Tag() event.Tag { return &s.click }

// ThumbProgress returns the current thumb position in [0..1] (may briefly
// overshoot under Bouncy), exposed for motion/pending evidence tests.
func (s *SwitchState) ThumbProgress() float32 { return s.thumbF.Value() }

// Measure returns the Switch's layout dimensions without drawing or reading
// input.
func (s *SwitchState) Measure(env *Env, gtx *layout.Context, pr SwitchProps) layout.Dimensions {
	return layout.Dimensions{Size: s.measureSize(env, gtx, pr)}
}

func (s *SwitchState) measureSize(env *Env, gtx *layout.Context, pr SwitchProps) image.Point {
	tw := gtx.Dp(theme.SwitchTrackW)
	th := gtx.Dp(theme.SwitchTrackH)
	h := max(th, gtx.Dp(theme.HitMin))
	w := tw
	if pr.Label != "" {
		lbl := measureLabel(env, gtx, theme.TypeBody(), pr.Label)
		w += gtx.Dp(theme.SpaceMD) + lbl.X
		h = max(h, lbl.Y)
	}
	if pr.Status == SwitchPending {
		w += gtx.Dp(theme.SpaceSM) + gtx.Dp(theme.IconXS)
	}
	return image.Pt(w, h)
}

// Layout lays out the Switch, handles clicks/Space (ignored while Disabled or
// SwitchPending), and returns whether a toggle was requested. While
// SwitchPending, the thumb stays at the confirmed Checked value and never
// fakes "on" (§6.11).
func (s *SwitchState) Layout(env *Env, gtx *layout.Context, pr SwitchProps) (toggled bool, err error) {
	size := s.measureSize(env, gtx, pr)
	busy := pr.Status == SwitchPending
	ig := *gtx
	if pr.Disabled {
		ig = gtx.Disabled()
	} else {
		toggled = s.click.Clicked(ig) && !busy
	}
	st := resolveState(env, &ig, s.Tag(), &s.click, pressOpts{disabled: pr.Disabled, busy: busy})
	s.click.Layout(ig, func(layout.Context) layout.Dimensions { return layout.Dimensions{Size: size} })

	// Never fake "on" while pending: thumb and track follow confirmed Checked.
	thumb := followState(env, &s.thumbF, pr.Checked)
	stretch := followState(env, &s.stretchF, st.pressed)
	track := followState(env, &s.trackF, pr.Checked)
	hover := followState(env, &s.hoverF, st.hovered)
	press := followState(env, &s.pressF, st.pressed)
	focus := followState(env, &s.focusF, st.focused)
	errT := followState(env, &s.errF, pr.Status == SwitchError)

	s.draw(env, gtx, size, pr, thumb, stretch, track, hover, press, focus, errT)
	return toggled, nil
}

func (s *SwitchState) draw(env *Env, gtx *layout.Context, size image.Point, pr SwitchProps, thumb, stretch, track, hover, press, focus, errT float32) {
	c := env.Th.Chrome()
	opScale := float32(1)
	if pr.Disabled {
		opScale = theme.DisabledOpacity
	}
	tw := gtx.Dp(theme.SwitchTrackW)
	th := gtx.Dp(theme.SwitchTrackH)
	ty := (size.Y - th) / 2
	trackR := image.Rect(0, ty, tw, ty+th)
	rad := th / 2

	s.drawTrackAndThumb(env, gtx, trackR, rad, &c, opScale, thumb, stretch, track, hover, press, focus, errT)

	x := tw + gtx.Dp(theme.SpaceMD)
	if pr.Status == SwitchPending {
		sy := (size.Y - gtx.Dp(theme.IconXS)) / 2
		at(gtx, image.Pt(x, sy), func(gtx *layout.Context) {
			s.spin.Layout(env, gtx, gtx.Dp(theme.IconXS), fade(c.FgMuted, opScale))
		})
		x += gtx.Dp(theme.IconXS) + gtx.Dp(theme.SpaceSM)
	}
	if pr.Label != "" {
		ly := centerLabelY(env, gtx, theme.TypeBody(), size.Y)
		ink := fade(c.Fg, opScale)
		if errT > 0 {
			ink = mix(ink, fade(env.Th.Status().Danger, opScale), unit01(errT))
		}
		drawRunAt(env, gtx, image.Pt(x, ly), size.X-x, size.Y-ly, theme.TypeBody(), ink, pr.Label, false)
	}
}

func (s *SwitchState) drawTrackAndThumb(env *Env, gtx *layout.Context, r image.Rectangle, rad int, c *theme.Chrome, opScale, thumb, stretch, track, hover, press, focus, errT float32) {
	offFill := mix(c.FillSoft, c.FillSoftHover, unit01(hover))
	onFill := mix(c.Action, c.ActionHover, unit01(hover))
	onFill = mix(onFill, c.ActionPressed, unit01(press))
	trackFill := mix(offFill, onFill, unit01(track))
	if errT > 0 {
		trackFill = mix(trackFill, env.Th.StatusTone(theme.StatusDanger).Fill, unit01(errT))
	}
	fillLayer(gtx, r, rad, fade(trackFill, opScale), 1)
	if errT > 0 {
		strokeRRect(gtx.Ops, r, rad, float32(gtx.Dp(theme.Hairline)), fade(env.Th.Status().Danger, opScale*unit01(errT)))
	} else if track < 1 {
		strokeRRect(gtx.Ops, r, rad, float32(gtx.Dp(theme.Hairline)), fade(c.BorderSubtle, opScale*(1-unit01(track))))
	}

	// Thumb: Bouncy travel + horizontal stretch on press (§6.11).
	d := gtx.Dp(theme.SwitchThumbD)
	pad := (r.Dy() - d) / 2
	extraW := int(float32(gtx.Dp(theme.SpaceXS))*max(stretch, 0) + 0.5)
	if env.ReducedMotion {
		extraW = 0
	}
	thumbW := d + extraW
	travel := r.Dx() - 2*pad - thumbW
	tx := r.Min.X + pad + int(float32(travel)*thumb+0.5)
	tx = min(max(tx, r.Min.X+pad), r.Max.X-pad-thumbW)
	ty := r.Min.Y + pad
	thumbR := image.Rect(tx, ty, tx+thumbW, ty+d)
	thumbCol := mix(c.SurfaceRaised, c.ActionFg, unit01(track))
	if track < 0.5 {
		thumbCol = mix(c.Fg, c.ActionFg, unit01(track))
	}
	fillLayer(gtx, thumbR, d/2, fade(thumbCol, opScale), 1)

	if focus > 0 {
		focusRing(gtx, r, rad, fade(c.FocusBorder, opScale), focus)
	}
}
