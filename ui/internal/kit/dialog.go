package kit

import (
	"errors"
	"fmt"
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Dialog, ui-kit §6.17: a `form` (create or edit one object) or `confirm`
// (the decision before a destructive or security-relevant action) on a
// scrim. The frame is radius.xl, type.title with a description, a scrollable
// body on the kit's ScrollArea (§6.29, thin fading bar, no yank) and a
// footer of Cancel and one primary button. It is the window's one dialog
// layer (UK-07): a dialog never opens over a dialog — a second Open while
// one is up is refused with ErrDialogAlreadyOpen — and ordinary success
// never opens one (§6.17).
//
// Keyboard (§6.17, §7): focus is trapped — Tab and Shift+Tab cycle through
// the body controls the Body declared (DialogBands) and the two footer
// buttons — and returns to the opener on close. The scroll follows focus: a
// focused control scrolled out of the body is brought back in, with room for
// its §3.4 ring, which is why the body's clip grows by the ring allowance
// and the content takes the width that remains. Escape closes only when safe
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
	// it used. It declares each focusable control's band on bands (see
	// DialogBands). It runs once per frame, here: its fields keep state.
	Body func(gtx *layout.Context, bands *DialogBands) image.Point
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
	// dialog slot: the gallery's specimens. ScrimRect, when non-empty,
	// paints the scrim over that rectangle first, so a specimen can show
	// the dialog against its dimmed page (§6.17).
	Inline    bool
	ScrimRect image.Rectangle
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

// DialogBands is the handle a dialog Body draws with: it declares each
// focusable control's band — its y range in the body's own coordinates — so
// the focus trap cycles them in draw order and the scroll keeps the focused
// one in view (§6.17). The footer's two buttons are bands the dialog adds
// itself.
type DialogBands struct {
	bands []dialogBand
}

// dialogBand is one focusable control's y range in body coordinates.
type dialogBand struct {
	tag    event.Tag
	y0, y1 int
}

// Focusable declares the band of one focusable body control: the control's
// tag and its y range in the body's own coordinates, as the Body laid it
// out.
func (b *DialogBands) Focusable(tag event.Tag, y0, y1 int) {
	b.bands = append(b.bands, dialogBand{tag: tag, y0: y0, y1: y1})
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
	// scroll is the body's ScrollArea (§6.29): the thin fading bar, the
	// no-yank rule, and the offset the focus reveal drives.
	scroll *ScrollAreaState
	// bands are the last frame's focusable bands, in draw order; the trap
	// and the reveal read them (the areas the pointer acts on are also the
	// ones the last frame drew). viewLenPx is the last frame's visible body
	// height in content pixels, for the reveal.
	bands     []dialogBand
	viewLenPx int
	rect      image.Rectangle
}

// NewDialogState builds the state of one dialog.
func NewDialogState() *DialogState {
	return &DialogState{
		open:   motion.NewPresence(motion.Snappy(), motion.Exit()),
		scrim:  motion.NewFollower(motion.Smooth()),
		scroll: NewScrollAreaState(),
	}
}

// Open opens the dialog.
func (d *DialogState) Open() {
	d.open.Show(true)
	d.claim = true
	d.focusOnOpen = true
	d.scroll.SetOffset(0)
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
func (d *DialogState) Layout(env *Env, gtx *layout.Context, props *DialogProps) (DialogResult, error) {
	var res DialogResult
	if !d.open.Visible() {
		env.releaseDialog(d)
		return res, nil
	}
	if err := d.claimLayer(env, props); err != nil {
		return res, err
	}
	if d.open.Shown() {
		d.input(env, gtx, props, &res)
	}
	d.advance(env, props.Inline)
	if err := d.place(env, gtx, props, &res); err != nil {
		return res, fmt.Errorf("lay the dialog out: %w", err)
	}
	d.focusOpen(gtx, props)
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

// claimLayer takes the window's one dialog slot (§6.17).
func (d *DialogState) claimLayer(env *Env, props *DialogProps) error {
	if props.Inline {
		return nil
	}
	if d.claim {
		if !env.claimDialog(d) {
			d.Close()
			return ErrDialogAlreadyOpen
		}
		d.claim = false
	}
	return nil
}

// focusOpen applies the open focus once the frame's body drew, because the
// body declares its bands while it draws: a confirm's safe choice — its
// Cancel — or the body's first band, else Cancel (§6.17).
func (d *DialogState) focusOpen(gtx *layout.Context, props *DialogProps) {
	if !d.focusOnOpen || props.Inline {
		return
	}
	first := props.Safe
	if first == nil && props.Variant == DialogConfirm && props.Cancel != nil {
		// A confirm's safe choice is its Cancel (§6.17).
		first = props.Cancel.Tag()
	}
	if first == nil && len(d.bands) > 0 {
		first = d.bands[0].tag
	}
	if first == nil && props.Cancel != nil {
		first = props.Cancel.Tag()
	}
	if first != nil {
		gtx.Execute(key.FocusCmd{Tag: first})
	}
	d.focusOnOpen = false
}

// consumeSubmit turns a pending Submit into the result, unless the form is
// busy — a submitting dialog blocks a repeat submit (§6.17) — or the frame
// already closed the dialog.
func (d *DialogState) consumeSubmit(props *DialogProps, res *DialogResult) {
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
func (d *DialogState) input(env *Env, gtx *layout.Context, props *DialogProps, res *DialogResult) {
	if !d.open.Shown() || props.Inline {
		return
	}
	d.trapFocus(env, gtx, props)
	if env.popLayer == nil && env.menuLayer == nil {
		d.escapeInput(gtx, props, res)
	}
}

// trapFocus cycles Tab and Shift+Tab through the body's bands and the two
// footer buttons, so focus never leaves the dialog (§6.17). The presses come
// from the Env's recorded flags, not raw key events: TrackInput is the sole
// raw Tab reader, and key events go to the first matching gtx.Event caller.
func (d *DialogState) trapFocus(env *Env, gtx *layout.Context, props *DialogProps) {
	tab, shiftTab := env.takeTab(), env.takeShiftTab()
	if !tab && !shiftTab {
		return
	}
	tags := d.trapTags(props)
	if len(tags) == 0 {
		return
	}
	cur := d.focusedBand(gtx, tags)
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
	d.reveal(next, dialogRing(gtx))
}

// trapTags is the dialog's tab order: the body's bands in draw order, then
// Cancel, then the primary (§6.17).
func (d *DialogState) trapTags(props *DialogProps) []event.Tag {
	tags := make([]event.Tag, 0, len(d.bands)+2)
	for _, band := range d.bands {
		tags = append(tags, band.tag)
	}
	if props.Cancel != nil {
		tags = append(tags, props.Cancel.Tag())
	}
	if props.Primary != nil {
		tags = append(tags, props.Primary.Tag())
	}
	return tags
}

// focusedBand is the index of the focused tag in tags, or -1.
func (d *DialogState) focusedBand(gtx *layout.Context, tags []event.Tag) int {
	for i, tag := range tags {
		if gtx.Focused(tag) {
			return i
		}
	}
	return -1
}

// reveal scrolls the band at index i into the body's visible slot, with
// room for its §3.4 ring on both edges. It runs when focus moved to the
// band, against the last frame's geometry (the geometry focus acts on):
// viewLenPx is the visible body height in content pixels and ring the px
// ring allowance the clip grew by.
func (d *DialogState) reveal(i, ring int) {
	if i < 0 || i >= len(d.bands) || d.viewLenPx <= 0 {
		return
	}
	band := d.bands[i]
	if band.y1+ring > d.scroll.Offset()+d.viewLenPx {
		d.scroll.SetOffset(band.y1 + ring - d.viewLenPx)
	}
	if band.y0-ring < d.scroll.Offset() {
		d.scroll.SetOffset(band.y0 - ring)
	}
}

// escapeInput applies the Escape rule (§6.17): a form with unsaved input
// refuses it; everything else closes as a cancellation.
func (d *DialogState) escapeInput(gtx *layout.Context, props *DialogProps, res *DialogResult) {
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

// dialogRing is the px room a §3.4 focus ring needs outside a control: the
// stroke and its offset. The body's clip grows by it, so no control's ring
// is ever cut.
func dialogRing(gtx *layout.Context) int {
	return gtx.Dp(theme.FocusStroke) + gtx.Dp(theme.FocusOffset)
}
