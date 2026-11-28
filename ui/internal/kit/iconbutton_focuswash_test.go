package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// FUG-109 (ui-kit §3.4): the app chrome buttons show keyboard focus as the
// variant's hover wash instead of the outer ring — many of them rest against
// the window edge, where the ring's far segment would be cut. The wash rides
// the ring's Snappy transition and stacks over the selected fill when the
// button is toggled, so focus stays visible on it too.

// washScene lays FocusWash icon buttons into a 240×140 AppBg frame with the
// button's box at (20, 20); pill, when set, paints the rail's external
// SelectedWash pill behind the button first, the NoSelectedFill case.
type washScene struct {
	h    *harness
	box  image.Rectangle
	pill bool
}

const (
	washBtnSide  = 24 // SizeXS, also HitMin
	washFrameW   = 240
	washFrameH   = 140
	washBtnXY    = 20
	washFrameGap = 8
)

func newWashScene(t *testing.T, light, pill bool) *washScene {
	t.Helper()
	h := newHarness(t)
	h.env.ReducedMotion = true
	if light {
		sh, err := fonts.Shaper()
		if err != nil {
			t.Fatalf("build the shaper: %v", err)
		}
		th := theme.Light(sh)
		h.env.Th = &th
	}
	return &washScene{h: h, box: image.Rect(washBtnXY, washBtnXY, washBtnXY+washBtnSide, washBtnXY+washBtnSide), pill: pill}
}

// lay draws one frame with the button focused or not.
func (s *washScene) lay(t *testing.T, focused bool) {
	t.Helper()
	h := s.h
	h.frameSize(t, washFrameW, washFrameH)
	c := h.env.Th.Chrome()
	paint.FillShape(h.gtx.Ops, c.AppBg, clip.Rect{Max: image.Pt(washFrameW, washFrameH)}.Op())
	if s.pill {
		// The rail draws its sliding SelectedWash pill under the button
		// (slotshell layoutRail); NoSelectedFill leaves the body clear for it.
		paint.FillShape(h.gtx.Ops, c.SelectedWash, clip.UniformRRect(s.box, h.gtx.Dp(theme.RadiusSM)).Op(h.gtx.Ops))
	}
	props := IconButtonProps{Icon: IconLock, Label: "Lock vault", Variant: IconButtonGhost, Size: SizeXS, FocusWash: true, Toggled: s.pill, NoSelectedFill: s.pill}
	st := NewIconButtonState()
	if focused {
		h.env.Preview = Preview{Focus: st.Tag()}
	} else {
		h.env.Preview = Preview{}
	}
	at(&h.gtx, s.box.Min, func(gtx *layout.Context) {
		gtx.Constraints = layout.Exact(image.Pt(washBtnSide, washBtnSide))
		if _, err := st.Layout(h.env, gtx, &props); err != nil {
			t.Fatalf("icon button layout: %v", err)
		}
	})
	h.end()
}

func (s *washScene) shot(t *testing.T) *image.RGBA {
	t.Helper()
	return s.h.shot(t, washFrameW, washFrameH)
}

// The headless rasterizer wobbles opaque pixels by ±1–2 LSB between frames,
// up to 4 near anti-aliased edges, while decisive ink starts at ~8 (the wash
// stacked over the rail pill) and reaches ~50 (the old ring). The wash
// asserts therefore count pixels past the noise floor inside the button box,
// and the ring assert looks for ink past the ink floor outside it.
const (
	// washInkFloor is the ink floor for the ring check; the wash count
	// reads anything past 2, since the wash stacked over the selected fill
	// moves dark-theme fills by only ~4-5 LSB.
	washInkFloor = 8
	washMinInk   = 100
)

// washDelta is the largest per-channel distance between two pixels.
func washDelta(x, y color.RGBA) int {
	d := [4]int{int(x.R) - int(y.R), int(x.G) - int(y.G), int(x.B) - int(y.B), int(x.A) - int(y.A)}
	m := 0
	for _, v := range d {
		if v < 0 {
			v = -v
		}
		m = max(m, v)
	}
	return m
}

// washInkCount counts the pixels inside box whose channels moved past the
// noise floor between a and b — the wash fills the body, so a focused
// button leaves hundreds of them and render noise leaves a handful.
func washInkCount(a, b *image.RGBA, box image.Rectangle) int {
	n := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if washDelta(a.RGBAAt(x, y), b.RGBAAt(x, y)) > 2 {
				n++
			}
		}
	}
	return n
}

// washInkOutside returns the bounding box of the ink past the ink floor
// outside box, Empty when the render kept every decisive change inside it.
func washInkOutside(a, b *image.RGBA, box image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			if image.Pt(x, y).In(box) {
				continue
			}
			if washDelta(a.RGBAAt(x, y), b.RGBAAt(x, y)) > washInkFloor {
				out = out.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return out
}

func TestChromeFocusWashDrawsNothingOutsideTheBoxAndDiffersInside(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		light bool
	}{
		{"light", true},
		{"dark", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, tc.light, false)
			s.lay(t, false)
			base := s.shot(t)
			s.lay(t, true)
			focused := s.shot(t)

			if n := washInkCount(base, focused, s.box); n < washMinInk {
				t.Fatalf("%d pixels moved past the noise floor inside the box; the wash never showed", n)
			}
			if out := washInkOutside(base, focused, s.box); !out.Empty() {
				t.Fatalf("focus drew ink on %v outside the box %v; that is a ring, not the wash", out, s.box)
			}
		})
	}
}

func TestChromeFocusWashStacksOverTheSelectedFillWhenToggled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		pill bool // the rail's NoSelectedFill + external SelectedWash pill case
	}{
		{"selected wash fill", false},
		{"no selected fill over an external pill", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, false, tc.pill)
			s.lay(t, false)
			toggled := s.shot(t)
			s.lay(t, true)
			toggledFocused := s.shot(t)

			if n := washInkCount(toggled, toggledFocused, s.box); n < washMinInk {
				t.Fatalf("%d pixels moved past the noise floor on a focused toggled button; the wash does not stack over the selected fill", n)
			}
			if out := washInkOutside(toggled, toggledFocused, s.box); !out.Empty() {
				t.Fatalf("focus on a toggled button drew ink on %v outside the box %v", out, s.box)
			}
		})
	}
}

func TestChromeFocusWashIconKeepsContrastOnTheFocusFill(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		light bool
		pill  bool
	}{
		{"light", true, false},
		{"dark", false, false},
		{"light toggled", true, true},
		{"dark toggled", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, tc.light, tc.pill)
			s.lay(t, true)
			img := s.shot(t)

			// The fill beside the icon at focus = 1 is the wash composite the
			// design pins (hover wash alone, or stacked over the selected
			// fill); the icon draws in the variant's interaction color, the
			// chrome Fg (UK-E04). The sample sits inside the rounded body but
			// clear of the glyph, which spans local x 5..19.
			fill := img.RGBAAt(s.box.Min.X+2, s.box.Min.Y+washBtnSide/2)
			fg := color.NRGBA(s.h.env.Th.Chrome().Fg)
			if got := contrastRatio(fg, color.NRGBA{R: fill.R, G: fill.G, B: fill.B, A: 255}); got < 4.5 {
				t.Fatalf("icon contrast on the focus fill = %.2f, want >= 4.5 (fill %v, fg %v)", got, fill, fg)
			}
		})
	}
}

func TestIconButtonWithoutFocusWashStillDrawsItsOuterRing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	lay := func(focused bool) *image.RGBA {
		h.frameSize(t, washFrameW, washFrameH)
		paint.FillShape(h.gtx.Ops, h.env.Th.Chrome().AppBg, clip.Rect{Max: image.Pt(washFrameW, washFrameH)}.Op())
		props := IconButtonProps{Icon: IconLock, Label: "Close", Variant: IconButtonGhost, Size: SizeXS}
		st := NewIconButtonState()
		if focused {
			h.env.Preview = Preview{Focus: st.Tag()}
		} else {
			h.env.Preview = Preview{}
		}
		at(&h.gtx, image.Pt(washBtnXY, washBtnXY), func(gtx *layout.Context) {
			gtx.Constraints = layout.Exact(image.Pt(washBtnSide, washBtnSide))
			if _, err := st.Layout(h.env, gtx, &props); err != nil {
				t.Fatalf("icon button layout: %v", err)
			}
		})
		h.end()
		return h.shot(t, washFrameW, washFrameH)
	}
	base := lay(false)
	focused := lay(true)

	box := image.Rect(washBtnXY, washBtnXY, washBtnXY+washBtnSide, washBtnXY+washBtnSide)
	if out := washInkOutside(base, focused, box); out.Empty() {
		t.Fatal("the plain button's focused render left no ink outside its box; the outer ring did not draw")
	}
}
