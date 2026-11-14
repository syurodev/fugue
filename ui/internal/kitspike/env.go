package kitspike

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

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Env is the per-window frame environment every component draws with: the
// theme, the shaper, the motion driver and the pointer-or-keyboard focus
// modality (UK-05). It is owned by the window, not by components.
type Env struct {
	Th     *theme.Theme
	Sh     *text.Shaper
	Driver *motion.Driver

	// dt is the frame step the driver handed out; components advance
	// springs with it.
	dt time.Duration

	// modality is true after the last focus move came from the keyboard
	// (Tab) and false after any pointer press. Gio v0.10.3 has no
	// focus-visible of its own; this is the whole mechanism.
	modality bool
	inputTag event.Tag
	now      time.Time

	// SimHover and SimPressed simulate hover and press for headless
	// screenshots (the shooter cannot move a real pointer onto every
	// control). Components compare their tag against them. Nil in normal
	// use.
	SimHover    event.Tag
	SimPressed  event.Tag
	SimFocus    event.Tag
	SimSelected bool
}

// textShaperAlias removed; see Sh.

// TrackInput registers the window-level listeners that maintain the focus
// modality. Call it first in the frame, before any component: the pointer
// area must be registered at the back of the window so every press is seen
// (Gio delivers pointer events to every area under the pointer, so this
// does not block anything).
func (e *Env) TrackInput(gtx *layout.Context) {
	// Tab moves focus; a filter that names Tab explicitly receives it even
	// though the window also consumes it for focus movement.
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameTab, Optional: key.ModShift | key.ModCtrl | key.ModAlt | key.ModCommand})
		if !ok {
			break
		}
		if ev.(key.Event).State == key.Press {
			e.modality = true
		}
	}
	defer clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &e.inputTag)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &e.inputTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if ev.(pointer.Event).Kind == pointer.Press {
			e.modality = false
		}
	}
}

// FocusVisible reports whether tag is focused AND focus came from the
// keyboard, so the focus ring shows only for keyboard focus (UK-05).
func (e *Env) FocusVisible(gtx *layout.Context, tag event.Tag) bool {
	if e.SimFocus == tag {
		return true
	}
	return gtx.Focused(tag) && e.modality
}

// Hovered reports whether the pointer rests on tag, honoring the screenshot
// simulation.
func (e *Env) Hovered(tag event.Tag) bool {
	return e.SimHover == tag
}

// Pressed reports whether tag is being held down, honoring the screenshot
// simulation.
func (e *Env) Pressed(tag event.Tag) bool {
	return e.SimPressed == tag
}

// Begin starts a frame: advance the driver and remember the step. The window
// calls this before drawing.
func (e *Env) Begin(now time.Time) {
	e.dt = e.Driver.Begin(now)
	e.now = now
}

// Now is the current frame's clock.
func (e *Env) Now() time.Time { return e.now }

// FrameEnd schedules the next frame if anything moved.
func (e *Env) FrameEnd(gtx *layout.Context) {
	if at, ok := e.Driver.Next(); ok {
		gtx.Execute(op.InvalidateCmd{At: at})
	}
}

// Keep marks that something is still moving (a thin wrapper so components
// do not each hold the driver).
func (e *Env) Keep(moving bool) {
	if moving {
		e.Driver.Keep()
	}
}

// DT is the current frame step for advancing springs.
func (e *Env) DT() time.Duration { return e.dt }
