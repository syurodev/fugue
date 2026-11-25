package kit

import "testing"

// TestPollClickExpiresNextFrameWithoutLayout verifies that a click drained by
// PollClick expires on the next frame if Layout did not run in the same frame.
func TestPollClickExpiresNextFrameWithoutLayout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	btn := NewIconButtonState()
	props := IconButtonProps{Icon: IconPlus, Label: "Add"}

	// Frame 1: lay out the button to register its hit area.
	h.frame(t)
	if _, err := btn.Layout(h.env, &h.gtx, &props); err != nil {
		t.Fatalf("IconButton.Layout: %v", err)
	}
	h.end()

	// Frame 2: click arrives, PollClick drains it, but Layout is skipped.
	h.click(12, 12)
	h.frame(t)
	if !btn.PollClick(&h.gtx, false, false) {
		t.Fatal("PollClick returned false on the clicked frame")
	}
	h.end()

	// Frame 3: next frame must not see the stale click in PollClick or Layout.
	h.frame(t)
	if btn.PollClick(&h.gtx, false, false) {
		t.Fatal("PollClick repeated the click on the next frame without Layout")
	}
	clicked, err := btn.Layout(h.env, &h.gtx, &props)
	if err != nil {
		t.Fatalf("IconButton.Layout: %v", err)
	}
	if clicked {
		t.Fatal("Layout repeated the click on the next frame after PollClick without Layout")
	}
	h.end()
}

// TestPollClickRespectsDisabledAndBusy verifies that PollClick never reports a
// click while Disabled or Busy is set and drains pending clicks so re-enabling
// does not fire a stale click.
func TestPollClickRespectsDisabledAndBusy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	btn := NewButtonState()
	props := ButtonProps{Text: labelSave}

	h.frame(t)
	if _, err := btn.Layout(h.env, &h.gtx, props); err != nil {
		t.Fatalf("Button.Layout: %v", err)
	}
	h.end()

	// Click while disabled.
	h.click(20, 12)
	h.frame(t)
	if btn.PollClick(&h.gtx, true, false) {
		t.Fatal("PollClick reported click while disabled")
	}
	if clicked, err := btn.Layout(h.env, &h.gtx, ButtonProps{Text: labelSave, Disabled: true}); err != nil || clicked {
		t.Fatalf("disabled Button.Layout clicked=%v err=%v", clicked, err)
	}
	h.end()

	// Re-enabled next frame: stale click must not fire.
	h.frame(t)
	if btn.PollClick(&h.gtx, false, false) {
		t.Fatal("PollClick fired stale click after re-enable")
	}
	if _, err := btn.Layout(h.env, &h.gtx, props); err != nil {
		t.Fatalf("Button.Layout: %v", err)
	}
	h.end()

	// Click while busy.
	h.click(20, 12)
	h.frame(t)
	if btn.PollClick(&h.gtx, false, true) {
		t.Fatal("PollClick reported click while busy")
	}
	if clicked, err := btn.Layout(h.env, &h.gtx, ButtonProps{Text: labelSave, Busy: true}); err != nil || clicked {
		t.Fatalf("busy Button.Layout clicked=%v err=%v", clicked, err)
	}
	h.end()

	// Idle next frame: stale click must not fire.
	h.frame(t)
	if btn.PollClick(&h.gtx, false, false) {
		t.Fatal("PollClick fired stale click after busy cleared")
	}
	h.end()
}

// TestPollClickAndLayoutSameFrameCountsOnce verifies that PollClick and Layout
// in the same frame report the click for that frame only and never repeat it on
// the following frame.
func TestPollClickAndLayoutSameFrameCountsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	btn := NewIconButtonState()
	toggled := false

	h.frame(t)
	if _, err := btn.Layout(h.env, &h.gtx, &IconButtonProps{Icon: IconPlus, Label: labelPin, Toggled: toggled}); err != nil {
		t.Fatalf("IconButton.Layout: %v", err)
	}
	h.end()

	h.click(12, 12)
	h.frame(t)
	polled := btn.PollClick(&h.gtx, false, false)
	if !polled {
		t.Fatal("PollClick returned false on clicked frame")
	}
	toggled = !toggled
	laidOut, err := btn.Layout(h.env, &h.gtx, &IconButtonProps{Icon: IconPlus, Label: labelPin, Toggled: toggled})
	if err != nil {
		t.Fatalf("IconButton.Layout: %v", err)
	}
	if !laidOut {
		t.Fatal("Layout in the same frame did not report the polled click")
	}
	h.end()

	// Next frame must see zero clicks in both PollClick and Layout.
	h.frame(t)
	if btn.PollClick(&h.gtx, false, false) {
		t.Fatal("PollClick repeated click on the next frame")
	}
	laidOut2, err := btn.Layout(h.env, &h.gtx, &IconButtonProps{Icon: IconPlus, Label: labelPin, Toggled: toggled})
	if err != nil {
		t.Fatalf("IconButton.Layout: %v", err)
	}
	if laidOut2 {
		t.Fatal("Layout repeated click on the next frame")
	}
	h.end()
}
