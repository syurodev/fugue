package kit

import (
	"image"
	"sort"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	virtualAboveWindow = 50
	virtualBelowWindow = 50
	maxVirtualHeights  = 2000
)

// VirtualItem describes one item in a VirtualList (§6.26). Key identifies the
// item across frames and revisions; Revision invalidates its cached height;
// Estimate is the fallback height before measurement (defaults to theme.Row).
type VirtualItem struct {
	Key      string
	Revision uint64
	Estimate unit.Dp
}

// VirtualItemState holds per-key persistent state that survives across item
// revisions (§6.26).
type VirtualItemState struct {
	Expanded       bool
	MeasuredHeight int
}

// VirtualRowFunc lays out one visible VirtualItem within gtx and returns its
// measured dimensions.
type VirtualRowFunc func(env *Env, gtx *layout.Context, index int, item VirtualItem, state *VirtualItemState) layout.Dimensions

// VirtualListProps configures a VirtualList for the current frame (§6.26).
type VirtualListProps struct {
	// FollowBottom pins the list to the end until the reader scrolls up
	// (default false).
	FollowBottom bool
}

// VirtualListState holds the bounded LRU height cache with the callers'
// payloads, per-key persistent state, scroll offset, anchor, and scrollbar
// state of one VirtualList (§6.26).
type VirtualListState struct {
	tag           bool
	cache         *VirtualHeightCache
	itemStates    map[string]*VirtualItemState
	bar           scrollBar
	scrollOffset  int
	maxOffset     int
	prevOffset    int
	following     bool
	followSet     bool
	anchorKey     string
	anchorTop     int
	pendingAnchor bool
	ensureKey     string
	rendered      int
}

// NewVirtualListState builds a VirtualListState with a bounded LRU height
// cache of its own.
func NewVirtualListState() *VirtualListState {
	return newListState(newVirtualHeightCache(maxVirtualHeights))
}

// NewVirtualListStateShared builds a VirtualListState over an injected
// height cache, so the lists of one window — one transcript session per
// list — share a single bound (desktop-app §7). The cache outlives no list:
// the caller drops it when the window's last list is gone.
func NewVirtualListStateShared(cache *VirtualHeightCache) *VirtualListState {
	return newListState(cache)
}

func newListState(cache *VirtualHeightCache) *VirtualListState {
	return &VirtualListState{
		cache:      cache,
		itemStates: map[string]*VirtualItemState{},
		bar:        newScrollBar(),
	}
}

// Tag returns the list's scroll event tag.
func (s *VirtualListState) Tag() event.Tag { return &s.tag }

// Offset returns the current scroll offset in px.
func (s *VirtualListState) Offset() int { return s.scrollOffset }

// MaxOffset returns the maximum scroll offset in px from the last frame.
func (s *VirtualListState) MaxOffset() int { return s.maxOffset }

// Following reports whether the list is currently pinned to the bottom.
func (s *VirtualListState) Following() bool { return s.following }

// BarAlpha reports the current scrollbar opacity in [0, 1].
func (s *VirtualListState) BarAlpha() float32 { return s.bar.barAlpha }

// Rendered reports how many items were laid out in the last frame.
func (s *VirtualListState) Rendered() int { return s.rendered }

// CacheLen reports the number of entries in the bounded LRU height cache.
func (s *VirtualListState) CacheLen() int { return len(s.cache.entries) }

// SetOffset moves the scroll offset and disables following when moving away
// from the bottom.
func (s *VirtualListState) SetOffset(v int) {
	s.scrollOffset = max(v, 0)
	s.following = s.maxOffset > 0 && s.scrollOffset >= s.maxOffset
}

// BreakFollow unpins the list from the bottom until the reader scrolls back
// to it: the caller moves the reader or hands control over to them, and the
// next frames must not drag the view down (§6.26). The follow prop itself
// stays as it is, so landing at the bottom resumes the pin.
func (s *VirtualListState) BreakFollow() { s.following = false }

// Visible returns the indexes of the items intersecting the viewport at the
// current offset, over the cache's heights: what the reader is looking at,
// for callers that page, anchor or test against the visible items (§6.26).
func (s *VirtualListState) Visible(gtx *layout.Context, items []VirtualItem, viewport int) (first, last int) {
	return visibleRange(s.prefix(gtx, items), s.scrollOffset, viewport)
}

// ItemState returns the persistent per-key state, creating it on first use.
func (s *VirtualListState) ItemState(key string) *VirtualItemState {
	st, ok := s.itemStates[key]
	if !ok {
		st = &VirtualItemState{}
		s.itemStates[key] = st
	}
	return st
}

// Expanded reports the persistent expanded state for key.
func (s *VirtualListState) Expanded(key string) bool {
	if st, ok := s.itemStates[key]; ok {
		return st.Expanded
	}
	return false
}

// SetExpanded sets the persistent expanded state for key.
func (s *VirtualListState) SetExpanded(key string, open bool) {
	s.ItemState(key).Expanded = open
}

// ToggleExpanded flips the persistent expanded state for key and returns the
// new state.
func (s *VirtualListState) ToggleExpanded(key string) bool {
	st := s.ItemState(key)
	st.Expanded = !st.Expanded
	return st.Expanded
}

// PayloadOf returns the caller's per-key payload of type T, building it with
// build when the list holds none. This is where a row func keeps the heavy
// state a VirtualItemState cannot carry — a parsed document, a per-item
// widget state — within the list's bound: the payload evicts with the key's
// latest measured height (§6.26). Every call for one key must name the same
// type; the builder decides it:
//
//	st := kit.PayloadOf(s, item.Key, func() *cardState { return &cardState{} })
func PayloadOf[T any](s *VirtualListState, key string, build func() T) T {
	if p, ok := s.cache.payload(key, func() any { return build() }).(T); ok {
		return p
	}
	// The payload is only ever of the builder's type; a mismatch means two
	// call sites disagree about it, and the zero value fails the caller
	// loudly on use.
	var zero T
	return zero
}

// EnsureVisible requests scrolling just enough on the next frame to keep key
// inside the viewport.
func (s *VirtualListState) EnsureVisible(key string) {
	s.ensureKey = key
}

// AccountForPrepended marks a prepend of count items above the reader so the
// next frame refines the anchor to measured heights.
func (s *VirtualListState) AccountForPrepended(count, estimate int) {
	if count > 0 && estimate > 0 {
		s.scrollOffset += count * estimate
	}
	s.pendingAnchor = true
}

// Layout lays out only the visible items plus a render buffer, maintains the
// bounded height cache, preserves the scroll anchor on insert above, and draws
// the shared ScrollArea bar (§6.26).
func (s *VirtualListState) Layout(env *Env, gtx *layout.Context, p VirtualListProps, items []VirtualItem, row VirtualRowFunc) layout.Dimensions {
	s.rendered = 0
	s.syncFollowState(p.FollowBottom)

	viewport := gtx.Constraints.Max.Y
	width := gtx.Constraints.Max.X
	n := len(items)
	if n == 0 || viewport <= 0 || width <= 0 {
		s.maxOffset, s.scrollOffset = 0, 0
		return layout.Dimensions{Size: image.Pt(max(width, 0), max(viewport, 0))}
	}

	nested := env.scrollAxes[1]
	env.scrollAxes[1] = true
	defer func() { env.scrollAxes[1] = nested }()

	prefix := s.prefix(gtx, items)
	s.maxOffset = max(0, prefix[n]-viewport)

	area := clip.Rect{Max: image.Pt(width, viewport)}.Push(gtx.Ops)
	scrolled := false
	if !nested {
		scrolled = s.pollEvents(env, gtx, image.Rectangle{Max: image.Pt(width, viewport)}, p.FollowBottom)
	}

	wasPending := s.pendingAnchor
	s.applyAnchorAndFollow(items, prefix, viewport, p.FollowBottom)
	s.layoutWindow(env, gtx, items, prefix, width, viewport, row)

	s.maxOffset = max(0, prefix[n]-viewport)
	if p.FollowBottom && s.following {
		s.scrollOffset = s.maxOffset
	}
	s.scrollOffset = min(max(s.scrollOffset, 0), s.maxOffset)

	s.bar.stepAndDraw(env, gtx, &s.tag, image.Pt(width, viewport), prefix[n], viewport, s.scrollOffset, s.maxOffset, layout.Vertical, scrolled)
	area.Pop()

	s.recordAnchor(items, prefix, viewport, wasPending)
	return layout.Dimensions{Size: image.Pt(width, viewport)}
}

func (s *VirtualListState) syncFollowState(followBottom bool) {
	if followBottom && !s.followSet {
		s.following = true
		s.followSet = true
		return
	}
	if !followBottom {
		s.following = false
		s.followSet = false
	}
}

func (s *VirtualListState) layoutWindow(env *Env, gtx *layout.Context, items []VirtualItem, prefix []int, width, viewport int, row VirtualRowFunc) {
	n := len(items)
	first, last := visibleRange(prefix, s.scrollOffset, viewport)
	w0, w1 := max(0, first-virtualAboveWindow), min(n-1, last+virtualBelowWindow)

	origin := op.Offset(image.Pt(0, -s.scrollOffset)).Push(gtx.Ops)
	saved := gtx.Constraints
	gtx.Constraints = layout.Constraints{Max: image.Pt(width, 1<<20)}
	for i := w0; i <= w1; i++ {
		item := items[i]
		st := s.ItemState(item.Key)
		var dims layout.Dimensions
		at(gtx, image.Pt(0, prefix[i]), func(gtx *layout.Context) {
			dims = row(env, gtx, i, item, st)
		})
		s.rendered++
		st.MeasuredHeight = dims.Size.Y
		s.cache.put(virtualHeightKey{key: item.Key, revision: item.Revision}, dims.Size.Y)
		if delta := dims.Size.Y - (prefix[i+1] - prefix[i]); delta != 0 {
			for j := i + 1; j <= n; j++ {
				prefix[j] += delta
			}
		}
	}
	gtx.Constraints = saved
	origin.Pop()
}

func (s *VirtualListState) prefix(gtx *layout.Context, items []VirtualItem) []int {
	prefix := make([]int, len(items)+1)
	for i, item := range items {
		prefix[i+1] = prefix[i] + s.heightOf(gtx, item)
	}
	return prefix
}

func (s *VirtualListState) heightOf(gtx *layout.Context, item VirtualItem) int {
	if h, ok := s.cache.get(virtualHeightKey{key: item.Key, revision: item.Revision}); ok && h > 0 {
		return h
	}
	if h, ok := s.cache.latest(item.Key); ok && h > 0 {
		return h
	}
	est := item.Estimate
	if est <= 0 {
		est = theme.Row
	}
	return max(gtx.Dp(est), 1)
}

func (s *VirtualListState) applyAnchorAndFollow(items []VirtualItem, prefix []int, viewport int, followBottom bool) {
	if !s.following && s.anchorKey != "" && (s.scrollOffset == s.prevOffset || s.pendingAnchor) {
		s.restoreAnchorOffset(items, prefix)
	}
	s.pendingAnchor = false
	s.applyEnsureVisible(items, prefix, viewport)

	if followBottom && s.following {
		s.scrollOffset = max(0, prefix[len(items)]-viewport)
	}
	s.scrollOffset = min(max(s.scrollOffset, 0), max(0, prefix[len(items)]-viewport))
}

func (s *VirtualListState) restoreAnchorOffset(items []VirtualItem, prefix []int) {
	for i, item := range items {
		if item.Key == s.anchorKey {
			s.scrollOffset = prefix[i] - s.anchorTop
			return
		}
	}
}

func (s *VirtualListState) applyEnsureVisible(items []VirtualItem, prefix []int, viewport int) {
	if s.ensureKey == "" {
		return
	}
	target := s.ensureKey
	s.ensureKey = ""
	for i, item := range items {
		if item.Key != target {
			continue
		}
		top, bot := prefix[i], prefix[i+1]
		if top < s.scrollOffset {
			s.scrollOffset = top
			s.following = false
		} else if bot > s.scrollOffset+viewport {
			s.scrollOffset = bot - viewport
		}
		return
	}
}

func (s *VirtualListState) pollEvents(env *Env, gtx *layout.Context, box image.Rectangle, followBottom bool) bool {
	defer clip.Rect(box).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.tag)
	filter := scrollFilter(&s.tag, layout.Vertical)
	scrolled := false
	for {
		e, ok := gtx.Event(filter)
		if !ok {
			break
		}
		if pe, isPtr := e.(pointer.Event); isPtr && s.handleScrollPointer(env, pe) {
			scrolled = true
		}
	}
	s.scrollOffset = min(max(s.scrollOffset, 0), s.maxOffset)
	if followBottom && s.scrollOffset >= s.maxOffset {
		s.following = true
	}
	return scrolled
}

func (s *VirtualListState) handleScrollPointer(env *Env, pe pointer.Event) bool {
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
	if delta == 0 {
		return false
	}
	if delta < 0 {
		s.following = false
	}
	s.scrollOffset += delta
	s.bar.lastAct = env.Now()
	return true
}

func (s *VirtualListState) recordAnchor(items []VirtualItem, prefix []int, viewport int, correctedFromEstimate bool) {
	s.prevOffset = s.scrollOffset
	if correctedFromEstimate {
		return
	}
	if len(items) == 0 {
		s.anchorKey = ""
		return
	}
	first, _ := visibleRange(prefix, s.scrollOffset, viewport)
	s.anchorKey = items[first].Key
	s.anchorTop = prefix[first] - s.scrollOffset
}

func visibleRange(prefix []int, offset, viewport int) (first, last int) {
	n := len(prefix) - 1
	if n <= 0 {
		return 0, 0
	}
	first = sort.Search(n, func(i int) bool { return prefix[i+1] > offset })
	if first == n {
		return n - 1, n - 1
	}
	last = sort.Search(n, func(i int) bool { return prefix[i+1] >= offset+viewport })
	return first, min(last, n-1)
}
