package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// DrawFloatShadow paints the elevation.float shadow of ui-kit §3.6 under r,
// with corners of radius, for a floating surface (menu, popover, toast,
// dialog). It is the only shadow in the app: the layers, offsets and alphas
// come from theme.ElevationFloat, and the ink is plain black — components
// never mix a shadow of their own. Gio has no blur, so the softness is the
// layered translucent rounded rects themselves; the spike measured the
// stack at a p50 of 0.64 ms per frame over 120 frames.
func DrawFloatShadow(gtx *layout.Context, r image.Rectangle, radius int) {
	for _, l := range theme.ElevationFloat() {
		grow := gtx.Dp(l.Grow)
		sag := gtx.Dp(l.Sag)
		// Grow on every side, but sag further down: a cast shadow, not a
		// halo. The corners widen with the layer so the rects stay nested.
		rr := image.Rectangle{
			Min: r.Min.Sub(image.Pt(grow, grow-sag)),
			Max: r.Max.Add(image.Pt(grow, grow+sag)),
		}
		fillRRect(gtx.Ops, rr, radius+grow, theme.ShadowInk(l.Alpha))
	}
}

// fillRRect fills a rounded rectangle with c.
func fillRRect(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	paint.FillShape(ops, c, clip.UniformRRect(r, radius).Op(ops))
}
