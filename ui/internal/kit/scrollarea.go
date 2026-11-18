package kit

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// scrollIdleDelay is how long the ScrollArea bar stays visible after the last
// scroll event when the pointer is not hovering (§6.29).
const scrollIdleDelay = time.Second

// scrollUnit bounds one scroll event's range on pointer.Filter.
const scrollUnit = 1 << 24

// ScrollAreaProps configures a non-virtualized ScrollArea (§6.29).
type ScrollAreaProps struct {
	Axis         layout.Axis
	FollowBottom bool
}

// scrollBar holds the shared idle timer and bar motion for ScrollArea and
// VirtualList (§6.26, §6.29).
type scrollBar struct {
	hovered  bool
	lastAct  time.Time
	barA     motion.Follower
	barAlpha float32
}

func newScrollBar() scrollBar {
	return scrollBar{barA: motion.NewFollower(motion.Snappy())}
}

func (b *scrollBar) idleRemaining(env *Env, scrolled bool) time.Duration {
	if scrolled {
		b.lastAct = env.Now()
	}
	if b.lastAct.IsZero() {
		return 0
	}
	rem := b.lastAct.Add(scrollIdleDelay).Sub(env.Now())
	if rem < 0 {
		return 0
	}
	return rem
}

func (b *scrollBar) stepAndDraw(env *Env, gtx *layout.Context, tag event.Tag, outSize image.Point, contentLen, viewLen, offset, maxOffset int, axis layout.Axis, scrolled bool) {
	canScroll := maxOffset > 0
	active := canScroll && (b.hovered || env.previewHover(tag) || b.idleRemaining(env, scrolled) > 0)
	if active && !b.hovered && !b.lastAct.IsZero() {
		env.Driver.WakeAt(b.lastAct.Add(scrollIdleDelay))
	}
	b.barAlpha = followSmooth(env, &b.barA, boolFloat(active))
	if canScroll && b.barAlpha > 0 {
		drawScrollBar(env, gtx, outSize, contentLen, viewLen, offset, maxOffset, axis, b.barAlpha)
	}
}

func drawScrollBar(env *Env, gtx *layout.Context, outSize image.Point, contentLen, viewLen, offset, maxOffset int, axis layout.Axis, alpha float32) {
	thick := gtx.Dp(theme.SpaceXS)
	margin := gtx.Dp(theme.SpaceXXS)
	minThumb := gtx.Dp(theme.SpaceXXL)
	c := fade(env.Th.Chrome().FgSubtle, alpha)
	if axis == layout.Vertical {
		track := max(outSize.Y-2*margin, thick)
		thumbH := min(track, max(minThumb, track*viewLen/max(contentLen, 1)))
		y := margin + thumbOffset(offset, maxOffset, track-thumbH)
		r := image.Rect(outSize.X-margin-thick, y, outSize.X-margin, y+thumbH)
		paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, thick/2).Op(gtx.Ops))
		return
	}
	track := max(outSize.X-2*margin, thick)
	thumbW := min(track, max(minThumb, track*viewLen/max(contentLen, 1)))
	x := margin + thumbOffset(offset, maxOffset, track-thumbW)
	r := image.Rect(x, outSize.Y-margin-thick, x+thumbW, outSize.Y-margin)
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, thick/2).Op(gtx.Ops))
}

// ScrollAreaState holds the scroll offset, idle timer and bar motion of one
// ScrollArea.
type ScrollAreaState struct {
	tag       bool
	offset    int
	maxOffset int
	following bool
	followSet bool
	bar       scrollBar
}

// NewScrollAreaState builds a ScrollAreaState at offset 0.
func NewScrollAreaState() *ScrollAreaState {
	return &ScrollAreaState{bar: newScrollBar()}
}

// Tag returns the scroll area's event tag.
func (s *ScrollAreaState) Tag() event.Tag { return &s.tag }

// Offset returns the current scroll offset in px.
func (s *ScrollAreaState) Offset() int { return s.offset }

// SetOffset moves the scroll offset and stops FollowBottom when moving away
// from the end.
func (s *ScrollAreaState) SetOffset(v int) {
	s.offset = max(v, 0)
	s.following = s.maxOffset > 0 && s.offset >= s.maxOffset
}

// Following reports whether the area is currently pinned to the bottom.
func (s *ScrollAreaState) Following() bool { return s.following }

// BarAlpha reports the current scrollbar opacity in [0, 1].
func (s *ScrollAreaState) BarAlpha() float32 { return s.bar.barAlpha }

// Layout lays out content inside a clipped viewport on p.Axis, handles scroll
// and hover events (unless an enclosing ScrollArea already owns p.Axis), and
// draws a thin radius.full bar that fades in on scroll or hover and out after
// ~1 s idle (§6.29).
func (s *ScrollAreaState) Layout(env *Env, gtx *layout.Context, p ScrollAreaProps, content func(*layout.Context) layout.Dimensions) layout.Dimensions {
	if p.FollowBottom && !s.followSet {
		s.following = true
		s.followSet = true
	}

	axisIdx := axisIndex(p.Axis)
	nested := env.scrollAxes[axisIdx]
	env.scrollAxes[axisIdx] = true
	defer func() { env.scrollAxes[axisIdx] = nested }()

	call, cdims := recordScrollContent(gtx, p.Axis, content)
	outSize, contentLen, viewLen := scrollViewport(gtx.Constraints.Max, cdims.Size, p.Axis)
	s.maxOffset = max(0, contentLen-viewLen)

	scrolled := false
	if !nested && outSize.X > 0 && outSize.Y > 0 {
		scrolled = s.pollEvents(env, gtx, image.Rectangle{Max: outSize}, p.Axis)
	}
	s.updateOffset(p.FollowBottom)
	s.drawClipped(env, gtx, call, outSize, contentLen, viewLen, p.Axis, scrolled)
	return layout.Dimensions{Size: outSize}
}

func axisIndex(axis layout.Axis) int {
	if axis == layout.Vertical {
		return 1
	}
	return 0
}

func recordScrollContent(gtx *layout.Context, axis layout.Axis, content func(*layout.Context) layout.Dimensions) (op.CallOp, layout.Dimensions) {
	m := op.Record(gtx.Ops)
	cg := *gtx
	if axis == layout.Vertical {
		cg.Constraints = layout.Constraints{Min: image.Pt(gtx.Constraints.Min.X, 0), Max: image.Pt(gtx.Constraints.Max.X, 1<<20)}
	} else {
		cg.Constraints = layout.Constraints{Min: image.Pt(0, gtx.Constraints.Min.Y), Max: image.Pt(1<<20, gtx.Constraints.Max.Y)}
	}
	dims := content(&cg)
	return m.Stop(), dims
}

func scrollViewport(maxPt, contentPt image.Point, axis layout.Axis) (outSize image.Point, contentLen, viewLen int) {
	if maxPt.X <= 0 || maxPt.Y <= 0 {
		return contentPt, contentPt.Y, contentPt.Y
	}
	if axis == layout.Horizontal {
		return image.Pt(min(contentPt.X, maxPt.X), maxPt.Y), contentPt.X, maxPt.X
	}
	return image.Pt(maxPt.X, min(contentPt.Y, maxPt.Y)), contentPt.Y, maxPt.Y
}

func (s *ScrollAreaState) updateOffset(followBottom bool) {
	if followBottom && s.following {
		s.offset = s.maxOffset
	}
	s.offset = min(max(s.offset, 0), s.maxOffset)
	if followBottom && s.offset >= s.maxOffset {
		s.following = true
	}
}

func (s *ScrollAreaState) drawClipped(env *Env, gtx *layout.Context, call op.CallOp, outSize image.Point, contentLen, viewLen int, axis layout.Axis, scrolled bool) {
	area := clip.Rect(image.Rectangle{Max: outSize}).Push(gtx.Ops)
	shift := image.Pt(0, -s.offset)
	if axis == layout.Horizontal {
		shift = image.Pt(-s.offset, 0)
	}
	off := op.Offset(shift).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()

	s.bar.stepAndDraw(env, gtx, &s.tag, outSize, contentLen, viewLen, s.offset, s.maxOffset, axis, scrolled)
	area.Pop()
}

func (s *ScrollAreaState) pollEvents(env *Env, gtx *layout.Context, box image.Rectangle, axis layout.Axis) bool {
	defer clip.Rect(box).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.tag)
	filter := scrollFilter(&s.tag, axis)
	scrolled := false
	for {
		e, ok := gtx.Event(filter)
		if !ok {
			return scrolled
		}
		pe, isPtr := e.(pointer.Event)
		if !isPtr {
			continue
		}
		if s.handlePointer(env, pe, axis) {
			scrolled = true
		}
	}
}

func scrollFilter(tag event.Tag, axis layout.Axis) pointer.Filter {
	f := pointer.Filter{Target: tag, Kinds: pointer.Enter | pointer.Leave | pointer.Cancel | pointer.Scroll}
	if axis == layout.Vertical {
		f.ScrollY = pointer.ScrollRange{Min: -scrollUnit, Max: scrollUnit}
	} else {
		f.ScrollX = pointer.ScrollRange{Min: -scrollUnit, Max: scrollUnit}
	}
	return f
}

func (s *ScrollAreaState) handlePointer(env *Env, pe pointer.Event, axis layout.Axis) bool {
	if pe.Kind == pointer.Enter {
		s.bar.hovered = true
		return false
	}
	if pe.Kind == pointer.Leave || pe.Kind == pointer.Cancel {
		s.bar.hovered = false
		return false
	}
	if pe.Kind != pointer.Scroll {
		return false
	}
	delta := int(pe.Scroll.Y)
	if axis == layout.Horizontal {
		delta = int(pe.Scroll.X)
	}
	if delta == 0 {
		return false
	}
	if delta < 0 {
		s.following = false
	}
	s.offset += delta
	s.bar.lastAct = env.Now()
	return true
}

func thumbOffset(offset, maxOffset, slack int) int {
	if maxOffset <= 0 || slack <= 0 {
		return 0
	}
	return slack * offset / maxOffset
}
