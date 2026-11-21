package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestLineRowCountsAndAnchorsLines covers ui-kit §3.9 and UK-E12: LineRow
// computes floor((width+gap)/(line.width+gap)), anchors lines to the left edge
// with rounded caps and no track, uses LineHeight/LineHeightSM, never animates,
// and keeps line positions invariant when Progress values change.
func TestLineRowCountsAndAnchorsLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		w, lineW, gap, want int
	}{
		{0, 3, 2, 0},
		{2, 3, 2, 0},
		{3, 3, 2, 1},
		{4, 3, 2, 1},
		{7, 3, 2, 1},
		{8, 3, 2, 2},
		{48, 3, 2, 10},
		{52, 3, 2, 10},
		{53, 3, 2, 11},
	} {
		if got := LineRowCount(tc.w, tc.lineW, tc.gap); got != tc.want {
			t.Errorf("LineRowCount(%d, %d, %d) = %d, want %d", tc.w, tc.lineW, tc.gap, got, tc.want)
		}
	}

	h := newHarness(t)
	h.frameSize(t, 52, 20)
	h.gtx.Constraints.Min = image.Point{}
	var seen []int
	dims := LineRow(h.env, &h.gtx, LineRowProps{
		Size:     LineMD,
		Fallback: h.env.Th.Chrome().LineRest,
		ColorAt: func(i, count int) color.NRGBA {
			seen = append(seen, count)
			return h.env.Th.Status().Info
		},
	})
	h.end()
	if len(seen) != 10 || seen[0] != 10 {
		t.Fatalf("LineRow called ColorAt %d times (counts=%v), want 10", len(seen), seen)
	}
	if dims.Size != image.Pt(52, h.gtx.Dp(theme.LineHeight)) {
		t.Fatalf("LineMD dims = %v, want (52, %d)", dims.Size, h.gtx.Dp(theme.LineHeight))
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("LineRow must never request animation frames")
	}

	h.frameSize(t, 52, 20)
	h.gtx.Constraints.Min = image.Point{}
	smDims := LineRow(h.env, &h.gtx, LineRowProps{Size: LineSM, Fallback: h.env.Th.Chrome().LineRest})
	h.end()
	if smDims.Size.Y != h.gtx.Dp(theme.LineHeightSM) {
		t.Fatalf("LineSM height = %d, want %d", smDims.Size.Y, h.gtx.Dp(theme.LineHeightSM))
	}

	win, err := glwin.Open(60, 20)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	assertLineRowPixelsAnchoredLeft(t, win, h)
}

func assertLineRowPixelsAnchoredLeft(t *testing.T, win *headless.Window, h *harness) {
	t.Helper()
	renderAt := func(val float32) *image.RGBA {
		h.frameSize(t, 52, 14)
		paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
		Progress(h.env, &h.gtx, ProgressProps{Value: val})
		if err := win.Frame(&h.ops); err != nil {
			t.Fatalf("render line row: %v", err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 52, 14))
		if err := win.Screenshot(img); err != nil {
			t.Fatalf("screenshot: %v", err)
		}
		return img
	}
	imgLow := renderAt(0.25)
	imgHigh := renderAt(0.75)
	bg := imgLow.RGBAAt(49, 7) // 4 px remainder on right edge has no line and no track
	if gapPx := imgLow.RGBAAt(3, 7); gapPx != bg {
		t.Fatalf("gap pixel between lines = %v, want background %v (no track)", gapPx, bg)
	}
	// Line centers sit at x = 1, 6, 11, ... regardless of Value.
	for i := range 10 {
		cx := i*5 + 1
		if px := imgLow.RGBAAt(cx, 7); px == bg {
			t.Fatalf("line %d center x=%d is background %v; lines must anchor left", i, cx, bg)
		}
		if (imgLow.RGBAAt(cx, 7) == bg) != (imgHigh.RGBAAt(cx, 7) == bg) {
			t.Fatalf("line %d inked state shifted when Value changed", i)
		}
	}
	for x := 48; x < 52; x++ {
		if px := imgHigh.RGBAAt(x, 7); px != bg {
			t.Fatalf("right remainder x=%d painted %v, want background %v", x, px, bg)
		}
	}
}

// TestProgressTwoTierAssignsLinesByMidpoint covers ui-kit §6.5 and UK-E12:
// midpoints <= Value get solid used hue, <= Secondary (> Value) get 40%
// secondary hue, rest get LineRest, any non-zero Value colors at least one
// line, and no Secondary or Value >= Secondary produces no second tier.
func TestProgressTwoTierAssignsLinesByMidpoint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	win, err := glwin.Open(48, 14)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	render := func(p ProgressProps) []color.RGBA {
		h.frameSize(t, 48, 14)
		paint.Fill(h.gtx.Ops, h.env.Th.Chrome().CanvasBg)
		Progress(h.env, &h.gtx, p)
		if err := win.Frame(&h.ops); err != nil {
			t.Fatalf("render progress: %v", err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 48, 14))
		if err := win.Screenshot(img); err != nil {
			t.Fatalf("screenshot: %v", err)
		}
		cols := make([]color.RGBA, 10)
		for i := range 10 {
			cols[i] = img.RGBAAt(i*5+1, 7)
		}
		return cols
	}
	assertTwoTierMidpoints(t, render)
	assertOneTierOverGateAndTiny(t, render)
}

func assertLineRange(t *testing.T, got []color.RGBA, start, end int, want color.RGBA, label string) {
	t.Helper()
	for i := start; i < end; i++ {
		if got[i] != want {
			t.Fatalf("%s line %d = %v, want %v", label, i, got[i], want)
		}
	}
}

func assertTwoTierMidpoints(t *testing.T, render func(ProgressProps) []color.RGBA) {
	t.Helper()
	allSuccess := render(ProgressProps{Value: 0.50})[0]
	allRest := render(ProgressProps{Value: 0})[0]
	twoTier := render(ProgressProps{Value: 0.42, Secondary: 0.85})
	secSample := twoTier[5]
	if secSample == allSuccess || secSample == allRest {
		t.Fatalf("two-tier secondary line matches used or rest: got %v, used=%v, rest=%v", secSample, allSuccess, allRest)
	}
	// Midpoints: 0.05..0.35 (0..3) <= 0.42 used; 0.45..0.85 (4..8) <= 0.85 sec; 0.95 (9) rest.
	assertLineRange(t, twoTier, 0, 4, allSuccess, "two-tier used")
	assertLineRange(t, twoTier, 4, 9, secSample, "two-tier secondary")
	assertLineRange(t, twoTier, 9, 10, allRest, "two-tier rest")
}

func assertOneTierOverGateAndTiny(t *testing.T, render func(ProgressProps) []color.RGBA) {
	t.Helper()
	allSuccess := render(ProgressProps{Value: 0.50})[0]
	allRest := render(ProgressProps{Value: 0})[0]
	allDanger := render(ProgressProps{Value: 1.0, Tone: ProgressDanger})[0]

	oneTier := render(ProgressProps{Value: 0.50})
	assertLineRange(t, oneTier, 0, 5, allSuccess, "one-tier used")
	assertLineRange(t, oneTier, 5, 10, allRest, "one-tier rest")

	overGate := render(ProgressProps{Value: 0.88, Secondary: 0.85})
	assertLineRange(t, overGate, 0, 9, allDanger, "over-gate used")
	assertLineRange(t, overGate, 9, 10, allRest, "over-gate rest")

	tiny := render(ProgressProps{Value: 0.01})
	assertLineRange(t, tiny, 0, 1, allSuccess, "tiny used")
	assertLineRange(t, tiny, 1, 10, allRest, "tiny rest")
}

// TestProgressLevelToneSwitchesAt70And85AndNeverLowers covers ui-kit §6.5 and
// UK-E12: level tone switches from success (<0.70) to warning ([0.70, 0.85))
// to danger (>=0.85), callers may raise the level, and callers never lower it.
func TestProgressLevelToneSwitchesAt70And85AndNeverLowers(t *testing.T) {
	t.Parallel()
	th := theme.Light(nil)
	for _, tc := range []struct {
		name   string
		caller ProgressTone
		value  float32
		want   ProgressTone
	}{
		{"zero is success", ProgressDefault, 0.0, ProgressSuccess},
		{"69.9% is success", ProgressDefault, 0.699, ProgressSuccess},
		{"70% switches to warning", ProgressDefault, 0.70, ProgressWarning},
		{"84.9% stays warning", ProgressDefault, 0.849, ProgressWarning},
		{"85% switches to danger", ProgressDefault, 0.85, ProgressDanger},
		{"100% is danger", ProgressDefault, 1.0, ProgressDanger},
		{"caller raises success->warning", ProgressWarning, 0.42, ProgressWarning},
		{"caller raises success->danger", ProgressDanger, 0.42, ProgressDanger},
		{"caller raises warning->danger", ProgressDanger, 0.78, ProgressDanger},
		{"caller cannot lower warning->success", ProgressSuccess, 0.78, ProgressWarning},
		{"caller cannot lower danger->success", ProgressSuccess, 0.88, ProgressDanger},
		{"caller cannot lower danger->warning", ProgressWarning, 0.88, ProgressDanger},
	} {
		if got := ResolveProgressTone(tc.caller, tc.value); got != tc.want {
			t.Errorf("%s: ResolveProgressTone(%d, %v) = %d, want %d", tc.name, tc.caller, tc.value, got, tc.want)
		}
	}

	if got := ResolveProgressLineTone(&th, ProgressDefault, 0.42); got != th.StatusLineTone(theme.StatusSuccess) {
		t.Fatalf("0.42 line tone = %+v, want StatusSuccess", got)
	}
	if got := ResolveProgressLineTone(&th, ProgressDefault, 0.75); got != th.StatusLineTone(theme.StatusWarning) {
		t.Fatalf("0.75 line tone = %+v, want StatusWarning", got)
	}
	if got := ResolveProgressLineTone(&th, ProgressDefault, 0.85); got != th.StatusLineTone(theme.StatusDanger) {
		t.Fatalf("0.85 line tone = %+v, want StatusDanger", got)
	}
	if got := ResolveProgressLineTone(&th, ProgressWarning, 0.42); got != th.StatusLineTone(theme.StatusWarning) {
		t.Fatalf("raised warning at 0.42 = %+v, want StatusWarning", got)
	}
	if got := ResolveProgressLineTone(&th, ProgressSuccess, 0.90); got != th.StatusLineTone(theme.StatusDanger) {
		t.Fatalf("non-lowerable danger at 0.90 = %+v, want StatusDanger", got)
	}
}

type progressShotSpec struct {
	name    string
	light   bool
	uiScale float32
}

func progressShotSpecs() []progressShotSpec {
	specs := make([]progressShotSpec, 0, 6)
	for _, sc := range []struct {
		label string
		scale float32
	}{{"ui090", 0.9}, {"ui100", 1.0}, {"ui125", 1.25}} {
		for _, mode := range []struct {
			label string
			light bool
		}{{modeLight, true}, {modeDark, false}} {
			specs = append(specs, progressShotSpec{
				name:    "progress-" + mode.label + "-" + sc.label,
				light:   mode.light,
				uiScale: sc.scale,
			})
		}
	}
	return specs
}

func renderProgressShot(t *testing.T, win *headless.Window, spec progressShotSpec) *image.RGBA {
	t.Helper()
	h := newHarness(t)
	th := theme.Dark(h.env.Sh)
	if spec.light {
		th = theme.Light(h.env.Sh)
	}
	h.env.Th = &th
	h.env.UIScale = spec.uiScale
	size := image.Pt(320, 140)
	h.frameSize(t, size.X, size.Y)
	paint.Fill(h.gtx.Ops, th.Chrome().CanvasBg)
	layout.UniformInset(theme.SpaceMD).Layout(h.gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Progress(h.env, &gtx, ProgressProps{Value: 0.45})
			}),
			layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Progress(h.env, &gtx, ProgressProps{Value: 0.42, Secondary: 0.85})
			}),
			layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Progress(h.env, &gtx, ProgressProps{Value: 0.78, Secondary: 0.85})
			}),
			layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Progress(h.env, &gtx, ProgressProps{Value: 0.90, Secondary: 0.85})
			}),
		)
	})
	if err := win.Frame(&h.ops); err != nil {
		t.Fatalf("render progress shot %s: %v", spec.name, err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", spec.name, err)
	}
	return img
}

// TestProgressShotsRender renders one-tier, two-tier, and over-the-gate
// Progress bars in light and dark modes at 90%, 100%, and 125% UI scale.
func TestProgressShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(320, 140)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range progressShotSpecs() {
		img := renderProgressShot(t, win, spec)
		if isUniformImage(img) {
			t.Fatalf("shot %s rendered uniform image", spec.name)
		}
		rendered[spec.name] = img
	}
	if imagesEqual(rendered["progress-light-ui100"], rendered["progress-dark-ui100"]) {
		t.Fatal("progress light and dark shots are identical")
	}
	if imagesEqual(rendered["progress-light-ui090"], rendered["progress-light-ui100"]) ||
		imagesEqual(rendered["progress-light-ui100"], rendered["progress-light-ui125"]) {
		t.Fatal("progress shots at 90/100/125% must differ")
	}
}

func isUniformImage(img *image.RGBA) bool {
	if len(img.Pix) < 4 {
		return true
	}
	r0, g0, b0, a0 := img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3]
	for i := 4; i < len(img.Pix); i += 4 {
		if img.Pix[i] != r0 || img.Pix[i+1] != g0 || img.Pix[i+2] != b0 || img.Pix[i+3] != a0 {
			return false
		}
	}
	return true
}

func imagesEqual(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}
