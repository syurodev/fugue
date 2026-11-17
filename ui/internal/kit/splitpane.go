package kit

import (
	"image"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// SplitPane, ui-kit §6.27: two panes separated by a hairline handle with an
// 8 dp pointer target. Supports Horizontal (left/right) and Vertical
// (top/bottom) splits, pointer drag clamped to MinFirst/MinSecond, double-click
// reset to DefaultRatio (distinguished from drags > 4 dp; drag releases do not
// count toward double-click), and keyboard resizing (Arrow keys by 16 dp,
// Shift+Arrow by 64 dp, Home/End to the minimum/maximum limits) with a §3.4
// focus ring. Ratio is caller-owned state (UK-02).
//
//	ratio, changed, dims := split.Layout(env, gtx, props, firstPane, secondPane)

const doubleClickWindow = 400 * time.Millisecond

// SplitPaneProps are the per-frame, caller-owned properties of SplitPane (UK-02).
type SplitPaneProps struct {
	Orientation  Orientation
	Ratio        float32
	DefaultRatio float32
	MinFirst     unit.Dp
	MinSecond    unit.Dp
}

// SplitPaneState is the long-lived state the caller keeps per SplitPane (UK-02).
type SplitPaneState struct {
	tag           struct{}
	focusF        motion.Follower
	hovered       bool
	pressing      bool
	dragged       bool
	pressPos      f32.Point
	pressOffset   float32
	lastClickPos  f32.Point
	lastClickTime time.Time
}

// NewSplitPaneState builds the state for one SplitPane.
func NewSplitPaneState() *SplitPaneState {
	return &SplitPaneState{focusF: motion.NewFollower(motion.Snappy())}
}

// Tag returns the handle's focus/input tag for keyboard navigation and previews.
func (s *SplitPaneState) Tag() event.Tag { return &s.tag }

// Dragging reports whether the handle is currently being dragged.
func (s *SplitPaneState) Dragging() bool { return s.dragged }

// Hovered reports whether the pointer is currently over the handle.
func (s *SplitPaneState) Hovered() bool { return s.hovered }

// Layout lays out first and second panes and handles pointer/keyboard resizing.
func (s *SplitPaneState) Layout(env *Env, gtx *layout.Context, p SplitPaneProps, first, second layout.Widget) (float32, bool, layout.Dimensions) {
	box := gtx.Constraints.Max
	if box.X <= 0 || box.Y <= 0 {
		return p.Ratio, false, layout.Dimensions{}
	}
	total, cross := splitAxes(box, p.Orientation)
	minFirst := gtx.Dp(p.MinFirst)
	minSecond := gtx.Dp(p.MinSecond)
	firstPx := clampSplit(int(math.Round(float64(p.Ratio)*float64(total))), total, minFirst, minSecond)

	// Lay out both panes clipped to their own rects before the handle overlay.
	s.layoutPane(gtx, p.Orientation, 0, firstPx, cross, first)
	s.layoutPane(gtx, p.Orientation, firstPx, total-firstPx, cross, second)

	// Process handle input in SplitPane-local coordinates so moving the handle
	// during a drag never shifts ev.Position out from under the pointer.
	handleRect := splitHandleRect(gtx, p.Orientation, firstPx, total, cross)
	newRatio, changed := s.handleInput(gtx, p, handleRect, firstPx, total, minFirst, minSecond)
	if changed {
		firstPx = clampSplit(int(math.Round(float64(newRatio)*float64(total))), total, minFirst, minSecond)
	}

	s.drawHandle(env, gtx, p.Orientation, firstPx, total, cross)
	return newRatio, changed, layout.Dimensions{Size: box}
}

func (s *SplitPaneState) layoutPane(gtx *layout.Context, orient Orientation, start, length, cross int, w layout.Widget) {
	if w == nil || length <= 0 || cross <= 0 {
		return
	}
	var origin image.Point
	var size image.Point
	if orient == Vertical {
		origin = image.Pt(0, start)
		size = image.Pt(cross, length)
	} else {
		origin = image.Pt(start, 0)
		size = image.Pt(length, cross)
	}
	at(gtx, origin, func(g *layout.Context) {
		defer clip.Rect{Max: size}.Push(g.Ops).Pop()
		cg := *g
		cg.Constraints = layout.Exact(size)
		w(cg)
	})
}

func (s *SplitPaneState) handleInput(gtx *layout.Context, p SplitPaneProps, handleRect image.Rectangle, firstPx, total, minFirst, minSecond int) (float32, bool) {
	defer clip.Rect(handleRect).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.tag)
	if p.Orientation == Vertical {
		pointer.CursorRowResize.Add(gtx.Ops)
	} else {
		pointer.CursorColResize.Add(gtx.Ops)
	}

	newRatio := p.Ratio
	changed := false

	if r, ok := s.pointerInput(gtx, p, firstPx, total, minFirst, minSecond); ok {
		newRatio = r
		changed = true
	}
	if r, ok := s.keyInput(gtx, p, firstPx, total, minFirst, minSecond); ok {
		newRatio = r
		changed = true
	}
	return newRatio, changed
}

func (s *SplitPaneState) pointerInput(gtx *layout.Context, p SplitPaneProps, firstPx, total, minFirst, minSecond int) (float32, bool) {
	newRatio := p.Ratio
	changed := false
	dragThresh := float64(gtx.Dp(theme.SpaceXS)) // ~4 dp (§6.27)

	for {
		e, ok := gtx.Event(pointer.Filter{
			Target: &s.tag,
			Kinds:  pointer.Enter | pointer.Leave | pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			return newRatio, changed
		}
		pe, is := e.(pointer.Event)
		if !is {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			s.hovered = true
		case pointer.Leave:
			s.hovered = false
		case pointer.Press:
			if pe.Buttons.Contain(pointer.ButtonPrimary) {
				s.pressing = true
				s.dragged = false
				s.pressPos = pe.Position
				s.pressOffset = axisCoord(pe.Position, p.Orientation) - float32(firstPx)
			}
		case pointer.Drag:
			if !s.pressing {
				continue
			}
			dist := math.Hypot(float64(pe.Position.X-s.pressPos.X), float64(pe.Position.Y-s.pressPos.Y))
			if !s.dragged && dist > dragThresh {
				s.dragged = true
			}
			if s.dragged {
				targetPx := int(math.Round(float64(axisCoord(pe.Position, p.Orientation) - s.pressOffset)))
				clamped := clampSplit(targetPx, total, minFirst, minSecond)
				newRatio = float32(clamped) / float32(total)
				changed = true
			}
		case pointer.Release:
			if !s.pressing {
				continue
			}
			if s.dragged {
				// A drag release must not count toward a double-click (§6.27).
				s.lastClickTime = time.Time{}
			} else if !s.lastClickTime.IsZero() &&
				gtx.Now.Sub(s.lastClickTime) <= doubleClickWindow &&
				math.Hypot(float64(pe.Position.X-s.lastClickPos.X), float64(pe.Position.Y-s.lastClickPos.Y)) <= dragThresh {
				def := p.DefaultRatio
				if def <= 0 || def >= 1 {
					def = 0.5
				}
				clamped := clampSplit(int(math.Round(float64(def)*float64(total))), total, minFirst, minSecond)
				newRatio = float32(clamped) / float32(total)
				changed = true
				s.lastClickTime = time.Time{}
			} else {
				s.lastClickTime = gtx.Now
				s.lastClickPos = pe.Position
			}
			s.pressing = false
			s.dragged = false
		case pointer.Cancel:
			s.pressing = false
			s.dragged = false
		}
	}
}

func (s *SplitPaneState) keyInput(gtx *layout.Context, p SplitPaneProps, firstPx, total, minFirst, minSecond int) (float32, bool) {
	decKey, incKey := key.NameLeftArrow, key.NameRightArrow
	if p.Orientation == Vertical {
		decKey, incKey = key.NameUpArrow, key.NameDownArrow
	}
	filters := []event.Filter{
		key.FocusFilter{Target: &s.tag},
		key.Filter{Focus: &s.tag, Name: decKey, Optional: key.ModShift},
		key.Filter{Focus: &s.tag, Name: incKey, Optional: key.ModShift},
		key.Filter{Focus: &s.tag, Name: key.NameHome},
		key.Filter{Focus: &s.tag, Name: key.NameEnd},
	}

	newRatio := p.Ratio
	changed := false
	curPx := firstPx

	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			return newRatio, changed
		}
		ke, is := e.(key.Event)
		if !is || ke.State != key.Press {
			continue
		}
		step := gtx.Dp(theme.SpaceXL) // 16 dp (§6.27)
		if ke.Modifiers.Contain(key.ModShift) {
			step = gtx.Dp(4 * theme.SpaceXL) // 64 dp (§6.27)
		}
		lo, hi := splitLimits(total, minFirst, minSecond)
		switch ke.Name {
		case decKey:
			curPx = clampSplit(curPx-step, total, minFirst, minSecond)
		case incKey:
			curPx = clampSplit(curPx+step, total, minFirst, minSecond)
		case key.NameHome:
			curPx = lo
		case key.NameEnd:
			curPx = hi
		default:
			continue
		}
		newRatio = float32(curPx) / float32(total)
		changed = true
	}
}

func (s *SplitPaneState) drawHandle(env *Env, gtx *layout.Context, orient Orientation, firstPx, total, cross int) {
	hovered := s.hovered || env.previewHover(&s.tag)
	dragging := s.dragged || env.previewPress(&s.tag)
	thick := max(gtx.Dp(theme.Hairline), 1)
	lineColor := env.Th.Chrome().Border
	if dragging {
		thick = max(gtx.Dp(2*theme.Hairline), 2)
		lineColor = env.Th.Chrome().Action
	} else if hovered {
		thick = max(gtx.Dp(2*theme.Hairline), 2)
		lineColor = env.Th.Chrome().FgMuted
	}
	pos := min(max(firstPx-thick/2, 0), max(total-thick, 0))
	var bar image.Rectangle
	if orient == Vertical {
		bar = image.Rect(0, pos, cross, pos+thick)
	} else {
		bar = image.Rect(pos, 0, pos+thick, cross)
	}
	paint.FillShape(gtx.Ops, lineColor, clip.Rect(bar).Op())

	focused := env.FocusVisible(gtx, &s.tag) || env.previewFocus(&s.tag)
	focusAlpha := followState(env, &s.focusF, focused)
	if focusAlpha > 0 {
		// Inset the cross-axis ends so the §3.4 focus ring stays inside SplitPane.
		pad := gtx.Dp(theme.FocusOffset + theme.FocusStroke)
		var ringRect image.Rectangle
		if orient == Vertical {
			ringRect = image.Rect(pad, pos, max(cross-pad, pad), pos+thick)
		} else {
			ringRect = image.Rect(pos, pad, pos+thick, max(cross-pad, pad))
		}
		focusRing(gtx, ringRect, thick, env.Th.Chrome().FocusBorder, focusAlpha)
	}
}

func splitAxes(box image.Point, orient Orientation) (total, cross int) {
	if orient == Vertical {
		return box.Y, box.X
	}
	return box.X, box.Y
}

func axisCoord(pt f32.Point, orient Orientation) float32 {
	if orient == Vertical {
		return pt.Y
	}
	return pt.X
}

func splitHandleRect(gtx *layout.Context, orient Orientation, firstPx, total, cross int) image.Rectangle {
	hitHalf := max(gtx.Dp(theme.SpaceMD)/2, 1) // 8 dp total (§6.27)
	lo := max(firstPx-hitHalf, 0)
	hi := min(firstPx+hitHalf, total)
	if orient == Vertical {
		return image.Rect(0, lo, cross, hi)
	}
	return image.Rect(lo, 0, hi, cross)
}

func splitLimits(total, minFirst, minSecond int) (lo, hi int) {
	lo = min(max(minFirst, 0), total)
	hi = max(total-max(minSecond, 0), 0)
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

func clampSplit(firstPx, total, minFirst, minSecond int) int {
	lo, hi := splitLimits(total, minFirst, minSecond)
	return min(max(firstPx, lo), hi)
}
