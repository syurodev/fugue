package appwin

import (
	"image"
	"maps"
	"math"
	"slices"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// A slot closed from inside its own frame (FUG-64): the header's close
// button runs while the cells draw, so the document loses a column under a
// frame that planned for it. The frame must still finish, and the next one
// lays out the smaller grid.

// The grid area of the close scenes: wide enough for the main column and two
// regular ones at their minimums, so no column folds into the tab strip.
const (
	closeAreaW = 1500
	closeAreaH = 800
)

// closeDoc is the default layout with one regular column per kind and the
// sidebar's session in the main slot.
func closeDoc(kinds ...SlotKind) LayoutDoc {
	doc := kindsFixture(kinds...)
	doc.Columns[0].Slots[0].Ref = SlotRef{Kind: KindSession, ID: testGridMainSess}
	return doc
}

// closeScene is a frame-driven grid: the screens, the headless frame loop
// and the area every frame lays the grid out into, at the window's origin,
// where pointer positions and the grid's rects are the same coordinates.
type closeScene struct {
	sc   *screens
	h    *frameRunner
	area image.Rectangle
}

// newCloseScene builds the shell over doc.
func newCloseScene(t *testing.T, doc LayoutDoc) *closeScene {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-close-frame"})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: testGridMainSess, Vendor: client.VendorClaude, State: stateReady},
	}})
	sc.grid.doc = cloneLayout(doc)
	return &closeScene{sc: sc, h: newFrameRunner(t), area: image.Rectangle{Max: image.Pt(closeAreaW, closeAreaH)}}
}

// frame lays one frame of the grid out the way layoutGridContainer does,
// with the fold plan derived from this frame's document; after, when set,
// inspects the frame's own context.
func (s *closeScene) frame(t *testing.T, after func(gtx *layout.Context)) {
	t.Helper()
	s.h.frame(t, s.area.Max, func(gtx *layout.Context) {
		plan := PlanFold(pxToDp(gtx, s.area.Dx()), true, len(s.sc.grid.doc.Columns))
		s.sc.grid.layoutArea(gtx, s.h.env, s.sc, s.area, plan)
		if after != nil {
			after(gtx)
		}
	})
}

// closeHitPoint is the center of the close button in the header of the slot
// at p: the header's last child, flush right inside its inset and centered
// in the band.
func (s *closeScene) closeHitPoint(gtx *layout.Context, p Pos) f32.Point {
	props := kit.IconButtonProps{Icon: kit.IconClose, Label: closeLabel, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
	head := s.sc.grid.rects[p.key()]
	side := s.sc.grid.closeBtns[p.key()].Measure(s.h.env, gtx, &props).Size.X
	return f32.Pt(float32(head.Max.X-gtx.Dp(theme.SpaceXS)-side/2), float32(head.Min.Y+min(gtx.Dp(theme.SlotHeaderHeight), head.Dy())/2))
}

// tap moves the pointer to at, presses and lays the frame that consumes it,
// then releases and lays the frame that consumes the release — the click
// lands while that frame draws the slots. It fails when the pointer is not
// over the slot's close button, so a moved header reports the miss and not
// a wrong grid.
func (s *closeScene) tap(t *testing.T, p Pos, at f32.Point) {
	t.Helper()
	s.h.router.Queue(pointer.Event{Kind: pointer.Move, Position: at, Source: pointer.Mouse})
	s.h.router.Queue(pointer.Event{Kind: pointer.Press, Position: at, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	s.frame(t, nil)
	if !s.sc.grid.closeBtns[p.key()].Hovered() {
		t.Fatalf("the pointer at %v is not over the close button of slot %s", at, p.key())
	}
	s.h.router.Queue(pointer.Event{Kind: pointer.Release, Position: at, Source: pointer.Mouse})
	s.frame(t, nil)
}

// TestClosingAColumnInsideItsFrameKeepsTheFrameAlive pins FUG-64: clicking a
// regular slot's close button shrinks the document under the frame drawing
// it; the frame finishes, only that slot leaves, and the next frame lays
// out the smaller grid.
func TestClosingAColumnInsideItsFrameKeepsTheFrameAlive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		open   []SlotKind
		closed SlotKind
	}{
		{"the only regular column", []SlotKind{KindInbox}, KindInbox},
		{"the last of three columns", []SlotKind{KindInbox, KindDiff}, KindDiff},
		{"the middle of three columns", []SlotKind{KindInbox, KindDiff}, KindInbox},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertSlotCloseInsideItsFrame(t, tc.open, tc.closed)
		})
	}
}

// assertSlotCloseInsideItsFrame clicks the close button of the slot that
// holds closed in a grid with one regular column per kind of open, and
// checks that the frame finished with exactly that column gone.
func assertSlotCloseInsideItsFrame(t *testing.T, open []SlotKind, closed SlotKind) {
	t.Helper()
	doc := closeDoc(open...)
	closedAt, ok := findCell(doc, SlotRef{Kind: closed})
	if !ok {
		t.Fatalf("slot %s is not in the scene", closed)
	}
	s := newCloseScene(t, doc)
	var at f32.Point
	s.frame(t, func(gtx *layout.Context) { at = s.closeHitPoint(gtx, closedAt) })
	s.tap(t, closedAt, at)

	if got := len(s.sc.grid.doc.Columns); got != len(open) {
		t.Fatalf("the grid has %d columns after closing %s, want %d", got, closed, len(open))
	}
	for _, kind := range open {
		if got := hasKind(s.sc.grid.doc, kind); got != (kind != closed) {
			t.Fatalf("slot %s open = %v after closing %s", kind, got, closed)
		}
	}
	// The frame after the close lays the smaller grid out.
	s.frame(t, nil)
	if len(s.sc.grid.rects) != len(open) || s.sc.grid.lastVisible != len(open) {
		t.Fatalf("the next frame laid out %d cells for %d visible columns, want %d each",
			len(s.sc.grid.rects), s.sc.grid.lastVisible, len(open))
	}
}

// TestStaleColumnCountsStayInsideTheDocument pins the guards a click cannot
// reach (FUG-64): asked for a column the document lost, the ratio helpers
// leave it alone and the handles draw only the boundaries that remain.
func TestStaleColumnCountsStayInsideTheDocument(t *testing.T) {
	t.Parallel()
	// The count a frame planned with before a close took the third column.
	const stale = 3
	doc := kindsFixture(KindInbox)
	if got := columnRatio(doc, 2, stale); got != 0 {
		t.Fatalf("the ratio of a boundary past the document is %v, want 0", got)
	}
	if got, want := columnRatio(doc, 0, stale), columnRatio(doc, 0, 2); got != want {
		t.Fatalf("a stale count moved boundary 0's ratio to %v, want %v", got, want)
	}

	g := newSlotGrid()
	g.doc = cloneLayout(doc)
	g.setColumnRatio(2, stale, 0.25)
	if diff := cmp.Diff(doc, g.doc); diff != "" {
		t.Fatalf("a move of a boundary past the document changed the layout (-want +got):\n%s", diff)
	}
	g.doc = cloneLayout(doc)
	g.setColumnRatio(0, stale, 0.25)
	if got := columnRatio(g.doc, 0, 2); math.Abs(float64(got-0.25)) > 1e-6 {
		t.Fatalf("boundary 0's ratio is %v after a move under a stale count, want 0.25 over the columns that remain", got)
	}

	h := newFrameRunner(t)
	area := image.Rectangle{Max: image.Pt(900, 600)}
	h.frame(t, area.Max, func(gtx *layout.Context) {
		geom := g.computeRects(gtx, area, 2)
		g.overlaySplits(gtx, h.env, geom, area, stale)
		g.overlayRowSplit(gtx, h.env, geom, stale, stale)
		g.overlaySplits(gtx, h.env, geom, area, 0)
	})
	if diff := cmp.Diff([]int{0}, slices.Sorted(maps.Keys(g.splitC))); diff != "" {
		t.Fatalf("the handles under a stale count (-want +got):\n%s", diff)
	}
	if len(g.splitR) != 0 {
		t.Fatalf("a row handle was laid out for a column that is gone: %v", maps.Keys(g.splitR))
	}
}
