package kit

import (
	"image"
	"image/color"

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
