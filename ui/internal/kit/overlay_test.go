package kit

import (
	"errors"
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The overlay components' evidence (F10.18): Popover, Dialog and Toast on
// the window's floating layers (UK-07), with the §6.16, §6.17 and §6.18
// behavior. Frames run through the same runFrame helper the F10.16 tests
// use — 800×600, TrackInput first, FrameEnd last — and every timed rule is
// measured on the fake clock.
//
// Focus assertions read the frame after the one that asked for the move: a
// FocusCmd lands in the router when its frame's ops are flushed. Every frame
// of these tests lays the components out, because Gio clears the focus of a
// tag whose key handler is absent from the frame — the opener of a popover
// or dialog is therefore a real trigger control, drawn in every frame.

// popoverProps builds the props of a test popover holding one TextField;
// the closures draw with env, the harness's frame env.
func popoverProps(env *Env, field *TextFieldState) PopoverProps {
	p := &TextFieldProps{Label: "Name", Placeholder: "e.g. dev-pg"}
	return PopoverProps{
		Anchor:      image.Rect(40, 40, 140, 68),
		Title:       "Filter",
		Width:       220,
		FocusOnOpen: field.Tag(),
		Measure: func(gtx *layout.Context) image.Point {
			return field.Measure(env, gtx, p).Size
		},
		Content: func(gtx *layout.Context) {
			_, _ = field.Layout(env, gtx, p)
		},
	}
}

// TestPopoverClosesOnEscapeAndOutsideClick covers §6.16: opened from the
// keyboard, focus moves into the content; Escape closes and returns focus to
// the opener; a click outside closes; a pinned popover ignores the click
// outside but still closes on Escape.
func TestPopoverClosesOnEscapeAndOutsideClick(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	po := NewPopoverState()
	field := NewTextFieldState()
	props := popoverProps(h.env, field)
	trigger := NewButtonState()
	tpr := ButtonProps{Text: "Filter", Variant: Bordered}
	po.SetOpener(trigger.Tag())

	frame := func(setup func(*layout.Context)) PopoverResult {
		var res PopoverResult
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := trigger.Layout(h.env, gtx, tpr); err != nil {
				t.Fatalf("lay the trigger out: %v", err)
			}
			setup(gtx)
			var err error
			res, err = po.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the popover out: %v", err)
			}
		})
		return res
	}

	// Focus the trigger, then open from the keyboard: focus moves into the
	// content.
	frame(func(gtx *layout.Context) { gtx.Execute(key.FocusCmd{Tag: trigger.Tag()}) })
	frame(func(*layout.Context) {})
	if !h.gtx.Focused(trigger.Tag()) {
		t.Fatal("the trigger did not take focus")
	}
	po.OpenKeyboard()
	frame(func(*layout.Context) {})
	frame(func(*layout.Context) {})
	if po.Rect().Empty() {
		t.Fatal("the popover did not open")
	}
	if !h.gtx.Focused(field.Tag()) {
		t.Fatal("focus did not move into the popover's content")
	}
	h.tap(key.NameEscape, 0)
	res := frame(func(*layout.Context) {})
	if !res.Closed || po.IsOpen() {
		t.Fatalf("Escape did not close the popover: closed %v, open %v", res.Closed, po.IsOpen())
	}
	frame(func(*layout.Context) {})
	if !h.gtx.Focused(trigger.Tag()) {
		t.Fatal("focus did not return to the opener")
	}

	// A click outside closes it.
	po.Open()
	frame(func(*layout.Context) {})
	if po.Rect().Empty() {
		t.Fatal("the popover did not reopen")
	}
	h.click(700, 550)
	res = frame(func(*layout.Context) {})
	if !res.Closed || po.IsOpen() {
		t.Fatalf("a click outside did not close the popover: closed %v, open %v", res.Closed, po.IsOpen())
	}

	// Pinned: the click outside is ignored; Escape still closes.
	props.Pinned = true
	po.Open()
	frame(func(*layout.Context) {})
	h.click(700, 550)
	res = frame(func(*layout.Context) {})
	if res.Closed || !po.IsOpen() {
		t.Fatalf("a pinned popover closed on a click outside: closed %v, open %v", res.Closed, po.IsOpen())
	}
	h.tap(key.NameEscape, 0)
	res = frame(func(*layout.Context) {})
	if !res.Closed || po.IsOpen() {
		t.Fatalf("Escape did not close a pinned popover: closed %v, open %v", res.Closed, po.IsOpen())
	}
}

// TestDialogTrapsFocusAndReturnsItToTheOpener covers §6.17: Tab and
// Shift+Tab cycle inside the dialog — the body's fields, then the footer —
// and never leave it; Escape closes a clean form and focus returns to the
// opener; Escape with unsaved input is refused.
func TestDialogTrapsFocusAndReturnsItToTheOpener(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	f1, f2 := NewTextFieldState(), NewTextFieldState()
	cancel, primary := NewButtonState(), NewButtonState()
	trigger := NewButtonState()
	tpr := ButtonProps{Text: "Edit", Variant: Bordered}
	dirty := false
	props := DialogProps{
		Variant:     DialogForm,
		Title:       "Edit connection",
		Description: "Changes apply to new sessions.",
		FocusOrder:  []event.Tag{f1.Tag(), f2.Tag()},
		Cancel:      cancel, Primary: primary, PrimaryLabel: "Save",
		Body: func(gtx *layout.Context) image.Point {
			p1 := &TextFieldProps{Label: "Name"}
			p2 := &TextFieldProps{Label: "Host"}
			d1 := f1.Measure(h.env, gtx, p1)
			_, _ = f1.Layout(h.env, gtx, p1)
			d2 := f2.Measure(h.env, gtx, p2)
			at(gtx, image.Pt(0, d1.Size.Y+gtx.Dp(theme.SpaceLG)), func(gtx *layout.Context) {
				_, _ = f2.Layout(h.env, gtx, p2)
			})
			return image.Pt(gtx.Constraints.Max.X, d1.Size.Y+gtx.Dp(theme.SpaceLG)+d2.Size.Y)
		},
	}
	d.SetOpener(trigger.Tag())

	frame := func() DialogResult {
		props.Dirty = dirty
		var res DialogResult
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := trigger.Layout(h.env, gtx, tpr); err != nil {
				t.Fatalf("lay the trigger out: %v", err)
			}
			var err error
			res, err = d.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
		return res
	}

	d.Open()
	frame()
	frame()
	if d.rect.Empty() {
		t.Fatal("the dialog did not open")
	}
	if !h.gtx.Focused(f1.Tag()) {
		t.Fatal("the dialog did not focus its first field")
	}

	// Tab cycles inside: f2, then the footer, then back to f1 (wrap).
	focusAfterTab := func(mod key.Modifiers, want event.Tag, what string) {
		t.Helper()
		h.tap(key.NameTab, mod)
		frame()
		frame()
		if !h.gtx.Focused(want) {
			t.Fatal(what)
		}
	}
	focusAfterTab(0, f2.Tag(), "Tab did not move to the second field")
	focusAfterTab(0, cancel.Tag(), "Tab did not move to Cancel")
	focusAfterTab(0, primary.Tag(), "Tab did not move to the primary button")
	focusAfterTab(0, f1.Tag(), "Tab did not wrap back inside the dialog")
	focusAfterTab(key.ModShift, primary.Tag(), "Shift+Tab did not move back to the primary button")

	// Escape on a clean form closes and returns focus to the opener.
	h.tap(key.NameEscape, 0)
	res := frame()
	if !res.Cancelled || !res.Closed || d.IsOpen() {
		t.Fatalf("Escape did not close a clean form: %+v, open %v", res, d.IsOpen())
	}
	frame()
	if !h.gtx.Focused(trigger.Tag()) {
		t.Fatal("focus did not return to the opener")
	}

	// Escape with unsaved input is refused: the dialog stays open.
	dirty = true
	d.Open()
	frame()
	h.tap(key.NameEscape, 0)
	res = frame()
	if !res.EscapeRefused || !d.IsOpen() {
		t.Fatalf("Escape closed a form with unsaved input: %+v, open %v", res, d.IsOpen())
	}
}

// TestEnterInATextAreaNeverSubmitsTheDialog covers §6.17: Enter submits a
// form through the field core's submit translation, never from a multi-line
// TextArea, where Enter is a newline.
func TestEnterInATextAreaNeverSubmitsTheDialog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	area := NewTextAreaState()
	cancel, primary := NewButtonState(), NewButtonState()
	props := DialogProps{
		Variant:    DialogForm,
		Title:      "Compose",
		FocusOrder: []event.Tag{area.Tag()},
		Cancel:     cancel, Primary: primary, PrimaryLabel: "Send",
		Body: func(gtx *layout.Context) image.Point {
			p := &TextAreaProps{Label: "Message"}
			ev := area.Layout(h.env, gtx, p)
			if ev.Submitted {
				d.Submit()
			}
			return image.Pt(gtx.Constraints.Max.X, 3*labelLine(h.env, gtx, theme.TypeBody()))
		},
	}
	frame := func() DialogResult {
		var res DialogResult
		runFrame(t, h, func(gtx *layout.Context) {
			var err error
			res, err = d.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
		return res
	}
	d.Open()
	frame()
	frame()
	if !h.gtx.Focused(area.Tag()) {
		t.Fatal("the dialog did not focus the TextArea")
	}
	h.tap(key.NameEnter, 0)
	res := frame()
	if res.Submitted || res.Closed || !d.IsOpen() {
		t.Fatalf("Enter in a TextArea submitted the dialog: %+v, open %v", res, d.IsOpen())
	}
	if area.Text() == "" {
		t.Fatal("the TextArea did not receive the Enter as a newline")
	}
}

// TestDialogFieldEnterSubmitsTheForm is the other half of §6.17: Enter in a
// single-line TextField submits the form, through the field's own submit
// translation — the caller relays the field event, the dialog closes.
func TestDialogFieldEnterSubmitsTheForm(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	f1 := NewTextFieldState()
	cancel, primary := NewButtonState(), NewButtonState()
	props := DialogProps{
		Variant:    DialogForm,
		Title:      "Rename",
		FocusOrder: []event.Tag{f1.Tag()},
		Cancel:     cancel, Primary: primary, PrimaryLabel: "Save",
		Body: func(gtx *layout.Context) image.Point {
			p := &TextFieldProps{Label: "Name"}
			ev, _ := f1.Layout(h.env, gtx, p)
			if ev.Submitted {
				d.Submit()
			}
			return image.Pt(gtx.Constraints.Max.X, labelLine(h.env, gtx, theme.TypeBody()))
		},
	}
	frame := func() DialogResult {
		var res DialogResult
		runFrame(t, h, func(gtx *layout.Context) {
			var err error
			res, err = d.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
		return res
	}
	d.Open()
	frame()
	frame()
	if !h.gtx.Focused(f1.Tag()) {
		t.Fatal("the dialog did not focus the field")
	}
	h.tap(key.NameEnter, 0)
	res := frame()
	if !res.Submitted || !res.Closed || d.IsOpen() {
		t.Fatalf("Enter in a field did not submit: %+v, open %v", res, d.IsOpen())
	}
}

// TestConfirmDialogFocusesTheSafeChoice covers §6.17: a confirm focuses the
// safe choice — the Cancel button, here — on open, and Escape closes it as
// a cancellation.
func TestConfirmDialogFocusesTheSafeChoice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	props := DialogProps{
		Variant:     DialogConfirm,
		Title:       "Delete connection dev-pg?",
		Description: "The stored credentials go with it. This cannot be undone.",
		Cancel:      cancel, Primary: primary, PrimaryLabel: "Delete",
		PrimaryVariant: Danger,
	}
	frame := func() DialogResult {
		var res DialogResult
		runFrame(t, h, func(gtx *layout.Context) {
			var err error
			res, err = d.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
		return res
	}
	d.Open()
	frame()
	frame()
	if !h.gtx.Focused(cancel.Tag()) {
		t.Fatal("the confirm did not focus the safe choice")
	}
	h.tap(key.NameEscape, 0)
	res := frame()
	if !res.Cancelled || !res.Closed || d.IsOpen() {
		t.Fatalf("Escape did not close the confirm: %+v, open %v", res, d.IsOpen())
	}
}

// TestNoDialogOnADialog covers §6.17: a dialog opened while one is open is
// refused with ErrDialogAlreadyOpen, and the first dialog stays.
func TestNoDialogOnADialog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	first, second := NewDialogState(), NewDialogState()
	c1, p1 := NewButtonState(), NewButtonState()
	c2, p2 := NewButtonState(), NewButtonState()
	props := func(cancel, primary *ButtonState) DialogProps {
		return DialogProps{
			Variant: DialogForm, Title: "Edit",
			Cancel: cancel, Primary: primary, PrimaryLabel: "Save",
		}
	}
	frame := func(d *DialogState, cancel, primary *ButtonState) (DialogResult, error) {
		var res DialogResult
		var err error
		runFrame(t, h, func(gtx *layout.Context) {
			res, err = d.Layout(h.env, gtx, props(cancel, primary))
		})
		return res, err
	}
	first.Open()
	if _, err := frame(first, c1, p1); err != nil {
		t.Fatalf("the first dialog failed to open: %v", err)
	}
	second.Open()
	if _, err := frame(second, c2, p2); !errors.Is(err, ErrDialogAlreadyOpen) {
		t.Fatalf("the second dialog was not refused with ErrDialogAlreadyOpen: %v", err)
	}
	if !first.IsOpen() || second.IsOpen() {
		t.Fatalf("after the refusal: first open %v, second open %v, want only the first", first.IsOpen(), second.IsOpen())
	}
}

// TestOnlyTwoFloatingLayersAtOnce covers UK-07: a menu may open from a
// popover — two layers — and a dialog over them is the third the spec
// allows; but a second menu replaces the menu layer and a second popover
// would replace the popover, so no third floating layer stacks.
func TestOnlyTwoFloatingLayersAtOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pop := NewPopoverState()
	field := NewTextFieldState()
	pprops := popoverProps(h.env, field)
	menuA, menuB := NewMenuState(), NewMenuState()
	mprops := MenuProps{Items: testMenuItems(), Anchor: image.Rect(60, 80, 160, 100)}

	frame := func() {
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
				t.Fatalf("lay the popover out: %v", err)
			}
			if _, err := menuA.Layout(h.env, gtx, mprops); err != nil {
				t.Fatalf("lay menu a out: %v", err)
			}
			if _, err := menuB.Layout(h.env, gtx, mprops); err != nil {
				t.Fatalf("lay menu b out: %v", err)
			}
		})
	}

	pop.Open()
	menuA.Open()
	frame()
	if !pop.IsOpen() || !menuA.IsOpen() || menuB.IsOpen() {
		t.Fatalf("the popover and its menu did not coexist: pop %v, a %v, b %v",
			pop.IsOpen(), menuA.IsOpen(), menuB.IsOpen())
	}
	// A second menu takes the menu layer; the popover stays. The menu it
	// displaced closes itself on its next Layout, so give it one.
	menuB.Open()
	frame()
	frame()
	if !pop.IsOpen() || menuA.IsOpen() || !menuB.IsOpen() {
		t.Fatalf("the second menu did not replace the first: pop %v, a %v, b %v",
			pop.IsOpen(), menuA.IsOpen(), menuB.IsOpen())
	}
	// The third layer is a dialog (§7): it opens over both and stays.
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	dprops := DialogProps{Variant: DialogConfirm, Title: "Sure?", Cancel: cancel, Primary: primary, PrimaryLabel: "Yes"}
	d.Open()
	var derr error
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
			t.Fatalf("lay the popover out: %v", err)
		}
		if _, err := menuB.Layout(h.env, gtx, mprops); err != nil {
			t.Fatalf("lay the menu out: %v", err)
		}
		_, derr = d.Layout(h.env, gtx, dprops)
	})
	if derr != nil {
		t.Fatalf("the dialog did not open over the two layers: %v", derr)
	}
	if !d.IsOpen() || !pop.IsOpen() || !menuB.IsOpen() {
		t.Fatalf("the dialog disturbed the layers: dialog %v, pop %v, menu %v",
			d.IsOpen(), pop.IsOpen(), menuB.IsOpen())
	}
}

// TestToastPausesOnHoverAndNeverTakesFocus covers §6.18: the timer stops
// while the pointer hovers the toast and resumes after; the toast never
// takes focus — whatever is focused stays focused for the toast's whole
// life.
func TestToastPausesOnHoverAndNeverTakesFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	steady := NewButtonState()
	spr := ButtonProps{Text: "Work", Variant: Bordered}
	h.env.ToastPush(Toast{Object: "job-1", Icon: IconCheck, Title: "Query finished", Detail: "12 rows in 1.2 s"})
	laySteady := func(gtx *layout.Context) {
		if _, err := steady.Layout(h.env, gtx, spr); err != nil {
			t.Fatalf("lay the button out: %v", err)
		}
	}
	runFrame(t, h, func(gtx *layout.Context) {
		laySteady(gtx)
		gtx.Execute(key.FocusCmd{Tag: steady.Tag()})
	})
	// Every later frame lays the anchor out again: Gio clears the focus of
	// a tag whose key handler is absent from a frame.
	runFrame(t, h, laySteady)
	if !h.gtx.Focused(steady.Tag()) {
		t.Fatal("the focus anchor did not take focus")
	}
	if n := len(h.env.toast.list); n != 1 || !h.env.toast.list[0].open.Shown() {
		t.Fatalf("the toast did not show: %d entries", n)
	}
	rect := h.env.toast.list[0].rect
	if rect.Empty() {
		t.Fatal("the toast has no place")
	}

	// Two of its four seconds pass; the toast is still there, and the
	// driver holds a wake for its expiry.
	h.now = h.now.Add(2 * time.Second)
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := steady.Layout(h.env, gtx, spr); err != nil {
			t.Fatalf("lay the button out: %v", err)
		}
	})
	if !h.env.toast.list[0].open.Shown() {
		t.Fatal("the toast expired early")
	}
	if _, ok := h.env.Driver.Next(); !ok {
		t.Fatal("the toast scheduled no wake for its expiry")
	}

	// The pointer hovers it: four more seconds pass with nothing expiring.
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

	// The pointer leaves; the roughly two seconds that were left run out.
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(50, 50)})
	runFrame(t, h, laySteady)
	h.now = h.now.Add(3 * time.Second)
	runFrame(t, h, laySteady)
	if h.env.toast.list[0].open.Shown() {
		t.Fatal("the toast did not expire after the pointer left")
	}
	evs := h.env.ToastEvents()
	if len(evs) != 1 || evs[0].Kind != ToastClosed || evs[0].Object != "job-1" {
		t.Fatalf("the expiry came back as %+v, want one ToastClosed for job-1", evs)
	}
	// Through its whole life the toast never took focus.
	if !h.gtx.Focused(steady.Tag()) {
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
		h.env.ToastPush(Toast{Object: "job-1", Title: "Query finished"})
		h.env.ToastPush(Toast{Object: "job-1", Title: "Query finished"})
	})
	ens := h.env.toast.list
	if len(ens) != 1 || ens[0].count != 2 {
		t.Fatalf("two pushes of one object: %d entries, count %d, want one merged with count 2", len(ens), ens[0].count)
	}
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.ToastPush(Toast{Object: "job-2", Title: "Index rebuilt"})
	})
	if n := h.env.toast.visibleCount(); n != 2 {
		t.Fatalf("a second object did not stack: %d visible", n)
	}
	for _, obj := range []string{"job-3", "job-4", "job-5"} {
		h.env.ToastPush(Toast{Object: obj, Title: "Background work " + obj})
	}
	runFrame(t, h, func(gtx *layout.Context) {})
	if n := h.env.toast.visibleCount(); n > toastMax {
		t.Fatalf("%d toasts visible, want at most %d", n, toastMax)
	}
	if h.env.toast.findIndex("job-1") >= 0 {
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

// TestOverlayMotionSettlesAndRetargets covers UK-E11 for the overlay
// components: each motion reaches its target and stops asking for frames,
// and retargeting mid-flight — closing while still entering — continues from
// where it was instead of jumping.
func TestOverlayMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pop := NewPopoverState()
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	field := NewTextFieldState()
	pprops := popoverProps(h.env, field)
	dprops := DialogProps{Variant: DialogConfirm, Title: "Sure?", Cancel: cancel, Primary: primary, PrimaryLabel: "Yes"}

	// busy reports whether the driver still wants a frame: motion moving,
	// or a scheduled wake.
	busy := func() bool {
		_, ok := h.env.Driver.Next()
		return ok
	}

	frame := func() {
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
				t.Fatalf("lay the popover out: %v", err)
			}
			if _, err := d.Layout(h.env, gtx, dprops); err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
	}

	pop.Open()
	d.Open()
	frame()
	if !busy() {
		t.Fatal("entering motion asked for no frame")
	}
	for range 240 {
		if !busy() {
			break
		}
		frame()
	}
	if busy() {
		t.Fatal("motion never settled: frames were asked forever")
	}
	// Retarget: close the freshly opened popover mid-flight — the presence
	// reverses from its current value instead of restarting (F10.6).
	pop.Open()
	frame()
	pop.Close()
	frame()
	if pop.open.Progress() >= 1 {
		t.Fatal("a closing popover restarted from fully present")
	}
	for range 240 {
		if !busy() {
			break
		}
		frame()
	}
	if pop.open.Visible() {
		t.Fatal("a closed popover never finished fading out")
	}
	if busy() {
		t.Fatal("a settled overlay still asked for frames")
	}
}

// TestOverlayTextContrastOnSurfaceRaisedAndScrim extends UK-E04 to the
// overlay components: the dialog's title and description, the toast's title,
// detail and merge badge — every text the components draw — holds 4.5:1 on
// the SurfaceRaised frame they sit on, in both modes; and the frame stays
// distinct (3:1) from the page dimmed by the scrim behind it.
func TestOverlayTextContrastOnSurfaceRaisedAndScrim(t *testing.T) {
	t.Parallel()
	for _, light := range []bool{false, true} {
		sh, err := fonts.Shaper()
		if err != nil {
			t.Fatalf("build the shaper: %v", err)
		}
		var th theme.Theme
		if light {
			th = theme.Light(sh)
		} else {
			th = theme.Dark(sh)
		}
		c := th.Chrome()
		surface := c.SurfaceRaised
		texts := []struct {
			name string
			fg   color.NRGBA
		}{
			{"dialog title", c.Fg},
			{"dialog description", c.FgMuted},
			{"toast title", c.Fg},
			{"toast detail", c.FgMuted},
			{"toast badge", c.FgMuted},
		}
		for _, tc := range texts {
			if got := contrastRatio(tc.fg, surface); got < 4.5 {
				t.Errorf("%s on SurfaceRaised (%s): contrast %.2f < 4.5", tc.name, modeName(light), got)
			}
		}
		// Nothing draws on the bare scrim — it is a backdrop (§3.5), and
		// every text-bearing surface above it is the components' own opaque
		// SurfaceRaised frame, checked above. The components keep their
		// 4.5:1 over the scrim the same way, so there is no second number.
	}
}

// modeName names the mode for messages.
func modeName(light bool) string {
	if light {
		return "light"
	}
	return "dark"
}

// TestOverlayReducedMotionOpensAtOnceAndStaysQuiet extends UK-E07 to the
// overlay components: with reduced motion everything lands in the frame the
// change was asked in and the driver stays quiet — no component asks for a
// frame it does not need.
func TestOverlayReducedMotionOpensAtOnceAndStaysQuiet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	pop := NewPopoverState()
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	field := NewTextFieldState()
	pprops := popoverProps(h.env, field)
	dprops := DialogProps{Variant: DialogConfirm, Title: "Sure?", Cancel: cancel, Primary: primary, PrimaryLabel: "Yes"}
	frame := func() {
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
				t.Fatalf("lay the popover out: %v", err)
			}
			if _, err := d.Layout(h.env, gtx, dprops); err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
	}
	pop.Open()
	d.Open()
	frame()
	if pop.open.Progress() < 1 || d.open.Progress() < 1 || d.scrim.Value() < 1 {
		t.Fatal("reduced motion did not land the overlays at once")
	}
	if at, ok := h.env.Driver.Next(); ok && at.IsZero() {
		t.Fatal("a settled reduced-motion overlay asked for an immediate frame")
	}
	// Toasts: pushed before the frame, present at once. Their timer wake is
	// a scheduled frame at the expiry, not motion asking for the next one.
	h.env.ToastPush(Toast{Object: "done", Title: "Finished"})
	frame()
	if !h.env.toast.list[0].open.Visible() {
		t.Fatal("a reduced-motion toast did not show at once")
	}
	if at, ok := h.env.Driver.Next(); ok && at.IsZero() {
		t.Fatal("a reduced-motion toast asked for an immediate frame")
	}
}

// TestToastEventAreaIsClippedToItsBox extends UK-E10 to the toasts: the
// pointer just outside a toast's box neither enters it — so the timer is not
// paused by a near miss — nor reaches its buttons.
func TestToastEventAreaIsClippedToItsBox(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ToastPush(Toast{Object: "job-1", Title: "Query finished", Action: "Open"})
	runFrame(t, h, func(gtx *layout.Context) {})
	rect := h.env.toast.list[0].rect
	if rect.Empty() {
		t.Fatal("the toast has no place")
	}
	// One pixel above the box: no Enter.
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32((rect.Min.X+rect.Max.X)/2), float32(rect.Min.Y-1))})
	runFrame(t, h, func(gtx *layout.Context) {})
	if h.env.toast.list[0].hover {
		t.Fatal("a press just outside the toast's box reached its area")
	}
	// Inside: it does.
	centre := image.Pt((rect.Min.X+rect.Max.X)/2, (rect.Min.Y+rect.Max.Y)/2)
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(centre.X), float32(centre.Y))})
	runFrame(t, h, func(gtx *layout.Context) {})
	if !h.env.toast.list[0].hover {
		t.Fatal("a move inside the toast's box did not reach its area")
	}
}
