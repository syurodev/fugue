package theme

import (
	"image/color"
	"math"
)

// The §3.5 fills and washes are perceived targets: "Fg at 6%" means the
// color an sRGB composite of Fg at 6% over the surface gives. Gio draws into
// an sRGB framebuffer and blends in linear light, where the same alpha lifts
// a dark surface several times higher (Fg at 6% over a dark canvas renders as
// 0x44, not 0x21) and a light surface barely at all. So every translucent
// token is solved here, once per mode, to land on its target when blended
// over the mode's main surface; over any other chrome surface it is close,
// and UK-E04 checks the contrast of the real blend on all of them.

// washCarrier is the smallest alpha a solved token is drawn at: large
// enough that the 8-bit color carries the target precisely, which a
// 3-in-255 alpha of the small dark-mode fractions could not. A deep tint
// whose color would fall outside the gamut at this alpha — a 30% red over
// white needs a blue below black — takes the next alpha that fits.
const washCarrier = 64

// perceived returns the translucent color that, blended in linear light over
// ref, gives the sRGB composite of tint at fraction over ref: per channel,
// the linear color c with ref + α(c − ref) = linear(sRGB-composite), at the
// smallest alpha from washCarrier up for which every channel is a valid
// color.
func perceived(ref, tint color.NRGBA, fraction float32) color.NRGBA {
	var out color.NRGBA
	for a := washCarrier; a <= 255; a++ {
		alpha := float64(a) / 255
		fits := true
		solve := func(r, t uint8) uint8 {
			target := toLinearF(uint8(math.Round(float64(r) + float64(fraction)*(float64(t)-float64(r)))))
			c := toLinearF(r) + (target-toLinearF(r))/alpha
			fits = fits && c >= 0 && c <= 1
			return fromLinear(math.Min(math.Max(c, 0), 1))
		}
		out = color.NRGBA{R: solve(ref.R, tint.R), G: solve(ref.G, tint.G), B: solve(ref.B, tint.B), A: uint8(a)}
		if fits {
			break
		}
	}
	return out
}

// toLinearF converts one sRGB channel to linear light.
func toLinearF(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// fromLinear converts linear light to one sRGB channel.
func fromLinear(l float64) uint8 {
	s := l * 12.92
	if l > 0.0031308 {
		s = 1.055*math.Pow(l, 1/2.4) - 0.055
	}
	return uint8(math.Round(math.Min(math.Max(s, 0), 1) * 255))
}

// placeholderOf derives the placeholder text tone: halfway from FgMuted
// toward Fg, the point where the text keeps its 4.5:1 budget over the
// lifted fills of every chrome surface in this mode (UK-E04; the fields'
// contrast test pins every combination).
func placeholderOf(muted, fg color.NRGBA) color.NRGBA {
	ch := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + 0.5*(float64(y)-float64(x))))
	}
	return color.NRGBA{R: ch(muted.R, fg.R), G: ch(muted.G, fg.G), B: ch(muted.B, fg.B), A: muted.A}
}
