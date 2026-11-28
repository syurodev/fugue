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
// the ring's Snappy transition. A toggled button replaces its selected fill
// with the selected-focus wash (the selected level plus one hover step) so
// the focus step reads as clearly as a hover does; a NoSelectedFill button's
// external pill carries the level instead.

// washState is the §4.1 state a scene frame renders the button in.
type washState int

const (
	washRest  washState = iota
	washHover           // the pointer state, the visibility reference
	washFocus           // the keyboard state under test
)

// washKind is the chrome case: an untoggled button, or a toggled one that
// draws its own SelectedWash. The rail's NoSelectedFill button is not a kit
// scene: its pill crossfade lives in appwin's layoutRail and is asserted
// there.
type washKind int

const (
	washUntoggled washKind = iota
	washSelected
)

// washScene lays FocusWash icon buttons into a 240×140 AppBg frame with the
// button's box at (20, 20).
type washScene struct {
	h    *harness
	box  image.Rectangle
	kind washKind
}

const (
	washBtnSide = 24 // SizeXS, also HitMin
	washFrameW  = 240
	washFrameH  = 140
	washBtnXY   = 20
	// washInkFloor is the ink floor for the ring check: the headless
	// rasterizer wobbles opaque pixels by ±1–2 LSB (up to 4 at AA edges)
	// while the old ring moved fills by ~50, so past 8 is ring ink and only
	// that. washMinInk is how many pixels the wash must move inside the box.
	washInkFloor = 8
	washMinInk   = 100
)

func newWashScene(t *testing.T, light bool, kind washKind) *washScene {
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
	return &washScene{h: h, box: image.Rect(washBtnXY, washBtnXY, washBtnXY+washBtnSide, washBtnXY+washBtnSide), kind: kind}
}

// lay draws one frame with the button in st; the pill scene paints the rail's
// external SelectedWash pill first, the way layoutRail does.
func (s *washScene) lay(t *testing.T, st washState) {
	t.Helper()
	h := s.h
	h.frameSize(t, washFrameW, washFrameH)
	c := h.env.Th.Chrome()
	paint.FillShape(h.gtx.Ops, c.AppBg, clip.Rect{Max: image.Pt(washFrameW, washFrameH)}.Op())
	props := IconButtonProps{
		Icon: IconLock, Label: "Lock vault", Variant: IconButtonGhost, Size: SizeXS, FocusWash: true,
		Toggled: s.kind == washSelected,
	}
	stt := NewIconButtonState()
	switch st {
	case washRest:
		h.env.Preview = Preview{}
	case washHover:
		h.env.Preview = Preview{Hover: stt.Tag()}
	case washFocus:
		h.env.Preview = Preview{Focus: stt.Tag()}
	}
	at(&h.gtx, s.box.Min, func(gtx *layout.Context) {
		gtx.Constraints = layout.Exact(image.Pt(washBtnSide, washBtnSide))
		if _, err := stt.Layout(h.env, gtx, &props); err != nil {
			t.Fatalf("icon button layout: %v", err)
		}
	})
	h.end()
}

func (s *washScene) shot(t *testing.T) *image.RGBA {
	t.Helper()
	return s.h.shot(t, washFrameW, washFrameH)
}

// washPatch is the interior strip of the button box the pixel asserts read:
// inside the rounded body, clear of the glyph, which spans local x 5..19.
func washPatch(box image.Rectangle) image.Rectangle {
	return image.Rect(box.Min.X+2, box.Min.Y+washBtnSide/2-4, box.Min.X+5, box.Min.Y+washBtnSide/2+4)
}

// patchMean returns the per-channel mean color over r; averaging rides out
// the headless rasterizer's ±1–2 LSB wobble that single pixels show.
func patchMean(img *image.RGBA, r image.Rectangle) [3]float64 {
	var sum [3]float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			sum[0] += float64(c.R)
			sum[1] += float64(c.G)
			sum[2] += float64(c.B)
			n++
		}
	}
	for i := range sum {
		sum[i] /= float64(n)
	}
	return sum
}

// patchStep is the largest per-channel distance between two patch means.
func patchStep(a, b [3]float64) float64 {
	m := 0.0
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		m = max(m, d)
	}
	return m
}

// washInkCount counts the pixels inside box whose channels moved past 2 LSB
// between a and b — the plain hover wash moves a fill by ~11–17, so a
// focused untoggled button leaves hundreds and render noise leaves a
// handful. The toggled cases assert on patch steps instead (their fill moves
// dark themes by only ~7).
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

// washInkOutside returns the bounding box of the ink past the ink floor
// outside box, Empty when the render kept every decisive change inside it.
func washInkOutside(a, b *image.RGBA, box image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for y := range a.Bounds().Dy() {
		for x := range a.Bounds().Dx() {
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
		{modeLight, true},
		{modeDark, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, tc.light, washUntoggled)
			s.lay(t, washRest)
			base := s.shot(t)
			s.lay(t, washFocus)
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

// TestChromeFocusWashStepIsAtLeastAHoverStep pins the visibility rule of
// §3.4 (FUG-109): for every chrome case, the focused-vs-unfocused step must
// be at least the hover-vs-rest step on the same surface, measured in the
// same headless render, within 1 LSB. Stacking a translucent wash over the
// selected fill moved dark fills by ~3/255 — below any hover step — which is
// why the toggled cases replace the selected fill with the selected-focus
// wash instead.
func TestChromeFocusWashStepIsAtLeastAHoverStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		light bool
		kind  washKind
	}{
		{modeLight + " untoggled", true, washUntoggled},
		{modeDark + " untoggled", false, washUntoggled},
		{modeLight + " toggled on SelectedWash", true, washSelected},
		{modeDark + " toggled on SelectedWash", false, washSelected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The visibility reference: hover vs rest on the same surface, an
			// untoggled button in the same render pipeline.
			ref := newWashScene(t, tc.light, washUntoggled)
			ref.lay(t, washRest)
			restImg := ref.shot(t)
			ref.lay(t, washHover)
			hoverImg := ref.shot(t)
			hoverStep := patchStep(patchMean(restImg, washPatch(ref.box)), patchMean(hoverImg, washPatch(ref.box)))

			s := newWashScene(t, tc.light, tc.kind)
			s.lay(t, washRest)
			rest := s.shot(t)
			s.lay(t, washFocus)
			focused := s.shot(t)
			focusStep := patchStep(patchMean(rest, washPatch(s.box)), patchMean(focused, washPatch(s.box)))

			if focusStep < hoverStep-1 {
				t.Fatalf("focus step %.2f LSB < hover step %.2f LSB − 1; the focused button is not as visible as a hovered one", focusStep, hoverStep)
			}
		})
	}
}

// TestChromeFocusWashLandsOnSelectedFocusWash pins where the toggled cases
// land: the focused fill must be the selected-focus wash over the surface —
// not the selected fill, and not a stack over it — equal to a bare swatch of
// the token composited by the same pipeline within 1 LSB.
func TestChromeFocusWashLandsOnSelectedFocusWash(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		light bool
		kind  washKind
	}{
		{modeLight + " toggled on SelectedWash", true, washSelected},
		{modeDark + " toggled on SelectedWash", false, washSelected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, tc.light, tc.kind)
			s.lay(t, washFocus)
			focused := s.shot(t)

			// The reference swatch: the token over AppBg at the button's box,
			// composited by the same rasterizer.
			h := newHarness(t)
			h.env.ReducedMotion = true
			if tc.light {
				sh, err := fonts.Shaper()
				if err != nil {
					t.Fatalf("build the shaper: %v", err)
				}
				th := theme.Light(sh)
				h.env.Th = &th
			}
			h.frameSize(t, washFrameW, washFrameH)
			c := h.env.Th.Chrome()
			paint.FillShape(h.gtx.Ops, c.AppBg, clip.Rect{Max: image.Pt(washFrameW, washFrameH)}.Op())
			paint.FillShape(h.gtx.Ops, c.SelectedFocusWash, clip.UniformRRect(s.box, h.gtx.Dp(theme.RadiusSM)).Op(h.gtx.Ops))
			h.end()
			swatch := h.shot(t, washFrameW, washFrameH)

			got := patchMean(focused, washPatch(s.box))
			want := patchMean(swatch, washPatch(s.box))
			if step := patchStep(got, want); step > 1 {
				t.Fatalf("focused fill lands %.2f LSB from the SelectedFocusWash swatch (got %v, want %v); the level is wrong", step, got, want)
			}
		})
	}
}

func TestChromeFocusWashIconKeepsContrastOnTheFocusFill(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		light bool
		kind  washKind
	}{
		{modeLight, true, washUntoggled},
		{modeDark, false, washUntoggled},
		{modeLight + " toggled", true, washSelected},
		{modeDark + " toggled", false, washSelected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newWashScene(t, tc.light, tc.kind)
			s.lay(t, washFocus)
			img := s.shot(t)

			// The fill beside the icon at focus = 1 is the fill the design
			// pins — the hover wash, or the selected-focus wash — and the
			// icon draws in the variant's interaction color, the chrome Fg
			// (UK-E04).
			fill := img.RGBAAt(s.box.Min.X+2, s.box.Min.Y+washBtnSide/2)
			fg := s.h.env.Th.Chrome().Fg
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
