package kitspike

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"golang.org/x/image/math/fixed"
)

// The small public surface the gallery command uses.

// Color is the color type of the theme tokens.
type Color = color.NRGBA

// Size is a dp size.
type Size = unit.Dp

// FillRect fills r with c (the window background).
func FillRect(gtx *layout.Context, r image.Rectangle, c Color) {
	fillRect(gtx.Ops, r, c)
}

// FillRRect fills a rounded rectangle with c.
func FillRRect(gtx *layout.Context, r image.Rectangle, radius int, c Color) {
	fillRRect(gtx.Ops, r, radius, c)
}

// StrokeRRect strokes a rounded rectangle's outline.
func StrokeRRect(gtx *layout.Context, r image.Rectangle, radius int, width float32, c Color) {
	strokeRRect(gtx.Ops, r, radius, width, c)
}

// DrawText lays txt out with a type token and paints it in c.
func DrawText(gtx *layout.Context, sh *text.Shaper, t Type, maxWidth int, txt string, c Color) layout.Dimensions {
	return drawText(gtx, sh, t, maxWidth, txt, c)
}

// MeasureText measures txt laid out with a type token.
func MeasureText(gtx *layout.Context, sh *text.Shaper, t Type, maxWidth int, txt string) image.Point {
	return measureText(gtx, sh, t, maxWidth, txt)
}

// DrawIcon draws one packaged icon with the fresh path of Q5(a).
func DrawIcon(gtx *layout.Context, name string, size Size, c Color) error {
	return drawIconFresh(gtx, name, size, c)
}

// DrawIconCached draws one packaged icon with the cached stroke of Q5(b).
func DrawIconCached(gtx *layout.Context, name string, size Size, c Color) error {
	return drawIconCached(gtx, name, size, c)
}

// IconNames lists the packaged icons.
func IconNames() []string { return iconNames }

// VietnameseInk is the §3.3 stress string: stacked double diacritics are the
// tallest ink Inter produces.
const VietnameseInk = "Ễ Ặ Ỡ Ậ ỷ ộ"

// TextParameters builds the shaping parameters of a type token: absolute
// line height, LineHeightScale 1 (§3.0), wrapped at maxWidth px.
func TextParameters(sh *text.Shaper, t Type, m unit.Metric, maxWidth int) text.Parameters {
	return text.Parameters{
		Font:            t.font(),
		PxPerEm:         fixed.I(m.Sp(t.Size)),
		MaxWidth:        maxWidth,
		LineHeight:      fixed.I(m.Sp(t.Line)),
		LineHeightScale: 1,
		WrapPolicy:      text.WrapWords,
	}
}

// LayeredShadowTex exposes the layered shadow of §3.6 for the bench.
func LayeredShadowTex(gtx *layout.Context, r image.Rectangle, radius int, strength float32) {
	LayeredShadow(gtx, r, radius, strength)
}

// BlurShadowTex exposes the texture shadow of §3.6 for the bench.
func BlurShadowTex(gtx *layout.Context, r image.Rectangle) {
	TextureShadow(gtx, r)
}
