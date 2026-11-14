package kitspike

import (
	"testing"

	"gioui.org/io/pointer"
)

func TestDebugDblClick(t *testing.T) {
	h := newHarness(t)
	s := NewSplitPaneState(0.5)
	h.frame(400, 200)
	s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
	h.end()
	// drag right
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
	t.Logf("drag done ratio %v", s.Ratio())
	for round := 0; round < 2; round++ {
		h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(352, 100), Buttons: pointer.ButtonPrimary})
		h.frame(400, 200)
		s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
		h.end()
		h.router.Queue(pointer.Event{Kind: pointer.Release, Position: imageF32Pt(352, 100)})
		h.frame(400, 200)
		s.Layout(h.env, &h.gtx, noopPane, noopPane, SplitProps{Default: 0.5, MinLeft: 10, MinRight: 10})
		h.end()
		t.Logf("round %d: ratio %v resetting %v lastUp %v now %v", round, s.Ratio(), s.resetting, s.lastUp.UnixMilli(), clockNow.UnixMilli())
	}
}
