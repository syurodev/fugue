package glwin

import (
	"image"
	"image/color"
	"strconv"
	"testing"

	"gioui.org/op"
	"gioui.org/op/paint"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestOpenedWindowsRenderTogether covers the reason the package exists:
// several windows of different sizes, opened at once and kept, all render —
// the scenario that failed with "eglInitialize failed: 0x3001" when windows
// were released and reopened under software GL.
func TestOpenedWindowsRenderTogether(t *testing.T) {
	t.Parallel()
	for _, size := range []int{8, 16, 24, 32} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			win, err := Open(size, size)
			if err != nil {
				t.Fatalf("open a window: %v", err)
			}
			var ops op.Ops
			paint.Fill(&ops, color.NRGBA{R: 0xff, A: 0xff})
			if err := win.Frame(&ops); err != nil {
				t.Fatalf("render: %v", err)
			}
			img := image.NewRGBA(image.Rect(0, 0, size, size))
			if err := win.Screenshot(img); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got := img.RGBAAt(size/2, size/2); got.R < 0xfa || got.G > 5 {
				t.Fatalf("the pixel is %v, want red", got)
			}
		})
	}
}
