package theme_test

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestWashTokensRenderTheirPerceivedValue covers the §3.5 targets on the
// real pipeline: every fill and wash token — FillSoft, FillSoftHover,
// BorderSubtle, DangerSoft, the hover, pressed and selected washes and the
// ripples — is painted headlessly over the mode's main surface and the
// pixel it leaves is within ±2 per channel of the sRGB composite of its
// tint at the spec fraction. Gio's sRGB framebuffer blends in linear light,
// so an unsolved Fg-at-6% would render as 0x44 over the dark canvas, not
// the 0x27 a designer's 6% means.
func TestWashTokensRenderTheirPerceivedValue(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(16, 16)
	if err != nil {
		t.Fatalf("open a headless window: %v", err)
	}
	for _, m := range bothModes() {
		c := m.theme.Chrome()
		onSurface, onAction := washTargets(&m)
		for _, tc := range append(onSurface, onAction) {
			surface := c.CanvasBg
			if tc.name == onAction.name {
				surface = c.Action
			}
			var ops op.Ops
			gtx := layout.Context{Ops: &ops, Constraints: layout.Exact(image.Pt(16, 16))}
			paint.Fill(gtx.Ops, surface)
			paint.Fill(gtx.Ops, tc.got)
			if err := win.Frame(&ops); err != nil {
				t.Fatalf("render %s: %v", tc.name, err)
			}
			img := image.NewRGBA(image.Rect(0, 0, 16, 16))
			if err := win.Screenshot(img); err != nil {
				t.Fatalf("read %s back: %v", tc.name, err)
			}
			p := img.RGBAAt(8, 8)
			checkPerceived(t, m.name, tc, surface, color.NRGBA{R: p.R, G: p.G, B: p.B, A: 0xff})
		}
	}
}
