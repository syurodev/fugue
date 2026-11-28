package kit

import (
	"image"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Toast, ui-kit §6.18: a status icon, a specific title, a detail of at most
// two lines, at most one action and a close button, on SurfaceRaised with
// BorderSubtle, radius.xl and elevation.float, at the window's bottom-right
// corner above the floating layers (UK-07). At most three are visible; the
// same object's events merge into one toast; the timer is 4 s, or 6 s for
// one with an action or a warning, counted by the frame clock and
// Driver.WakeAt — never a sleep — and paused while the pointer hovers the
// toast. A toast never takes focus: it registers no key filter and no focus
// tag, so §4.2's rule that an agent's approval request never arrives as a
// toast cannot be defeated by a stray keyboard event either.
//
// Motion (§3.7): a toast slides in from the bottom edge while fading in on
// Snappy; the others shift over on Smooth; closing — by the button, or when
// the time is up — slides right while fading out on Exit. Reduced motion
// snaps it all (UK-04).
//
// The manager lives on the window's Env, like the tooltip's: the window
// pushes toasts when its own events produce them and drains the frame's
// outcomes through ToastEvents. Push must come from the frame loop, the way
// every other UI state change does (UK-06: the kit stores no callback, and
// the caller reacts to the frame's events). The one panel painter
// (drawToastPanel) serves both the manager and the gallery's inline
// specimens, so a specimen is the manager's pixels.

const (
	// toastLife is the §6.18 time; a toast with an action or a warning
	// gets toastLifeLong.
	toastLife     = 4 * time.Second
	toastLifeLong = 6 * time.Second
	// toastMax is how many toasts are visible at once (§6.18): a fourth
	// push exits the oldest.
	toastMax = 3
)

// Toast is one notification (§6.18). Object is the merge key: pushing again
// with the same Object merges into the visible toast instead of stacking —
// the count draws as a badge and the timer restarts. Action, when set, is
// the one action's label; its click comes back as a ToastEvent.
type Toast struct {
	Object  string
	Icon    IconName
	Title   string
	Detail  string
	Action  string
	Warning bool
}

// ToastEventKind is what happened to a toast.
type ToastEventKind int

// The §6.18 outcomes. A toast closed by its timer and one closed by its
// button report the same ToastClosed; the caller learns of the action from
// ToastAction alone.
const (
	ToastAction ToastEventKind = iota
	ToastClosed
)

// ToastEvent is one toast outcome of a frame, drained with ToastEvents.
type ToastEvent struct {
	Object string
	Kind   ToastEventKind
	// Count is the merged toast's event count when the outcome happened.
	Count int
}

// ToastPush pushes a toast, merging into the visible toast of the same
// object (§6.18). A fourth visible toast exits the oldest. Call it from the
// frame loop.
func (e *Env) ToastPush(t *Toast) {
	e.toast.ensureInit()
	if at := e.toast.findIndex(t.Object); at >= 0 {
		en := e.toast.list[at]
		en.count++
		en.remaining = en.life
		e.toast.moveLast(at)
		return
	}
	// Only a genuinely new toast evicts: a merge leaves the stack as it is.
	if e.toast.visibleCount() >= toastMax {
		e.toast.dismissOldest(t.Object)
	}
	life := toastLife
	if t.Action != "" || t.Warning {
		life = toastLifeLong
	}
	en := &toastEntry{
		t: *t, count: 1, life: life, remaining: life,
		open:   motion.NewPresence(motion.Snappy(), motion.Exit()),
		posX:   motion.NewFollower(motion.Smooth()),
		posY:   motion.NewFollower(motion.Smooth()),
		action: NewButtonState(),
		close:  NewIconButtonState(),
	}
	en.open.Show(true)
	e.toast.list = append(e.toast.list, en)
}

// ToastEvents drains the frame's toast outcomes.
func (e *Env) ToastEvents() []ToastEvent {
	e.toast.ensureInit()
	out := e.toast.events
	e.toast.events = nil
	return out
}

// toastEntry is one toast's state. Its area and pressables are input tags,
// so the entry must not move in memory once pushed.
type toastEntry struct {
	t     Toast
	count int
	life  time.Duration
	// remaining is the unpaused time left: the frame's real step counts it
	// down while the pointer is elsewhere (§6.18) — never the motion step,
	// which a stalled or idle window caps.
	remaining time.Duration
	// hover is the pointer over the toast; placed remembers that a slot
	// was assigned, so the first one is taken as is instead of sliding in
	// from the window corner.
	hover  bool
	placed bool
	open   motion.Presence
	// posX, posY follow the toast's slot on Smooth (§6.18: the others
	// shift over).
	posX, posY motion.Follower
	rect       image.Rectangle
	// area is the toast's hit area; action and close its two controls.
	area   struct{}
	action *ButtonState
	close  *IconButtonState
}

// toastManager is the window's toast state (§6.18).
type toastManager struct {
	ready bool
	// lastNow is the previous frame's clock, for the timers' real step.
	lastNow time.Time
	list    []*toastEntry
	// events are the outcomes since the last ToastEvents.
	events []ToastEvent
}

func (m *toastManager) ensureInit() {
	if m.ready {
		return
	}
	m.ready = true
	m.lastNow = time.Time{}
}

// findIndex is the visible entry of object, or -1.
func (m *toastManager) findIndex(object string) int {
	for i, en := range m.list {
		if en.t.Object == object && en.open.Shown() {
			return i
		}
	}
	return -1
}

// moveLast moves the entry at at to the end of the list: the newest sits at
// the bottom of the stack (§6.18).
func (m *toastManager) moveLast(at int) {
	en := m.list[at]
	m.list = append(m.list[:at], m.list[at+1:]...)
	m.list = append(m.list, en)
}

func (m *toastManager) visibleCount() int {
	n := 0
	for _, en := range m.list {
		if en.open.Shown() {
			n++
		}
	}
	return n
}

// dismissOldest exits the oldest toast — but not object, whose new push
// merges instead — and reports it closed.
func (m *toastManager) dismissOldest(object string) {
	for _, en := range m.list {
		if en.open.Shown() && en.t.Object != object {
			en.open.Show(false)
			m.events = append(m.events, ToastEvent{Object: en.t.Object, Kind: ToastClosed, Count: en.count})
			return
		}
	}
}

// endFrame runs the toasts' frame at the frame's end (Env.FrameEnd): timers,
// motion, placement and drawing, after everything else deferred this frame.
func (m *toastManager) endFrame(env *Env, gtx *layout.Context) {
	m.ensureInit()
	m.countDown(env, gtx.Now)
	m.advance(env)
	m.place(env, gtx)
	m.draw(env, gtx)
	m.sweep()
}

// countDown applies the frame's real step to every visible toast's timer and
// asks the driver to wake exactly when the next one expires (§6.18: timed by
// the frame clock and Driver.WakeAt, never a sleep). A hovered toast is
// paused: its step is not counted.
func (m *toastManager) countDown(env *Env, now time.Time) {
	step := time.Duration(0)
	if !m.lastNow.IsZero() && now.After(m.lastNow) {
		step = now.Sub(m.lastNow)
	}
	m.lastNow = now
	for _, en := range m.list {
		if !en.open.Shown() || en.hover {
			continue
		}
		en.remaining -= step
		if en.remaining > 0 {
			env.Driver.WakeAt(now.Add(en.remaining))
			continue
		}
		en.remaining = 0
		en.open.Show(false)
		m.events = append(m.events, ToastEvent{Object: en.t.Object, Kind: ToastClosed, Count: en.count})
	}
}

// advance moves every entry's presence and position, or snaps under reduced
// motion (UK-04).
func (m *toastManager) advance(env *Env) {
	for _, en := range m.list {
		if env.ReducedMotion {
			en.open.Snap(en.open.Shown())
			continue
		}
		if en.open.Advance(env.DT()) || en.posX.Moving() || en.posY.Moving() {
			env.Keep()
		}
	}
}

// sweep drops entries that have faded out completely.
func (m *toastManager) sweep() {
	keep := m.list[:0]
	for _, en := range m.list {
		if en.open.Visible() {
			keep = append(keep, en)
		}
	}
	m.list = keep
}

// toastHeight is one toast's box: icon, title, a detail of at most two
// lines, and the paddings; never under a control's height.
func toastHeight(env *Env, gtx *layout.Context, t *Toast, w int) int {
	line := labelLine(env, gtx, theme.TypeLabel())
	h := 2*gtx.Dp(theme.SpaceLG) + line
	if t.Detail != "" {
		dh := wrappedTextHeight(env, gtx, t.Detail, detailWidth(gtx, w))
		h += gtx.Dp(theme.SpaceXS) + min(dh, 2*labelLine(env, gtx, theme.TypeSmall()))
	}
	return max(h, gtx.Dp(theme.ControlMD)+2*gtx.Dp(theme.SpaceLG))
}

// detailWidth is the width the detail wraps at: the toast minus the icon
// column and the room the action and close buttons take.
func detailWidth(gtx *layout.Context, w int) int {
	closeW := max(gtx.Dp(theme.ControlXS), gtx.Dp(theme.HitMin))
	actW := max(gtx.Dp(theme.ControlSM), gtx.Dp(theme.HitMin))
	room := closeW + actW + 2*gtx.Dp(theme.SpaceSM)
	return max(w-2*gtx.Dp(theme.SpaceLG)-gtx.Dp(theme.IconMD)-gtx.Dp(theme.SpaceSM)-room, gtx.Dp(theme.HitMin))
}

// place resolves every visible toast's slot: bottom right, the newest at the
// bottom, and moves the others over on Smooth (§6.18). The first slot a
// toast learns is taken as is, so nothing slides in from the window corner.
// A toast on its way out keeps the place it had.
func (m *toastManager) place(env *Env, gtx *layout.Context) {
	w := gtx.Dp(theme.ToastWidth)
	win := image.Rectangle{Max: env.win}
	margin, gap := gtx.Dp(theme.SpaceLG), gtx.Dp(theme.SpaceSM)
	bottom := win.Max.Y - margin
	for i := len(m.list) - 1; i >= 0; i-- {
		en := m.list[i]
		if !en.open.Visible() {
			continue
		}
		if !en.open.Shown() {
			bottom = en.rect.Min.Y - gap
			continue
		}
		h := toastHeight(env, gtx, &en.t, w)
		x := win.Max.X - margin - w
		rect := image.Rectangle{Min: image.Pt(x, bottom-h), Max: image.Pt(x+w, bottom)}
		bottom = rect.Min.Y - gap
		en.rect = rect
		targetX, targetY := float32(rect.Min.X), float32(rect.Min.Y)
		if env.ReducedMotion || !en.placed {
			en.posX.Snap(targetX)
			en.posY.Snap(targetY)
			en.placed = true
			continue
		}
		en.posX.Follow(targetX, env.DT())
		en.posY.Follow(targetY, env.DT())
	}
}

// draw records every visible toast and hands the macro to op.Defer — after
// everything else deferred this frame, so a toast rides above a dialog's
// scrim (§6.18: above the floating layers).
func (m *toastManager) draw(env *Env, gtx *layout.Context) {
	if len(m.list) == 0 {
		return
	}
	macro := op.Record(gtx.Ops)
	for _, en := range m.list {
		if en.open.Visible() {
			m.drawToast(env, gtx, en)
		}
	}
	op.Defer(gtx.Ops, macro.Stop())
}

// toastClick is what the panel's controls reported in one frame.
type toastClick int

const (
	toastNoClick toastClick = iota
	toastActionClick
	toastCloseClick
)

// drawToast draws one toast — hit area and hover, motion offsets, the shared
// panel painter, and the outcome of its controls. Entering slides up from
// the bottom edge while fading in; leaving slides right while fading out
// (§3.7).
func (m *toastManager) drawToast(env *Env, gtx *layout.Context, en *toastEntry) {
	prog := en.open.Progress()
	rect := en.motionRect(gtx, prog)

	// The hit area and the hover that pauses the timer (§6.18).
	m.hoverArea(gtx, en, rect)
	defer paint.PushOpacity(gtx.Ops, unit01(prog)).Pop()
	click, err := drawToastPanel(env, gtx, rect, &en.t, en.count, en.open.Shown(), en.action, en.close)
	if err != nil {
		env.noteToastError(err)
		return
	}
	switch click {
	case toastActionClick:
		en.open.Show(false)
		m.events = append(m.events, ToastEvent{Object: en.t.Object, Kind: ToastAction, Count: en.count})
	case toastCloseClick:
		en.open.Show(false)
		m.events = append(m.events, ToastEvent{Object: en.t.Object, Kind: ToastClosed, Count: en.count})
	case toastNoClick:
	}
}

// motionRect is the box one toast paints in: its slot, slid up by what is
// still missing while entering and right by what is already gone while
// leaving (§3.7).
func (en *toastEntry) motionRect(gtx *layout.Context, prog float32) image.Rectangle {
	rect := image.Rect(int(en.posX.Value()+0.5), int(en.posY.Value()+0.5),
		int(en.posX.Value()+0.5)+en.rect.Dx(), int(en.posY.Value()+0.5)+en.rect.Dy())
	if en.open.Shown() {
		return rect.Add(image.Pt(0, int((1-prog)*float32(gtx.Dp(theme.SpaceXXL)))))
	}
	return rect.Add(image.Pt(int((1-prog)*float32(rect.Dx())), 0))
}

// hoverArea registers the toast's hit area and reads the Enter/Leave that
// pause its timer.
func (m *toastManager) hoverArea(gtx *layout.Context, en *toastEntry, rect image.Rectangle) {
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &en.area)
	area.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &en.area, Kinds: pointer.Enter | pointer.Leave})
		if !ok {
			return
		}
		if pe, is := ev.(pointer.Event); is {
			switch pe.Kind {
			case pointer.Enter:
				en.hover = true
			case pointer.Leave:
				en.hover = false
			case pointer.Press, pointer.Release, pointer.Cancel, pointer.Move, pointer.Drag, pointer.Scroll:
			}
		}
	}
}
