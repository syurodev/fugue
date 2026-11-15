package main

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/op"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestGallery builds the gallery over a real env in one mode at one UI
// scale.
func newTestGallery(t *testing.T, light bool, ui float32) *gallery {
	t.Helper()
	env, err := newEnv(light)
	if err != nil {
		t.Fatalf("build the env: %v", err)
	}
	env.UIScale = ui
	return newGallery(env, light)
}

// layFrame lays one frame of page cur out at a pixel scale and the env's
// UI scale, the way a window would. It returns the pixel HitMin and the
// window size for the bounds checks.
func layFrame(t *testing.T, g *gallery, cur int, px, ui float32) (hit int, size image.Point) {
	t.Helper()
	var router input.Router
	var ops op.Ops
	size = windowSize(px, ui)
	g.cur = cur
	gtx := frameContext(&ops, g.env, size, px)
	gtx.Source = router.Source()
	gtx.Now = time.Unix(1_700_000_000, 0)
	hit = gtx.Dp(theme.HitMin)
	if err := g.frame(&gtx); err != nil {
		t.Fatalf("lay %s out: %v", pageNames()[cur], err)
	}
	router.Frame(&ops)
	return hit, size
}

// expectedCells is the matrix UK-E03 demands for one page: every Button
// variant × size × state on the Button page, every IconButton variant ×
// state on the IconButton page.
func expectedCells(cur int) map[cell]bool {
	want := map[cell]bool{}
	switch pageNames()[cur] {
	case "Button":
		for vi := range numVariants {
			for si := range numSizes {
				for _, st := range buttonStates() {
					want[cell{compButton, vi, si, st}] = true
				}
			}
		}
	case "IconButton":
		for vi := range 2 {
			for _, st := range iconStates() {
				want[cell{compIconButton, vi, 0, st}] = true
			}
		}
	}
	return want
}

// pageCoversItsMatrix lays one page out in one mode and asserts UK-E03.
func pageCoversItsMatrix(t *testing.T, light bool, cur int) {
	t.Helper()
	g := newTestGallery(t, light, 1)
	if _, size := layFrame(t, g, cur, 1, 1); size.X == 0 {
		t.Fatal("empty frame")
	}
	for c := range expectedCells(cur) {
		if !g.pgs.cells[c] {
			t.Fatalf("page %s is missing cell %+v", pageNames()[cur], c)
		}
	}
}

// TestGalleryPagesCoverEveryVariantAndState covers UK-E03: every page
// presents its full variant × size × state matrix in light and in dark.
func TestGalleryPagesCoverEveryVariantAndState(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name  string
		light bool
	}{{"light", true}, {"dark", false}} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			for i := range pageNames() {
				pageCoversItsMatrix(t, mode.light, i)
			}
		})
	}
}

// checkPageBounds asserts UK-E09 for one laid-out frame: no clipped label,
// every hit area at least size.hit.min, no two text runs overlapping.
func checkPageBounds(t *testing.T, g *gallery, hit int, size image.Point) {
	t.Helper()
	bounds := image.Rectangle{Max: size}
	for _, r := range g.pgs.runs {
		if !r.In(bounds) {
			t.Fatalf("text run %v clipped by the page bounds %v", r, bounds)
		}
	}
	for _, r := range g.pgs.ctl {
		if r.Dx() < hit || r.Dy() < hit {
			t.Fatalf("control at %v is %v×%v px, below the %v×%v hit minimum", r.Min, r.Dx(), r.Dy(), hit, hit)
		}
	}
	for a := range g.pgs.runs {
		for b := a + 1; b < len(g.pgs.runs); b++ {
			if g.pgs.runs[a].Overlaps(g.pgs.runs[b]) {
				t.Fatalf("text runs overlap: %v and %v", g.pgs.runs[a], g.pgs.runs[b])
			}
		}
	}
}

// TestEveryPageLaysOutAtEveryScale covers UK-E09: at 90% and 125% (and
// 100%, for free) no label is clipped, every control keeps a hit area of
// at least size.hit.min, and no two text runs on a page overlap — asserted
// on the laid-out text bounds the pages record.
func TestEveryPageLaysOutAtEveryScale(t *testing.T) {
	t.Parallel()
	for _, ui := range []float32{0.9, 1, 1.25} {
		for _, px := range []float32{1, 1.25} {
			for i := range pageNames() {
				t.Run(fmt.Sprintf("%s-ui%v-px%v", pageNames()[i], ui, px), func(t *testing.T) {
					t.Parallel()
					g := newTestGallery(t, false, ui)
					hit, size := layFrame(t, g, i, px, ui)
					checkPageBounds(t, g, hit, size)
				})
			}
		}
	}
}
