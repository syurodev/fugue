package transcriptview

import (
	"image"
	"log/slog"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// estimateH is the height assumed for the shift an older page's prepended
// cards cause before any measurement: a fixed px guess the next frame's
// anchor refines to the measured heights, as in F10.4a.
const estimateH = 96

func logTVErr(err error) {
	if err != nil {
		slog.Default().Warn("transcriptview layout error", "err", err)
	}
}

// Callbacks forwards transcript interactions to the enclosing window without
// coupling transcriptview to host writes or OS openers.
type Callbacks struct {
	OnOpenLink   func(url string)
	OnFileChip   func(ref mdview.FileRef)
	OnIDChip     func(id string)
	OnFullOutput func(itemID string)
	OnViewDiff   func(itemID string)
}

// State is one session's view state (F10.13, F10.4c): VirtualList owns the
// scroll offset, follow pin, anchor, and bounded LRU height cache.
type State struct {
	list *kit.VirtualListState
}

// NewState builds a session's view state over the window's shared height cache.
func NewState(cache *kit.VirtualHeightCache) *State {
	return &State{list: kit.NewVirtualListStateShared(cache)}
}

// Following reports whether the view pins the newest item on screen.
func (s *State) Following() bool { return s.list.Following() }

// FollowOff ends the follow; the reader scrolls on their own.
func (s *State) FollowOff() { s.list.BreakFollow() }

// BackToTop moves the reader to the start of the history and ends the follow.
func (s *State) BackToTop() { s.list.SetOffset(0) }

// ScrollTo sets the scroll offset and ends the follow.
func (s *State) ScrollTo(offset int) { s.list.SetOffset(offset) }

// AccountForPrepended shifts the scroll offset by the estimated height of count
// prepended items so the reader's anchor stays in place.
func (s *State) AccountForPrepended(count int) {
	s.list.AccountForPrepended(count, estimateH)
}

// Offset reports the current scroll offset in px.
func (s *State) Offset() int { return s.list.Offset() }

// AtTop reports whether the view is scrolled to the start of the history.
func (s *State) AtTop() bool { return s.Offset() == 0 }

// Rendered reports how many display rows the last frame laid out.
func (s *State) Rendered() int { return s.list.Rendered() }

// SetExpanded sets the expansion state of a display row or item ID.
func (s *State) SetExpanded(key string, open bool) { s.list.SetExpanded(key, open) }

func virtualItems(items []*session.Item) []kit.VirtualItem {
	return virtualDisplayItems(buildDisplayRows(items))
}

func virtualDisplayItems(rows []displayRow) []kit.VirtualItem {
	vis := make([]kit.VirtualItem, len(rows))
	for i := range rows {
		vis[i] = kit.VirtualItem{Key: rows[i].key, Revision: rows[i].rev, Estimate: theme.CardEstimate}
	}
	return vis
}

// Layout lays the transcript out inside the caller's constraints.
func Layout(env *kit.Env, gtx *layout.Context, st *State, items []*session.Item) layout.Dimensions {
	return LayoutWithCallbacks(env, gtx, st, items, Callbacks{})
}

// LayoutWithCallbacks lays the transcript out centered up to theme.ProseMeasure
// and delivers link, chip, and full-output actions to cb.
func LayoutWithCallbacks(env *kit.Env, gtx *layout.Context, st *State, items []*session.Item, cb Callbacks) layout.Dimensions {
	rows := buildDisplayRows(items)
	vis := virtualDisplayItems(rows)
	availW := gtx.Constraints.Max.X
	colMax := gtx.Dp(theme.ProseMeasure)
	colW := min(availW, colMax)
	padX := max((availW-colW)/2, 0)

	inner := *gtx
	inner.Constraints.Min.X = colW
	inner.Constraints.Max.X = colW

	off := op.Offset(image.Pt(padX, 0)).Push(gtx.Ops)
	dims := st.list.Layout(env, &inner, kit.VirtualListProps{FollowBottom: true}, vis,
		func(env *kit.Env, gtx *layout.Context, i int, _ kit.VirtualItem, _ *kit.VirtualItemState) layout.Dimensions {
			return layoutRow(env, gtx, st, &rows[i], cb)
		})
	off.Pop()
	return layout.Dimensions{Size: image.Pt(availW, dims.Size.Y)}
}
