package kit

import (
	"errors"
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
)

// TestToastPausesOnHoverAndNeverTakesFocus covers §6.18: the timer stops
// while the pointer hovers the toast and resumes after; the toast never
// takes focus — whatever is focused stays focused for the toast's whole
// life.
func TestToastPausesOnHoverAndNeverTakesFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	steady := NewButtonState()
	spr := ButtonProps{Text: testTriggerLabel, Variant: Bordered}
	laySteady := func(gtx *layout.Context) {
		if _, err := steady.Layout(h.env, gtx, spr); err != nil {
			t.Fatalf("lay the button out: %v", err)
		}
	}
	h.env.ToastPush(&Toast{Object: testToastObject, Icon: IconCheck, Title: testToastTitle, Detail: testToastDetail})
	runFrame(t, h, func(gtx *layout.Context) {
		laySteady(gtx)
		gtx.Execute(key.FocusCmd{Tag: steady.Tag()})
	})
	runFrame(t, h, laySteady)
	if !h.gtx.Focused(steady.Tag()) {
		t.Fatal("the focus anchor did not take focus")
	}
	rect := assertToastShown(t, h)

	// Two of its four seconds pass; the toast is still there, and the
	// driver holds a wake for its expiry.
	h.now = h.now.Add(2 * time.Second)
	runFrame(t, h, laySteady)
	if !h.env.toast.list[0].open.Shown() {
		t.Fatal("the toast expired early")
	}
	if _, ok := h.env.Driver.Next(); !ok {
		t.Fatal("the toast scheduled no wake for its expiry")
	}
	assertToastHoverPause(t, h, laySteady, rect)
	assertToastExpiry(t, h, laySteady, steady.Tag())
}

// assertToastShown fails when the pushed toast is not visible and returns
// its rect.
func assertToastShown(t *testing.T, h *harness) image.Rectangle {
	t.Helper()
	if n := len(h.env.toast.list); n != 1 || !h.env.toast.list[0].open.Shown() {
		t.Fatalf("the toast did not show: %d entries", n)
	}
	rect := h.env.toast.list[0].rect
	if rect.Empty() {
		t.Fatal("the toast has no place")
	}
	return rect
}

// assertToastHoverPause moves the pointer onto the toast, expects the hover
// and that four more seconds do not expire it.
func assertToastHoverPause(t *testing.T, h *harness, laySteady func(*layout.Context), rect image.Rectangle) {
	t.Helper()
	centre := image.Pt((rect.Min.X+rect.Max.X)/2, (rect.Min.Y+rect.Max.Y)/2)
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(centre.X), float32(centre.Y))})
	runFrame(t, h, laySteady)
	if !h.env.toast.list[0].hover {
		t.Fatal("the toast did not see the hover")
	}
	h.now = h.now.Add(4 * time.Second)
	runFrame(t, h, laySteady)
	if !h.env.toast.list[0].open.Shown() {
		t.Fatal("the toast expired while hovered")
	}
}

// assertToastExpiry moves the pointer away, expects the remaining time to
// run out with one ToastClosed event, and that the anchor kept focus all
// along.
func assertToastExpiry(t *testing.T, h *harness, laySteady func(*layout.Context), anchor event.Tag) {
	t.Helper()
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(50, 50)})
	runFrame(t, h, laySteady)
	h.now = h.now.Add(3 * time.Second)
	runFrame(t, h, laySteady)
	if h.env.toast.list[0].open.Shown() {
		t.Fatal("the toast did not expire after the pointer left")
	}
	evs := h.env.ToastEvents()
	if len(evs) != 1 || evs[0].Kind != ToastClosed || evs[0].Object != testToastObject {
		t.Fatalf("the expiry came back as %+v, want one ToastClosed for %s", evs, testToastObject)
	}
	if !h.gtx.Focused(anchor) {
		t.Fatal("the toast took focus")
	}
}

// TestToastsForOneObjectMerge covers §6.18: a second event of the same
// object merges into the visible toast with a count, not a second toast;
// other objects stack; a fourth push exits the oldest so at most three are
// visible.
func TestToastsForOneObjectMerge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.ToastPush(&Toast{Object: testToastObject, Title: testToastTitle})
		h.env.ToastPush(&Toast{Object: testToastObject, Title: testToastTitle})
	})
	ens := h.env.toast.list
	if len(ens) != 1 || ens[0].count != 2 {
		t.Fatalf("two pushes of one object: %d entries, count %d, want one merged with count 2", len(ens), ens[0].count)
	}
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.ToastPush(&Toast{Object: "job-2", Title: "Index rebuilt"})
	})
	if n := h.env.toast.visibleCount(); n != 2 {
		t.Fatalf("a second object did not stack: %d visible", n)
	}
	pushOverflow(t, h)
}

var errToastBounded = errors.New("toast draw failed")

// TestToastPushMergeKeepsAFullStack covers the merge-first rule: with three
// toasts visible, a push into one of them merges and evicts nothing.
func TestToastPushMergeKeepsAFullStack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	runFrame(t, h, func(gtx *layout.Context) {
		for _, obj := range []string{"job-a", "job-b", "job-c"} {
			h.env.ToastPush(&Toast{Object: obj, Title: "Work " + obj})
		}
	})
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.ToastPush(&Toast{Object: "job-b", Title: "Work job-b"})
	})
	if n := h.env.toast.visibleCount(); n != toastMax {
		t.Fatalf("%d toasts visible after a merge, want %d", n, toastMax)
	}
	at := h.env.toast.findIndex("job-b")
	if at < 0 || h.env.toast.list[at].count != 2 {
		t.Fatalf("job-b not merged with count 2 (index %d)", at)
	}
	for _, ev := range h.env.ToastEvents() {
		if ev.Kind == ToastClosed {
			t.Fatalf("merge closed %q", ev.Object)
		}
	}
}

// TestToastErrorsStayBounded pins that an undrained persistent draw error
// keeps one pending entry, not one per frame.
func TestToastErrorsStayBounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for range 5 {
		h.env.noteToastError(errToastBounded)
	}
	if n := len(h.env.toastErrors); n != 1 {
		t.Fatalf("%d pending toast errors, want 1", n)
	}
}

// pushOverflow pushes three more objects: at most three toasts stay visible
// and the oldest exits with its event.
func pushOverflow(t *testing.T, h *harness) {
	t.Helper()
	for _, obj := range []string{"job-3", "job-4", "job-5"} {
		h.env.ToastPush(&Toast{Object: obj, Title: "Background work " + obj})
	}
	runFrame(t, h, func(gtx *layout.Context) {})
	if n := h.env.toast.visibleCount(); n > toastMax {
		t.Fatalf("%d toasts visible, want at most %d", n, toastMax)
	}
	if h.env.toast.findIndex(testToastObject) >= 0 {
		t.Fatal("the oldest toast was not exited by the fourth push")
	}
	closed := 0
	for _, ev := range h.env.ToastEvents() {
		if ev.Kind == ToastClosed {
			closed++
		}
	}
	if closed == 0 {
		t.Fatal("no ToastClosed event for the dismissed oldest")
	}
}
