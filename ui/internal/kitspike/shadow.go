package kitspike

import (
	"image"
	"sync"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// The two soft-shadow candidates of §3.6, measured against no shadow in the
// gallery's bench (Q6). Gio has no blur shader: the layered version widens
// translucent rounded rects; the texture version pre-blurs once at build
// time and stretches the sprite per frame.

// shadowLayers are the alpha steps of the layered shadow, each growing by 3
// dp and sagging like a cast shadow.
var shadowLayers = []struct {
	grow  int
	alpha float32
}{
	{2, 0.10}, {5, 0.06}, {8, 0.035}, {11, 0.02},
}

// LayeredShadow draws a soft shadow under r with four widening translucent
// layers.
func LayeredShadow(gtx *layout.Context, r image.Rectangle, radius int, strength float32) {
	for i, l := range shadowLayers {
		grow := gtx.Dp(unit.Dp(l.grow))
		sag := gtx.Dp(unit.Dp(1 + i))
		rr := image.Rectangle{Min: r.Min.Sub(image.Pt(grow, grow-sag)), Max: r.Max.Add(image.Pt(grow, grow+sag))}
		fillRRect(gtx.Ops, rr, radius+grow, fade(black, l.alpha*strength))
	}
}

// shadowMargin is the transparent margin baked around the sprite.
const shadowMargin = 24

// shadowSprite is the pre-blurred shadow texture, built once per process:
// a 160×120 sprite whose solid rect is the 112×72 middle.
var shadowSprite *image.RGBA
var shadowSpriteSize = image.Pt(160, 120)
var shadowCore = image.Rect(shadowMargin, shadowMargin, shadowSpriteSize.X-shadowMargin, shadowSpriteSize.Y-shadowMargin)

var spriteOnce sync.Once

func buildSprite() {
	src := image.NewRGBA(image.Rect(0, 0, shadowSpriteSize.X, shadowSpriteSize.Y))
	for y := shadowCore.Min.Y; y < shadowCore.Max.Y; y++ {
		for x := shadowCore.Min.X; x < shadowCore.Max.X; x++ {
			src.Set(x, y, black)
		}
	}
	blurAlpha(src, 7, 3)
	shadowSprite = src
	shadowImageOp = paint.NewImageOp(src)
}

// blurAlpha blurs the alpha channel of m in place with `passes` box blurs of
// the given radius: three passes of a box blur approximate a gaussian well
// enough for a shadow.
func blurAlpha(m *image.RGBA, radius, passes int) {
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	a := make([]float32, w*h)
	for i := range a {
		a[i] = float32(m.Pix[i*4+3])
	}
	tmp := make([]float32, len(a))
	for p := 0; p < passes; p++ {
		blurPass(a, tmp, w, h, radius, true)
		blurPass(tmp, a, w, h, radius, false)
	}
	for i := range a {
		m.Pix[i*4+3] = uint8(min(a[i], 255))
	}
}

func blurPass(src, dst []float32, wd, ht, r int, horizontal bool) {
	for y := 0; y < ht; y++ {
		for x := 0; x < wd; x++ {
			var sum float32
			n := 0
			for d := -r; d <= r; d++ {
				xx, yy := x, y
				if horizontal {
					xx = x + d
				} else {
					yy = y + d
				}
				if xx < 0 || xx >= wd || yy < 0 || yy >= ht {
					continue
				}
				sum += src[yy*wd+xx]
				n++
			}
			dst[y*wd+x] = sum / float32(n)
		}
	}
}

// shadowImageOp uploads the sprite once; NewImageOp caches by handle.
var shadowImageOp paint.ImageOp

// TextureShadow draws the pre-blurred sprite stretched so its core covers r
// (the transparent margin hangs outside it).
func TextureShadow(gtx *layout.Context, r image.Rectangle) {
	spriteOnce.Do(buildSprite)
	x0, y0 := float32(r.Min.X-shadowMargin), float32(r.Min.Y-shadowMargin)
	dw := float32(r.Dx() + 2*shadowMargin)
	dh := float32(r.Dy() + 2*shadowMargin)
	sx := dw / float32(shadowSpriteSize.X)
	sy := dh / float32(shadowSpriteSize.Y)
	defer op.Affine(f32.NewAffine2D(sx, 0, x0, 0, sy, y0)).Push(gtx.Ops).Pop()
	defer clip.Rect(image.Rectangle{Max: shadowSpriteSize}).Push(gtx.Ops).Pop()
	shadowImageOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}
