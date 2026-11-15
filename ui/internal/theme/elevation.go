package theme

import (
	"image/color"

	"gioui.org/unit"
)

// ElevationLayer is one layer of the floating-surface shadow of ui-kit §3.6:
// a translucent rounded rect that grows by Grow on every side and sags by
// Sag further down, at Alpha opacity. Gio has no blur; stacking a few of
// these reads as a soft shadow, and the spike measured the stack at a p50 of
// 0.64 ms per frame — close enough to free.
type ElevationLayer struct {
	Grow  unit.Dp
	Sag   unit.Dp
	Alpha float32
}

// ElevationFloat is the one shadow token of §3.6, shared by menu, popover,
// toast and dialog; no other element casts a shadow. The layer count,
// offsets and alphas were fixed in kit v1 (F10.8) from the spike's shadow
// measurements: four layers, each 3 dp wider than the last, sagging 1 dp
// more each, fading from 10% to 2%.
func ElevationFloat() [4]ElevationLayer {
	return [4]ElevationLayer{
		{Grow: 2, Sag: 1, Alpha: 0.10},
		{Grow: 5, Sag: 2, Alpha: 0.06},
		{Grow: 8, Sag: 3, Alpha: 0.035},
		{Grow: 11, Sag: 4, Alpha: 0.02},
	}
}

// ShadowInk returns the ink of one shadow layer: plain black at the layer's
// alpha. The shadow's color is a theme decision like any other, so no
// component mixes its own ink (UK-03).
func ShadowInk(alpha float32) color.NRGBA {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	return color.NRGBA{A: uint8(alpha*255 + 0.5)}
}
