package kitspike

import (
	"image/color"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The interaction wash tokens of §3.5, derived here until theme owns them.
// The spike proposes adding these exact derivations to theme (Fg-based
// washes work on every surface because Fg has full contrast there).

// HoverWash is the overlay when the pointer rests on a row, ghost button or
// tab.
func HoverWash(t *theme.Theme) color.NRGBA {
	return fade(t.Chrome().Fg, 0.07)
}

// PressedWash is the overlay while the control is held down.
func PressedWash(t *theme.Theme) color.NRGBA {
	return fade(t.Chrome().Fg, 0.14)
}

// SelectedWash is the background of the selected item; stronger than hover
// so it stays visible under a hover.
func SelectedWash(t *theme.Theme) color.NRGBA {
	return fade(t.Chrome().Fg, 0.11)
}

// FgDisabled is the text and icon color of a disabled control.
func FgDisabled(t *theme.Theme) color.NRGBA {
	return fade(t.Chrome().Fg, 0.35)
}

// Scrim is the dim behind a floating layer.
func Scrim() color.NRGBA {
	return color.NRGBA{A: 0x66}
}

// fade returns c with its alpha scaled by a, clamped to [0, 1].
func fade(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A) * clamp01(a))
	return c
}

// mix blends from a to b by t in [0, 1].
func mix(a, b color.NRGBA, t float32) color.NRGBA {
	t = clamp01(t)
	lerp := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t) }
	return color.NRGBA{R: lerp(a.R, b.R), G: lerp(a.G, b.G), B: lerp(a.B, b.B), A: lerp(a.A, b.A)}
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
