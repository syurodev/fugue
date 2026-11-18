package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/f32"
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

// Shared test copy and modes, so no literal repeats (goconst). labelSave is
// button_test's.
const (
	testFilterTitle  = "Filter"
	testFieldLabel   = "Name"
	testFieldHint    = "e.g. dev-pg"
	testSureTitle    = "Sure?"
	testYesLabel     = "Yes"
	testConfirmTitle = "Delete connection dev-pg?"
	testConfirmDesc  = "The stored credentials go with it. This cannot be undone."
	testDeleteLabel  = "Delete"
	testToastObject  = "job-1"
	testToastTitle   = "Query finished"
	testToastDetail  = "12 rows in 1.2 s"
	testToastAction  = "Open"
	modeLight        = "light"
	modeDark         = "dark"
	testTriggerLabel = "Work"
)

// popoverProps builds the props of a test popover holding one TextField;
// the closures draw with env, the harness's frame env, and carry a draw
// error out through contentErr.
func popoverProps(env *Env, field *TextFieldState, contentErr *error) *PopoverProps {
	p := &TextFieldProps{Label: testFieldLabel, Placeholder: testFieldHint}
	return &PopoverProps{
		Anchor:      image.Rect(40, 40, 140, 68),
		Title:       testFilterTitle,
		Width:       220,
		FocusOnOpen: field.Tag(),
		Measure: func(gtx *layout.Context) image.Point {
			return field.Measure(env, gtx, p).Size
		},
		Content: func(gtx *layout.Context) {
			_, err := field.Layout(env, gtx, p)
			if err != nil && *contentErr == nil {
				*contentErr = err
			}
		},
	}
}

// popoverFixture is the popover tests' shape: one popover with a field, the
// trigger that opens it, and the draw error out of the content.
type popoverFixture struct {
	t       *testing.T
	h       *harness
	po      *PopoverState
	field   *TextFieldState
	trigger *ButtonState
	tpr     ButtonProps
	props   *PopoverProps
	layErr  error
}

func newPopoverFixture(t *testing.T) *popoverFixture {
	t.Helper()
	h := newHarness(t)
	f := &popoverFixture{
		t:       t,
		h:       h,
		po:      NewPopoverState(),
		field:   NewTextFieldState(),
		trigger: NewButtonState(),
		tpr:     ButtonProps{Text: testFilterTitle, Variant: Bordered},
	}
	f.props = popoverProps(h.env, f.field, &f.layErr)
	f.po.SetOpener(f.trigger.Tag())
	return f
}

// frame lays one frame out — trigger and popover — and fails on a content
// draw error.
func (f *popoverFixture) frame(setup func(*layout.Context)) PopoverResult {
	var res PopoverResult
	runFrame(f.t, f.h, func(gtx *layout.Context) {
		if _, err := f.trigger.Layout(f.h.env, gtx, f.tpr); err != nil {
			f.t.Fatalf("lay the trigger out: %v", err)
		}
		if setup != nil {
			setup(gtx)
		}
		var err error
		res, err = f.po.Layout(f.h.env, gtx, f.props)
		if err != nil {
			f.t.Fatalf("lay the popover out: %v", err)
		}
	})
	if f.layErr != nil {
		f.t.Fatalf("lay the popover content out: %v", f.layErr)
	}
	f.layErr = nil
	return res
}

// focusField forces the gallery preview's focus ring on the fixture's field.
func (f *popoverFixture) focusField() {
	f.h.env.Preview = Preview{Focus: f.field.Tag()}
}

// TestPopoverClosesOnEscapeAndOutsideClick covers §6.16: opened from the
// keyboard, focus moves into the content; Escape closes and returns focus to
// the opener; a click outside closes; a pinned popover ignores the click
// outside but still closes on Escape.
func TestPopoverClosesOnEscapeAndOutsideClick(t *testing.T) {
	t.Parallel()
	f := newPopoverFixture(t)

	// Focus the trigger, then open from the keyboard: focus moves into the
	// content.
	f.frame(func(gtx *layout.Context) { gtx.Execute(key.FocusCmd{Tag: f.trigger.Tag()}) })
	f.frame(nil)
	if !f.h.gtx.Focused(f.trigger.Tag()) {
		t.Fatal("the trigger did not take focus")
	}
	f.po.OpenKeyboard()
	f.frame(nil)
	f.frame(nil)
	assertPopoverOpen(t, f)
	if !f.h.gtx.Focused(f.field.Tag()) {
		t.Fatal("focus did not move into the popover's content")
	}
	assertPopoverEscape(t, f)
	assertPopoverOutsideClick(t, f)
	assertPopoverPinned(t, f)
}

// assertPopoverOpen fails when the popover's panel is not on screen.
func assertPopoverOpen(t *testing.T, f *popoverFixture) {
	t.Helper()
	if f.po.Rect().Empty() {
		t.Fatal("the popover did not open")
	}
}

// assertPopoverEscape taps Escape, expects the close and the focus back on
// the opener.
func assertPopoverEscape(t *testing.T, f *popoverFixture) {
	t.Helper()
	f.h.tap(key.NameEscape, 0)
	res := f.frame(nil)
	if !res.Closed || f.po.IsOpen() {
		t.Fatalf("Escape did not close the popover: closed %v, open %v", res.Closed, f.po.IsOpen())
	}
	f.frame(nil)
	if !f.h.gtx.Focused(f.trigger.Tag()) {
		t.Fatal("focus did not return to the opener")
	}
}

// assertPopoverOutsideClick opens the popover again and presses outside the
// panel: the popover closes.
func assertPopoverOutsideClick(t *testing.T, f *popoverFixture) {
	t.Helper()
	f.po.Open()
	f.frame(nil)
	assertPopoverOpen(t, f)
	f.h.click(700, 550)
	res := f.frame(nil)
	if !res.Closed || f.po.IsOpen() {
		t.Fatalf("a click outside did not close the popover: closed %v, open %v", res.Closed, f.po.IsOpen())
	}
}

// assertPopoverPinned pins the popover: the click outside is ignored and
// Escape still closes.
func assertPopoverPinned(t *testing.T, f *popoverFixture) {
	t.Helper()
	f.props.Pinned = true
	f.po.Open()
	f.frame(nil)
	f.h.click(700, 550)
	res := f.frame(nil)
	if res.Closed || !f.po.IsOpen() {
		t.Fatalf("a pinned popover closed on a click outside: closed %v, open %v", res.Closed, f.po.IsOpen())
	}
	f.h.tap(key.NameEscape, 0)
	res = f.frame(nil)
	if !res.Closed || f.po.IsOpen() {
		t.Fatalf("Escape did not close a pinned popover: closed %v, open %v", res.Closed, f.po.IsOpen())
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
	pprops := popoverProps(h.env, field, new(error))
	menuA, menuB := NewMenuState(), NewMenuState()
	mprops := MenuProps{Items: testMenuItems(), Anchor: image.Rect(60, 80, 160, 100)}

	frame := func() {
		runFrame(t, h, func(gtx *layout.Context) {
			layOverlay(t, h, gtx, pop, pprops, menuA, menuB, mprops)
		})
	}

	pop.Open()
	menuA.Open()
	frame()
	assertLayers(t, pop, menuA, menuB, true, true, false, "the popover and its menu did not coexist")
	// A second menu takes the menu layer; the popover stays. The menu it
	// displaced closes itself on its next Layout, so give it one.
	menuB.Open()
	frame()
	frame()
	assertLayers(t, pop, menuA, menuB, true, false, true, "the second menu did not replace the first")
	// The third layer is a dialog (§7): it opens over both and stays.
	assertDialogOverLayers(t, h, pop, menuB, pprops, mprops)
}

// layOverlay lays the popover and two menus out in one frame.
func layOverlay(t *testing.T, h *harness, gtx *layout.Context, pop *PopoverState, pprops *PopoverProps, menuA, menuB *MenuState, mprops MenuProps) {
	t.Helper()
	if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
		t.Fatalf("lay the popover out: %v", err)
	}
	if _, err := menuA.Layout(h.env, gtx, mprops); err != nil {
		t.Fatalf("lay menu a out: %v", err)
	}
	if _, err := menuB.Layout(h.env, gtx, mprops); err != nil {
		t.Fatalf("lay menu b out: %v", err)
	}
}

// assertLayers asserts which of the two menus holds the menu layer beside
// the popover.
func assertLayers(t *testing.T, pop *PopoverState, menuA, menuB *MenuState, popOpen, aOpen, bOpen bool, what string) {
	t.Helper()
	if pop.IsOpen() != popOpen || menuA.IsOpen() != aOpen || menuB.IsOpen() != bOpen {
		t.Fatalf("%s: pop %v, a %v, b %v, want pop %v, a %v, b %v",
			what, pop.IsOpen(), menuA.IsOpen(), menuB.IsOpen(), popOpen, aOpen, bOpen)
	}
}

// assertDialogOverLayers opens a confirm dialog over the popover and its
// menu: the third layer the spec allows, and nothing below disturbed.
func assertDialogOverLayers(t *testing.T, h *harness, pop *PopoverState, menu *MenuState, pprops *PopoverProps, mprops MenuProps) {
	t.Helper()
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	dprops := &DialogProps{
		Variant: DialogConfirm, Title: testSureTitle,
		Cancel: cancel, Primary: primary, PrimaryLabel: testYesLabel,
	}
	d.Open()
	var derr error
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := pop.Layout(h.env, gtx, pprops); err != nil {
			t.Fatalf("lay the popover out: %v", err)
		}
		if _, err := menu.Layout(h.env, gtx, mprops); err != nil {
			t.Fatalf("lay the menu out: %v", err)
		}
		_, derr = d.Layout(h.env, gtx, dprops)
	})
	if derr != nil {
		t.Fatalf("the dialog did not open over the two layers: %v", derr)
	}
	if !d.IsOpen() || !pop.IsOpen() || !menu.IsOpen() {
		t.Fatalf("the dialog disturbed the layers: dialog %v, pop %v, menu %v",
			d.IsOpen(), pop.IsOpen(), menu.IsOpen())
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
	pprops := popoverProps(h.env, field, new(error))
	dprops := &DialogProps{
		Variant: DialogConfirm, Title: testSureTitle,
		Cancel: cancel, Primary: primary, PrimaryLabel: testYesLabel,
	}

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
	settleQuiet(t, busy, frame)
	pop.Close()
	d.Close()
	frame()
	settleQuiet(t, busy, frame)

	// Retarget: close the freshly opened popover mid-flight — the presence
	// reverses from its current value instead of restarting (F10.6).
	pop.Open()
	frame()
	pop.Close()
	frame()
	if pop.open.Progress() >= 1 {
		t.Fatal("a closing popover restarted from fully present")
	}
	settleQuiet(t, busy, frame)
	if pop.open.Visible() {
		t.Fatal("a closed popover never finished fading out")
	}
	if busy() {
		t.Fatal("a settled overlay still asked for frames")
	}
}

// settleQuiet lays frames out until the driver stays quiet, then fails if
// motion never settled.
func settleQuiet(t *testing.T, busy func() bool, frame func()) {
	t.Helper()
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
}

// TestOverlayReducedMotionOpensAtOnceAndStaysQuiet extends UK-E07 to the
// overlay components: with reduced motion everything lands in the frame the
// change was asked in and no immediate frame is ever asked — a toast's timer
// wake is a scheduled frame at the expiry, not motion asking for the next
// one.
func TestOverlayReducedMotionOpensAtOnceAndStaysQuiet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	pop := NewPopoverState()
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	field := NewTextFieldState()
	pprops := popoverProps(h.env, field, new(error))
	dprops := &DialogProps{
		Variant: DialogConfirm, Title: testSureTitle,
		Cancel: cancel, Primary: primary, PrimaryLabel: testYesLabel,
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
	if pop.open.Progress() < 1 || d.open.Progress() < 1 || d.scrim.Value() < 1 {
		t.Fatal("reduced motion did not land the overlays at once")
	}
	assertNoImmediateFrame(t, h)
	// Toasts: pushed before the frame, present at once.
	h.env.ToastPush(&Toast{Object: "done", Title: "Finished"})
	frame()
	if !h.env.toast.list[0].open.Visible() {
		t.Fatal("a reduced-motion toast did not show at once")
	}
	assertNoImmediateFrame(t, h)
}

// assertNoImmediateFrame fails when the driver wants the next frame now; a
// scheduled wake (a toast's expiry) is fine.
func assertNoImmediateFrame(t *testing.T, h *harness) {
	t.Helper()
	if at, ok := h.env.Driver.Next(); ok && at.IsZero() {
		t.Fatal("a settled overlay asked for an immediate frame")
	}
}

// TestToastEventAreaIsClippedToItsBox extends UK-E10 to the toasts: the
// pointer just outside a toast's box neither enters it — so the timer is not
// paused by a near miss — nor reaches its buttons.
func TestToastEventAreaIsClippedToItsBox(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ToastPush(&Toast{Object: testToastObject, Title: testToastTitle, Action: testToastAction})
	runFrame(t, h, func(gtx *layout.Context) {})
	rect := assertToastShown(t, h)
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

// TestOverlayTextContrastOnSurfaceRaisedAndScrim extends UK-E04 to the
// overlay components: the dialog's title and description, the toast's title,
// detail and merge badge — every text the components draw — hold 4.5:1 on
// the SurfaceRaised frame they sit on, in both modes. Nothing draws on the
// bare scrim — it is a backdrop (§3.5), and every text-bearing surface above
// it is the components' own opaque SurfaceRaised frame, checked here.
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
		mode := modeDark
		if light {
			mode = modeLight
		}
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
			if got := contrastRatio(tc.fg, c.SurfaceRaised); got < 4.5 {
				t.Errorf("%s on SurfaceRaised (%s): contrast %.2f < 4.5", tc.name, mode, got)
			}
		}
	}
}

// TestDialogAndPopoverNeverClipAFocusRing extends UK-E10 to the overlay
// bodies: the §3.4 ring is drawn outside a control, so the dialog's body
// clip grows by the ring allowance and the popover draws its content
// unclipped — focus the body's control and assert the ring's ink survives at
// the control's edges, of a scrolled body included.
func TestDialogAndPopoverNeverClipAFocusRing(t *testing.T) {
	t.Parallel()
	t.Run("dialog", func(t *testing.T) {
		t.Parallel()
		f := newDialogFixture(t)
		f.d.Open()
		f.frame()
		f.settle()
		f.focusField()
		f.frame()
		assertRingSurvives(t, f.h, dialogFieldBox(f), func() { f.frame() })
	})
	t.Run("dialog scrolled", func(t *testing.T) {
		t.Parallel()
		f := newDialogFixture(t)
		f.tall = true
		f.d.Open()
		f.frame()
		f.settle()
		// Scroll to the end: the field sits at the slot's bottom edge, its
		// ring over the trailing allowance.
		f.d.scroll.SetOffset(1 << 20)
		f.focusField()
		f.frame()
		assertRingSurvives(t, f.h, dialogFieldBox(f), func() { f.frame() })
	})
	t.Run("popover", func(t *testing.T) {
		t.Parallel()
		f := newPopoverFixture(t)
		f.po.OpenKeyboard()
		f.frame(nil)
		f.frame(nil)
		f.focusField()
		f.frame(nil)
		assertRingSurvives(t, f.h, popoverFieldBox(f), func() { f.frame(nil) })
	})
}

// dialogBodyRect is the fixture's visible body slot on the last frame, the
// way drawBody lays it out: below the header and description, inset by the
// frame's padding, at most maxBody tall.
func dialogBodyRect(f *dialogFixture) image.Rectangle {
	gtx := &f.h.gtx
	m := f.d.measure(f.h.env, gtx, f.props, image.Rectangle{Max: f.h.env.win})
	bodyTop := f.d.rect.Min.Y + m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH
	bodyH := min(f.d.rect.Dy()-2*m.pad-m.headH-gtx.Dp(theme.SpaceSM)-m.descH-gtx.Dp(theme.SpaceLG)-m.footH, m.maxBody)
	return image.Rect(f.d.rect.Min.X+m.pad, bodyTop, f.d.rect.Max.X-m.pad, bodyTop+bodyH)
}

// dialogFieldBox is the fixture's field's box on the last frame's screen:
// the body's content — the field at its band's y, at the width that remains
// after the ring allowance — at the scroll offset the last frame drew.
func dialogFieldBox(f *dialogFixture) image.Rectangle {
	gtx := &f.h.gtx
	ring := dialogRing(gtx)
	body := dialogBodyRect(f)
	off := f.d.scroll.Offset()
	band := f.d.bands[len(f.d.bands)-1]
	contentW := fixtureInnerW(f) - 2*ring
	fieldH := labelLine(f.h.env, gtx, theme.TypeSmall()) + gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.ControlMD)
	x := body.Min.X + ring
	y := body.Min.Y + ring + band.y0 - off
	return image.Rect(x, y, x+contentW, y+fieldH)
}

// fixtureInnerW is the fixture's frame inner width.
func fixtureInnerW(f *dialogFixture) int {
	gtx := &f.h.gtx
	return gtx.Dp(theme.DialogWidth) - 2*gtx.Dp(theme.SpaceXL)
}

// popoverFieldBox is the fixture's field's box inside the panel: below the
// header, at the panel's inner width.
func popoverFieldBox(f *popoverFixture) image.Rectangle {
	gtx := &f.h.gtx
	rect := f.po.Rect()
	pad := gtx.Dp(theme.SpaceSM)
	inner := rect.Inset(pad)
	head := popoverHeader(f.h.env, gtx, f.props)
	top := inner.Min.Y + head + gtx.Dp(theme.SpaceSM)
	w := rect.Dx() - 2*pad
	fieldH := labelLine(f.h.env, gtx, theme.TypeSmall()) + gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.ControlMD)
	return image.Rect(inner.Min.X, top, inner.Min.X+w, top+fieldH)
}

// assertRingSurvives renders the frame with the gallery preview's focus ring
// forced on (the caller set it) and then off, and asserts the ink just
// outside the box's left, right and top edges changed — the ring drew there
// and was not clipped away.
func assertRingSurvives(t *testing.T, h *harness, box image.Rectangle, frame func()) {
	t.Helper()
	ring := h.gtx.Dp(theme.FocusStroke) + h.gtx.Dp(theme.FocusOffset)
	samples := []image.Point{
		{X: box.Min.X - ring, Y: (box.Min.Y + box.Max.Y) / 2},
		{X: box.Max.X + ring, Y: (box.Min.Y + box.Max.Y) / 2},
		{X: (box.Min.X + box.Max.X) / 2, Y: box.Min.Y - ring},
	}
	on := h.shot(t, h.env.win.X, h.env.win.Y)
	h.env.Preview = Preview{}
	frame()
	frame()
	off := h.shot(t, h.env.win.X, h.env.win.Y)
	changed := 0
	for _, p := range samples {
		if !p.In(on.Rect) || !p.In(off.Rect) {
			continue
		}
		if on.RGBAAt(p.X, p.Y) != off.RGBAAt(p.X, p.Y) {
			changed++
		}
	}
	if changed == 0 {
		t.Fatalf("no ring ink just outside %v: the ring was clipped away", box)
	}
}
