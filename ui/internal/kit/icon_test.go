package kit

import (
	"image"
	"image/color"
	"math"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestEveryIconParses runs the path parser over every packaged icon, so a
// new glyph cannot ship with path data the parser mishandles: the copy
// icon's Z once sent close() into an endless loop because it never
// consumed its token, and none of the first nine icons had a Z to catch it.
func TestEveryIconParses(t *testing.T) {
	t.Parallel()
	for _, name := range []IconName{
		IconCheck, IconChevronDown, IconChevronRight, IconSearch, IconClose,
		IconEye, IconEyeOff, IconCopy, IconRestart, IconHelp,
		IconFolder, IconGitBranch, IconGitPullRequest,
	} {
		t.Run(string(name), func(t *testing.T) {
			t.Parallel()
			paths, err := parseIconPath(mustIconData(t, name))
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if len(paths) == 0 {
				t.Fatalf("%s parsed to no subpaths", name)
			}
			for _, sp := range paths {
				if len(sp.Segs) == 0 {
					t.Fatalf("%s has an empty subpath", name)
				}
			}
		})
	}
}

// mustIconData returns the generated data of one icon, failing the test on
// an unknown name.
func mustIconData(t *testing.T, n IconName) string {
	t.Helper()
	d, err := pathOf(n)
	if err != nil {
		t.Fatalf("path data of %s: %v", n, err)
	}
	return d
}

// TestIconStrokeWidthIsTheTokenAtEveryDensity pins §3.0 and §6.1 at every
// density: the inked stroke of an icon is theme.IconStroke(size) dp wide —
// IconStroke × PxPerDp × UIScale in pixels — at any pixel density and UI
// scale, and the cached stroke is built once per dp size and reused across
// densities, because nothing in it holds device pixels.
//
// The measurement renders IconCheck headlessly and sums the alpha coverage
// of a scan row through its exactly 45° segment (20,6)→(9,17): a diagonal
// stroke of perpendicular width w inks a horizontal scan across an extent
// of w·√2, so w = extent/√2. Coverage sums are exact for antialiased
// geometry, so the comparison holds to a ±0.5 px band.
func TestIconStrokeWidthIsTheTokenAtEveryDensity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		size   unit.Dp
		stroke float32
	}{
		{"IconSM class", theme.IconSM, 1.25},
		{"IconMD class", theme.IconMD, 1.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			th := theme.Dark(nil)
			// One window for the whole subtest: the densities share it.
			win := openHeadless(t)
			for _, pxPerDp := range []float32{2, 1.25, 1} {
				for _, ui := range []float32{1.25, 1} {
					assertStrokeWidth(t, win, &th, tc.size, tc.stroke, pxPerDp, ui)
				}
			}
		})
	}
	t.Run("cache is reused across densities", func(t *testing.T) {
		t.Parallel()
		th := theme.Dark(nil)
		win := openHeadless(t)
		env := &Env{Th: &th}
		for _, pxPerDp := range []float32{2, 1.25, 1} {
			env.UIScale = 1.25
			measureStrokeWidth(t, win, env, theme.IconMD, pxPerDp)
			if n := len(env.iconStrokes); n != 1 {
				t.Fatalf("cache holds %d strokes at PxPerDp %v, want the one dp-size entry", n, pxPerDp)
			}
		}
	})
}

// openHeadless opens the 64×64 render window the width measurements draw
// into, through glwin: it is never released.
func openHeadless(t *testing.T) *headless.Window {
	t.Helper()
	win, err := glwin.Open(64, 64)
	if err != nil {
		t.Fatalf("open a headless window: %v", err)
	}
	return win
}

// assertStrokeWidth builds a fresh Env at the density and checks the inked
// stroke against the token: the cache must be correct at whatever density
// builds it first, not only at density 1.
func assertStrokeWidth(t *testing.T, win *headless.Window, th *theme.Theme, size unit.Dp, stroke, pxPerDp, ui float32) {
	t.Helper()
	env := &Env{Th: th, UIScale: ui}
	got := measureStrokeWidth(t, win, env, size, pxPerDp)
	want := float64(stroke) * float64(pxPerDp) * float64(ui)
	if math.Abs(got-want) > 0.5 {
		t.Errorf("PxPerDp %v, UI scale %v: stroke %.2f px, want %.2f ±0.5", pxPerDp, ui, got, want)
	}
}

// measureStrokeWidth draws IconCheck at size into win with the given
// density and the env's UI scale and returns the perpendicular ink width in
// pixels, measured from the alpha coverage of a scan row.
func measureStrokeWidth(t *testing.T, win *headless.Window, env *Env, size unit.Dp, pxPerDp float32) float64 {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: pxPerDp, PxPerSp: pxPerDp}),
		Constraints: layout.Exact(image.Pt(64, 64)),
	}
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	if _, err := env.Icon(&gtx, IconCheck, size, white); err != nil {
		t.Fatalf("draw the icon: %v", err)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatalf("render a frame: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("read the frame back: %v", err)
	}

	// The scan row crosses only the 45° segment (20,6)→(9,17): at grid row
	// y, the segment's center is x = 20-(y-6), as long as y stays between 6
	// and 17 and clear of the join at (9,17).
	sizePx := float64(gtx.Dp(size))
	row := int(8.0 / 24.0 * sizePx)
	gridY := float64(row) / sizePx * 24
	center := (20 - (gridY - 6)) / 24 * sizePx

	// The runs of inked pixels on the row; the icon is opaque white on a
	// transparent background, so the alpha channel is the coverage. Take
	// the run whose center is nearest the expected stroke center and sum
	// its coverage: a row's coverage under one stroke is exactly the
	// stroke's horizontal extent there, whatever the AA phases.
	const inkFloor = 3
	type run struct {
		from, to int
		sum      float64
	}
	var runs []run
	for x := range 64 {
		a := img.Pix[img.PixOffset(x, row)+3]
		if a <= inkFloor {
			continue
		}
		if len(runs) == 0 || runs[len(runs)-1].to != x {
			runs = append(runs, run{from: x})
		}
		last := &runs[len(runs)-1]
		last.to = x + 1
		last.sum += float64(a) / 255
	}
	best, bestDist := -1, math.MaxFloat64
	for i, r := range runs {
		d := math.Abs(float64(r.from+r.to)/2 - center)
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	if best < 0 {
		t.Fatalf("no ink under the expected stroke on row %d (runs %v)", row, runs)
	}
	// A 45° stroke of perpendicular width w inks a horizontal extent of
	// w·√2.
	return runs[best].sum / math.Sqrt2
}
