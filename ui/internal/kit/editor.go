package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The shared editing core of ui-kit UK-08: TextField, TextArea and, later,
// Combobox and the composer all draw their text through one fieldCore over
// a widget.Editor, so typing Vietnamese (DA-06), selection, copy, cut,
// paste and undo behave the same everywhere. The core owns the box's input
// and the §3.7 motion of the field chrome — the fill lift on Snappy, the
// focus ring fade, the helper↔error crossfade with its Smooth height —
// while the variants (textfield.go, textarea.go) own their layout and
// behavior. Events are read in the frame that saw them (UK-06); props and
// state stay separate (UK-02). A field's tag is its editor, so gallery
// previews and focus scripting address fields through Tag.

// secretMask is the rune a masked field draws for every character.
const secretMask = '•'

// FieldEvents are the UK-06 events of one frame, reported by TextField and
// TextArea alike. Submitted fires only when the caller asked for a submit
// key: Submit is a prop of the variant (TextField asks; TextArea and the
// composer do not — the composer's submit key is its own, §6.9).
type FieldEvents struct {
	// Changed reports that the text changed this frame.
	Changed bool
	// Submitted reports that the caller's submit key produced a submit.
	Submitted bool
	// FocusLost reports that the field had focus last frame and none now.
	FocusLost bool
}

// fieldErrorState is the §6.8 visibility rule of the error line, shared by
// the variants: an error shows only after the field was left or a submit
// happened, then stays and updates while editing; an untouched empty field
// shows none.
type fieldErrorState struct {
	// left and submitted remember that the field lost focus or submitted
	// once; shown keeps the error visible from then on while it applies.
	left, submitted, shown bool
}

// visible advances the rule with the frame's events. errMsg is the
// caller's message, bad any state the variant adds (an out-of-range
// number); the error visuals hold while either applies after the gate.
func (e *fieldErrorState) visible(errMsg string, bad bool, ev FieldEvents) bool {
	if ev.FocusLost {
		e.left = true
	}
	if ev.Submitted {
		e.submitted = true
	}
	e.shown = (e.left || e.submitted || e.shown) && (errMsg != "" || bad)
	return e.shown
}

// fieldOpts are the core knobs a variant sets per frame.
type fieldOpts struct {
	// masked collapses the selection every frame (§6.8): a masked secret
	// field offers nothing to copy or cut — the editor ignores Cmd+C/X
	// when nothing is selected — so the clipboard write command is never
	// issued, however the selection was made (Cmd+A, a double-click, a
	// Shift+arrow drag).
	masked bool
	// submit enables the editor's Enter→SubmitEvent translation; the
	// caller owns the submit key (§6.9).
	submit bool
	// escapeClears is the search variant's Escape-clears rule (§6.8).
	escapeClears bool
}

// fieldCore is the long-lived state one logical field keeps (UK-02): the
// editor itself, the box's pointer tracking, and the §3.7 followers of the
// chrome. Embed it by value in the variant's state struct; the editor and
// boxTag are input tags, so the field must not move in memory.
type fieldCore struct {
	ed widget.Editor
	// boxTag backs the pointer area of the whole box: a click on the
	// padding focuses (the editor only covers its own text runs), and the
	// fill lifts while the pointer is anywhere over the box.
	boxTag struct{}
	// hoverF follows the pointer over the box and focusF the keyboard
	// focus; both lift the fill to FillSoftHover (§6.8) on Snappy.
	hoverF, focusF motion.Follower
	// swapF crossfades helper into error text (§6.8) on Snappy; lineF
	// opens the below-line's height on Smooth.
	swapF, lineF motion.Follower
	// clearF fades the search clear button in when there is text;
	// revealF crossfades the secret eye (§6.8) — both on Snappy.
	clearF, revealF motion.Follower
	// hover is the live pointer state from the box's Enter/Leave events.
	hover bool
	// hadFocus is last frame's focus, for the FocusLost edge (UK-06).
	hadFocus bool
	// maskedCaret is where the caret of a masked field last rested while
	// its selection was collapsed; a forced collapse restores it.
	maskedCaret int
}

// Tag is the field's input tag — its editor — for focus scripting and
// gallery previews.
func (f *fieldCore) Tag() event.Tag { return &f.ed }

// newFieldCore builds the core of a field variant: singleLine for the
// one-line fields (Enter becomes the submit the caller asked for), flowing
// for TextArea. Every follower gets its spring — a zero follower has none.
func newFieldCore(singleLine bool) fieldCore {
	s := motion.Snappy()
	f := fieldCore{
		hoverF:  motion.NewFollower(s),
		focusF:  motion.NewFollower(s),
		swapF:   motion.NewFollower(s),
		clearF:  motion.NewFollower(s),
		revealF: motion.NewFollower(s),
		lineF:   motion.NewFollower(motion.Smooth()),
	}
	f.ed.SingleLine = singleLine
	return f
}

// text returns the field's value.
func (f *fieldCore) text() string { return f.ed.Text() }

// setText replaces the value: the search clear, a caller's reset.
func (f *fieldCore) setText(s string) { f.ed.SetText(s) }

// caretToEnd moves the caret after the value.
func (f *fieldCore) caretToEnd() {
	f.ed.MoveCaret(f.ed.Len(), f.ed.Len())
}

// begin runs the input half of one frame at the field's box: it registers
// the box area (hover, click-to-focus on the padding), drains the editor's
// events — collapsing a masked selection between events, so a Cmd+A that
// lands in the same frame as a Cmd+C still finds nothing to copy —,
// handles the search Escape and tracks the focus edge. A disabled field
// consumes nothing (§4.1).
func (f *fieldCore) begin(gtx *layout.Context, box image.Rectangle, o fieldOpts, disabled bool) FieldEvents {
	var ev FieldEvents
	f.trackBox(gtx, box, disabled)
	if disabled {
		return ev
	}
	if o.masked {
		f.ed.InputHint = key.HintPassword
		f.drainUndo(gtx)
	} else {
		f.ed.InputHint = key.HintAny
	}
	f.ed.Submit = o.submit
	if o.escapeClears && f.escape(gtx) {
		ev.Changed = true
	}
	// With the submit key on, the composer's rule (§6.9) is Enter submits
	// and Shift+Enter inserts the newline: the editor reports both as a
	// SubmitEvent, so the core reads the raw Return presses first, inserts
	// the newline itself where Shift was held, and lets only the unshifted
	// submits through. On macOS (Gio v0.10.3 os_macos.go) the platform IME
	// consumes Return while marked text is active and delivers no key
	// event, so a composition never submits.
	newlines := 0
	if o.submit {
		newlines = f.returnNewlines(gtx)
		ev.Changed = ev.Changed || newlines > 0
	}
	if f.drainEditor(gtx, o, &ev) > newlines {
		ev.Submitted = true
	}
	if o.masked {
		f.collapseMasked()
	}
	if f.hadFocus && !gtx.Focused(&f.ed) {
		ev.FocusLost = true
	}
	f.hadFocus = gtx.Focused(&f.ed)
	return ev
}

func (f *fieldCore) drainUndo(gtx *layout.Context) {
	for {
		if _, ok := gtx.Event(
			key.Filter{Focus: &f.ed, Required: key.ModShortcut, Optional: key.ModShift, Name: key.Name("Z")},
			key.Filter{Focus: &f.ed, Required: key.ModCtrl, Optional: key.ModShift, Name: key.Name("Z")},
		); !ok {
			return
		}
	}
}

// drainEditor applies the frame's editor events to ev and returns how many
// submits fired, the number a submit key with Shift inserts against: the
// masked collapse runs between events, so a Cmd+A that lands in the same
// frame as a Cmd+C still finds nothing to copy.
func (f *fieldCore) drainEditor(gtx *layout.Context, o fieldOpts, ev *FieldEvents) int {
	submits := 0
	for {
		e, ok := f.ed.Update(*gtx)
		if !ok {
			return submits
		}
		switch e.(type) {
		case widget.ChangeEvent:
			ev.Changed = true
		case widget.SubmitEvent:
			submits++
		}
		if o.masked {
			f.collapseMasked()
		}
	}
}

// returnNewlines applies the composer's Shift+Enter rule when the submit key
// is on (§6.9): every Return press with Shift inserts one newline at the
// caret, which the editor itself did not insert because its submit
// translation consumed the key, and returns how many it inserted. The caller
// compares the count against the frame's SubmitEvents to keep a shifted
// Return from submitting.
func (f *fieldCore) returnNewlines(gtx *layout.Context) int {
	newlines := 0
	for {
		e, ok := gtx.Event(
			key.Filter{Focus: &f.ed, Required: key.ModShift, Name: key.NameReturn},
			key.Filter{Focus: &f.ed, Required: key.ModShift, Name: key.NameEnter},
		)
		if !ok {
			return newlines
		}
		ke, isKey := e.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		f.ed.Insert("\n")
		newlines++
	}
}

// trackBox registers the box's pointer area and reads it: the fill lifts
// on hover and a press anywhere on the box focuses the field (the editor
// focuses its own runs itself; this covers the padding). A disabled box
// consumes nothing (§4.1). The area is a sibling of the editor's own, so
// Gio delivers the pointer to both and this hides nothing from the editor.
func (f *fieldCore) trackBox(gtx *layout.Context, box image.Rectangle, disabled bool) {
	ig := *gtx
	if disabled {
		ig = gtx.Disabled()
	}
	defer clip.Rect(box).Push(ig.Ops).Pop()
	event.Op(ig.Ops, &f.boxTag)
	for {
		e, ok := ig.Event(pointer.Filter{
			Target: &f.boxTag,
			Kinds:  pointer.Enter | pointer.Leave | pointer.Press,
		})
		if !ok {
			break
		}
		pe, isPointer := e.(pointer.Event)
		if !isPointer {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			f.hover = true
		case pointer.Leave:
			f.hover = false
		case pointer.Press:
			ig.Execute(key.FocusCmd{Tag: &f.ed})
		case pointer.Cancel, pointer.Release, pointer.Move, pointer.Drag, pointer.Scroll:
			// The box tracks hover and press-to-focus only.
		}
	}
}

// escape applies the search variant's Escape rule (§6.8) and reports
// whether the value changed: a non-empty field clears, an empty one is
// left alone (§7 keeps Escape for the overlay above it).
func (f *fieldCore) escape(gtx *layout.Context) bool {
	for {
		e, ok := gtx.Event(key.Filter{Focus: &f.ed, Name: key.NameEscape})
		if !ok {
			return false
		}
		ke, isKey := e.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		if f.ed.Text() == "" {
			continue
		}
		f.ed.SetText("")
		return true
	}
}

// collapseMasked keeps a masked field's selection empty (§6.8): wherever a
// selection came from — Cmd+A, a double-click, a Shift+arrow drag — the
// caret returns to where it rested, so the editor's copy and cut find
// nothing selected and never issue the clipboard write.
func (f *fieldCore) collapseMasked() {
	start, end := f.ed.Selection()
	if start == end {
		f.maskedCaret = start
		return
	}
	f.ed.SetCaret(f.maskedCaret, f.maskedCaret)
}

// focused reports the field's §4.1 focus, real or forced by the gallery's
// preview; the ring still needs the keyboard modality (UK-05).
func (f *fieldCore) focused(env *Env, gtx *layout.Context) bool {
	return env.FocusVisible(gtx, &f.ed) || env.previewFocus(&f.ed)
}

// recordRun notes the bounds of one text run in the local frame of the
// control being drawn, when the gallery's recorder is attached (UK-E09):
// the gallery knows the control's window position and translates. Gio
// does not expose the current transform (ui-guide §5), so the kit records
// at the draw sites that know their own offsets. Production windows leave
// the recorder nil and nothing is recorded.
func recordRun(env *Env, r image.Rectangle) {
	if env.RunRecorder != nil {
		env.RunRecorder(r)
	}
}

// hovered reports the field's §4.1 hover, real or forced.
func (f *fieldCore) hovered(env *Env) bool {
	return f.hover || env.previewHover(&f.ed)
}

// fieldMotion holds the animated values of one frame.
type fieldMotion struct {
	// lift is the combined hover/focus fill weight (0..1); the focus ring
	// fades in with focus alone.
	hover, focus, lift, swap float32
	// linePx is the animated height of the helper/error line.
	linePx float32
}

// advance moves the chrome's followers toward the frame's targets and asks
// for the next frame while any of them moves (the kit's frame discipline):
// the fill lift, the ring, and the crossfades on Snappy, the below-line
// height on Smooth. Reduced motion snaps everything (UK-04).
func (f *fieldCore) advance(env *Env, hovered, focused, hasError bool, linePx float32) fieldMotion {
	fm := fieldMotion{
		hover: followState(env, &f.hoverF, hovered),
		focus: followState(env, &f.focusF, focused),
		swap:  followState(env, &f.swapF, hasError),
	}
	fm.lift = max(fm.hover, fm.focus)
	fm.linePx = followSmooth(env, &f.lineF, linePx)
	return fm
}

// fieldLook resolves the field's colors (UK-03): the FillSoft box that
// lifts to FillSoftHover under the pointer or the caret, the danger border
// when the §4.1 error state holds, and the variant-tinted ring (§3.8:
// fields take FocusBorder). Disabled draws the whole look at half opacity.
func fieldLookOf(env *Env, disabled, errored bool) controlLook {
	c := env.Th.Chrome()
	l := controlLook{base: c.FillSoft, hover: c.FillSoftHover, fg: c.Fg, ring: c.FocusBorder}
	if errored {
		l.border = env.Th.Status().Danger
	}
	if disabled {
		l = l.dim(theme.DisabledOpacity)
	}
	return l
}

// paintBox draws the field's body: the fill layers — the rest tint replaced
// by the lift, never stacked on it (the F10.9 layer rule, a contrast
// decision in linear light) — the hairline danger border when the field is
// in error, and the focus ring fading in last. A field never shrinks on
// press and never ripples: §3.7 reserves those for buttons.
func paintBox(gtx *layout.Context, r image.Rectangle, look controlLook, fm fieldMotion) {
	radius := gtx.Dp(theme.RadiusMD)
	fillLayer(gtx, r, radius, look.base, 1-fm.lift)
	fillLayer(gtx, r, radius, look.hover, fm.lift)
	if look.border.A != 0 {
		strokeRRect(gtx.Ops, r, radius, float32(gtx.Dp(theme.Hairline)), look.border)
	}
	focusRing(gtx, r, radius, look.ring, fm.focus)
}

// followSmooth advances a px height toward px on its Smooth spring,
// asking for the next frame while it moves. Reduced motion snaps it.
func followSmooth(env *Env, f *motion.Follower, px float32) float32 {
	if env.ReducedMotion {
		f.Snap(px)
		return px
	}
	v := f.Follow(px, env.DT())
	if f.Moving() {
		env.Keep()
	}
	return v
}
