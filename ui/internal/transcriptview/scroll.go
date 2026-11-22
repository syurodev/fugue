package transcriptview

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The scroll rules of desktop-app v0.9 §5.4 ("Cuộn", "Thao tác của user
// thắng"): a "Latest" jump button at the bottom edge, the reader's own
// expand/collapse choices winning over the auto rules, and the anchors the
// appwin arms — a sent message, a reopened session, a `#n` jump.

// maxChoiceEntries bounds one session's user-choice store (F10.4d): paging
// through a 10,000-item transcript cannot grow it without limit. Entries
// evict oldest-first; an evicted choice only re-opens an item to the auto
// rules, which act on the running turn — items far outside the viewport.
const maxChoiceEntries = 1024

// choiceStore holds the keys the reader manually expanded or collapsed. The
// open/closed values live in the list's per-key state; the store adds only
// the "the reader chose this, the auto rules stand down" mark that must
// survive the payload cache's eviction when the item leaves the render
// window and comes back.
type choiceStore struct {
	chose map[string]bool
	ring  []string
	next  int
}

func (c *choiceStore) add(key string) {
	if c.chose == nil {
		c.chose = map[string]bool{}
	}
	if c.chose[key] {
		return
	}
	if c.ring == nil {
		c.ring = make([]string, maxChoiceEntries)
	}
	if old := c.ring[c.next]; old != "" {
		delete(c.chose, old)
	}
	c.ring[c.next] = key
	c.chose[key] = true
	c.next = (c.next + 1) % maxChoiceEntries
}

func (c *choiceStore) has(key string) bool { return c.chose[key] }

func (c *choiceStore) len() int { return len(c.chose) }

// toggleUserChoice flips key's expansion and records the reader's choice:
// the auto open and close rules stand down for it (desktop-app v0.9 §5.4,
// "Thao tác của user thắng"). The click paths of the work-group, reasoning
// and sub-group headers all come through here.
func (s *State) toggleUserChoice(key string) {
	s.choices.add(key)
	s.list.ToggleExpanded(key)
}

// userChose reports whether the reader manually toggled key: their choice
// wins over auto-open and auto-collapse across frames and virtualization.
func (s *State) userChose(key string) bool { return s.choices.has(key) }

// AnchorTurn anchors itemID near the top with SpaceTurnContext of the
// preceding item above it: a sent message, a reopened session's last turn,
// a `#n` jump (desktop-app v0.9 §5.4). The follow ends — the reader left
// the newest edge on purpose — and the reply streams below without dragging
// the view.
func (s *State) AnchorTurn(itemID string) { s.list.AnchorAt(itemID, theme.SpaceTurnContext) }

// FollowLatest re-engages the follow-at-bottom: the "Latest" button's jump.
func (s *State) FollowLatest() { s.list.ResumeFollow() }

// AnchoredAt reports the item the view is anchored to, if any. The appwin
// reads it to pin the reopen and turn-anchor wiring.
func (s *State) AnchoredAt() string { return s.list.AnchorKey() }

// latestShown and latestDot report the "Latest" button's last-frame state:
// shown while the reader is away from the newest edge, dotted while a
// running item is off-screen (v0.9, "Nút Mới nhất").
type latestButton struct {
	click widget.Clickable
	shown bool
	dot   bool
	rect  image.Rectangle
}

// breakFollowOnContent wraps the content callbacks: opening a link, a file,
// a diff or jumping through an `#n` chip is a reader act, so the bottom pin
// ends first (v0.9: "bấm link hay mở tìm kiếm thì ngừng bám").
func breakFollowOnContent(st *State, cb Callbacks) Callbacks {
	off := st.FollowOff
	if cb.OnOpenLink != nil {
		open := cb.OnOpenLink
		cb.OnOpenLink = func(url string) { off(); open(url) }
	}
	if cb.OnFileChip != nil {
		chip := cb.OnFileChip
		cb.OnFileChip = func(ref mdview.FileRef) { off(); chip(ref) }
	}
	if cb.OnOpenFile != nil {
		file := cb.OnOpenFile
		cb.OnOpenFile = func(ref mdview.FileRef) { off(); file(ref) }
	}
	if cb.OnViewDiff != nil {
		diff := cb.OnViewDiff
		cb.OnViewDiff = func(itemID string) { off(); diff(itemID) }
	}
	if cb.OnIDChip != nil {
		id := cb.OnIDChip
		cb.OnIDChip = func(itemID string) { off(); id(itemID) }
	}
	return cb
}

// runningRow returns the last display row with a running item, -1 when the
// transcript has none.
func runningRow(rows []displayRow) int {
	idx := -1
	for i := range rows {
		if rowRuns(&rows[i]) {
			idx = i
		}
	}
	return idx
}

func rowRuns(row *displayRow) bool {
	if row.group != nil {
		return row.group.running
	}
	return row.item != nil && isItemRunning(row.item)
}

// drawLatest lays the "Latest" jump button over the bottom edge of the
// viewport when the reader is away from the newest edge, with the running
// dot while a running item is off-screen (desktop-app v0.9 §5.4). Clicking
// it re-engages the follow-at-bottom.
func drawLatest(env *kit.Env, gtx *layout.Context, st *State, rows []displayRow, vis []kit.VirtualItem) {
	shown := !st.Following() && st.list.MaxOffset() > 0
	dot := false
	st.latest.rect = image.Rectangle{}
	if shown {
		viewport := gtx.Constraints.Max.Y
		first, last := st.list.Visible(gtx, vis, viewport)
		if run := runningRow(rows); run >= 0 && (run < first || run > last) {
			dot = true
		}
	}
	st.latest.shown, st.latest.dot = shown, dot
	if !shown {
		return
	}
	if st.latest.click.Clicked(*gtx) {
		st.FollowLatest()
	}

	rec := op.Record(gtx.Ops)
	pill := layoutPill(env, gtx, &st.latest.click, dot)
	call := rec.Stop()
	margin := gtx.Dp(theme.SpaceMD)
	x := max((gtx.Constraints.Max.X-pill.Size.X)/2, 0)
	y := max(gtx.Constraints.Max.Y-pill.Size.Y-margin, 0)
	st.latest.rect = image.Rect(x, y, x+pill.Size.X, y+pill.Size.Y)
	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
}

// LatestShown reports whether the last frame drew the "Latest" button.
func (s *State) LatestShown() bool { return s.latest.shown }

// LatestDot reports whether the button's running dot showed.
func (s *State) LatestDot() bool { return s.latest.dot }

// LatestRect returns the button's last-frame rect, for pointer tests.
func (s *State) LatestRect() image.Rectangle { return s.latest.rect }

// layoutPill draws the button's pill and returns its size; the recorded ops
// play back at the pill's final position.
func layoutPill(env *kit.Env, gtx *layout.Context, click *widget.Clickable, dot bool) layout.Dimensions {
	ch := env.Th.Chrome()
	padX := gtx.Dp(theme.SpaceMD)
	padY := gtx.Dp(theme.SpaceSM)
	rec := op.Record(gtx.Ops)
	inner := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !dot {
				return layout.Dimensions{}
			}
			d := gtx.Dp(theme.SpaceSM)
			paint.FillShape(gtx.Ops, env.Th.Status().Info, clip.Ellipse(image.Rect(0, 0, d, d)).Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(d, d)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !dot {
				return layout.Dimensions{}
			}
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, theme.TypeSmall(), ch.Fg, "Latest")
		}),
	)
	call := rec.Stop()
	size := image.Pt(inner.Size.X+2*padX, inner.Size.Y+2*padY)
	paint.FillShape(gtx.Ops, ch.SurfaceRaised,
		clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops))
	stroke := clip.Stroke{
		Path:  clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusMD)).Path(gtx.Ops),
		Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}
	paint.FillShape(gtx.Ops, ch.BorderSubtle, stroke.Op())
	off := op.Offset(image.Pt(padX, padY)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: size}
	})
	return layout.Dimensions{Size: size}
}
