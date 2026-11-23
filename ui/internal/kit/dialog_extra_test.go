package kit

import (
	"testing"

	"gioui.org/io/key"
)

const testExtraLabel = "Reject and restore"

// extraDialog is a confirm with the Extra footer action and a header icon.
func extraDialog() (*DialogState, *DialogProps, *ButtonState) {
	cancel, primary, extra := NewButtonState(), NewButtonState(), NewButtonState()
	props := &DialogProps{
		Variant:     DialogConfirm,
		Title:       testConfirmTitle,
		Description: testConfirmDesc,
		Icon:        IconLock,
		Cancel:      cancel, Primary: primary, PrimaryLabel: labelSave,
		Extra: extra, ExtraLabel: testExtraLabel, ExtraVariant: Danger,
	}
	return NewDialogState(), props, extra
}

// TestDialogExtraActionSitsOnTheFooterLeft covers the optional left footer
// action: it shares the footer row with Cancel and the primary, sits at the
// left content edge clear of both, reports its click without closing the
// dialog, and joins the Tab order right before Cancel.
func TestDialogExtraActionSitsOnTheFooterLeft(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d, props, extra := extraDialog()
	d.Open()
	res := dialogFrames(t, h, d, props, 2)
	b := res.Boxes
	assertExtraFooter(t, &b)

	// Tab from the safe choice (Cancel) wraps back through the primary to the
	// Extra action, which sits before Cancel in the trap.
	focusFrames := func(mod key.Modifiers) {
		h.tap(key.NameTab, mod)
		dialogFrames(t, h, d, props, 2)
	}
	focusFrames(key.ModShift)
	if !h.gtx.Focused(extra.Tag()) {
		t.Fatal("Shift+Tab from Cancel did not reach the Extra action")
	}

	h.click(float32(b.Extra.Min.X+b.Extra.Dx()/2), float32(b.Extra.Min.Y+b.Extra.Dy()/2))
	res = dialogFrames(t, h, d, props, 1)
	if !res.Extra || res.Closed || res.Submitted || !d.IsOpen() {
		t.Fatalf("an Extra click must report and keep the dialog open: %+v, open %v", res, d.IsOpen())
	}
}

// assertExtraFooter fails unless the three footer boxes exist, share a row,
// and run Extra, Cancel, primary left to right with Extra on the left edge.
func assertExtraFooter(t *testing.T, b *DialogBoxes) {
	t.Helper()
	if b.Extra.Empty() || b.Cancel.Empty() || b.Primary.Empty() {
		t.Fatalf("footer boxes missing: %+v", b)
	}
	if b.Extra.Min.Y != b.Cancel.Min.Y || b.Cancel.Min.Y != b.Primary.Min.Y {
		t.Fatalf("footer buttons are not on one row: %+v", b)
	}
	if b.Extra.Min.X != b.Body.Min.X || b.Extra.Max.X > b.Cancel.Min.X || b.Cancel.Max.X > b.Primary.Min.X {
		t.Fatalf("footer order or edge is wrong: %+v", b)
	}
}

// TestDialogWithoutExtraHasNoExtraBox pins the zero value: no Extra state
// means no Extra box and no click, so every other dialog is unchanged.
func TestDialogWithoutExtraHasNoExtraBox(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d, props, _ := extraDialog()
	props.Extra, props.Icon = nil, ""
	d.Open()
	res := dialogFrames(t, h, d, props, 2)
	if !res.Boxes.Extra.Empty() || res.Extra {
		t.Fatalf("a dialog without Extra drew one: %+v", res)
	}
}
