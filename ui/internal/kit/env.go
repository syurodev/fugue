package kit

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Env is the per-window frame environment every component draws with (the
// window owns one, never a component): the theme and shaper, the F10.6
// motion driver, the UI and content text scale factors, the reduced-motion
// setting, and the focus-visible modality — Gio v0.10.3 has no focus-visible
// of its own, so the kit tracks the modality at window level: Tab turns it
// on, any pointer press turns it off (ui-kit §9.1, UK-05).
//
// An Env must be used as a pointer and lives as long as its window: it
// caches icon strokes in an op.Ops that is never reset (a clip.PathSpec is
// bound to the ops that recorded it, ui-kit §6.1), and it holds no device
// pixels — the icon cache is keyed by dp size and stays correct when the
// monitor or the UI scale changes (§3.0).
type Env struct {
	// Th is the mode the window draws in.
	Th *theme.Theme
	// Sh is the text shaper.
	Sh *text.Shaper
	// Driver is the F10.6 motion driver; Begin, Keep and FrameEnd drive it.
	Driver motion.Driver
	// UIScale multiplies both PxPerDp and PxPerSp of the window's metric
	// (Metric), the "interface size" setting of §3.0: 0.9, 1, 1.1 or 1.25.
	// Zero means 1.
	UIScale float32
	// ContentScale multiplies the size and line of the content type tokens
	// only (theme.Type.Content: prose and code), never the shared metric
	// (§3.0). Zero means 1.
	ContentScale float32
	// Platform picks the notation of keyboard shortcuts (Kbd). The window
	// sets it once from the OS it runs on; the kit never asks the OS.
	Platform Platform
	// ReducedMotion replaces animation with instant changes (UK-04): the
	// spinner holds still, springs snap. Filled by the window from the
	// app's settings.
	ReducedMotion bool
	// TimeScale multiplies every frame step, springs and the spinner alike:
	// the gallery's slow-motion mode runs everything at a quarter speed so
	// motion can be judged by eye (§8). Zero means 1.
	TimeScale float32
	// KeyboardOnly is the gallery's keyboard-only mode: focus moves count as
	// coming from the keyboard for the whole session, so focus rings stay
	// visible no matter how focus landed (UK-05). Production windows leave
	// it false; a pointer press then clears the modality again.
	KeyboardOnly bool
	// Preview forces interaction states the gallery and the evidence tests
	// must render without pointing a real pointer at every cell (UK-09):
	// one pointer can hover one control at a time, but a state matrix shows
	// every hover at once. The tags short-circuit the matching interaction
	// check on the controls they name. Production windows leave it zero.
	Preview Preview
	// RunRecorder, when non-nil, receives the bounds of every text run the
	// kit draws — label, placeholder, helper, error, button label — in the
	// local frame of the control being drawn (UK-E09): the gallery attaches
	// one per placed cell, knows the cell's window position and translates.
	// Gio exposes no current transform (ui-guide §5), so the kit records at
	// the draw sites that know their own offsets. Production windows leave
	// it nil and nothing is recorded.
	RunRecorder func(image.Rectangle)

	// win is the window size in px and origin the window position of the
	// layout cursor: Gio cannot read the current transform, so the overlay
	// learns where an anchor is from TrackInput and Offset (UK-07).
	win, origin image.Point
	// menuLayer and popLayer are the window's floating layers (UK-07): at
	// most one of each, so a menu may open from a popover; the seqs order
	// them for Escape. dialog is the window's one dialog layer (§6.17).
	menuLayer, popLayer event.Tag
	menuSeq, popSeq     int
	dialog              event.Tag
	// toast is the window's toast manager (UK-07, §6.18).
	toast toastManager
	// toastErrors carry a toast's draw errors from the frame end, where the
	// manager runs, out to ToastError.
	toastErrors []error
	// tip is the window's tooltip manager (UK-07, §6.7).
	tip tooltipManager

	// modality is true while focus moves come from the keyboard.
	modality bool
	// tabPress and shiftTabPress record an unmodified, resp. Shift+Tab,
	// press seen by TrackInput this frame, so the components that act on
	// Tab (a Combobox with a keyboard-moved highlight, the Dialog's focus
	// trap) consume it without racing TrackInput, the sole raw reader:
	// key events go to the first matching gtx.Event caller.
	tabPress, shiftTabPress bool
	// inputTag backs the window-level listeners of TrackInput; its address
	// is the tag, so it must not move.
	inputTag event.Tag

	// scrollAxes guards against two nested ScrollAreas on the same axis
	// (§6.29).
	scrollAxes [2]bool

	// dt and now are the current frame's step and clock, from Begin.
	dt  time.Duration
	now time.Time

	// iconOps records every icon stroke once and is never reset: a
	// clip.PathSpec stays bound to the ops it was recorded in (§6.1).
	iconOps op.Ops
	// iconStrokes caches one stroke per icon and dp size, on the 24 grid.
	iconStrokes map[iconKey]clip.Op
	// iconPaths caches the parsed path data per icon.
	iconPaths map[IconName][]iconSubpath
}

// iconKey identifies one cached stroke: an icon at a dp size. The key is dp,
// never device pixels: the cached path is stroked on the 24 grid and the
// affine transform at draw time carries it to whatever metric is current.
type iconKey struct {
	name IconName
	size unit.Dp
}

// Preview names the controls whose interaction states a gallery frame
// forces (Env.Preview); the zero value forces nothing.
type Preview struct {
	// Hover and Press render the named control hovered and held down.
	Hover, Press event.Tag
	// Focus renders the named control with its keyboard focus ring, even
	// though no window focus event moved it.
	Focus event.Tag
	// Open renders a Select control in its open state (rotated chevron).
	Open event.Tag
}

// Metric returns the window metric with the UI scale of §3.0 applied: both
// PxPerDp and PxPerSp multiply by the same factor, so every page zooms like
// VS Code's zoom instead of only its text. The window calls this once per
// frame before layout.
func (e *Env) Metric(device unit.Metric) unit.Metric {
	s := e.uiScale()
	return unit.Metric{PxPerDp: device.PxPerDp * s, PxPerSp: device.PxPerSp * s}
}

// uiScale normalizes the setting: unset and non-positive mean 1.
func (e *Env) uiScale() float32 {
	if e.UIScale > 0 {
		return e.UIScale
	}
	return 1
}

// contentScale normalizes the content text setting: unset and non-positive
// mean 1.
func (e *Env) contentScale() float32 {
	if e.ContentScale > 0 {
		return e.ContentScale
	}
	return 1
}

// Content resolves a type token for drawing (§3.0): content tokens (prose,
// code) scale size and line by the content text setting, keeping their
// ratio; UI tokens pass through untouched.
func (e *Env) Content(t theme.Type) (size, line unit.Sp) {
	if !t.Content {
		return t.Size, t.Line
	}
	s := e.contentScale()
	return t.Size * unit.Sp(s), t.Line * unit.Sp(s)
}

// Begin starts a frame at now: it advances the motion driver and remembers
// the step. The window calls it before layout, with the same time it puts
// into gtx.Now.
func (e *Env) Begin(now time.Time) {
	e.dt = e.Driver.Begin(now)
	if e.TimeScale > 0 {
		e.dt = time.Duration(float64(e.dt) * float64(e.TimeScale))
	}
	e.now = now
}

// Now is the current frame's clock.
func (e *Env) Now() time.Time { return e.now }

// DT is the time step springs and phase angles advance by this frame,
// capped by the driver.
func (e *Env) DT() time.Duration { return e.dt }

// Keep marks that something is still moving and needs the next frame.
func (e *Env) Keep() { e.Driver.Keep() }

// FrameEnd schedules the next frame if anything asked for one. The window
// calls it after layout.
func (e *Env) FrameEnd(gtx *layout.Context) {
	e.tip.endFrame(e, gtx)
	e.toast.endFrame(e, gtx)
	if at, ok := e.Driver.Next(); ok {
		gtx.Execute(op.InvalidateCmd{At: at})
	}
}

// ToastError drains the first draw error the frame's toasts hit, if any: the
// manager runs at the frame end, where a Layout return cannot carry it.
func (e *Env) ToastError() error {
	if len(e.toastErrors) == 0 {
		return nil
	}
	err := e.toastErrors[0]
	e.toastErrors = e.toastErrors[:0]
	return err
}

// TrackInput registers the window-level listeners that maintain the
// focus-visible modality (UK-05) and records the window's size for the
// overlay (UK-07). The window calls it first in every frame,
// before any component: the pointer area is pushed at the back of the frame
// so it sees presses anywhere (Gio delivers pointer events to every area
// under the pointer, so this hides nothing).
func (e *Env) TrackInput(gtx *layout.Context) {
	e.win, e.origin = gtx.Constraints.Max, image.Point{}
	e.tabPress, e.shiftTabPress = false, false
	e.tip.beginFrame()
	if e.KeyboardOnly {
		// The gallery's keyboard-only mode: every focus move came from the
		// keyboard as far as the controls are concerned, so the rings stay
		// on for the whole session (UK-05).
		e.modality = true
		return
	}
	// Tab turns the keyboard modality on. Naming Tab explicitly receives it
	// even though the window also consumes it for focus movement.
	e.trackTab(gtx)
	// A pointer press anywhere turns it off again.
	defer clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &e.inputTag)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &e.inputTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, isPointer := ev.(pointer.Event); isPointer && pe.Kind == pointer.Press {
			e.modality = false
		}
	}
}

func (e *Env) trackTab(gtx *layout.Context) {
	for {
		ev, ok := gtx.Event(key.Filter{
			Name:     key.NameTab,
			Optional: key.ModShift | key.ModCtrl | key.ModAlt | key.ModCommand,
		})
		if !ok {
			return
		}
		if ke, isKey := ev.(key.Event); isKey && ke.State == key.Press {
			e.modality = true
			switch ke.Modifiers {
			case 0:
				e.tabPress = true
			case key.ModShift:
				e.shiftTabPress = true
			}
		}
	}
}

// takeTab consumes a pending unmodified Tab press recorded by TrackInput.
func (e *Env) takeTab() bool {
	if !e.tabPress {
		return false
	}
	e.tabPress = false
	return true
}

// takeShiftTab consumes a pending Shift+Tab press recorded by TrackInput.
func (e *Env) takeShiftTab() bool {
	if !e.shiftTabPress {
		return false
	}
	e.shiftTabPress = false
	return true
}

// FocusVisible reports whether tag is focused and focus came from the
// keyboard, so a control draws its focus ring only in that case (UK-05): a
// pointer click focuses quietly.
func (e *Env) FocusVisible(gtx *layout.Context, tag event.Tag) bool {
	return tag != nil && gtx.Focused(tag) && e.modality
}

// previewHover reports whether the gallery frame forces tag hovered.
func (e *Env) previewHover(tag event.Tag) bool {
	return tag != nil && e.Preview.Hover == tag
}

// previewPress reports whether the gallery frame forces tag held down.
func (e *Env) previewPress(tag event.Tag) bool {
	return tag != nil && e.Preview.Press == tag
}

// previewFocus reports whether the gallery frame forces tag's focus ring.
func (e *Env) previewFocus(tag event.Tag) bool {
	return tag != nil && e.Preview.Focus == tag
}

// previewOpen reports whether the gallery frame forces tag's open state.
func (e *Env) previewOpen(tag event.Tag) bool {
	return tag != nil && e.Preview.Open == tag
}
