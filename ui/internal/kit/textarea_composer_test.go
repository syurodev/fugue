package kit

import (
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"
)

// TestTextAreaEnterSubmitsAndShiftEnterNewlines pins the composer's submit
// key (§6.9, F10.4b): with EnterSubmits, a plain Return reports Submitted and
// never inserts, Shift+Return inserts the newline and never submits, and the
// keypad Enter submits like the keyboard's Return.
func TestTextAreaEnterSubmitsAndShiftEnterNewlines(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextAreaState()
	p := &TextAreaProps{MaxLines: 4, EnterSubmits: true, Placeholder: "compose"}
	runFrame(t, h, func(gtx *layout.Context) {
		st.Layout(h.env, gtx, p)
		gtx.Execute(key.FocusCmd{Tag: st.Tag()})
	})
	runFrame(t, h, func(gtx *layout.Context) {
		st.Layout(h.env, gtx, p)
	})

	// Plain Return: submitted, nothing inserted.
	h.tap(key.NameReturn, 0)
	var ev FieldEvents
	runFrame(t, h, func(gtx *layout.Context) {
		ev = st.Layout(h.env, gtx, p)
	})
	if !ev.Submitted || st.Text() != "" {
		t.Fatalf("return: submitted=%v text=%q, want submitted with an empty draft", ev.Submitted, st.Text())
	}

	// Shift+Return: the newline lands, no submit fires.
	h.tap(key.NameReturn, key.ModShift)
	ev = FieldEvents{}
	runFrame(t, h, func(gtx *layout.Context) {
		ev = st.Layout(h.env, gtx, p)
	})
	if ev.Submitted || st.Text() != "\n" {
		t.Fatalf("shift+return: submitted=%v text=%q, want the newline and no submit", ev.Submitted, st.Text())
	}

	// The keypad Enter submits like Return.
	h.tap(key.NameEnter, 0)
	ev = FieldEvents{}
	runFrame(t, h, func(gtx *layout.Context) {
		ev = st.Layout(h.env, gtx, p)
	})
	if !ev.Submitted || st.Text() != "\n" {
		t.Fatalf("keypad enter: submitted=%v text=%q, want submitted with the draft intact", ev.Submitted, st.Text())
	}
}

// TestTextAreaWithoutEnterSubmitsNeverSubmits pins the default: Enter inserts
// the newline and the core never reports Submitted — the submit key belongs
// to the caller, which is why a multi-line form field never submits on
// Return.
func TestTextAreaWithoutEnterSubmitsNeverSubmits(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextAreaState()
	p := &TextAreaProps{MaxLines: 4}
	runFrame(t, h, func(gtx *layout.Context) {
		st.Layout(h.env, gtx, p)
		gtx.Execute(key.FocusCmd{Tag: st.Tag()})
	})
	h.tap(key.NameReturn, 0)
	var ev FieldEvents
	runFrame(t, h, func(gtx *layout.Context) {
		ev = st.Layout(h.env, gtx, p)
	})
	if ev.Submitted || st.Text() != "\n" {
		t.Fatalf("default return: submitted=%v text=%q, want the newline and no submit", ev.Submitted, st.Text())
	}
}

// TestTextAreaScrollToCaretFollowsTypingBelowTheFold covers the composer's
// caret rule (§6.9, F10.4b): past the cap, typing below the fold moves the
// in-box viewport so the caret line stays visible, and a wheel scroll away
// from the caret is left alone until the caret moves again.
func TestTextAreaScrollToCaretFollowsTypingBelowTheFold(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextAreaState()
	p := &TextAreaProps{MaxLines: 3}
	st.SetText("l1\nl2\nl3\nl4\nl5")
	areaSettle(t, h, st, p)
	st.MoveCaretToEnd()
	areaSettle(t, h, st, p)
	st.ScrollToCaret()
	if st.scroll.Offset() <= 0 {
		t.Fatalf("offset = %d with the caret below the fold, want the viewport to have followed it", st.scroll.Offset())
	}

	// The wheel scrolls away from the caret; the caret has not moved, so
	// nothing snaps back.
	st.scroll.SetOffset(0)
	areaSettle(t, h, st, p)
	st.ScrollToCaret()
	if st.scroll.Offset() != 0 {
		t.Fatalf("offset = %d with the caret unmoved, want the wheel scroll left alone", st.scroll.Offset())
	}

	// Typing moves the caret: the viewport follows it again.
	st.core.ed.Insert("!")
	areaSettle(t, h, st, p)
	st.ScrollToCaret()
	if st.scroll.Offset() <= 0 {
		t.Fatalf("offset = %d after typing below the fold, want the viewport to have followed the caret", st.scroll.Offset())
	}
}
