package kit

import (
	"fmt"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The reader-anchored scroll rules of desktop-app v0.9 §5.4 at the list's
// own boundary (F10.4d): an external anchor lands before the first paint and
// rides the item key, the scroll keys scroll like the wheel does, and a drag
// — the marking gesture — ends the bottom pin.

// scrollItems builds n uniformly 24 px rows named k-<i>.
func scrollItems(n int) []VirtualItem {
	items := make([]VirtualItem, n)
	for i := range items {
		items[i] = VirtualItem{Key: fmt.Sprintf("k-%d", i), Revision: 1, Estimate: theme.RowDense}
	}
	return items
}

// TestAnchorAtLandsBeforeTheFirstPaint pins the external anchor: one frame
// after AnchorAt the named key sits at the requested distance from the top,
// the pin is off, and the anchor rides the key when items insert above.
func TestAnchorAtLandsBeforeTheFirstPaint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := scrollItems(40)
	const rowH = 24
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 1, fixedRow(rowH))

	// Anchor k-10 with 24 px of context above it: the first paint is already
	// there, not part-way.
	vl.AnchorAt("k-10", unit.Dp(24))
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 1, fixedRow(rowH))
	if got := vl.Offset(); got != 10*rowH-24 {
		t.Fatalf("offset after the anchor = %d, want %d", got, 10*rowH-24)
	}
	if vl.Following() {
		t.Fatal("the anchor left the bottom pin on")
	}
	if got := vl.AnchorKey(); got != "k-10" {
		t.Fatalf("anchor key = %q, want k-10", got)
	}

	// Five items insert above: the anchor keeps k-10 at 24 px from the top.
	prep := scrollItems(5)
	for i := range prep {
		prep[i].Key = fmt.Sprintf("pre-%d", i)
	}
	items = append(prep, items...)
	layVirtualFrames(t, h, vl, VirtualListProps{}, items, 120, 2, fixedRow(rowH))
	if got := vl.Offset(); got != 15*rowH-24 {
		t.Fatalf("offset after the insert = %d, want %d (anchor rides the key)", got, 15*rowH-24)
	}
}

// TestScrollKeysMoveAndPinLikeTheWheel pins the scroll keys: End lands at
// the bottom and re-engages the pin, PageUp breaks it, PageDown re-engages
// at the bottom again, and Home goes to the top with the pin off.
func TestScrollKeysMoveAndPinLikeTheWheel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := scrollItems(60)
	const rowH, viewport = 24, 240
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if !vl.Following() {
		t.Fatal("the follow-bottom list did not pin on its first frame")
	}

	h.tap(key.NamePageUp, 0)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if vl.Following() {
		t.Fatal("a PageUp press kept the bottom pin")
	}
	if got := vl.Offset(); got >= vl.MaxOffset() {
		t.Fatalf("offset after PageUp = %d, want it away from the bottom %d", got, vl.MaxOffset())
	}

	h.tap(key.NameEnd, 0)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if !vl.Following() {
		t.Fatal("End at the bottom did not re-engage the pin")
	}

	h.tap(key.NameHome, 0)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if vl.Following() || vl.Offset() != 0 {
		t.Fatalf("Home left offset %d with the pin on %v", vl.Offset(), vl.Following())
	}

	// One viewport above the bottom edge, a PageDown lands exactly at it,
	// which re-engages the pin (v0.9: only the newest edge does).
	vl.SetOffset(40 * rowH)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if vl.Following() {
		t.Fatal("the mid-list position kept the bottom pin")
	}
	h.tap(key.NamePageDown, 0)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, viewport, 1, fixedRow(rowH))
	if !vl.Following() {
		t.Fatal("PageDown to the bottom did not re-engage the pin")
	}
}

// TestDragGestureEndsTheFollow pins the marking gesture: a drag over the
// list ends the bottom pin like an upward wheel, and new content stops
// dragging the view.
func TestDragGestureEndsTheFollow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vl := NewVirtualListState()
	items := scrollItems(60)
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, 240, 1, fixedRow(24))
	if !vl.Following() {
		t.Fatal("the list did not pin on its first frame")
	}

	h.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: f32.Pt(150, 100), Buttons: pointer.ButtonPrimary})
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, 240, 1, fixedRow(24))
	// A pressed Move is the drag the router delivers.
	h.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(150, 60), Buttons: pointer.ButtonPrimary})
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, 240, 1, fixedRow(24))
	if vl.Following() {
		t.Fatal("a drag over the list kept the bottom pin")
	}
	before := vl.Offset()
	layVirtualFrames(t, h, vl, VirtualListProps{FollowBottom: true}, items, 240, 1, fixedRow(24))
	if got := vl.Offset(); got != before {
		t.Fatalf("offset drifted %d -> %d without a reader act", before, got)
	}
}
