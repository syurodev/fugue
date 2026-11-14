package kitspike

import (
	"testing"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
)

// TestSplitDragFollowsPointer covers §6.27: the drag maps the pointer x onto
// the ratio with no spring, within the min widths.
func TestSplitDragFollowsPointer(t *testing.T) {
	h := newHarness(t)
	s := NewSplitPaneState(0.5)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 60, MinRight: 60})
	h.end()
	// The handle sits at 0.5 of (400-8) ≈ 196..204. Press and drag to x=300.
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(200, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 60, MinRight: 60})
	h.end()
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: imageF32Pt(300, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 60, MinRight: 60})
	h.end()
	if r := s.Ratio(); r < 0.7 || r > 0.8 {
		t.Fatalf("after drag to 300 ratio = %v, want ~0.75", r)
	}
}

// TestSplitClampsToMinimums covers the min widths: a drag far left stops at
// MinLeft.
func TestSplitClampsToMinimums(t *testing.T) {
	h := newHarness(t)
	s := NewSplitPaneState(0.5)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 100, MinRight: 100})
	h.end()
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(196, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 100, MinRight: 100})
	h.end()
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: imageF32Pt(5, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 100, MinRight: 100})
	h.end()
	if got := s.Ratio(); got < 0.24 || got > 0.26 {
		t.Fatalf("ratio = %v, want ~0.25 (100/(400-8))", got)
	}
}

// TestSplitArrowsMoveHandle covers §6.27 keyboard: 16 dp per press, 64 with
// Shift, only while the handle is focused.
func TestSplitArrowsMoveHandle(t *testing.T) {
	h := newHarness(t)
	s := NewSplitPaneState(0.5)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	// Without focus the arrows do nothing.
	h.press(key.NameRightArrow, 0)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	if r := s.Ratio(); r != 0.5 {
		t.Fatalf("unfocused arrows moved the ratio to %v", r)
	}
	// Focus during the frame, then one arrow = 16 dp over a 392 span.
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.gtx.Execute(key.FocusCmd{Tag: s})
	h.end()
	h.press(key.NameRightArrow, 0)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	want := float32(196+16) / 392
	if r := s.Ratio(); r < want-0.01 || r > want+0.01 {
		t.Fatalf("after Right ratio = %v, want %v", r, want)
	}
	// Shift+arrow = 64 dp.
	h.press(key.NameRightArrow, key.ModShift)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	want = float32(196+16+64) / 392
	if r := s.Ratio(); r < want-0.01 || r > want+0.01 {
		t.Fatalf("after Shift+Right ratio = %v, want %v", r, want)
	}
}

// TestSplitDoubleClickResets covers §6.27: two quick releases return to the
// default ratio with the Smooth spring (so it animates, not snaps).
func TestSplitDoubleClickResets(t *testing.T) {
	h := newHarness(t)
	s := NewSplitPaneState(0.5)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	// Drag to the right.
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(196, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: imageF32Pt(350, 100), Buttons: pointer.ButtonPrimary})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: imageF32Pt(350, 100)})
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	if r := s.Ratio(); r < 0.8 {
		t.Fatalf("drag did not take: ratio %v", r)
	}
	// Double click on the handle: press+release twice within 300 ms.
	dclick := func() {
		h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(352, 100), Buttons: pointer.ButtonPrimary})
		h.frame(400, 200)
		s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
		h.end()
		h.router.Queue(pointer.Event{Kind: pointer.Release, Position: imageF32Pt(352, 100)})
		h.frame(400, 200)
		s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
		h.end()
	}
	dclick()
	dclick()
	// The reset animates with Smooth: after the settle frames it is back.
	for i := 0; i < 60; i++ {
		h.frame(400, 200)
		s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
		h.end()
	}
	if r := s.Ratio(); r < 0.49 || r > 0.51 {
		t.Fatalf("after double click ratio = %v, want 0.5", r)
	}
}

// noopPane is a pane that draws nothing.
func noopPane(gtx *layout.Context) layout.Dimensions { return layout.Dimensions{} }
