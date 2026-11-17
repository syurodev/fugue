package kit

import (
	"errors"
	"fmt"
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// Dialog, ui-kit §6.17: a `form` (create or edit one object) or `confirm`
// (the decision before a destructive or security-relevant action) on a
// scrim. The frame is radius.xl, type.title with a description, a scrollable
// body and a footer of Cancel and one primary button. It is the window's one
// dialog layer (UK-07): a dialog never opens over a dialog — a second Open
// while one is up is refused with ErrDialogAlreadyOpen — and ordinary
// success never opens one (§6.17).
//
// Keyboard (§6.17, §7): focus is trapped — Tab and Shift+Tab cycle through
// the body controls the caller lists in FocusOrder and the two footer
// buttons — and returns to the opener on close. Escape closes only when safe
// (a form with unsaved input refuses it; a confirm closes, the safe choice),
// and Enter submits a form through the kit fields' own submit translation:
// the caller calls Submit when a field reports Submitted, so Enter inside a
// multi-line TextArea is a newline and never a submit, and no key event is
// ever rerouted through private plumbing. A confirm focuses the safe choice
// (§6.17).
//
// Submitting (§6.17): Busy blocks repeat submits, and a close is never
// reported as a cancellation of a request that cannot be cancelled — the
// caller owns the operation and the result says only what the dialog did.
//
// Motion (§3.7): the scrim fades on Smooth, the frame fades in and grows
// from 0.95 on Snappy; closing is Exit for both; a body height change (an
// error line, a new step) follows Smooth through the field core's own line
// spring. Reduced motion snaps it all (UK-04).
//
// Props and state are separate (UK-02): the caller rebuilds DialogProps
// every frame and keeps one DialogState per logical dialog. The body comes
// as one draw function the dialog runs exactly once per frame — the kit
// fields keep state and must not run twice (UK-02).

// ErrDialogAlreadyOpen is refused when a dialog is opened while one is open
// (§6.17: never a dialog on a dialog).
var ErrDialogAlreadyOpen = errors.New("a dialog is already open")

// DialogVariant is the §6.17 kind.
type DialogVariant int

// The §6.17 kinds.
const (
	DialogForm    DialogVariant = iota // create or edit one object
	DialogConfirm                      // the decision before a destructive action
)

// DialogProps are the per-frame, caller-owned properties (UK-02).
type DialogProps struct {
	Variant     DialogVariant
	Title       string
	Description string
	// Body draws the dialog's scrollable content — the form's kit fields,
	// a confirm's message — at the frame's inner width and returns the box
	// it used. It runs once per frame, here: its fields keep state.
	Body func(gtx *layout.Context) image.Point
	// FocusOrder are the body's focusable tags in Tab order; the focus
	// trap cycles them before the footer buttons (§6.17).
	FocusOrder []event.Tag
	// Cancel and Primary are the footer buttons. The dialog lays them out
	// and reports their clicks in the result; PrimaryLabel names the
	// primary (a specific verb, §4.5) and PrimaryVariant is its variant —
	// Danger for a confirm that destroys data (§6.17).
	Cancel, Primary *ButtonState
	PrimaryLabel    string
	PrimaryVariant  ButtonVariant
	// Dirty reports a form with unsaved input: Escape then refuses to
	// close (§6.17).
	Dirty bool
	// Busy blocks a repeat submit (§6.17).
	Busy bool
	// Safe, for a confirm, is the tag of the safe choice the dialog
	// focuses when it opens (§6.17); nil focuses the Cancel button.
	Safe event.Tag
	// Inline draws the frame where the cursor is, without the scrim or the
	// dialog slot: the gallery's specimens.
	Inline bool
}

// DialogResult is what happened in the frame. The dialog closes itself on
// every terminal outcome; the caller owns what the outcome then means.
type DialogResult struct {
	// Cancelled is the Cancel button, or Escape on a safe close.
	Cancelled bool
	// Submitted is a submit through Submit or the primary button.
	Submitted bool
	// EscapeRefused is Escape ignored because the form has unsaved input
	// (§6.17); the dialog stays open.
	EscapeRefused bool
	// Closed is any terminal outcome.
	Closed bool
	// Size is the inline specimen's frame box.
	Size image.Point
}

// DialogState is the long-lived state of one dialog (UK-02).
type DialogState struct {
	open   motion.Presence
	scrim  motion.Follower
	claim  bool
	opener event.Tag
	// focusOnOpen moves focus to the first tag on the next frame.
	focusOnOpen bool
	// submit is set by Submit and consumed by the next Layout.
	submit bool
	// scroll is the body's scroll offset; body the scroll area's tag. They
	// are pointers, so the state must not move in memory.
	scroll int
	body   struct{}
	rect   image.Rectangle
	origin image.Point
}

// NewDialogState builds the state of one dialog.
func NewDialogState() *DialogState {
	return &DialogState{
		open:  motion.NewPresence(motion.Snappy(), motion.Exit()),
		scrim: motion.NewFollower(motion.Smooth()),
	}
}

// Open opens the dialog.
func (d *DialogState) Open() {
	d.open.Show(true)
	d.claim = true
	d.focusOnOpen = true
	d.scroll = 0
	d.submit = false
}

// SetOpener records the control that opened the dialog: focus returns to it
// when the dialog closes (§6.17, UK-05).
func (d *DialogState) SetOpener(tag event.Tag) { d.opener = tag }

// Close closes the dialog on Exit.
func (d *DialogState) Close() { d.open.Show(false) }

// IsOpen reports whether the dialog was last asked to be open.
func (d *DialogState) IsOpen() bool { return d.open.Shown() }

// Submit asks the dialog to submit its form, the way a field's Enter does:
// the caller calls it in the frame a TextField reported Submitted (§6.17 —
// Enter submits a form, never from a multi-line TextArea, because the
// TextArea's core never translates Enter into a submit).
func (d *DialogState) Submit() { d.submit = true }

// Layout runs the dialog's frame. A closed dialog does nothing; one opened
// while another holds the dialog slot is refused with ErrDialogAlreadyOpen
// (§6.17).
func (d *DialogState) Layout(env *Env, gtx *layout.Context, props DialogProps) (DialogResult, error) {
	var res DialogResult
	if !d.open.Visible() {
		env.releaseDialog(d)
		return res, nil
	}
	if !props.Inline {
		if d.claim {
			if !env.claimDialog(d) {
				d.Close()
				return res, ErrDialogAlreadyOpen
			}
			d.claim = false
		}
		if d.focusOnOpen {
			first := props.Safe
			if first == nil && props.Variant == DialogConfirm && props.Cancel != nil {
				// A confirm's safe choice is its Cancel (§6.17).
				first = props.Cancel.Tag()
			}
			if first == nil && len(props.FocusOrder) > 0 {
				first = props.FocusOrder[0]
			}
			if first != nil {
				gtx.Execute(key.FocusCmd{Tag: first})
			}
			d.focusOnOpen = false
		}
	}
	if d.open.Shown() {
		d.input(env, gtx, props, &res)
	}
	d.advance(env, props.Inline)
	if err := d.place(env, gtx, props, &res); err != nil {
		return res, fmt.Errorf("lay the dialog out: %w", err)
	}
	// Submit is consumed here, after the body drew: a field's Enter in this
	// frame reports in this frame (UK-06), and the dialog closes on it.
	d.consumeSubmit(props, &res)
	if res.Submitted && !res.Closed {
		res.Closed = true
		d.close(gtx)
	}
	res.Size = d.rect.Size()
	return res, nil
}

// consumeSubmit turns a pending Submit into the result, unless the form is
// busy — a submitting dialog blocks a repeat submit (§6.17) — or the frame
// already closed the dialog.
func (d *DialogState) consumeSubmit(props DialogProps, res *DialogResult) {
	if !d.submit {
		return
	}
	d.submit = false
	if props.Busy || res.Submitted || res.Closed {
		return
	}
	res.Submitted = true
}

// input reads the frame's keyboard: the focus trap and Escape. The dialog
// owns Escape only while no floating layer sits above the scrim (§7); a
// popover over the dialog gets it first.
func (d *DialogState) input(env *Env, gtx *layout.Context, props DialogProps, res *DialogResult) {
	if !d.open.Shown() || props.Inline {
		return
	}
	d.trapFocus(env, gtx, props)
	if env.popLayer == nil && env.menuLayer == nil {
		d.escapeInput(gtx, props, res)
	}
}

// trapFocus cycles Tab and Shift+Tab through the body tags and the two
// footer buttons, so focus never leaves the dialog (§6.17). The presses come
// from the Env's recorded flags, not raw key events: TrackInput is the sole
// raw Tab reader, and key events go to the first matching gtx.Event caller.
func (d *DialogState) trapFocus(env *Env, gtx *layout.Context, props DialogProps) {
	tab, shiftTab := env.takeTab(), env.takeShiftTab()
	if !tab && !shiftTab {
		return
	}
	tags := d.trapTags(props)
	if len(tags) == 0 {
		return
	}
	cur := -1
	for i, tag := range tags {
		if gtx.Focused(tag) {
			cur = i
			break
		}
	}
	step := 1
	if shiftTab {
		step = -1
	}
	next := 0
	if cur >= 0 {
		next = (cur + step + len(tags)) % len(tags)
	} else if step < 0 {
		next = len(tags) - 1
	}
	gtx.Execute(key.FocusCmd{Tag: tags[next]})
}

// trapTags is the dialog's tab order: the body's, then Cancel, then the
// primary (§6.17).
func (d *DialogState) trapTags(props DialogProps) []event.Tag {
	tags := make([]event.Tag, 0, len(props.FocusOrder)+2)
	tags = append(tags, props.FocusOrder...)
	if props.Cancel != nil {
		tags = append(tags, props.Cancel.Tag())
	}
	if props.Primary != nil {
		tags = append(tags, props.Primary.Tag())
	}
	return tags
}

// escapeInput applies the Escape rule (§6.17): a form with unsaved input
// refuses it; everything else closes as a cancellation.
func (d *DialogState) escapeInput(gtx *layout.Context, props DialogProps, res *DialogResult) {
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			return
		}
		ke, is := ev.(key.Event)
		if !is || ke.State != key.Press {
			continue
		}
		if props.Variant == DialogForm && props.Dirty {
			res.EscapeRefused = true
			return
		}
		d.cancel(gtx, res)
		return
	}
}

// cancel closes the dialog as cancelled, focus back on the opener.
func (d *DialogState) cancel(gtx *layout.Context, res *DialogResult) {
	res.Cancelled, res.Closed = true, true
	d.close(gtx)
}

// close closes on Exit and returns focus to the opener (§6.17, UK-05).
func (d *DialogState) close(gtx *layout.Context) {
	opener := d.opener
	d.Close()
	if opener != nil {
		gtx.Execute(key.FocusCmd{Tag: opener})
	}
}

// advance moves the frame's presence and the scrim's fade, or snaps them
// under reduced motion (UK-04).
func (d *DialogState) advance(env *Env, inline bool) {
	if inline {
		return
	}
	shown := d.open.Shown()
	target := float32(0)
	if shown {
		target = 1
	}
	if env.ReducedMotion {
		d.open.Snap(shown)
		d.scrim.Snap(target)
		return
	}
	moved := d.open.Advance(env.DT())
	d.scrim.Follow(target, env.DT())
	if moved || d.scrim.Moving() {
		env.Keep()
	}
}
