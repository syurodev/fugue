package shottest

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func writeGolden(t *testing.T, dir, name string, img *image.RGBA) {
	t.Helper()
	writePNG(t, filepath.Join(dir, name+".png"), img)
}

func TestComparePassesIdenticalAndAntialiasNoise(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := color.RGBA{R: 40, G: 80, B: 120, A: 255}
	writeGolden(t, dir, "scene", solid(100, 100, base))

	compareIn(t, dir, "scene", solid(100, 100, base))

	noisy := solid(100, 100, color.RGBA{R: base.R + linuxChannelTolerance, G: base.G, B: base.B, A: 255})
	compareIn(t, dir, "scene", noisy)
}

func TestCompareBudgetCatchesAMovedBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	golden := solid(100, 100, white)
	writeGolden(t, dir, "scene", golden)

	moved := solid(100, 100, white)
	for y := 10; y < 14; y++ { // a 4x100 band is 4% of the frame
		for x := range 100 {
			moved.SetRGBA(x, y, color.RGBA{A: 255})
		}
	}
	diff, share := compare(moved, golden, linuxChannelTolerance)
	if share < 0.039 || share > 0.041 {
		t.Fatalf("share = %v, want 0.04", share)
	}
	if got := diff.RGBAAt(5, 11); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("diff pixel = %v, want red", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		_, maxShare := budget(goos)
		if share <= maxShare {
			t.Fatalf("%s budget %v hides a %v diff", goos, maxShare, share)
		}
	}
}

func TestCompareFailureWritesActualAndDiff(t *testing.T) {
	t.Parallel()
	got := solid(10, 10, color.RGBA{R: 255, A: 255})

	diff, share := compare(got, solid(10, 10, color.RGBA{A: 255}), linuxChannelTolerance)
	if share != 1 {
		t.Fatalf("share = %v, want 1", share)
	}
	actual, diffPath := keep(t, "scene", got, diff)
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Dir(actual)); err != nil {
			t.Error(err)
		}
	})
	for _, p := range []string{actual, diffPath} {
		if !strings.Contains(p, "scene.") {
			t.Fatalf("path %q does not name the scene", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing output: %v", err)
		}
	}
}
