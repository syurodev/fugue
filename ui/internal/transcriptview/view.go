package transcriptview

import (
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// estimateH is the height assumed for the shift an older page's prepended
// cards cause before any measurement: a fixed px guess the next frame's
// anchor refines to the measured heights, as in F10.4a. The row estimates
// themselves are the theme's CardEstimate token.
const estimateH = 96

// State is one session's view state (F10.13): the kit's VirtualList owns the
// scroll offset, the follow pin and the anchor, and the State maps the
// session model's items onto it — the private list of F10.4a became
// kit.VirtualList in F10.17b, over the window's one shared height cache
// (desktop-app §7, F10.4a round 3). It changes only on the window
// goroutine.
type State struct {
	list *kit.VirtualListState
}

// NewState builds a session's view state over the window's shared height
// cache, following the newest item until the reader scrolls up. Every
// session of a window passes the same cache, so the bound covers them
// together.
func NewState(cache *kit.VirtualHeightCache) *State {
	return &State{list: kit.NewVirtualListStateShared(cache)}
}

// Following reports whether the view pins the newest item on screen.
func (s *State) Following() bool { return s.list.Following() }

// FollowOff ends the follow; the reader scrolls on their own. Scrolling
// back to the bottom resumes it.
func (s *State) FollowOff() { s.list.BreakFollow() }

// BackToTop moves the reader to the start of the history and ends the
// follow, so the next older page can load.
func (s *State) BackToTop() { s.list.SetOffset(0) }

// ScrollTo sets the scroll offset and ends the follow.
func (s *State) ScrollTo(offset int) { s.list.SetOffset(offset) }

// AccountForPrepended shifts the scroll offset by the estimated height of
// count items an older page added above the reader, so the item they were
// reading stays in place. The next frame's anchor refines the shift to the
// measured heights.
func (s *State) AccountForPrepended(count int) {
	s.list.AccountForPrepended(count, estimateH)
}

// Offset reports the scroll offset, so the caller can tell when the reader
// is back at the top of the history.
func (s *State) Offset() int { return s.list.Offset() }

// AtTop reports whether the view is scrolled to the start of the history.
func (s *State) AtTop() bool { return s.Offset() == 0 }

// Rendered reports how many cards the last frame laid out.
func (s *State) Rendered() int { return s.list.Rendered() }

// virtualItems maps the model's items onto the list's items: the item_id is
// the stable key across frames and revisions, the revision invalidates the
// cached height, and an unmeasured card assumes the estimate token.
func virtualItems(items []*session.Item) []kit.VirtualItem {
	vis := make([]kit.VirtualItem, len(items))
	for i, item := range items {
		vis[i] = kit.VirtualItem{Key: item.ItemID, Revision: item.Revision, Estimate: theme.CardEstimate}
	}
	return vis
}

// Layout lays the transcript out inside the caller's constraints: the kit's
// list renders only the visible cards plus its render window, keeps the
// reader's anchor across prepends, and pins the newest item on screen while
// the reader sits at the bottom. The caller fills the background.
func Layout(env *kit.Env, gtx *layout.Context, st *State, items []*session.Item) layout.Dimensions {
	vis := virtualItems(items)
	return st.list.Layout(env, gtx, kit.VirtualListProps{FollowBottom: true}, vis,
		func(env *kit.Env, gtx *layout.Context, i int, _ kit.VirtualItem, _ *kit.VirtualItemState) layout.Dimensions {
			return layoutCard(env, gtx, st, items[i])
		})
}
