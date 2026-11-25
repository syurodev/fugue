package kit

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
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

// frameState lays one frame with preview p and returns whether the frame asked
// for the next one.
func (r *hoverRig) frameState(p Preview) bool {
	r.t.Helper()
	r.h.env.Preview = p
	r.h.frame(r.t)
	if _, err := r.st.Layout(r.h.env, &r.h.gtx, r.p); err != nil {
		r.t.Fatalf("lay the button out: %v", err)
	}
	_, moving := r.h.env.Driver.Next()
	r.h.end()
	return moving
}

// settleIdle runs frames at rest until the button stops asking for them,
// then demands two more quiet ones (UK-E11).
func (r *hoverRig) settleIdle() {
	r.t.Helper()
	for i := 0; r.frameState(Preview{}); i++ {
		if i == 240 {
			r.t.Fatal("the button never stopped asking for frames")
		}
	}
	for range 2 {
		if r.frameState(Preview{}) {
			r.t.Fatal("a settled button asked for another frame")
		}
	}
}

func (r *hoverRig) verifySnapInExitOut(name string, active Preview, val func() float32) {
	r.t.Helper()
	r.frameState(active)
	if v := val(); v != 1 {
		r.t.Fatalf("%s first frame = %v, want 1", name, v)
	}
	r.frameState(Preview{})
	r.frameState(Preview{})
	if mid := val(); mid <= 0.05 || mid >= 0.95 {
		r.t.Fatalf("%s leave after 2 frames = %v, want mid-fade in (0.05, 0.95)", name, mid)
	}
	for i := 0; r.frameState(Preview{}); i++ {
		if val() < 0 {
			r.t.Fatalf("%s bounced below 0: %v", name, val())
		}
		if i == 240 {
			r.t.Fatalf("%s leave never settled", name)
		}
	}
	r.settleIdle()
	if v := val(); v != 0 {
		r.t.Fatalf("%s settled at %v, want 0", name, v)
	}
}

func (r *hoverRig) verifyFocusRetarget(st *ButtonState) {
	r.t.Helper()
	r.h.env.KeyboardOnly = true
	for range 3 {
		r.frameState(Preview{Focus: st.Tag()})
	}
	midFocus := st.focusF.Value()
	if midFocus <= 0.05 || midFocus >= 0.95 {
		r.t.Fatalf("focus after 3 frames = %v, want mid-flight in (0.05, 0.95)", midFocus)
	}
	r.frameState(Preview{})
	if next := st.focusF.Value(); math.Abs(float64(next-midFocus)) > 0.25 {
		r.t.Fatalf("focus jumped from %v to %v when retargeted", midFocus, next)
	}
	r.settleIdle()
	if v := st.focusF.Value(); v != 0 {
		r.t.Fatalf("focus settled at %v, want 0", v)
	}
}

func (r *hoverRig) verifyReducedMotion(st *ButtonState) {
	r.t.Helper()
	r.h.env.ReducedMotion = true
	if r.frameState(Preview{Hover: st.Tag(), Press: st.Tag()}) {
		r.t.Fatal("ReducedMotion hover/press enter requested a frame")
	}
	if st.hoverF.Value() != 1 || st.pressF.Value() != 1 {
		r.t.Fatalf("ReducedMotion enter left hover=%v press=%v, want 1, 1", st.hoverF.Value(), st.pressF.Value())
	}
	if r.frameState(Preview{}) {
		r.t.Fatal("ReducedMotion hover/press leave requested a frame")
	}
	if st.hoverF.Value() != 0 || st.pressF.Value() != 0 {
		r.t.Fatalf("ReducedMotion leave left hover=%v press=%v, want 0, 0", st.hoverF.Value(), st.pressF.Value())
	}
}

// TestButtonMotionSettlesAndRetargets covers UK-E11 and §3.7: hover and press
// appear on the first frame and fade out on Exit over >1 frame with no bounce,
// focus follows Snappy with mid-flight retargeting without a jump, every
// motion stops asking for frames once settled (two quiet frames in a row), and
// ReducedMotion snaps in one frame.
func TestButtonMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewButtonState()
	r := &hoverRig{t: t, h: h, st: st, p: ButtonProps{Text: labelSave, Variant: Secondary}}
	r.frameState(Preview{})
	r.settleIdle()
	r.verifySnapInExitOut("hover", Preview{Hover: st.Tag()}, st.hoverF.Value)
	r.verifySnapInExitOut("press", Preview{Press: st.Tag()}, st.pressF.Value)
	r.verifyFocusRetarget(st)
	r.verifyReducedMotion(st)
}

// TestButtonLabelSharesTheRowTextCenter verifies that a Button's label ink
// shares the vertical text center of a plain kit.Label on the same
// layout.Middle row across variants (Ghost, Primary), sizes (SizeSM, SizeMD)
// and UI scales (100%, 125%).
func TestButtonLabelSharesTheRowTextCenter(t *testing.T) {
	t.Parallel()
	for _, uiScale := range []float32{1, 1.25} {
		for _, size := range []struct {
			name string
			val  ButtonSize
		}{{"SizeSM", SizeSM}, {"SizeMD", SizeMD}} {
			for _, variant := range []struct {
				name string
				val  ButtonVariant
			}{{"Ghost", Ghost}, {"Primary", Primary}} {
				t.Run(fmt.Sprintf("%s-%s-ui%d", variant.name, size.name, int(uiScale*100)), func(t *testing.T) {
					t.Parallel()
					checkRowTextCenter(t, uiScale, variant.val, size.val)
				})
			}
		}
	}
}

func checkRowTextCenter(t *testing.T, uiScale float32, variant ButtonVariant, size ButtonSize) {
	t.Helper()
	h, img, labelRect, btnRect := renderRowShot(t, uiScale, variant, size)

	flexH := max(labelRect.Dy(), btnRect.Dy())
	labelArea := labelRect.Add(image.Pt(0, (flexH-labelRect.Dy())/2))
	btnArea := image.Rect(
		btnRect.Min.X+h.gtx.Dp(theme.RadiusMD),
		(flexH-btnRect.Dy())/2+1,
		btnRect.Max.X-h.gtx.Dp(theme.RadiusMD),
		(flexH-btnRect.Dy())/2+btnRect.Dy()-1,
	)
	lblBG := pixel(img, 0, 0)
	if labelArea.Min.Y > 0 {
		lblBG = pixel(img, labelArea.Min.X, labelArea.Min.Y-1)
	}
	btnBG := pixel(img, btnArea.Min.X, btnArea.Min.Y)

	lblInk, _, _, lblN := scan(img, labelArea, func(c color.RGBA) bool { return colorDist(c, lblBG) > 30 })
	btnInk, _, _, btnN := scan(img, btnArea, func(c color.RGBA) bool { return colorDist(c, btnBG) > 30 })
	if lblN == 0 || btnN == 0 {
		t.Fatalf("missing ink: label=%d btn=%d", lblN, btnN)
	}
	lblCenter := float64(lblInk.Min.Y+lblInk.Max.Y) / 2
	btnCenter := float64(btnInk.Min.Y+btnInk.Max.Y) / 2
	if diff := math.Abs(lblCenter - btnCenter); diff > 1.0 {
		t.Errorf("text ink vertical centers differ by %.2f px (label=%.2f in %v, button=%.2f in %v), want <= 1 px",
			diff, lblCenter, lblInk, btnCenter, btnInk)
	}
}

func renderRowShot(t *testing.T, uiScale float32, variant ButtonVariant, size ButtonSize) (*harness, *image.RGBA, image.Rectangle, image.Rectangle) {
	t.Helper()
	const (
		rowW = 400
		rowH = 80
	)
	h := newHarness(t)
	h.env.UIScale = uiScale
	h.env.ReducedMotion = true
	st := NewButtonState()
	bp := ButtonProps{Text: "Open inbox", Variant: variant, Size: size}

	h.frameSize(t, rowW, rowH)
	paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
	var labelRect, btnRect image.Rectangle
	rowGtx := h.gtx
	rowGtx.Constraints.Min = image.Point{}
	layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(rowGtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			dims := Label(h.env, &l, theme.TypeBody(), h.env.Th.Chrome().Fg, "Waiting for approval")
			labelRect = image.Rectangle{Max: dims.Size}
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			if _, err := st.Layout(h.env, &l, bp); err != nil {
				t.Fatalf("lay the button out: %v", err)
			}
			dims := st.Measure(h.env, &l, bp)
			btnRect = image.Rectangle{Max: dims.Size}.Add(image.Pt(labelRect.Max.X, 0))
			return dims
		}),
	)
	img := h.shot(t, rowW, rowH)
	h.end()
	return h, img, labelRect, btnRect
}

func colorDist(a, b color.RGBA) int {
	abs := func(x int) int {
		if x < 0 {
			return -x
		}
		return x
	}
	return abs(int(a.R)-int(b.R)) + abs(int(a.G)-int(b.G)) + abs(int(a.B)-int(b.B))
}
