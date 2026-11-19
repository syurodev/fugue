package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The field family's drawing and measurement half: the editor paint, the
// below-line, the shared metrics the variants measure from, and the icon
// crossfade. The core's input and motion half lives in editor.go; both files
// are one fieldCore package (UK-08) and change together.

// drawEditor lays the editor's text out with the materials the guide's
// macro pattern builds: the glyphs and the caret in fg, the selection in
// sel. The caller has clipped to the text area.
func drawEditor(env *Env, gtx *layout.Context, ed *widget.Editor, fg, sel color.NRGBA) layout.Dimensions {
	t := editorType()
	size, line := env.Content(t)
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: fg}.Add(gtx.Ops)
	textMat := m.Stop()
	sm := op.Record(gtx.Ops)
	paint.ColorOp{Color: sel}.Add(gtx.Ops)
	selMat := sm.Stop()
	ed.LineHeight = line
	ed.LineHeightScale = 1
	return ed.Layout(*gtx, env.Sh, fontOf(t), size, textMat, selMat)
}

// editorType is the type token of the text inside a field (§3.3).
func editorType() theme.Type { return theme.TypeBody() }

// belowLine draws the helper or error line: the two texts crossfade on
// swap (error wins at 1), wrapped within width (§6.8 — a long message
// wraps, it never runs past the field), clipped to the animated line
// height, so a new error line opens and content below the field moves
// with it. It returns the laid-out bounds of what it drew, local to its
// own origin, for the gallery's run recording (UK-E09).
func belowLine(env *Env, gtx *layout.Context, width int, helper, errMsg string, cHelper, cError color.NRGBA, fm fieldMotion) image.Rectangle {
	var bounds image.Rectangle
	if fm.linePx <= 0 {
		return bounds
	}
	defer clip.Rect(image.Rect(0, 0, width, int(fm.linePx+0.5))).Push(gtx.Ops).Pop()
	g := *gtx
	g.Constraints.Max.X = width
	if helper != "" && fm.swap < 1 {
		dims := drawText(env, &g, theme.TypeSmall(), fade(cHelper, 1-fm.swap), helper, true)
		bounds = bounds.Union(image.Rectangle{Max: dims.Size})
	}
	if errMsg != "" && fm.swap > 0 {
		dims := drawText(env, &g, theme.TypeSmall(), fade(cError, fm.swap), errMsg, true)
		bounds = bounds.Union(image.Rectangle{Max: dims.Size})
	}
	return bounds
}

// fieldSizes are the px metrics of one field frame, derived once for both
// the variant's measure and its layout: label height, box height and
// paddings, the below-line's gap. The below-line's own height is the
// animated state, carried separately from fieldLineHeight (UK-03).
type fieldSizes struct {
	labelH, boxH, padX, boxPadY int
	lineGapPx                   float32
}

// fieldMetrics derives fieldSizes at the current metric.
func fieldMetrics(env *Env, gtx *layout.Context, label string) fieldSizes {
	s := fieldSizes{
		boxH:      gtx.Dp(theme.ControlMD),
		padX:      gtx.Dp(theme.SpaceLG),
		boxPadY:   (gtx.Dp(theme.ControlMD) - labelLine(env, gtx, editorType())) / 2,
		lineGapPx: float32(gtx.Dp(theme.SpaceXS)),
	}
	if label != "" {
		s.labelH = labelLine(env, gtx, theme.TypeSmall()) + gtx.Dp(theme.SpaceXS)
	}
	return s
}

// total is the field's full height: label, box, gap and the below-line at
// linePx — the number a page places from.
func (s fieldSizes) total(linePx float32) int {
	return s.labelH + s.boxH + int(s.lineGapPx+linePx+0.5)
}

// wrappedTextHeight is the px height txt wraps to at width, without
// painting: the number the below-line opens to when txt shows.
func wrappedTextHeight(env *Env, gtx *layout.Context, txt string, width int) int {
	if txt == "" {
		return 0
	}
	m := op.Record(gtx.Ops)
	g := *gtx
	g.Constraints = layout.Constraints{Max: image.Pt(width, 1<<20)}
	dims := drawText(env, &g, theme.TypeSmall(), env.Th.Chrome().Fg, txt, true)
	m.Stop()
	return dims.Size.Y
}

// fieldLineHeight is the px height the below-line needs at width: the
// taller of the helper and — when the error shows — the error message,
// both wrapped (§6.8). While the two crossfade, both are visible, so the
// showing error reserves its own height too.
func fieldLineHeight(env *Env, gtx *layout.Context, helper, errMsg string, errored bool, width int) float32 {
	h := wrappedTextHeight(env, gtx, helper, width)
	if errored {
		h = max(h, wrappedTextHeight(env, gtx, errMsg, width))
	}
	return float32(h)
}

// fieldMinWidth is the natural width of a field: the widest of its label
// and its placeholder plus the horizontal paddings and the variant's
// slots, never under size.hit.min. slotW is the room the leading icon and
// the trailing button take.
func fieldMinWidth(env *Env, gtx *layout.Context, label, placeholder string, slotW int) int {
	w := slotW + measureLabel(env, gtx, editorType(), placeholder).X + 2*gtx.Dp(theme.SpaceLG)
	if label != "" {
		w = max(w, measureLabel(env, gtx, theme.TypeSmall(), label).X)
	}
	return max(w, gtx.Dp(theme.HitMin))
}

// crossfadeIcons draws a into b with the swap weight, each entry scaled up
// from 0.8 like every §3.7 content swap (the §6.3 icon-swap rule; the
// secret reveal is its first caller). Both slots are size×size at p.
func crossfadeIcons(env *Env, gtx *layout.Context, p image.Point, size unit.Dp, a, b IconName, c color.NRGBA, swap float32) error {
	if swap < 1 {
		if err := drawIconScaled(env, gtx, p, a, size, fade(c, 1-swap), 0.8+0.2*(1-swap)); err != nil {
			return err
		}
	}
	if swap > 0 {
		return drawIconScaled(env, gtx, p, b, size, fade(c, swap), 0.8+0.2*swap)
	}
	return nil
}
