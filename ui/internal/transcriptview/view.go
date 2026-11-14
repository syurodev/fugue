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

// State is the view's state: the layout cache, the scroll offset, and the
// follow flag that keeps the newest item on screen while the reader sits at
// the bottom. It changes only on the window goroutine.
type State struct {
	// cache is the window's shared layout cache; the State itself holds
	// only the session's own scroll and expansion state.
	cache *Cache
	// expanded holds one expansion bit per item_id, so a card the reader
	// opened stays open across its revisions.
	expanded map[string]bool
	follow   bool
	// anchor: the item the reader last saw at the top of the viewport and
	// where it sat, so a prepend can be corrected to the measured heights
	// once they are known. pendingAnchor marks an accounting the anchor may
	// still refine.
	anchorID      string
	anchorTop     int
	pendingAnchor bool
	prevOffset    int
	scrollOffset  int
	// rendered counts the item cards the last frame laid out.
	rendered int
}

// NewState builds a session's view state over the window's shared cache,
// following the newest item until the reader scrolls up.
func NewState(cache *Cache) *State {
	return &State{cache: cache, expanded: map[string]bool{}, follow: true}
}

// Following reports whether the view pins the newest item on screen.
func (s *State) Following() bool { return s.follow }

// FollowOff ends the follow; the reader scrolls on their own.
func (s *State) FollowOff() { s.follow = false }

// BackToTop moves the reader to the start of the history and ends the
// follow, so the next older page can load.
func (s *State) BackToTop() {
	s.follow = false
	s.scrollOffset = 0
}

// ScrollTo sets the scroll offset and ends the follow.
func (s *State) ScrollTo(offset int) {
	s.follow = false
	s.scrollOffset = offset
}

// AccountForPrepended shifts the scroll offset by the estimated height of
// count items an older page added above the reader, so the item they were
// reading stays in place. The next frame's anchor refines the shift to the
// measured heights.
func (s *State) AccountForPrepended(count int) {
	s.scrollOffset += count * estimateH
	s.pendingAnchor = true
}

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
	wasPending := st.pendingAnchor
	st.pollScroll(gtx, max(0, prefix[n]-viewport))
	if !st.follow && st.anchorID != "" && (st.scrollOffset == st.prevOffset || st.pendingAnchor) {
		// The reader has not scrolled, so any shift of the anchored item is
		// the content moving under them — a prepend, or a height settling:
		// move the offset with it, to the measured heights this frame has.
		for i, item := range items {
			if item.ItemID != st.anchorID {
				continue
			}
			st.scrollOffset = prefix[i] - st.anchorTop
			break
		}
	}
	st.pendingAnchor = false
	if st.follow {
		// The reader is at the bottom: keep the newest item on screen as
		// items arrive and open items grow.
		st.scrollOffset = max(0, prefix[n]-viewport)
	}
	st.scrollOffset = min(max(st.scrollOffset, 0), max(0, prefix[n]-viewport))
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
	st.recordAnchor(items, prefix, viewport, wasPending)
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

// heightOf returns the card's cached height, the previous revision's height
// for a revision not laid out yet, or the estimate for a card the view has
// never seen.
func (s *State) heightOf(item *session.Item) int {
	if c, ok := s.cache.cards.get(cardKey{itemID: item.ItemID, revision: item.Revision}); ok {
		return c.height
	}
	if height, ok := s.cache.cards.heightOf(item.ItemID); ok && height > 0 {
		return height
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
// content height. Scrolling up ends the follow, and landing back at the
// bottom resumes it.
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
			if pe.Scroll.Y < 0 {
				s.follow = false
			}
			s.scrollOffset += int(pe.Scroll.Y)
		}
	}
	s.scrollOffset = min(max(s.scrollOffset, 0), maxOffset)
	// The bottom is where the offset meets the content's end, including when
	// the content is shorter than the viewport: there, nothing ever scrolls,
	// and the follow stays on.
	if s.scrollOffset >= maxOffset {
		s.follow = true
	}
}

// recordAnchor remembers the item the reader sees at the viewport's top and
// where it sat, for the next frame's anchor correction. A frame that
// corrected from estimates keeps its anchor: its own measurements move the
// content again, and the next frame corrects exactly.
func (s *State) recordAnchor(items []*session.Item, prefix []int, viewport int, correctedFromEstimate bool) {
	s.prevOffset = s.scrollOffset
	if correctedFromEstimate {
		return
	}
	if len(items) == 0 {
		s.anchorID = ""
		return
	}
	first, _ := visibleRange(prefix, s.scrollOffset, viewport)
	s.anchorID = items[first].ItemID
	s.anchorTop = prefix[first] - s.scrollOffset
}
