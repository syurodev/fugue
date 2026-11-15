package kit

import (
	"image"
	"math"
	"testing"

	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The §3.7 motion evidence of the fields (UK-E11) and the TextArea's
// §6.9 grow-then-scroll. Like the button render tests they draw into the
// shared headless window and read pixels; the followers themselves are
// read for the settle/retarget contract, as in
// TestButtonMotionSettlesAndRetargets.

// fieldMotionFrame lays one frame of a field with the green background the
// render tests paint behind it, and reports whether the driver still asks
// for frames.
func fieldMotionFrame(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) bool {
	t.Helper()
	h.frame(t)
	paint.Fill(h.gtx.Ops, shotBG())
	if _, err := st.Layout(h.env, &h.gtx, p); err != nil {
		t.Fatalf("lay the field out: %v", err)
	}
	_, moving := h.env.Driver.Next()
	h.end()
	return moving
}

// settleField runs frames until the field stops asking for them, then
// demands two more quiet ones (UK-E11).
func settleField(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	for i := 0; fieldMotionFrame(t, h, st, p); i++ {
		if i == 240 {
			t.Fatal("the field never stopped asking for frames")
		}
	}
	for range 2 {
		if fieldMotionFrame(t, h, st, p) {
			t.Fatal("a settled field asked for another frame")
		}
	}
}

// fieldMetricsForTest burns one frame and returns the px height of the
// §3.3 small line (an open error line), the body line the editor draws
// with, and the box's text padding.
func fieldMetricsForTest(t *testing.T, h *harness) (linePx, bodyLine, boxPadY int) {
	t.Helper()
	h.frame(t)
	linePx = labelLine(h.env, &h.gtx, theme.TypeSmall())
	bodyLine = labelLine(h.env, &h.gtx, editorType())
	sz := fieldMetrics(h.env, &h.gtx, "", false)
	boxPadY = sz.boxPadY
	h.end()
	return linePx, bodyLine, boxPadY
}

// TestFieldMotionSettlesAndRetargets covers UK-E11 for the field chrome
// (§3.7, §6.8): the fill lift, the ring, the helper/error crossfade with
// its Smooth line and the clear fade — every one reaches its target and
// then stops asking for frames; a target changed mid-flight continues
// from the value the motion has, without a jump; and reduced motion snaps.
func TestFieldMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	line, _, _ := fieldMetricsForTest(t, h)
	st := NewTextFieldState()
	base := &TextFieldProps{Label: fieldLabel, Helper: "the host to reach"}

	settleField(t, h, st, base)
	if v := st.core.hoverF.Value(); v != 0 {
		t.Fatalf("a resting field holds lift %v", v)
	}
	motionFillLiftRetargets(t, h, st, base)
	motionErrorLineOpensAndCloses(t, h, line)
	motionClearFades(t, h)
	motionReducedSnaps(t, h, st, base)
}

// motionFillLiftRetargets drives the hover lift mid-flight, retargets it
// off and asserts the motion continues from where it was, then settles at
// both ends.
func motionFillLiftRetargets(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	for range 5 {
		h.env.Preview = Preview{Hover: st.Tag()}
		fieldMotionFrame(t, h, st, p)
	}
	mid := st.core.hoverF.Value()
	if mid < 0.05 || mid > 0.95 {
		t.Fatalf("hover is at %v five frames in, want it mid-flight", mid)
	}
	h.env.Preview = Preview{}
	fieldMotionFrame(t, h, st, p)
	if next := st.core.hoverF.Value(); math.Abs(float64(next-mid)) > 0.25 {
		t.Fatalf("hover jumped from %v to %v when its target changed", mid, next)
	}
	h.env.Preview = Preview{Hover: st.Tag()}
	settleField(t, h, st, p)
	if v := st.core.hoverF.Value(); v != 1 {
		t.Fatalf("hover settled at %v, want 1", v)
	}
	h.env.Preview = Preview{}
	settleField(t, h, st, p)
}

// motionErrorLineOpensAndCloses leaves a helperless field in error and
// asserts the line opens on Smooth and closes from its current height
// when the error clears.
func motionErrorLineOpensAndCloses(t *testing.T, h *harness, line int) {
	t.Helper()
	bare := NewTextFieldState()
	pb := &TextFieldProps{Label: fieldLabel, Error: "enter a host name"}
	frameTwo(t, h, bare, nil, pb, pb, bare.Tag())
	other := NewTextFieldState()
	frameTwo(t, h, bare, other, pb, &TextFieldProps{Label: "Port"}, other.Tag())
	settleField(t, h, bare, pb)
	if v := bare.core.swapF.Value(); v != 1 {
		t.Fatalf("the error crossfade settled at %v, want 1", v)
	}
	if v := bare.core.lineF.Value(); math.Abs(float64(v-float32(line))) > 1 {
		t.Fatalf("the error line settled at %v px, want %d", v, line)
	}
	pb.Error = ""
	for range 3 {
		fieldMotionFrame(t, h, bare, pb)
	}
	closing := bare.core.lineF.Value()
	if closing <= 0 || closing >= float32(line) {
		t.Fatalf("the closing line is at %v px three frames in, want it mid-flight", closing)
	}
	settleField(t, h, bare, pb)
	if v := bare.core.lineF.Value(); v != 0 {
		t.Fatalf("the closed line settled at %v px, want 0", v)
	}
}

// motionClearFades types into a search field and asserts the clear button
// settles shown, then hides when the text clears.
func motionClearFades(t *testing.T, h *harness) {
	t.Helper()
	s := NewTextFieldState()
	sp := &TextFieldProps{Variant: TextFieldSearch, Placeholder: "search"}
	frameTwo(t, h, s, nil, sp, sp, s.Tag())
	typeInto(t, h, s, sp, "q")
	settleField(t, h, s, sp)
	if v := s.core.clearF.Value(); v != 1 {
		t.Fatalf("the clear button settled at %v, want 1 with text", v)
	}
	s.core.setText("")
	settleField(t, h, s, sp)
	if v := s.core.clearF.Value(); v != 0 {
		t.Fatalf("the clear button settled at %v, want 0 without text", v)
	}
}

// motionReducedSnaps asserts reduced motion reaches the target in one
// frame.
func motionReducedSnaps(t *testing.T, h *harness, st *TextFieldState, p *TextFieldProps) {
	t.Helper()
	h.env.ReducedMotion = true
	h.env.Preview = Preview{Hover: st.Tag()}
	fieldMotionFrame(t, h, st, p)
	if v := st.core.hoverF.Value(); v != 1 {
		t.Fatalf("reduced motion left hover at %v after one frame, want 1", v)
	}
	h.env.ReducedMotion = false
	h.env.Preview = Preview{}
}

// TestTextAreaGrowsToItsMaxThenScrolls covers §6.9: the box grows with its
// content to MaxLines lines and then scrolls inside — the box stays at the
// cap while the viewport moves the content that is shown within it.
func TestTextAreaGrowsToItsMaxThenScrolls(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, bodyLine, padY := fieldMetricsForTest(t, h)
	st := NewTextAreaState()
	p := &TextAreaProps{MaxLines: 3, Placeholder: "write"}

	// One line: the box holds the content, no more.
	st.SetText("l1")
	areaSettle(t, h, st, p)
	one := st.boxF.Value()
	if want := float32(2*padY + bodyLine); math.Abs(float64(one-want)) > 1.5 {
		t.Fatalf("a one-line area is %v px tall, want %v", one, want)
	}

	// Five lines: the box stops at the cap; the pixels show three lines of
	// ink, not five.
	st.SetText("l1\nl2\nl3\nl4\nl5")
	areaSettle(t, h, st, p)
	cap := float32(2*padY + 3*bodyLine)
	if math.Abs(float64(st.boxF.Value()-cap)) > 1.5 {
		t.Fatalf("a five-line area is %v px tall, want the cap %v", st.boxF.Value(), cap)
	}
	before := areaShot(t, h, st, p)
	if ink := scanInkRows(before, 2); ink > 3*bodyLine+12 {
		t.Fatalf("%d ink rows inside the capped box, want about three lines", ink)
	}

	// Scrolling the viewport moves the content inside the unchanged box:
	// one line further down, same box height.
	st.list.Position.Offset = bodyLine
	after := areaShot(t, h, st, p)
	if !imagesDiffer(before, after) {
		t.Fatal("scrolling the viewport left the visible text unchanged")
	}
	if ink := scanInkRows(after, 2); ink > 3*bodyLine+12 {
		t.Fatalf("%d ink rows after the scroll, want the box still capped", ink)
	}
	if math.Abs(float64(st.boxF.Value()-cap)) > 1.5 {
		t.Fatalf("the box changed to %v px while scrolling, want the cap %v", st.boxF.Value(), cap)
	}
}

// areaSettle runs area frames until the box stops asking for them.
func areaSettle(t *testing.T, h *harness, st *TextAreaState, p *TextAreaProps) {
	t.Helper()
	for i := 0; areaFrame(t, h, st, p); i++ {
		if i == 240 {
			t.Fatal("the area never stopped asking for frames")
		}
	}
}

// areaFrame lays one frame of the text area out and reports whether the
// driver still asks for frames.
func areaFrame(t *testing.T, h *harness, st *TextAreaState, p *TextAreaProps) bool {
	t.Helper()
	h.frameSize(t, 200, 160)
	st.Layout(h.env, &h.gtx, p)
	_, moving := h.env.Driver.Next()
	h.end()
	return moving
}

// areaShot renders the area's frame and returns the pixels.
func areaShot(t *testing.T, h *harness, st *TextAreaState, p *TextAreaProps) *image.RGBA {
	t.Helper()
	h.frameSize(t, 200, 160)
	paint.Fill(h.gtx.Ops, shotBG())
	st.Layout(h.env, &h.gtx, p)
	img := h.shot(t, 200, 160)
	h.end()
	return img
}

// scanInkRows counts how many pixel rows below top hold ink — pixels that
// are not the green background.
func scanInkRows(img *image.RGBA, top int) int {
	rows := 0
	for y := top; y < img.Rect.Dy()-2; y++ {
		for x := 4; x < 196; x++ {
			if !isBackground(img.RGBAAt(x, y)) {
				rows++
				break
			}
		}
	}
	return rows
}

// imagesDiffer reports whether two shots differ anywhere.
func imagesDiffer(a, b *image.RGBA) bool {
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return true
		}
	}
	return false
}
