package kit

import (
	"image"
	"image/color"
	"math"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// The render tests of Button's §3.7 motion and its busy swap: they draw
// into the shared headless window of size shotW×shotH and read pixels, so
// they assert what the screen shows rather than the ops that were issued.

// The render window of the Button tests.
const (
	shotW = 256
	shotH = 128
)

// The color the render tests paint behind the control. Green is nothing a
// theme token ever is, so any pixel that is not green is the control; ink
// is "dark", which in the dark theme is ActionFg — the label and spinner on
// a Primary button's light fill.
func shotBG() color.NRGBA { return color.NRGBA{G: 0xff, A: 0xff} }

func isInk(c color.RGBA) bool { return int(c.R)+int(c.G)+int(c.B) < 200 }

// isBackground tolerates the one-level rounding of the sRGB framebuffer.
func isBackground(c color.RGBA) bool { return c.R <= 6 && c.G >= 249 && c.B <= 6 }
func isBody(c color.RGBA) bool       { return !isBackground(c) }

func pixel(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }

// buttonFrame lays one frame with the green background and the button at
// the origin, renders it and returns the pixels.
func buttonFrame(t *testing.T, h *harness, st *ButtonState, p ButtonProps) *image.RGBA {
	t.Helper()
	h.frame(t)
	paint.Fill(h.gtx.Ops, shotBG())
	if _, err := st.Layout(h.env, &h.gtx, p); err != nil {
		t.Fatalf("lay the button out: %v", err)
	}
	img := h.shot(t, shotW, shotH)
	h.end()
	return img
}

// scan returns the bounds of the pixels of img inside area that match f,
// and their centroid; an empty bounds when none match.
func scan(img *image.RGBA, area image.Rectangle, f func(color.RGBA) bool) (bounds image.Rectangle, cx, cy float64, n int) {
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if !f(pixel(img, x, y)) {
				continue
			}
			p := image.Rect(x, y, x+1, y+1)
			bounds = bounds.Union(p)
			cx, cy, n = cx+float64(x)+0.5, cy+float64(y)+0.5, n+1
		}
	}
	if n > 0 {
		cx, cy = cx/float64(n), cy/float64(n)
	}
	return bounds, cx, cy, n
}

// bodyBounds renders a settled frame of the button and returns the bounds
// of its body (everything that is not the green background).
func bodyBounds(t *testing.T, h *harness, st *ButtonState, p ButtonProps, settle bool) image.Rectangle {
	t.Helper()
	var img *image.RGBA
	for range settleFrames(settle) {
		img = buttonFrame(t, h, st, p)
	}
	b, _, _, _ := scan(img, img.Rect, isBody)
	return b
}

// settleFrames is how many frames a settled render takes: a second at 60 Hz
// lets every Snappy follower land.
func settleFrames(settle bool) []struct{} {
	if settle {
		return make([]struct{}, 60)
	}
	return make([]struct{}, 1)
}

// TestButtonBusySpinnerSitsInItsSlot covers A1 of the F10.9 review and
// §6.2: the busy spinner is centered in the icon slot when the button has
// an icon, otherwise centered in the idle box — never at the box's corner —
// at pixel scale 1 and 1.25. The ring is an arc, so the check is its ink:
// inside the slot, with the centroid near its middle.
func TestButtonBusySpinnerSitsInItsSlot(t *testing.T) {
	t.Parallel()
	for _, px := range []float32{1, 1.25} {
		for _, tc := range []struct {
			name string
			p    ButtonProps
		}{
			{"with icon", ButtonProps{Text: "Run", Icon: IconRestart, Variant: Primary, Busy: true}},
			{"no icon", ButtonProps{Text: "Run", Variant: Primary, Busy: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				checkSpinnerSlot(t, px, tc.p)
			})
		}
	}
}

// checkSpinnerSlot settles a busy button at pixel scale px and asserts the
// spinner's ink sits in, and is centered on, the slot the spec names.
func checkSpinnerSlot(t *testing.T, px float32, p ButtonProps) {
	t.Helper()
	h := newHarness(t)
	h.px = px
	st := NewButtonState()
	var img *image.RGBA
	for range 60 {
		img = buttonFrame(t, h, st, p)
	}
	w, hgt, cw := buttonBox(h.env, &h.gtx, p)
	d := h.gtx.Dp(buttonIconSize(p.Size))
	area := image.Rect(0, 0, w, hgt)
	x := (w - d) / 2 // no icon: centered in the box
	if p.Icon != "" {
		x = (w - cw) / 2
		// The label sits right of the slot; look only left of it.
		area.Max.X = x + d + h.gtx.Dp(buttonGap(p.Size))/2
	}
	slot := image.Rect(x, (hgt-d)/2, x+d, (hgt-d)/2+d)
	b, cx, cy, n := scan(img, area, isInk)
	if n < 20 {
		t.Fatalf("px %v: %d ink pixels in %v, the spinner is missing", px, n, area)
	}
	if !b.In(slot.Inset(-1)) {
		t.Errorf("px %v: spinner ink %v leaves its slot %v", px, b, slot)
	}
	mid := f32.Pt(float32(slot.Min.X+slot.Max.X)/2, float32(slot.Min.Y+slot.Max.Y)/2)
	if off := math.Hypot(cx-float64(mid.X), cy-float64(mid.Y)); off > float64(d)/4 {
		t.Errorf("px %v: spinner centroid (%.1f,%.1f) is %.1f px from the slot's middle %v", px, cx, cy, off, mid)
	}
}

// buttonGap is the px gap between a button's icon and its label at the
// harness metric.
func buttonGap(s ButtonSize) unit.Dp { _, g := buttonMetrics(s); return g }

// TestButtonPressScalesTheDrawingNotTheHitArea covers §3.7 and UK-E11: a
// held button draws at motion.press of its size — the body's pixel bounds
// shrink — while its event area stays the idle box: a click just inside
// the idle edge, outside the shrunk drawing, still activates it.
func TestButtonPressScalesTheDrawingNotTheHitArea(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.UIScale = 4 // a body of 100+ px, so 3% is measurable
	st := NewButtonState()
	p := ButtonProps{Text: labelSave, Variant: Primary}

	rest := bodyBounds(t, h, st, p, true)
	h.env.Preview = Preview{Press: st.Tag()}
	pressed := bodyBounds(t, h, st, p, true)
	if rest.Dx()-pressed.Dx() < 4 || rest.Dy()-pressed.Dy() < 2 {
		t.Fatalf("the pressed body %v did not shrink from the idle one %v", pressed, rest)
	}
	if rest.Dx()-pressed.Dx() > 12 {
		t.Fatalf("the pressed body %v shrank far past motion.press from %v", pressed, rest)
	}

	edge := f32.Pt(float32(rest.Max.X)-0.5, float32(rest.Dy())/2)
	if pressed.Max.X >= int(edge.X) {
		t.Fatalf("the probe %v is not outside the pressed drawing %v", edge, pressed)
	}
	h.click(edge.X, edge.Y)
	if !clickFrame(t, h, st, p) {
		t.Fatalf("a click at %v, inside the idle box %v but outside the pressed drawing, did not activate the button", edge, rest)
	}
}

// TestButtonRippleStaysInsideItsRoundedBody covers §3.7 and UK-E11: a ripple
// spreading from a press in the very corner of the box paints the body and
// never the pixels outside its rounded corners — those keep the background —
// while the same press with reduced motion paints no ripple at all.
func TestButtonRippleStaysInsideItsRoundedBody(t *testing.T) {
	t.Parallel()
	p := ButtonProps{Text: "OK", Variant: Primary}
	press := func(reduced bool) (*image.RGBA, image.Rectangle) {
		h := newHarness(t)
		h.env.ReducedMotion = reduced
		st := NewButtonState()
		buttonFrame(t, h, st, p)
		h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(2, 2), Buttons: pointer.ButtonPrimary})
		var img *image.RGBA
		for range 6 {
			img = buttonFrame(t, h, st, p)
		}
		w, hgt, _ := buttonBox(h.env, &h.gtx, p)
		return img, image.Rect(0, 0, w, hgt)
	}
	with, body := press(false)
	without, _ := press(true)

	// The ripple is on the pixels next to the press point, inside the body.
	probe := image.Pt(12, 12)
	if pixel(with, probe.X, probe.Y) == pixel(without, probe.X, probe.Y) {
		t.Fatalf("no ripple at %v: %v with motion, %v reduced", probe, pixel(with, probe.X, probe.Y), pixel(without, probe.X, probe.Y))
	}
	// The pixels in the four corners, outside the rounded body, are background.
	for _, c := range []image.Point{{0, 0}, {1, 1}, {body.Max.X - 1, 0}, {0, body.Max.Y - 1}, {body.Max.X - 1, body.Max.Y - 1}} {
		if got := pixel(with, c.X, c.Y); !isBackground(got) {
			t.Errorf("the ripple painted the pixel %v outside the rounded body: %v", c, got)
		}
	}
}

// hoverRig drives one Secondary button through hover-on and hover-off
// frames and reports whether a frame asked for another.
type hoverRig struct {
	t  *testing.T
	h  *harness
	st *ButtonState
	p  ButtonProps
}

// frame lays one frame out with hover forced on or off and returns whether
// the frame asked for the next one.
func (r *hoverRig) frame(hover bool) bool {
	r.t.Helper()
	r.h.env.Preview = Preview{}
	if hover {
		r.h.env.Preview.Hover = r.st.Tag()
	}
	r.h.frame(r.t)
	if _, err := r.st.Layout(r.h.env, &r.h.gtx, r.p); err != nil {
		r.t.Fatalf("lay the button out: %v", err)
	}
	_, moving := r.h.env.Driver.Next()
	r.h.end()
	return moving
}

// settle runs frames until the button stops asking for them, then demands
// two more quiet ones (UK-E11).
func (r *hoverRig) settle(hover bool) {
	r.t.Helper()
	for i := 0; r.frame(hover); i++ {
		if i == 240 {
			r.t.Fatal("the button never stopped asking for frames")
		}
	}
	for range 2 {
		if r.frame(hover) {
			r.t.Fatal("a settled button asked for another frame")
		}
	}
}

// TestButtonMotionSettlesAndRetargets covers UK-E11: every motion of a
// button reaches its target and then stops asking for frames — two quiet
// frames in a row — and a target changed mid-flight continues from the
// value the motion has, without a jump.
func TestButtonMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewButtonState()
	r := &hoverRig{t: t, h: h, st: st, p: ButtonProps{Text: labelSave, Variant: Secondary}}
	r.frame(false)
	r.settle(false)
	for range 5 {
		r.frame(true)
	}
	mid := st.hoverF.Value()
	if mid < 0.05 || mid > 0.95 {
		t.Fatalf("hover is at %v five frames in, want it mid-flight", mid)
	}
	r.frame(false)
	if next := st.hoverF.Value(); math.Abs(float64(next-mid)) > 0.25 {
		t.Fatalf("hover jumped from %v to %v when its target changed", mid, next)
	}
	for _, want := range []struct {
		hover bool
		value float32
	}{{false, 0}, {true, 1}} {
		r.settle(want.hover)
		if v := st.hoverF.Value(); v != want.value {
			t.Fatalf("hover settled at %v, want %v", v, want.value)
		}
	}
}
