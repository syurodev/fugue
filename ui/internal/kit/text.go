package kit

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Text primitives, ui-kit §3.3: every string the app draws goes through one
// of these with a type token from theme, so line heights, weights and the
// content text setting are decided in exactly one place. Callers pass a
// theme color (Fg, FgMuted, …); the kit never mixes a color of its own
// (UK-03).

// Label draws a single line of txt in token t and color c: control labels,
// names, metadata. It honors the constraints' width but never wraps.
func Label(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string) layout.Dimensions {
	return drawText(env, gtx, t, c, txt, false)
}

// EllipsisLabel draws a single line of txt in token t and color c, truncated
// with an ellipsis ("…") to fit within gtx.Constraints.Max.X.
func EllipsisLabel(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string) layout.Dimensions {
	if gtx.Constraints.Max.X <= 0 || txt == "" {
		return layout.Dimensions{}
	}
	size, line := env.Content(t)
	g := *gtx
	g.Constraints.Min = image.Point{}
	g.Constraints.Max.Y = 1 << 20
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	l := widget.Label{
		Alignment:  text.Start,
		MaxLines:   1,
		Truncator:  "…",
		WrapPolicy: text.WrapGraphemes,
	}
	if t.Line != 0 {
		l.LineHeight = line
		l.LineHeightScale = 1
	}
	return l.Layout(g, env.Sh, fontOf(t), size, txt, m.Stop())
}

// MiddleEllipsisLabel draws a single line of txt in token t and color c,
// ellipsizing in the middle (keeping any trailing "/name" segment when space
// allows) so the text fits within gtx.Constraints.Max.X.
func MiddleEllipsisLabel(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	if maxW <= 0 || txt == "" {
		return layout.Dimensions{}
	}
	if measureLabel(env, gtx, t, txt).X <= maxW {
		return Label(env, gtx, t, c, txt)
	}
	return EllipsisLabel(env, gtx, t, c, fitMiddleEllipsis(env, gtx, t, txt, maxW))
}

// MeasureLabel returns the unconstrained single-line size of txt in token t.
func MeasureLabel(env *Env, gtx *layout.Context, t theme.Type, txt string) image.Point {
	return measureLabel(env, gtx, t, txt)
}

func fitMiddleEllipsis(env *Env, gtx *layout.Context, t theme.Type, txt string, maxW int) string {
	if slash := strings.LastIndexByte(txt, '/'); slash > 0 {
		tail := txt[slash:]
		if measureLabel(env, gtx, t, "…"+tail).X <= maxW {
			prefix := []rune(txt[:slash])
			best := binarySearchFit(0, len(prefix), func(n int) bool {
				return measureLabel(env, gtx, t, string(prefix[:n])+"…"+tail).X <= maxW
			})
			return string(prefix[:best]) + "…" + tail
		}
	}
	runes := []rune(txt)
	best := binarySearchFit(1, len(runes)-1, func(n int) bool {
		tail := (n + 1) / 2
		head := n - tail
		return measureLabel(env, gtx, t, string(runes[:head])+"…"+string(runes[len(runes)-tail:])).X <= maxW
	})
	if best <= 0 {
		return txt
	}
	tail := (best + 1) / 2
	head := best - tail
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

func binarySearchFit(lo, hi int, fits func(int) bool) int {
	best := -1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if fits(mid) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// Paragraph draws txt in token t and color c, wrapped at the constraints'
// width: prose, descriptions, helper text.
func Paragraph(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string) layout.Dimensions {
	return drawText(env, gtx, t, c, txt, true)
}

// fontOf returns the face of a type token: Inter for text, JetBrains Mono
// for code (§3.3, DA-04).
func fontOf(t theme.Type) font.Font {
	if t.Mono {
		return font.Font{Typeface: fonts.Mono, Style: font.Regular, Weight: t.Weight}
	}
	return font.Font{Typeface: fonts.Sans, Style: font.Regular, Weight: t.Weight}
}

// drawText lays txt out with t's absolute line height (LineHeightScale 1,
// §3.0) and paints it in c; wrap wraps at the constraints' width, otherwise
// the text stays on one line.
func drawText(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string, wrap bool) layout.Dimensions {
	size, line := env.Content(t)
	g := *gtx
	g.Constraints.Min = image.Point{}
	g.Constraints.Max.Y = 1 << 20
	if wrap {
		g.Constraints.Max.X = gtx.Constraints.Max.X
	} else {
		g.Constraints.Max.X = 1 << 20
	}
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	l := widget.Label{Alignment: text.Start, MaxLines: 0, WrapPolicy: text.WrapWords}
	if t.Line == 0 {
		// codeInline: the line height belongs to the containing text; the
		// token is only ever laid inside a prose run, so leave Gio's
		// inherited metrics alone rather than forcing an absolute height.
	} else {
		l.LineHeight = line
		l.LineHeightScale = 1
	}
	return l.Layout(g, env.Sh, fontOf(t), size, txt, m.Stop())
}
