package kitspike

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// SplitProps are the per-frame properties (UK-02).
type SplitProps struct {
	// Default is the ratio double-click returns to.
	Default  float32
	MinLeft  int
	MinRight int
}

// SplitPaneState is the long-lived state (UK-02): the ratio, the drag and
// the double-click reset. Its address is the input tag.
type SplitPaneState struct {
	ratio float32
	// drag is the pointer.ID currently dragging the handle.
	drag     pointer.ID
	dragging bool
	// lastUp is when the handle was released; two quick releases reset.
	lastUp time.Time
	// reset animates the double-click reset with Smooth (§3.7: a command,
	// not a drag, may animate); dragging cancels it.
	reset     motion.Spring
	resetting bool
}

// NewSplitPaneState builds state with the starting ratio.
func NewSplitPaneState(ratio float32) *SplitPaneState {
	return &SplitPaneState{ratio: ratio, reset: motion.NewSpringSpec(ratio, motion.Smooth())}
}

// Ratio is the current left share (0..1), after clamping.
func (s *SplitPaneState) Ratio() float32 { return s.ratio }

// SetRatio moves the handle programmatically, snapping.
func (s *SplitPaneState) SetRatio(r float32) {
	s.ratio = r
	s.resetting = false
	s.reset = motion.NewSpringSpec(r, motion.Smooth())
}

// Layout lays the two panes and the handle into gtx's full width. It clips
// each pane to its rectangle and hands it Exact constraints.
func (s *SplitPaneState) Layout(env *Env, gtx *layout.Context, left, right func(gtx *layout.Context) layout.Dimensions, p SplitProps) {
	tag := s
	w := gtx.Constraints.Max.X
	h := gtx.Constraints.Max.Y
	hit := dp(gtx, HandleHit)
	span := max(w-hit, 1)

	// The animated double-click reset (§3.7: a command may animate; a drag
	// never does).
	if s.resetting {
		s.reset.Target = p.Default
		s.reset.Advance(env.DT())
		s.ratio = s.reset.Value
		env.Keep(!s.reset.AtRest())
		if s.reset.AtRest() {
			s.resetting = false
		}
	}
	leftW := clamp(int(float32(span)*s.ratio), p.MinLeft, span-p.MinRight)
	s.ratio = float32(leftW) / float32(span)

	// Pointer handling on the hit area. The transform here is the window
	// origin, so event positions are window x directly.
	handleRect := image.Rect(leftW, 0, leftW+hit, h)
	func() {
		defer clip.Rect(handleRect).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, tag)
		pointer.CursorColResize.Add(gtx.Ops)
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tag,
				Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
			if !ok {
				break
			}
			e := ev.(pointer.Event)
			switch e.Kind {
			case pointer.Press:
				s.drag = e.PointerID
				s.dragging = true
				s.resetting = false
				s.follow(e.Position.X, span, hit)
			case pointer.Drag:
				if s.dragging && e.PointerID == s.drag {
					s.follow(e.Position.X, span, hit)
				}
			case pointer.Release:
				if s.dragging && e.PointerID == s.drag {
					s.dragging = false
					// Double-click reset (§6.27).
					if !s.lastUp.IsZero() && gtx.Now.Sub(s.lastUp) < 300*time.Millisecond {
						s.reset = motion.NewSpringSpec(s.ratio, motion.Smooth())
						s.resetting = true
						s.lastUp = time.Time{}
					} else {
						s.lastUp = gtx.Now
					}
				}
			case pointer.Cancel:
				s.dragging = false
			}
		}
	}()

	// Keyboard (§6.27): the handle is focusable; arrows move 16 dp,
	// Shift+arrow 64 dp.
	move := func(stepDp int) {
		s.ratio = clampF32(float32(leftW+stepDp)/float32(span), 0, 1)
		s.resetting = false
	}
	for {
		e, ok := gtx.Event(key.Filter{Focus: tag, Name: key.NameLeftArrow, Optional: key.ModShift})
		if !ok {
			break
		}
		if ev := e.(key.Event); ev.State == key.Press {
			step := dp(gtx, HandleStep)
			if ev.Modifiers.Contain(key.ModShift) {
				step = dp(gtx, HandleStepBig)
			}
			move(-step)
		}
	}
	for {
		e, ok := gtx.Event(key.Filter{Focus: tag, Name: key.NameRightArrow, Optional: key.ModShift})
		if !ok {
			break
		}
		if ev := e.(key.Event); ev.State == key.Press {
			step := dp(gtx, HandleStep)
			if ev.Modifiers.Contain(key.ModShift) {
				step = dp(gtx, HandleStepBig)
			}
			move(+step)
		}
	}
	leftW = clamp(int(float32(span)*s.ratio), p.MinLeft, span-p.MinRight)

	// Left pane, clipped to its area.
	lg := *gtx
	lg.Constraints = layout.Exact(image.Pt(leftW, h))
	func() {
		defer clip.Rect(image.Rect(0, 0, leftW, h)).Push(gtx.Ops).Pop()
		left(&lg)
	}()

	// Handle: a hairline centered in the hit area; a wash while dragging
	// (§6.27).
	c := env.Th.Chrome()
	if s.dragging {
		fillRect(gtx.Ops, image.Rect(leftW, 0, leftW+hit, h), HoverWash(env.Th))
	}
	half := (hit - dp(gtx, Hairline)) / 2
	fillRect(gtx.Ops, image.Rect(leftW+half, 0, leftW+half+dp(gtx, Hairline), h), c.Border)
	if gtx.Focused(tag) && env.FocusVisible(gtx, tag) {
		focusRing(env, gtx, image.Rect(leftW+1, dp(gtx, SpaceXXS), leftW+hit-1, h-dp(gtx, SpaceXXS)), dp(gtx, RadiusSM))
	}

	// Right pane.
	rx := leftW + hit
	rg := *gtx
	rg.Constraints = layout.Exact(image.Pt(w-rx, h))
	func() {
		defer op.Offset(image.Pt(rx, 0)).Push(gtx.Ops).Pop()
		defer clip.Rect(image.Rect(0, 0, w-rx, h)).Push(gtx.Ops).Pop()
		right(&rg)
	}()
}

// follow maps a window-space pointer x to the ratio, with no spring (§6.27:
// drag follows the pointer; release stands still).
func (s *SplitPaneState) follow(x float32, span, hit int) {
	s.ratio = clampF32((x-float32(hit)/2)/float32(span), 0, 1)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
