package kit

import (
	"image/color"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Progress, ui-kit §6.5: a determinate one- or two-tier progress indicator
// drawn as a left-anchored row of rounded vertical lines (LineRow, §3.9) with
// no track and no animation. Used lines take one solid level tone selected by
// Value (< 0.70 success, 0.70..< 0.85 warning, >= 0.85 danger; a caller may
// raise the level, never lower it), optional Secondary (> Value) takes the
// tone at 40%, and remaining lines take Chrome.LineRest.

// ProgressTone selects the indicator color from theme tokens (§3.5, §6.5).
type ProgressTone uint8

// Progress tone variants (§3.5, §6.5). ProgressDefault selects the level tone
// from Value automatically; ProgressSuccess, ProgressWarning, and
// ProgressDanger act as minimum level floors. ProgressInfo keeps the info hue.
const (
	ProgressDefault ProgressTone = iota
	ProgressInfo
	ProgressSuccess
	ProgressWarning
	ProgressDanger
)

// ProgressProps are the per-frame, caller-owned properties of Progress (UK-02).
type ProgressProps struct {
	Value     float32 // determinate fraction in [0, 1]
	Secondary float32 // optional second-tier fraction in [0, 1]; <= Value disables tier 2
	Tone      ProgressTone
	Size      LineSize // LineMD (14 dp) or LineSM (10 dp)
}

// ProgressState is the long-lived state the caller keeps per Progress row (UK-02).
type ProgressState struct{}

// NewProgressState builds the state for one Progress row.
func NewProgressState() *ProgressState { return &ProgressState{} }

// ResolveProgressTone returns the effective ProgressTone for value and caller
// floor tone: < 0.70 is Success, 0.70 to < 0.85 is Warning, >= 0.85 is Danger;
// callers may raise the level, never lower it. ProgressInfo stays ProgressInfo.
func ResolveProgressTone(tone ProgressTone, value float32) ProgressTone {
	if tone == ProgressInfo {
		return ProgressInfo
	}
	level := levelToneForValue(value)
	if tone > level {
		return tone
	}
	return level
}

func levelToneForValue(value float32) ProgressTone {
	v := unit01(value)
	switch {
	case v >= 0.85:
		return ProgressDanger
	case v >= 0.70:
		return ProgressWarning
	default:
		return ProgressSuccess
	}
}

// ResolveProgressLineTone resolves the solid used and 40% secondary colors on
// th for tone and value.
func ResolveProgressLineTone(th *theme.Theme, tone ProgressTone, value float32) theme.LineTone {
	switch ResolveProgressTone(tone, value) {
	case ProgressInfo:
		return th.StatusLineTone(theme.StatusInfo)
	case ProgressSuccess:
		return th.StatusLineTone(theme.StatusSuccess)
	case ProgressWarning:
		return th.StatusLineTone(theme.StatusWarning)
	case ProgressDanger:
		return th.StatusLineTone(theme.StatusDanger)
	case ProgressDefault:
		return th.ActionLineTone()
	default:
		return th.ActionLineTone()
	}
}

// ResolveProgressColor returns the solid indicator color for tone on th.
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

// Measure returns the Progress row's natural box without drawing.
func (s *ProgressState) Measure(gtx *layout.Context) layout.Dimensions {
	return LineRowMeasure(gtx, LineMD)
}

// Layout draws the Progress row (§6.5).
func (s *ProgressState) Layout(env *Env, gtx *layout.Context, p ProgressProps) layout.Dimensions {
	return Progress(env, gtx, p)
}

// Progress draws a one- or two-tier line-row progress indicator (§6.5).
func Progress(env *Env, gtx *layout.Context, p ProgressProps) layout.Dimensions {
	v := unit01(p.Value)
	sec := unit01(p.Secondary)
	lt := ResolveProgressLineTone(env.Th, p.Tone, v)
	rest := env.Th.Chrome().LineRest
	hasSecondary := sec > v
	return LineRow(env, gtx, LineRowProps{
		Size:     p.Size,
		Fallback: rest,
		ColorAt: func(i, count int) color.NRGBA {
			if count <= 0 {
				return rest
			}
			mid := (float32(i) + 0.5) / float32(count)
			if mid <= v || (v > 0 && i == 0) {
				return lt.Used
			}
			if hasSecondary && (mid <= sec || (sec > 0 && i == 0)) {
				return lt.Secondary
			}
			return rest
		},
	})
}
