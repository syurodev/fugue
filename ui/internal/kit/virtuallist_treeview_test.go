package kit

import (
	"errors"
	"fmt"
	"image"
	"math"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

var errBoom = errors.New("boom")

const testRootKey = "root"

func layVirtualFrames(t *testing.T, h *harness, vl *VirtualListState, p VirtualListProps, items []VirtualItem, hgt, n int, row VirtualRowFunc) {
	t.Helper()
	for range n {
		h.frameSize(t, 300, hgt)
		h.env.TrackInput(&h.gtx)
		vl.Layout(h.env, &h.gtx, p, items, row)
		h.end()
	}
}

func layTreeFrames(t *testing.T, h *harness, tv *TreeViewState, p TreeViewProps, hgt, n int) TreeViewEvents {
	t.Helper()
	var ev TreeViewEvents
	for range n {
		h.frameSize(t, 260, hgt)
		h.env.TrackInput(&h.gtx)
		_, ev = tv.Layout(h.env, &h.gtx, p)
		h.end()
	}
	return ev
}

func fixedRow(hPx int) VirtualRowFunc {
	return func(_ *Env, gtx *layout.Context, _ int, _ VirtualItem, _ *VirtualItemState) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, hPx)}
	}
}

func TestVirtualListKeepsItsAnchorWhenItemsInsertAbove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := make([]VirtualItem, 40)
	for i := range items {
		items[i] = VirtualItem{Key: fmt.Sprintf("k-%d", i), Revision: 1, Estimate: theme.RowDense}
	}

	const rowH = 24
	// Scroll down 10 rows (240 px) so k-10 is anchored at the top.
	vl.SetOffset(10 * rowH)
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 2, fixedRow(rowH))
	if vl.Offset() != 10*rowH {
		t.Fatalf("initial offset = %d, want %d", vl.Offset(), 10*rowH)
	}

	// Insert 5 items above k-10 without calling AccountForPrepended: anchor
	// keeps k-10 at the top of the viewport.
	prep := make([]VirtualItem, 0, 5+len(items))
	for i := range 5 {
		prep = append(prep, VirtualItem{Key: fmt.Sprintf("pre-%d", i), Revision: 1, Estimate: theme.RowDense})
	}
	items = append(prep, items...)
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 2, fixedRow(rowH))
	if vl.Offset() != 15*rowH {
		t.Fatalf("offset after insert above = %d, want %d (anchor kept)", vl.Offset(), 15*rowH)
	}
}

func TestVirtualListLaysOutOnlyTheWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := make([]VirtualItem, 10000)
	for i := range items {
		items[i] = VirtualItem{Key: fmt.Sprintf("row-%d", i), Revision: 1, Estimate: theme.RowDense}
	}

	// Viewport 240 px = 10 rows; render window = 10 + 50 above + 50 below <= 120.
	vl.SetOffset(5000 * 24)
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 240, 2, fixedRow(24))
	if vl.Rendered() <= 0 || vl.Rendered() > 120 {
		t.Fatalf("rendered %d of 10000 items, want bounded window (1..120)", vl.Rendered())
	}
	if vl.CacheLen() > maxVirtualHeights {
		t.Fatalf("cache len %d exceeds bound %d", vl.CacheLen(), maxVirtualHeights)
	}

	// Per-key persistent state survives across revisions (§6.26).
	vl.SetExpanded("row-5000", true)
	items[5000].Revision = 2
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 240, 1, fixedRow(36))
	if !vl.Expanded("row-5000") {
		t.Fatal("expanded state lost across revision")
	}
	if vl.ItemState("row-5000").MeasuredHeight != 36 {
		t.Fatalf("measured height = %d, want 36", vl.ItemState("row-5000").MeasuredHeight)
	}
}

func TestVirtualListDoesNotStickToBottomUnlessAsked(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := make([]VirtualItem, 30)
	for i := range items {
		items[i] = VirtualItem{Key: fmt.Sprintf("m-%d", i), Revision: 1, Estimate: theme.RowDense}
	}

	// Default FollowBottom is false: appending does not jump to bottom.
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 2, fixedRow(24))
	if vl.Following() || vl.Offset() != 0 {
		t.Fatalf("default FollowBottom should be off, got following=%v offset=%d", vl.Following(), vl.Offset())
	}

	// With FollowBottom true, sticks to bottom until user scrolls up (no-yank).
	vl2 := NewVirtualListState()
	layVirtualFrames(t, h, vl2, VirtualListProps{FollowBottom: true}, items, 120, 2, fixedRow(24))
	if !vl2.Following() || vl2.Offset() != vl2.MaxOffset() {
		t.Fatalf("expected following at bottom, got following=%v offset=%d max=%d", vl2.Following(), vl2.Offset(), vl2.MaxOffset())
	}
	h.router.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(100, 60), Scroll: f32.Pt(0, -48)})
	layVirtualFrames(t, h, vl2, VirtualListProps{FollowBottom: true}, items, 120, 1, fixedRow(24))
	if vl2.Following() {
		t.Fatal("scrolling up must stop Following (no-yank rule)")
	}
	if vl2.BarAlpha() <= 0 {
		t.Fatal("scrollbar should appear on scroll")
	}
	// Advance past 1 s idle: bar fades out.
	h.now = h.now.Add(1500 * time.Millisecond)
	layVirtualFrames(t, h, vl2, VirtualListProps{FollowBottom: true}, items, 120, 40, fixedRow(24))
	if vl2.BarAlpha() != 0 {
		t.Fatalf("scrollbar alpha after idle = %v, want 0", vl2.BarAlpha())
	}
}

func TestTreeViewArrowsOpenCloseAndMoveToParent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tv := NewTreeViewState()
	roots := []TreeNode{
		{
			Key:         testRootKey,
			Label:       "Root",
			HasChildren: true,
			Children: []TreeNode{
				{Key: "c1", Label: "Child 1"},
				{Key: "c2", Label: "Child 2"},
			},
		},
		{Key: "leaf", Label: "Leaf"},
	}
	p := TreeViewProps{Roots: roots}

	// Click "root" body (past the chevron) to select and focus it.
	layTreeFrames(t, h, tv, p, 160, 2)
	h.click(120, 12)
	layTreeFrames(t, h, tv, p, 160, 2)
	if tv.Selected() != testRootKey || tv.Expanded(testRootKey) {
		t.Fatalf("after body click: selected=%q expanded=%v", tv.Selected(), tv.Expanded(testRootKey))
	}

	// Right arrow opens collapsed branch; second Right moves to first child.
	h.router.Queue(key.Event{Name: key.NameRightArrow, State: key.Press})
	layTreeFrames(t, h, tv, p, 160, 30)
	if !tv.Expanded(testRootKey) {
		t.Fatal("Right arrow should open collapsed branch")
	}
	h.router.Queue(key.Event{Name: key.NameRightArrow, State: key.Press})
	layTreeFrames(t, h, tv, p, 160, 2)
	if tv.Selected() != "c1" {
		t.Fatalf("Right on expanded branch selected %q, want c1", tv.Selected())
	}

	// Left arrow on child moves back to parent; second Left collapses it.
	h.router.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	layTreeFrames(t, h, tv, p, 160, 2)
	if tv.Selected() != testRootKey {
		t.Fatalf("Left on child selected %q, want root", tv.Selected())
	}
	h.router.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	layTreeFrames(t, h, tv, p, 160, 30)
	if tv.Expanded(testRootKey) {
		t.Fatal("Left on expanded branch should collapse it")
	}
	checkTreeChevronAndEnter(t, h, tv, p)
}

func checkTreeChevronAndEnter(t *testing.T, h *harness, tv *TreeViewState, p TreeViewProps) {
	t.Helper()
	h.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	ev := layTreeFrames(t, h, tv, p, 160, 1)
	if ev.Opened != testRootKey {
		t.Fatalf("Enter opened %q, want root", ev.Opened)
	}
	h.click(120, 36)
	layTreeFrames(t, h, tv, p, 160, 2)
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 160, 2)
	if !tv.Expanded(testRootKey) || tv.Selected() != "leaf" {
		t.Fatalf("chevron click: expanded=%v selected=%q, want true, leaf", tv.Expanded(testRootKey), tv.Selected())
	}
}

func TestTreeViewLoadsABranchOnlyWhenOpened(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tv := NewTreeViewState()
	loads := 0
	var pendingDone func([]TreeNode, error)
	p := TreeViewProps{
		Roots: []TreeNode{{Key: "lazy", Label: "Lazy", HasChildren: true}},
		Loader: func(nodeKey string, done func([]TreeNode, error)) {
			loads++
			pendingDone = done
		},
	}

	// Closed branch never calls Loader.
	layTreeFrames(t, h, tv, p, 120, 3)
	if loads != 0 {
		t.Fatalf("loader called %d times while closed, want 0", loads)
	}

	// Click chevron to open: Loader called once; before 150 ms no loading row.
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 120, 2)
	if loads != 1 {
		t.Fatalf("loader called %d times on open, want 1", loads)
	}
	if tv.List().Rendered() != 1 {
		t.Fatalf("rendered %d rows before 150 ms, want 1 (spinner delayed)", tv.List().Rendered())
	}

	// Past 150 ms: loading row with Spinner renders.
	h.now = h.now.Add(160 * time.Millisecond)
	layTreeFrames(t, h, tv, p, 120, 2)
	if tv.List().Rendered() != 2 {
		t.Fatalf("rendered %d rows after 150 ms, want 2 (branch + loading row)", tv.List().Rendered())
	}

	// Complete loader with error: error row renders.
	pendingDone(nil, errBoom)
	layTreeFrames(t, h, tv, p, 120, 2)
	if !tv.node("lazy").loading && tv.node("lazy").loadErr == nil {
		t.Fatal("expected loadErr after failed load")
	}

	// Close and re-open to retry, then succeed with children.
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 120, 30)
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 120, 2)
	if loads != 2 {
		t.Fatalf("loader called %d times on retry, want 2", loads)
	}
	pendingDone([]TreeNode{{Key: "k1", Label: "Loaded 1"}, {Key: "k2", Label: "Loaded 2"}}, nil)
	layTreeFrames(t, h, tv, p, 120, 2)
	if tv.List().Rendered() != 3 {
		t.Fatalf("rendered %d rows after load, want 3", tv.List().Rendered())
	}
}

func TestTreeViewStateIsKeyedByNode(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tv := NewTreeViewState()
	roots := []TreeNode{
		{Key: "a", Label: "A", HasChildren: true, Children: []TreeNode{{Key: "a1", Label: "A1"}}},
		{Key: "b", Label: "B", HasChildren: true, Children: []TreeNode{{Key: "b1", Label: "B1"}}},
	}
	tv.SetSelected("b1")
	tv.SetExpanded("b", true)
	layTreeFrames(t, h, tv, TreeViewProps{Roots: roots}, 160, 30)

	// Insert a new root above: selection and expansion stay on "b1" and "b".
	roots = append([]TreeNode{{Key: "pre", Label: "Pre"}}, roots...)
	layTreeFrames(t, h, tv, TreeViewProps{Roots: roots}, 160, 2)
	if tv.Selected() != "b1" || !tv.Expanded("b") {
		t.Fatalf("state lost on insert above: selected=%q expanded(b)=%v", tv.Selected(), tv.Expanded("b"))
	}
}

func TestTreeViewMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tv := NewTreeViewState()
	roots := []TreeNode{
		{Key: "b", Label: "Branch", HasChildren: true, Children: []TreeNode{{Key: "c", Label: "Child"}}},
	}
	p := TreeViewProps{Roots: roots}
	layTreeFrames(t, h, tv, p, 120, 2)

	// Open branch: chevron rotates toward pi/2 and presence grows on Smooth.
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 120, 3)
	midAng := tv.node("b").chevF.Value()
	if midAng <= 0 || midAng >= 1 {
		t.Fatalf("mid-open chevron progress = %v, want strictly between 0 and 1", midAng)
	}
	// Retarget closed mid-flight: settles back to 0.
	h.click(18, 12)
	layTreeFrames(t, h, tv, p, 120, 60)
	if got := tv.node("b").chevF.Value() * math.Pi / 2; got != 0 {
		t.Fatalf("retargeted chevron angle = %v, want 0", got)
	}
	if tv.node("b").presence.Visible() {
		t.Fatal("expected branch presence settled closed")
	}
}

func TestTreeViewTextContrastOnChromeSurfaces(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		env := newModeEnv(t, mode.th)
		c := env.Th.Chrome()
		st := env.Th.Status()
		if got := contrastRatio(c.Fg, c.AppBg); got < 4.5 {
			t.Fatalf("%s Fg on AppBg contrast = %.2f, want >= 4.5", mode.name, got)
		}
		if got := contrastRatio(c.FgMuted, c.AppBg); got < 4.5 {
			t.Fatalf("%s FgMuted on AppBg contrast = %.2f, want >= 4.5", mode.name, got)
		}
		if got := contrastRatio(st.Danger, c.AppBg); got < 4.5 {
			t.Fatalf("%s Danger on AppBg contrast = %.2f, want >= 4.5", mode.name, got)
		}
	}
}
