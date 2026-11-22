package kit

import (
	"errors"
	"image"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// dialogFixture is the shared shape of the dialog tests: one dialog with a
// one-field form body, its footer buttons and a laid-out trigger.
type dialogFixture struct {
	t       *testing.T
	h       *harness
	d       *DialogState
	field   *TextFieldState
	cancel  *ButtonState
	primary *ButtonState
	trigger *ButtonState
	tpr     ButtonProps
	props   *DialogProps
	bodyErr error
	dirty   bool
	// tall, when set, lays a tall spacer above the field, so the body
	// scrolls and the field can sit at a visible edge.
	tall bool
}

// newDialogFixture builds the fixture; dirty gates the props per frame.
func newDialogFixture(t *testing.T) *dialogFixture {
	t.Helper()
	h := newHarness(t)
	f := &dialogFixture{
		t:       t,
		h:       h,
		d:       NewDialogState(),
		field:   NewTextFieldState(),
		cancel:  NewButtonState(),
		primary: NewButtonState(),
		trigger: NewButtonState(),
		tpr:     ButtonProps{Text: testTriggerLabel, Variant: Bordered},
	}
	f.props = &DialogProps{
		Variant:     DialogForm,
		Title:       "Edit connection",
		Description: "Changes apply to new sessions.",
		Cancel:      f.cancel, Primary: f.primary, PrimaryLabel: labelSave,
		Body: func(gtx *layout.Context, bands *DialogBands) image.Point {
			return f.body(gtx, bands)
		},
	}
	f.d.SetOpener(f.trigger.Tag())
	return f
}

// body lays the fixture's field at the full body width, declaring its band
// (below the tall spacer when there is one); a draw error surfaces to the
// frame check.
func (f *dialogFixture) body(gtx *layout.Context, bands *DialogBands) image.Point {
	p := &TextFieldProps{Label: testFieldLabel}
	dims := f.field.Measure(f.h.env, gtx, p)
	y := 0
	if f.tall {
		// Tall enough that the body scrolls in the harness window: the
		// spacer alone passes the slot's height.
		y = f.h.gtx.Dp(theme.SpaceXL) * 40
	}
	if _, err := f.field.Layout(f.h.env, gtx, p); err != nil && f.bodyErr == nil {
		f.bodyErr = err
	}
	bands.Focusable(f.field.Tag(), y, y+dims.Size.Y)
	return image.Point{X: gtx.Constraints.Max.X, Y: y + dims.Size.Y}
}

// frame lays one frame out — the trigger with it, so focus survives (Gio
// clears the focus of a tag whose key handler is absent from a frame) — and
// reports the dialog's result.
func (f *dialogFixture) frame() DialogResult {
	f.props.Dirty = f.dirty
	var res DialogResult
	runFrame(f.t, f.h, func(gtx *layout.Context) {
		if _, err := f.trigger.Layout(f.h.env, gtx, f.tpr); err != nil {
			f.t.Fatalf("lay the trigger out: %v", err)
		}
		var err error
		res, err = f.d.Layout(f.h.env, gtx, f.props)
		if err != nil {
			f.t.Fatalf("lay the dialog out: %v", err)
		}
	})
	if f.bodyErr != nil {
		f.t.Fatalf("lay the dialog body out: %v", f.bodyErr)
	}
	f.bodyErr = nil
	return res
}

// settle lays one more frame out, so a FocusCmd of the previous frame is
// visible.
func (f *dialogFixture) settle() { f.frame() }

// focusField forces the gallery preview's focus ring on the fixture's field.
func (f *dialogFixture) focusField() {
	f.h.env.Preview = Preview{Focus: f.field.Tag()}
}

// TestDialogTrapsFocusAndReturnsItToTheOpener covers §6.17: Tab and
// Shift+Tab cycle inside the dialog — the body's field, then the footer —
// and never leave it; Escape closes a clean form and focus returns to the
// opener; Escape with unsaved input is refused.
func TestDialogTrapsFocusAndReturnsItToTheOpener(t *testing.T) {
	t.Parallel()
	f := newDialogFixture(t)
	f.d.Open()
	f.frame()
	f.settle()
	if f.d.rect.Empty() {
		t.Fatal("the dialog did not open")
	}
	if !f.h.gtx.Focused(f.field.Tag()) {
		t.Fatal("the dialog did not focus its first field")
	}

	// Tab cycles inside: the footer, then back to the field (wrap);
	// Shift+Tab goes back.
	focusAfterTab(t, f, 0, f.cancel.Tag(), "Tab did not move to Cancel")
	focusAfterTab(t, f, 0, f.primary.Tag(), "Tab did not move to the primary button")
	focusAfterTab(t, f, 0, f.field.Tag(), "Tab did not wrap back inside the dialog")
	focusAfterTab(t, f, key.ModShift, f.primary.Tag(), "Shift+Tab did not move back to the primary button")

	// Escape on a clean form closes and returns focus to the opener.
	f.h.tap(key.NameEscape, 0)
	res := f.frame()
	if !res.Cancelled || !res.Closed || f.d.IsOpen() {
		t.Fatalf("Escape did not close a clean form: %+v, open %v", res, f.d.IsOpen())
	}
	f.settle()
	if !f.h.gtx.Focused(f.trigger.Tag()) {
		t.Fatal("focus did not return to the opener")
	}

	// Escape with unsaved input is refused: the dialog stays open.
	f.dirty = true
	f.d.Open()
	f.frame()
	f.h.tap(key.NameEscape, 0)
	res = f.frame()
	if !res.EscapeRefused || !f.d.IsOpen() {
		t.Fatalf("Escape closed a form with unsaved input: %+v, open %v", res, f.d.IsOpen())
	}
}

func TestDialogBodyStartsBelowDescription(t *testing.T) {
	t.Parallel()
	f := newDialogFixture(t)
	const bodyLabel = "Dialog body marker"
	size := image.Pt(24, 20)
	f.props.Body = func(gtx *layout.Context, _ *DialogBands) image.Point {
		area := clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops)
		semantic.LabelOp(bodyLabel).Add(gtx.Ops)
		area.Pop()
		return size
	}
	f.d.Open()
	f.frame()

	gtx := &f.h.gtx
	m := f.d.measure(f.h.env, gtx, f.props, f.h.env.windowRect())
	origin := f.d.rect.Min.Add(image.Pt(m.pad+dialogRing(gtx), m.pad+m.headH+gtx.Dp(theme.SpaceSM)+m.descH))
	want := image.Rectangle{Min: origin, Max: origin.Add(size)}
	for _, node := range f.h.router.AppendSemantics(nil) {
		if node.Desc.Label == bodyLabel {
			if node.Desc.Bounds != want {
				t.Fatalf("dialog body bounds = %v, want %v", node.Desc.Bounds, want)
			}
			return
		}
	}
	t.Fatal("dialog body is clipped out of the viewport")
}

// focusAfterTab taps Tab (with mod) and asserts the tag took focus two
// frames later: the FocusCmd lands in the router at its frame's end.
func focusAfterTab(t *testing.T, f *dialogFixture, mod key.Modifiers, want event.Tag, what string) {
	t.Helper()
	f.h.tap(key.NameTab, mod)
	f.frame()
	f.settle()
	if !f.h.gtx.Focused(want) {
		t.Fatal(what)
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
	props := &DialogProps{
		Variant: DialogForm,
		Title:   "Compose",
		Cancel:  cancel, Primary: primary, PrimaryLabel: "Send",
		Body: func(gtx *layout.Context, bands *DialogBands) image.Point {
			p := &TextAreaProps{Label: "Message"}
			ev := area.Layout(h.env, gtx, p)
			if ev.Submitted {
				d.Submit()
			}
			bands.Focusable(area.Tag(), 0, labelLine(h.env, gtx, theme.TypeBody()))
			return image.Point{X: gtx.Constraints.Max.X, Y: 3 * labelLine(h.env, gtx, theme.TypeBody())}
		},
	}
	d.SetOpener(&cancel.click)
	d.Open()
	dialogFrames(t, h, d, props, 2)
	if !h.gtx.Focused(area.Tag()) {
		t.Fatal("the dialog did not focus the TextArea")
	}
	h.tap(key.NameEnter, 0)
	res := dialogFrames(t, h, d, props, 1)
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
	props := &DialogProps{
		Variant: DialogForm,
		Title:   "Rename",
		Cancel:  cancel, Primary: primary, PrimaryLabel: labelSave,
		Body: func(gtx *layout.Context, bands *DialogBands) image.Point {
			p := &TextFieldProps{Label: testFieldLabel}
			ev, err := f1.Layout(h.env, gtx, p)
			if err != nil {
				t.Errorf("lay the field out: %v", err)
			}
			if ev.Submitted {
				d.Submit()
			}
			bands.Focusable(f1.Tag(), 0, labelLine(h.env, gtx, theme.TypeBody()))
			return image.Point{X: gtx.Constraints.Max.X, Y: labelLine(h.env, gtx, theme.TypeBody())}
		},
	}
	d.SetOpener(&cancel.click)
	d.Open()
	dialogFrames(t, h, d, props, 2)
	if !h.gtx.Focused(f1.Tag()) {
		t.Fatal("the dialog did not focus the field")
	}
	h.tap(key.NameEnter, 0)
	res := dialogFrames(t, h, d, props, 1)
	if !res.Submitted || !res.Closed || d.IsOpen() {
		t.Fatalf("Enter in a field did not submit: %+v, open %v", res, d.IsOpen())
	}
}

// dialogFrames lays n frames of one dialog out (the body's field is laid out
// every frame, so its focus survives) and returns the last result.
func dialogFrames(t *testing.T, h *harness, d *DialogState, props *DialogProps, n int) DialogResult {
	t.Helper()
	var res DialogResult
	for range n {
		runFrame(t, h, func(gtx *layout.Context) {
			var err error
			res, err = d.Layout(h.env, gtx, props)
			if err != nil {
				t.Fatalf("lay the dialog out: %v", err)
			}
		})
	}
	return res
}

// TestConfirmDialogFocusesTheSafeChoice covers §6.17: a confirm focuses the
// safe choice — the Cancel button, here — on open, and Escape closes it as
// a cancellation.
func TestConfirmDialogFocusesTheSafeChoice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	props := &DialogProps{
		Variant:     DialogConfirm,
		Title:       testConfirmTitle,
		Description: testConfirmDesc,
		Cancel:      cancel, Primary: primary, PrimaryLabel: testDeleteLabel,
		PrimaryVariant: Danger,
	}
	d.SetOpener(&cancel.click)
	d.Open()
	dialogFrames(t, h, d, props, 2)
	if !h.gtx.Focused(cancel.Tag()) {
		t.Fatal("the confirm did not focus the safe choice")
	}
	h.tap(key.NameEscape, 0)
	res := dialogFrames(t, h, d, props, 1)
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
	propsFor := func(cancel, primary *ButtonState) *DialogProps {
		return &DialogProps{
			Variant: DialogForm, Title: "Edit",
			Cancel: cancel, Primary: primary, PrimaryLabel: labelSave,
		}
	}
	frame := func(d *DialogState, cancel, primary *ButtonState) error {
		var err error
		runFrame(t, h, func(gtx *layout.Context) {
			_, err = d.Layout(h.env, gtx, propsFor(cancel, primary))
		})
		return err
	}
	first.Open()
	if err := frame(first, c1, p1); err != nil {
		t.Fatalf("the first dialog failed to open: %v", err)
	}
	second.Open()
	if err := frame(second, c2, p2); !errors.Is(err, ErrDialogAlreadyOpen) {
		t.Fatalf("the second dialog was not refused with ErrDialogAlreadyOpen: %v", err)
	}
	if !first.IsOpen() || second.IsOpen() {
		t.Fatalf("after the refusal: first open %v, second open %v, want only the first", first.IsOpen(), second.IsOpen())
	}
}
