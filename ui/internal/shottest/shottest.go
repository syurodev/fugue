package shottest

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The golden PNGs are Linux references rendered through Mesa's software GL
// (llvmpipe), the renderer of the ubuntu CI job and of a developer's Xvfb. A
// second run on the same machine is byte-identical, so on Linux the only
// slack needed is a guard against a Mesa or font-stack bump moving an
// antialiased edge by a few levels.
//
// Other platforms rasterize through their own GL driver (Metal on macOS),
// which changes antialiasing on glyph and rounded-corner edges but not
// layout. They get a looser budget: still far below what a moved or
// overlapping block changes (a one-line shift of a text block alters several
// percent of the frame), yet enough for edge pixels. The values are
// estimates, not measurements of a macOS run; if the macOS job fails on
// edge noise alone, widen the darwin pair here and note the measured diff.
const (
	// linuxChannelTolerance is the largest per-channel difference (0-255) a
	// pixel may have and still count as equal on Linux.
	linuxChannelTolerance = 8
	// linuxMaxDiffShare is the share of pixels that may differ by more than
	// the tolerance on Linux.
	linuxMaxDiffShare = 0.001

	// otherChannelTolerance and otherMaxDiffShare are the same budget on
	// every other platform, see above.
	otherChannelTolerance = 40
	otherMaxDiffShare     = 0.01
)

// goldenDir is where goldens live, relative to the test package's directory
// (go test runs there).
const goldenDir = "testdata/shots"

// Compare fails t when got differs from testdata/shots/<name>.png by more
// than the platform budget. On failure it writes <name>.actual.png and
// <name>.diff.png to a fresh temporary directory (never into testdata),
// logs both paths, and reports the differing-pixel share. Update a golden
// with the package's regenerate test, not by copying the actual file.
func Compare(t *testing.T, name string, got *image.RGBA) {
	t.Helper()
	compareIn(t, goldenDir, name, got)
}

func compareIn(t *testing.T, dir, name string, got *image.RGBA) {
	t.Helper()
	tolerance, maxShare := budget(runtime.GOOS)
	want, err := loadGolden(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Errorf("shot %s: %v (generate it with the package's regenerate test: go test -tags regenerate -run Regenerate)", name, err)
		return
	}
	if got.Bounds() != want.Bounds() {
		actual, _ := keep(t, name, got, nil)
		t.Errorf("shot %s: size %v, golden is %v\n  actual: %s", name, got.Bounds().Size(), want.Bounds().Size(), actual)
		return
	}
	diff, share := compare(got, want, tolerance)
	if share <= maxShare {
		return
	}
	actual, diffPath := keep(t, name, got, diff)
	t.Errorf("shot %s: %.4f%% of pixels differ by more than %d/255 (budget %.4f%%)\n  actual: %s\n  diff:   %s",
		name, share*100, tolerance, maxShare*100, actual, diffPath)
}

func budget(goos string) (tolerance int, maxShare float64) {
	if goos == "linux" {
		return linuxChannelTolerance, linuxMaxDiffShare
	}
	return otherChannelTolerance, otherMaxDiffShare
}

func loadGolden(path string) (*image.RGBA, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read golden: %w", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode golden %s: %w", path, err)
	}
	rgba := image.NewRGBA(decoded.Bounds())
	draw.Draw(rgba, rgba.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return rgba, nil
}

// compare returns a diff image (differing pixels red over a dimmed copy of
// want) and the share of pixels where any channel differs by more than
// tolerance. Both images have equal bounds.
func compare(got, want *image.RGBA, tolerance int) (*image.RGBA, float64) {
	b := want.Bounds()
	diff := image.NewRGBA(b)
	differing := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gi, wi := got.PixOffset(x, y), want.PixOffset(x, y)
			di := diff.PixOffset(x, y)
			if channelDelta(got.Pix[gi:gi+4], want.Pix[wi:wi+4]) > tolerance {
				differing++
				copy(diff.Pix[di:di+4], []byte{255, 0, 0, 255})
				continue
			}
			w := want.Pix[wi : wi+3]
			copy(diff.Pix[di:di+4], []byte{w[0]/4 + 160, w[1]/4 + 160, w[2]/4 + 160, 255})
		}
	}
	pixels := b.Dx() * b.Dy()
	if pixels == 0 {
		return diff, 0
	}
	return diff, float64(differing) / float64(pixels)
}

// channelDelta is the largest difference over the four channels of two
// pixels given as RGBA byte quadruples.
func channelDelta(a, b []byte) int {
	d := 0
	for i := range 4 {
		d = max(d, abs(int(a[i])-int(b[i])))
	}
	return d
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// diffDir creates the directory for one failure's images. It is not
// t.TempDir because that directory is deleted when the test ends, before
// anyone can open the images the failure message points to.
func diffDir() (string, error) {
	dir, err := os.MkdirTemp("", "fugue-shot-")
	if err != nil {
		return "", fmt.Errorf("create diff directory: %w", err)
	}
	return dir, nil
}

// keep writes the actual (and, when non-nil, diff) image to a temporary
// directory that outlives the test.
func keep(t *testing.T, name string, actual, diff *image.RGBA) (actualPath, diffPath string) {
	t.Helper()
	dir, err := diffDir()
	if err != nil {
		t.Errorf("shot %s: %v", name, err)
		return "", ""
	}
	actualPath = filepath.Join(dir, name+".actual.png")
	writePNG(t, actualPath, actual)
	if diff != nil {
		diffPath = filepath.Join(dir, name+".diff.png")
		writePNG(t, diffPath, diff)
	}
	return actualPath, diffPath
}

func writePNG(t *testing.T, path string, img *image.RGBA) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Errorf("encode %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Errorf("write %s: %v", path, err)
	}
}
