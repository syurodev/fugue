package kit

import (
	"image"
	"image/color"
	"math"

	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Progress, ui-kit §6.5: a linear progress bar (radius.full) with determinate
// and indeterminate modes. In determinate mode, the filled portion animates to
// Value via Smooth and stops requesting frames once settled. In indeterminate
// mode, a pill sweeps across the track at a steady rate while laid out; under
// ReducedMotion (UK-04), motion stops and a static centered segment renders
// instead.
//
//	dims := progressState.Layout(env, gtx, kit.ProgressProps{
//	    Value: 0.65,
//	    Tone:  kit.ProgressInfo,
//	})

// ProgressTone selects the indicator color from theme tokens (§3.5, §6.5).
type ProgressTone uint8

const (
	ProgressDefault ProgressTone = iota
	ProgressInfo
	ProgressSuccess
	ProgressWarning
	ProgressDanger
)

// ProgressProps are the per-frame, caller-owned properties of Progress (UK-02).
type ProgressProps struct {
	Value         float32 // determinate fraction in [0, 1]
	Indeterminate bool
	Tone          ProgressTone
}

// ProgressState is the long-lived state the caller keeps per Progress bar (UK-02).
type ProgressState struct {
	valueF motion.Follower
	init   bool
	phase  float64
}

// NewProgressState builds the state for one Progress bar.
func NewProgressState() *ProgressState {
	return &ProgressState{valueF: motion.NewFollower(motion.Smooth())}
}

// ResolveProgressColor returns the indicator color for tone on th.
func ResolveProgressColor(th *theme.Theme, tone ProgressTone) color.NRGBA {
	st := th.Status()
	switch tone {
	case ProgressInfo:
		return st.Info
	case ProgressSuccess:
		return st.Success
	case ProgressWarning:
		return st.Warning
	case ProgressDanger:
		return st.Danger
	default:
		return th.Chrome().Action
	}
}

// Measure returns the Progress bar's natural box without advancing motion.
func (s *ProgressState) Measure(gtx *layout.Context) layout.Dimensions {
	return layout.Dimensions{Size: progressSize(gtx)}
}

// Layout draws the Progress bar and advances its animation when active (§6.5).
func (s *ProgressState) Layout(env *Env, gtx *layout.Context, p ProgressProps) layout.Dimensions {
	box := progressSize(gtx)
	radius := box.Y / 2
	track := image.Rectangle{Max: box}

	fillRRect(gtx.Ops, track, radius, env.Th.Chrome().FillSoft)
	fill := ResolveProgressColor(env.Th, p.Tone)

	defer clip.UniformRRect(track, radius).Push(gtx.Ops).Pop()
	if p.Indeterminate {
		s.drawIndeterminate(env, gtx, box, radius, fill)
		return layout.Dimensions{Size: box}
	}

	target := unit01(p.Value)
	if !s.init {
		s.init = true
		s.valueF.Snap(target)
	}
	v := unit01(followTo(env, &s.valueF, target))
	fillW := int(math.Round(float64(v) * float64(box.X)))
	if fillW > 0 {
		fillRRect(gtx.Ops, image.Rect(0, 0, fillW, box.Y), radius, fill)
	}
	return layout.Dimensions{Size: box}
}

func (s *ProgressState) drawIndeterminate(env *Env, gtx *layout.Context, box image.Point, radius int, fill color.NRGBA) {
	if box.X <= 0 {
		return
	}
	const barFrac = 0.35
	barW := max(int(math.Round(float64(box.X)*barFrac)), box.Y)
	if env.ReducedMotion {
		// Under ReducedMotion, stop requesting frames and show a static segment (§3.7, UK-04).
		x0 := (box.X - barW) / 2
		fillRRect(gtx.Ops, image.Rect(x0, 0, x0+barW, box.Y), radius, fill)
		return
	}
	env.Keep()
	s.phase = math.Mod(s.phase+env.DT().Seconds()/1.4, 1)
	span := float64(box.X + barW)
	x0 := int(math.Round(s.phase*span)) - barW
	fillRRect(gtx.Ops, image.Rect(x0, 0, x0+barW, box.Y), radius, fill)
}

func progressSize(gtx *layout.Context) image.Point {
	h := max(gtx.Dp(theme.SpaceSM), 2)
	w := gtx.Constraints.Max.X
	if w <= 0 {
		w = gtx.Constraints.Min.X
	}
	return gtx.Constraints.Constrain(image.Pt(w, h))
}
