package kit

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/text"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The evidence tests of TextField (§6.8): the IME commit order (UK-E06),
// the search Escape, the masked secret, the error rule and the number
// rule. They drive the real input path — the router, the editor and the
// IME event pairs — and read the field's own state where pixels cannot
// speak, the way the button tests read follower values.

// fieldLabel is the label the field tests carry.
const fieldLabel = "Host"

// fieldFrame lays one frame of a field out in the full harness box and
// returns the events the frame reported.
func fieldFrame(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) FieldEvents {
	t.Helper()
	h.frame(t)
	ev, err := st.Layout(h.env, &h.gtx, p)
	if err != nil {
		t.Fatalf("lay the field out: %v", err)
	}
	h.end()
	return ev
}

// frameTwo lays one or two fields out in one frame — so focus can move
// between real, registered handlers, and a focus command issued in the
// frame is not dropped — and optionally runs a focus command.
func frameTwo(t *testing.T, h *harness, a, b *TextFieldState, pa, pb *TextFieldProps, focus event.Tag) {
	t.Helper()
	h.frame(t)
	if _, err := a.Layout(h.env, &h.gtx, pa); err != nil {
		t.Fatalf("lay the first field out: %v", err)
	}
	if b != nil {
		if _, err := b.Layout(h.env, &h.gtx, pb); err != nil {
			t.Fatalf("lay the second field out: %v", err)
		}
	}
	if focus != nil {
		h.gtx.Execute(key.FocusCmd{Tag: focus})
	}
	h.end()
}

// typeInto delivers s the way a desktop window delivers typed text — as
// an EditEvent over the caret's range — and runs one frame over it.
func typeInto(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps, s string) FieldEvents {
	t.Helper()
	start, end := st.core.ed.Selection()
	h.router.Queue(key.EditEvent{Text: s, Range: key.Range{Start: start, End: end}})
	return fieldFrame(t, h, st, p)
}

// TestTextFieldTelexCommitThenSelection covers UK-E06: the real IME order
// of §6.8 — after a commit the caret sits at the start of the committed
// range, and the IME then sends a SelectionEvent to place it. "aa" commit
// to "â", "vieets" plus space commit to "viết", and one Backspace after
// that deletes exactly one character.
func TestTextFieldTelexCommitThenSelection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextFieldState()
	p := &TextFieldProps{Variant: TextFieldText, Placeholder: "nhập chữ"}

	telexFocus(t, h, st, p)
	telexCommitCircumflex(t, h, st, p)
	telexCommitWord(t, h, st, p)
	telexBackspaceDeletesOne(t, h, st, p)
}

// telexFocus focuses the field for the IME steps.
func telexFocus(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	fieldFrame(t, h, st, p)
	frameTwo(t, h, st, nil, p, p, st.Tag())
	if ev := fieldFrame(t, h, st, p); ev.FocusLost {
		t.Fatal("the focused field reported FocusLost")
	}
}

// telexCommitCircumflex types "a" and commits "â" over the composed
// range; the caret sits at the range start until the SelectionEvent
// places it after the letter.
func telexCommitCircumflex(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	if ev := typeInto(t, h, st, p, "a"); !ev.Changed {
		t.Fatal("typing did not report Changed")
	}
	if got := st.Text(); got != "a" {
		t.Fatalf("after the first a the field holds %q", got)
	}
	h.router.Queue(key.EditEvent{Text: "â", Range: key.Range{Start: 0, End: 1}})
	fieldFrame(t, h, st, p)
	if got := st.Text(); got != "â" {
		t.Fatalf("after the commit the field holds %q, want â", got)
	}
	if start, end := st.core.ed.Selection(); start != 0 || end != 0 {
		t.Fatalf("after the commit the caret sits at %d..%d, want 0..0", start, end)
	}
	h.router.Queue(key.SelectionEvent{Start: 1, End: 1})
	fieldFrame(t, h, st, p)
	if start, end := st.core.ed.Selection(); start != 1 || end != 1 {
		t.Fatalf("after the SelectionEvent the caret sits at %d..%d, want 1..1", start, end)
	}
}

// telexCommitWord types the raw letters of "vieets" and commits "viết"
// over the composed range; the trailing s is a tone key the IME holds, so
// the raw text stops at vieet and the commit replaces all of it.
func telexCommitWord(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	if ev := typeInto(t, h, st, p, "vieet"); !ev.Changed {
		t.Fatal("mid-composition typing did not report Changed")
	}
	if got := st.Text(); got != "âvieet" {
		t.Fatalf("mid-composition the field holds %q, want âvieet", got)
	}
	h.router.Queue(key.EditEvent{Text: "viết", Range: key.Range{Start: 1, End: 6}})
	fieldFrame(t, h, st, p)
	if got := st.Text(); got != "âviết" {
		t.Fatalf("after the second commit the field holds %q, want âviết", got)
	}
	h.router.Queue(key.SelectionEvent{Start: 5, End: 5})
	fieldFrame(t, h, st, p)
	if start, end := st.core.ed.Selection(); start != 5 || end != 5 {
		t.Fatalf("after the second SelectionEvent the caret sits at %d..%d, want 5..5", start, end)
	}
}

// telexBackspaceDeletesOne lands the space after the commit and then
// deletes exactly one character with one Backspace (§6.8, DA-E02).
func telexBackspaceDeletesOne(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	typeInto(t, h, st, p, " ")
	h.router.Queue(key.SelectionEvent{Start: 6, End: 6})
	fieldFrame(t, h, st, p)
	if start, end := st.core.ed.Selection(); start != 6 || end != 6 {
		t.Fatalf("after the space the caret sits at %d..%d, want 6..6", start, end)
	}
	if got := st.Text(); got != "âviết " {
		t.Fatalf("after the space the field holds %q, want âviết followed by a space", got)
	}
	h.tap(key.NameDeleteBackward, 0)
	fieldFrame(t, h, st, p)
	if got := st.Text(); got != "âviết" {
		t.Fatalf("one Backspace left %q, want âviết — it deleted more than one character", got)
	}
}

// TestSearchFieldEscapeClears covers §6.8: Escape empties the search
// variant's field and reports Changed; Escape on an empty field changes
// nothing.
func TestSearchFieldEscapeClears(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextFieldState()
	p := &TextFieldProps{Variant: TextFieldSearch, Placeholder: "search"}

	fieldFrame(t, h, st, p)
	frameTwo(t, h, st, nil, p, p, st.Tag())
	if ev := typeInto(t, h, st, p, "abc"); !ev.Changed {
		t.Fatal("typing did not report Changed")
	}
	if got := st.Text(); got != "abc" {
		t.Fatalf("the field holds %q, want abc", got)
	}

	h.tap(key.NameEscape, 0)
	if ev := fieldFrame(t, h, st, p); !ev.Changed {
		t.Fatal("Escape on a filled search field did not report Changed")
	}
	if got := st.Text(); got != "" {
		t.Fatalf("Escape left %q in the search field", got)
	}

	h.tap(key.NameEscape, 0)
	if ev := fieldFrame(t, h, st, p); ev.Changed {
		t.Fatal("Escape on an empty search field reported Changed")
	}
}

// captureLogs swaps the default logger for one that writes into the
// returned buffer, so a test can assert a value never reached it.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestSecretFieldIsMaskedAndNeverPrefilled covers §6.8: a fresh secret
// field is empty, typing fills it masked behind the reveal, and while
// masked no selection survives the frame — so copy and cut are refused
// after Cmd+A, after a double-click and after a Shift+arrow drag, and the
// clipboard write command is never issued. Across the whole interaction
// no log line contains the value.
func TestSecretFieldIsMaskedAndNeverPrefilled(t *testing.T) {
	t.Parallel()
	logs := captureLogs(t)
	h := newHarness(t)
	st := NewTextFieldState()
	p := &TextFieldProps{Variant: TextFieldSecret, Placeholder: "password"}

	secretFreshIsEmpty(t, h, st, p)
	secretTypedMasked(t, h, st, p)
	secretSelectionsCollapse(t, h, st, p)
	secretCopyCutRefused(t, h, st, p)
	secretRevealCopies(t, h, st, p)
	if out := logs.String(); strings.Contains(out, secretValue) {
		t.Fatalf("the value reached a log line: %q", out)
	}
}

// secretValue is the value the secret tests type.
const secretValue = "s3cret-value"

// secretFreshIsEmpty asserts the never-prefilled rule: a fresh state is
// empty, placeholder or not.
func secretFreshIsEmpty(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	if ev := fieldFrame(t, h, st, p); ev.Changed {
		t.Fatal("a fresh secret field reported Changed")
	}
	if got := st.Text(); got != "" {
		t.Fatalf("a fresh secret field holds %q, want empty", got)
	}
}

// secretTypedMasked focuses the field, types the value and asserts the
// mask rune is drawn while the value is intact.
func secretTypedMasked(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	frameTwo(t, h, st, nil, p, p, st.Tag())
	if st.core.ed.Mask != secretMask {
		t.Fatalf("a masked field shows mask %q, want the bullet", st.core.ed.Mask)
	}
	if ev := typeInto(t, h, st, p, secretValue); !ev.Changed {
		t.Fatal("typing into the secret field did not report Changed")
	}
	if got := st.Text(); got != secretValue {
		t.Fatalf("typing left %q in the field", got)
	}
}

// secretSelectionsCollapse walks the three selection routes — Cmd+A, a
// double-click, a Shift+arrow drag — and asserts none of them survives
// the frame.
func secretSelectionsCollapse(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	h.tap("A", key.ModShortcut)
	fieldFrame(t, h, st, p)
	assertMaskedCollapsed(t, st, "after Cmd+A")

	doubleClick(t, h, st, p)
	assertMaskedCollapsed(t, st, "after a double-click")

	h.tap(key.NameRightArrow, key.ModShift)
	fieldFrame(t, h, st, p)
	assertMaskedCollapsed(t, st, "after a Shift+arrow drag")
}

// secretCopyCutRefused asserts copy and cut write nothing to the
// clipboard and delete nothing while masked.
func secretCopyCutRefused(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	for range 3 {
		h.tap("C", key.ModShortcut)
		fieldFrame(t, h, st, p)
		if _, content, ok := h.router.WriteClipboard(); ok {
			t.Fatalf("copy while masked wrote %q to the clipboard", content)
		}
		h.tap("X", key.ModShortcut)
		fieldFrame(t, h, st, p)
		if _, content, ok := h.router.WriteClipboard(); ok {
			t.Fatalf("cut while masked wrote %q to the clipboard", content)
		}
	}
	if got := st.Text(); got != secretValue {
		t.Fatalf("the masked value changed to %q under copy and cut", got)
	}
}

// secretRevealCopies reveals the field and copies the whole value.
func secretRevealCopies(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	st.SetRevealed(true)
	fieldFrame(t, h, st, p)
	if st.core.ed.Mask != 0 {
		t.Fatal("a revealed field still draws its mask")
	}
	h.tap("A", key.ModShortcut)
	fieldFrame(t, h, st, p)
	h.tap("C", key.ModShortcut)
	fieldFrame(t, h, st, p)
	if _, content, ok := h.router.WriteClipboard(); !ok || string(content) != secretValue {
		t.Fatalf("copy after reveal wrote %q (ok %v), want the value", content, ok)
	}
}

// assertMaskedCollapsed asserts the masked field's selection is empty and
// its value intact at the point a copy would run.
func assertMaskedCollapsed(t *testing.T, st *TextFieldState, when string) {
	t.Helper()
	start, end := st.core.ed.Selection()
	if start != end {
		t.Fatalf("%s the masked selection spans %d..%d, want collapsed", when, start, end)
	}
	if got := st.Text(); got != secretValue {
		t.Fatalf("%s the field holds %q", when, got)
	}
}

// doubleClick queues two clicks at the field's text, close enough in time
// to count as a double-click word selection.
func doubleClick(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	h.click(10, 10)
	fieldFrame(t, h, st, p)
	h.click(10, 10)
	fieldFrame(t, h, st, p)
}

// TestFieldErrorShowsOnlyAfterLeaving covers §6.8: an untouched field with
// an error shows none; the error appears when the field is left or a
// submit happens, then stays and updates while editing.
func TestFieldErrorShowsOnlyAfterLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	p := &TextFieldProps{Label: fieldLabel, Helper: "the host to reach", Error: "enter a host name"}
	pPort := &TextFieldProps{Label: "Port"}
	pTok := &TextFieldProps{Label: "Token", Error: "a token is required"}

	errorUntouchedIsHidden(t, h, p, pTok)
	st := errorAppearsAfterLeaving(t, h, p, pPort)
	errorStaysWhileEditing(t, h, st, p)
	errorShowsOnSubmit(t, h, p)
}

// errorUntouchedIsHidden asserts the untouched field shows no error —
// with a helper the line stands with the helper text, without one it
// stays closed.
func errorUntouchedIsHidden(t *testing.T, h *harness, p, pTok *TextFieldProps) {
	t.Helper()
	st := NewTextFieldState()
	fieldFrame(t, h, st, p)
	if st.err.shown {
		t.Fatal("an untouched field showed its error")
	}
	if v := st.core.swapF.Value(); v != 0 {
		t.Fatalf("an untouched field showed %v of the error text", v)
	}
	bare := NewTextFieldState()
	fieldFrame(t, h, bare, pTok)
	if v := bare.core.lineF.Value(); v != 0 {
		t.Fatalf("an untouched helperless field opened %v of error line", v)
	}
}

// errorAppearsAfterLeaving edits a field, moves the focus away and
// asserts the error shows after the leave; it returns the field for the
// next phase.
func errorAppearsAfterLeaving(t *testing.T, h *harness, p, pPort *TextFieldProps) *TextFieldState {
	t.Helper()
	st := NewTextFieldState()
	frameTwo(t, h, st, nil, p, p, st.Tag())
	if ev := typeInto(t, h, st, p, "ju"); !ev.Changed {
		t.Fatal("typing did not report Changed")
	}
	if st.err.shown {
		t.Fatal("an error showed while the field was still focused")
	}
	other := NewTextFieldState()
	frameTwo(t, h, st, other, p, pPort, other.Tag())
	if ev := fieldFrame(t, h, st, p); !ev.FocusLost {
		t.Fatal("the field did not report FocusLost when focus moved away")
	}
	settleField(t, h, st, p)
	if !st.err.shown {
		t.Fatal("the error did not show after the field was left")
	}
	if v := st.core.swapF.Value(); v != 1 {
		t.Fatalf("the error crossfade settled at %v, want 1", v)
	}
	return st
}

// errorStaysWhileEditing edits the field again and asserts the error
// stays and updates with the message.
func errorStaysWhileEditing(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	frameTwo(t, h, st, nil, p, p, st.Tag())
	if ev := typeInto(t, h, st, p, "nk"); !ev.Changed {
		t.Fatal("typing again did not report Changed")
	}
	if !st.err.shown {
		t.Fatal("the error went away while editing after it had shown")
	}
	p.Error = "a host needs a dot"
	fieldFrame(t, h, st, p)
	if !st.err.shown {
		t.Fatal("the updated error did not show")
	}
}

// errorShowsOnSubmit marks a fresh field submitted through its own Enter
// and asserts the error shows although the field was never left.
func errorShowsOnSubmit(t *testing.T, h *harness, p *TextFieldProps) {
	t.Helper()
	fresh := NewTextFieldState()
	frameTwo(t, h, fresh, nil, p, p, fresh.Tag())
	h.tap(key.NameReturn, 0)
	fieldFrame(t, h, fresh, p)
	if !fresh.err.shown {
		t.Fatal("the error did not show on submit from an untouched field")
	}
}

// TestNumberFieldEmptyIsNotZero covers §6.8: the Number variant parses its
// text but an empty field is never 0 — not 0, not the minimum — and an
// out-of-range value feeds the error rule after the field was left.
func TestNumberFieldEmptyIsNotZero(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextFieldState()
	lo, hi := 5.0, 100.0
	p := &TextFieldProps{Variant: TextFieldNumber, Unit: "ms", Min: &lo, Max: &hi, Placeholder: "timeout"}

	// Empty: no value, not 0 and not the minimum.
	if ev := fieldFrame(t, h, st, p); ev.Changed {
		t.Fatal("an untouched number field reported Changed")
	}
	if v, ok := st.Number(); ok {
		t.Fatalf("an empty number field read %v as a value, want none", v)
	}

	frameTwo(t, h, st, nil, p, p, st.Tag())
	if ev := typeInto(t, h, st, p, "42"); !ev.Changed {
		t.Fatal("typing into the number field did not report Changed")
	}
	if v, ok := st.Number(); !ok || v != 42 {
		t.Fatalf("42 read back as (%v, %v)", v, ok)
	}
	if st.outOfRange(p) {
		t.Fatal("42 is inside 5..100")
	}

	// Leave: an in-range value is no error; clearing never becomes 0.
	other := NewTextFieldState()
	frameTwo(t, h, st, other, p, p, other.Tag())
	if st.err.shown {
		t.Fatal("an in-range value put the field in the error state")
	}
	frameTwo(t, h, st, other, p, p, st.Tag())
	h.router.Queue(key.SelectionEvent{Start: 2, End: 2})
	fieldFrame(t, h, st, p)
	h.tap(key.NameDeleteBackward, 0)
	h.tap(key.NameDeleteBackward, 0)
	fieldFrame(t, h, st, p)
	if v, ok := st.Number(); ok {
		t.Fatalf("after clearing, the number field read %v as a value, want none", v)
	}

	// Above the range, the error shows after the field was left again.
	if ev := typeInto(t, h, st, p, "200"); !ev.Changed {
		t.Fatal("typing 200 did not report Changed")
	}
	frameTwo(t, h, st, other, p, p, other.Tag())
	fieldFrame(t, h, st, p)
	if !st.err.shown || !st.outOfRange(p) {
		t.Fatalf("200 above 100 showed no error (shown %v, out of range %v)", st.err.shown, st.outOfRange(p))
	}
}

// themeModes are the two modes a kit contrast test walks.
func themeModes() []struct {
	name string
	th   func(*text.Shaper) theme.Theme
} {
	return []struct {
		name string
		th   func(*text.Shaper) theme.Theme
	}{
		{"light", theme.Light},
		{"dark", theme.Dark},
	}
}

// newModeEnv builds an env in one of themeModes' modes.
func newModeEnv(t *testing.T, th func(*text.Shaper) theme.Theme) *Env {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the shaper: %v", err)
	}
	m := th(sh)
	return &Env{Th: &m, Sh: sh}
}

// TestFieldTextContrastOnTheFills covers UK-E04 for the fields: the value
// text and the placeholder keep 4.5:1 over the FillSoft box and over its
// FillSoftHover lift, on every chrome surface, in both modes; the helper
// and the error line keep 4.5:1 on the surface they sit on; and the ring
// keeps 3:1 against the fill it rings.
func TestFieldTextContrastOnTheFills(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			env := newModeEnv(t, mode.th)
			for _, surface := range contrastSurfaces(env.Th) {
				checkFieldContrastOnSurface(t, env, surface)
			}
		})
	}
}

// checkFieldContrastOnSurface asserts the field's UK-E04 budgets on one
// chrome surface, at rest and under the hover lift.
func checkFieldContrastOnSurface(t *testing.T, env *Env, surface contrastSurface) {
	t.Helper()
	c := env.Th.Chrome()
	for _, hovered := range []bool{false, true} {
		look := fieldLookOf(env, false, false)
		bg := composited(surface.c, look, hovered, false, false)
		where := surface.name
		if hovered {
			where += " hovered"
		}
		if got := contrastRatio(c.Fg, bg); got < 4.5 {
			t.Errorf("value text on %s: contrast %.2f below 4.5", where, got)
		}
		if got := contrastRatio(c.Placeholder, bg); got < 4.5 {
			t.Errorf("placeholder on %s: contrast %.2f below 4.5", where, got)
		}
		if got := contrastRatio(look.ring, bg); got < 3 {
			t.Errorf("the focus ring on %s: contrast %.2f below 3", where, got)
		}
	}
	if got := contrastRatio(c.FgSubtle, surface.c); got < 4.5 {
		t.Errorf("helper text on %s: contrast %.2f below 4.5", surface.name, got)
	}
	if got := contrastRatio(env.Th.Status().Danger, surface.c); got < 4.5 {
		t.Errorf("error text on %s: contrast %.2f below 4.5", surface.name, got)
	}
}

// TestSecretFieldNoRevealAndNoUndo verifies vault passphrase fields cannot be
// revealed and ignore shortcut undo operations.
func TestSecretFieldNoRevealAndNoUndo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTextFieldState()
	st.SetRevealed(true)
	p := &TextFieldProps{Label: "Passphrase", Variant: TextFieldSecret, NoReveal: true}

	secretFreshIsEmpty(t, h, st, p)
	secretTypedMasked(t, h, st, p)
	if st.core.ed.Mask == 0 {
		t.Fatal("NoReveal secret field cleared mask after SetRevealed(true)")
	}
	h.tap("Z", key.ModShortcut)
	fieldFrame(t, h, st, p)
	if got := st.Text(); got != secretValue {
		t.Fatalf("undo modified masked secret field to %q, want %q", got, secretValue)
	}
}
