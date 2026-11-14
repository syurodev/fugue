package transcriptview

import (
	"image"
	"sort"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The virtualization window (desktop-app §7): fifty laid-out items beyond
// each side of the viewport, so scrolling never meets an unlaid-out card.
const (
	aboveWindow = 50
	belowWindow = 50
)

// estimateH is the height assumed for a card not yet laid out; the first
// frame that renders it replaces the estimate with its measured height.
const estimateH = 96

// scrollUnit bounds one scroll event's distance on the filter; the actual
// clamp is the content height.
const scrollUnit = 1 << 24

// State is the view's state: the layout cache and the scroll offset. It
// changes only on the window goroutine.
type State struct {
	cards        cardCache
	scrollOffset int
	// rendered counts the item cards the last frame laid out.
	rendered int
}

// NewState builds the view state with its bounded layout cache.
func NewState() *State {
	return &State{cards: *newCardCache(maxCards)}
}

// CachedCards reports the layout cache's size.
func (s *State) CachedCards() int { return s.cards.len() }

// Offset reports the scroll offset, so the caller can tell when the reader
// is back at the top of the history.
func (s *State) Offset() int { return s.scrollOffset }

// AtTop reports whether the view is scrolled to the start of the history.
func (s *State) AtTop() bool { return s.scrollOffset == 0 }

// Layout lays the transcript out inside the caller's constraints: only the
// visible items plus aboveWindow and belowWindow around them render, the rest
// contribute their cached height. The caller fills the background.
func Layout(gtx *layout.Context, th *theme.Theme, st *State, items []*session.Item) layout.Dimensions {
	st.rendered = 0
	n := len(items)
	viewport := gtx.Constraints.Max.Y
	width := gtx.Constraints.Max.X
	if n == 0 || viewport <= 0 {
		return layout.Dimensions{Size: image.Pt(width, max(viewport, 0))}
	}
	prefix := st.prefix(items)
	// The scroll area clips the pre-rendered cards outside the viewport.
	area := clip.Rect{Max: image.Pt(width, viewport)}.Push(gtx.Ops)
	event.Op(gtx.Ops, event.Tag(st))
	st.pollScroll(gtx, max(0, prefix[n]-viewport))
	first, last := visibleRange(prefix, st.scrollOffset, viewport)
	w0, w1 := max(0, first-aboveWindow), min(n-1, last+belowWindow)
	origin := op.Offset(image.Pt(0, -st.scrollOffset)).Push(gtx.Ops)
	saved := gtx.Constraints
	gtx.Constraints = layout.Constraints{Max: image.Pt(width, 1e6)}
	for i := w0; i <= w1; i++ {
		at := op.Offset(image.Pt(0, prefix[i])).Push(gtx.Ops)
		dims := layoutCard(gtx, th, st, items[i])
		at.Pop()
		prefix[i+1] = prefix[i] + dims.Size.Y
	}
	gtx.Constraints = saved
	origin.Pop()
	area.Pop()
	return layout.Dimensions{Size: image.Pt(width, viewport)}
}

// prefix returns the cumulative heights of items, using each card's cached
// height or the estimate; the caller updates it as cards measure.
func (s *State) prefix(items []*session.Item) []int {
	prefix := make([]int, len(items)+1)
	for i, item := range items {
		prefix[i+1] = prefix[i] + s.heightOf(item)
	}
	return prefix
}

// heightOf returns the card's cached height, or the estimate for a card the
// view has not laid out yet.
func (s *State) heightOf(item *session.Item) int {
	if c, ok := s.cards.get(cardKey{itemID: item.ItemID, revision: item.Revision}); ok {
		return c.height
	}
	return estimateH
}

// visibleRange returns the first and last item intersecting the viewport at
// the scroll offset, over the cumulative heights.
func visibleRange(prefix []int, offset, viewport int) (first, last int) {
	n := len(prefix) - 1
	first = sort.Search(n, func(i int) bool { return prefix[i+1] > offset })
	if first == n {
		return n - 1, n - 1
	}
	last = sort.Search(n, func(i int) bool { return prefix[i+1] >= offset+viewport })
	return first, min(last, n-1)
}

// pollScroll folds this frame's scroll events into the offset, clamped to the
// content height.
func (s *State) pollScroll(gtx *layout.Context, maxOffset int) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  event.Tag(s),
			Kinds:   pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -scrollUnit, Max: scrollUnit},
		})
		if !ok {
			break
		}
		if pe, isPointer := ev.(pointer.Event); isPointer && pe.Kind == pointer.Scroll {
			s.scrollOffset += int(pe.Scroll.Y)
		}
	}
	s.scrollOffset = min(max(s.scrollOffset, 0), maxOffset)
}
