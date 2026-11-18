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

// softTone derives a tinted fill (hue at 14% perceived over CanvasBg, like
// DangerSoft) and a foreground color shifted from hue toward Fg until it
// reaches at least 4.5:1 contrast over the rendered fill on every chrome
// background of the mode (UK-E04).
func softTone(c *Chrome, hue color.NRGBA) Tone {
	fill := perceived(c.CanvasBg, hue, 0.14)
	bgs := [5]color.NRGBA{c.AppBg, c.CanvasBg, c.SurfaceBase, c.SurfaceRaised, c.SurfaceTinted}
	var blended [5]color.NRGBA
	alpha := float64(fill.A) / 255
	for i, bg := range bgs {
		blend := func(b, f uint8) uint8 {
			return fromLinear(toLinearF(b) + alpha*(toLinearF(f)-toLinearF(b)))
		}
		blended[i] = color.NRGBA{R: blend(bg.R, fill.R), G: blend(bg.G, fill.G), B: blend(bg.B, fill.B), A: 0xff}
	}
	fg := hue
	for step := range 101 {
		t := float64(step) / 100
		cand := color.NRGBA{
			R: uint8(math.Round(float64(hue.R) + t*(float64(c.Fg.R)-float64(hue.R)))),
			G: uint8(math.Round(float64(hue.G) + t*(float64(c.Fg.G)-float64(hue.G)))),
			B: uint8(math.Round(float64(hue.B) + t*(float64(c.Fg.B)-float64(hue.B)))),
			A: 0xff,
		}
		ok := true
		for _, bg := range blended {
			if wcagContrast(cand, bg) < 4.5 {
				ok = false
				break
			}
		}
		if ok {
			fg = cand
			break
		}
	}
	return Tone{Fill: fill, Fg: fg}
}

func wcagContrast(a, b color.NRGBA) float64 {
	lum := func(c color.NRGBA) float64 {
		return 0.2126*toLinearF(c.R) + 0.7152*toLinearF(c.G) + 0.0722*toLinearF(c.B)
	}
	la, lb := lum(a), lum(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}
