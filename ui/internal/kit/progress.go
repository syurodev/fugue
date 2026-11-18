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

// Progress, ui-kit §6.5: a determinate linear progress bar (radius.full)
// for a real total reported by the backend (unknown durations use Spinner).
// The filled portion animates to Value via Smooth and stops requesting frames
// once settled; under ReducedMotion (UK-04) it snaps immediately.
//
//	dims := progressState.Layout(env, gtx, kit.ProgressProps{
//	    Value: 0.65,
//	    Tone:  kit.ProgressInfo,
//	})

// ProgressTone selects the indicator color from theme tokens (§3.5, §6.5).
type ProgressTone uint8

// Progress tone variants (§3.5, §6.5).
const (
	ProgressDefault ProgressTone = iota
	ProgressInfo
	ProgressSuccess
	ProgressWarning
	ProgressDanger
)

// ProgressProps are the per-frame, caller-owned properties of Progress (UK-02).
type ProgressProps struct {
	Value float32 // determinate fraction in [0, 1]
	Tone  ProgressTone
}

// ProgressState is the long-lived state the caller keeps per Progress bar (UK-02).
type ProgressState struct {
	valueF motion.Follower
	init   bool
}

// NewProgressState builds the state for one Progress bar.
func NewProgressState() *ProgressState {
	return &ProgressState{valueF: motion.NewFollower(motion.Smooth())}
}

// ResolveProgressColor returns the indicator color for tone on th.
func ResolveProgressColor(th *theme.Theme, tone ProgressTone) color.NRGBA {
	st := th.Status()
	switch tone {
	case ProgressDefault:
		return th.Chrome().Action
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

// Layout draws the Progress bar and advances its Smooth transition (§6.5).
func (s *ProgressState) Layout(env *Env, gtx *layout.Context, p ProgressProps) layout.Dimensions {
	box := progressSize(gtx)
	radius := box.Y / 2
	track := image.Rectangle{Max: box}

	fillRRect(gtx.Ops, track, radius, env.Th.Chrome().FillSoft)
	fill := ResolveProgressColor(env.Th, p.Tone)

	defer clip.UniformRRect(track, radius).Push(gtx.Ops).Pop()
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

func progressSize(gtx *layout.Context) image.Point {
	h := max(gtx.Dp(theme.SpaceSM), 2)
	w := gtx.Constraints.Max.X
	if w <= 0 {
		w = gtx.Constraints.Min.X
	}
	return gtx.Constraints.Constrain(image.Pt(w, h))
}
