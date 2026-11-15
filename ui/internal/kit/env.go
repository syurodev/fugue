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
	// ReducedMotion replaces animation with instant changes (UK-04): the
	// spinner holds still, springs snap. Filled by the window from the
	// app's settings.
	ReducedMotion bool

	// modality is true while focus moves come from the keyboard.
	modality bool
	// inputTag backs the window-level listeners of TrackInput; its address
	// is the tag, so it must not move.
	inputTag event.Tag

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
	if at, ok := e.Driver.Next(); ok {
		gtx.Execute(op.InvalidateCmd{At: at})
	}
}

// TrackInput registers the window-level listeners that maintain the
// focus-visible modality (UK-05). The window calls it first in every frame,
// before any component: the pointer area is pushed at the back of the frame
// so it sees presses anywhere (Gio delivers pointer events to every area
// under the pointer, so this hides nothing).
func (e *Env) TrackInput(gtx *layout.Context) {
	// Tab turns the keyboard modality on. Naming Tab explicitly receives it
	// even though the window also consumes it for focus movement.
	for {
		ev, ok := gtx.Event(key.Filter{
			Name:     key.NameTab,
			Optional: key.ModShift | key.ModCtrl | key.ModAlt | key.ModCommand,
		})
		if !ok {
			break
		}
		if ke, isKey := ev.(key.Event); isKey && ke.State == key.Press {
			e.modality = true
		}
	}
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

// FocusVisible reports whether tag is focused and focus came from the
// keyboard, so a control draws its focus ring only in that case (UK-05): a
// pointer click focuses quietly.
func (e *Env) FocusVisible(gtx *layout.Context, tag event.Tag) bool {
	return tag != nil && gtx.Focused(tag) && e.modality
}
