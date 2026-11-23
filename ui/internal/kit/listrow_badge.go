package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// drawRowBadge draws a short count as a neutral pill (FillSoft, TypeSmall in
// FgMuted) at the trailing edge and returns the new right edge. The count
// is text, so the pill carries no information in its color.
func drawRowBadge(env *Env, gtx *layout.Context, pill image.Rectangle, h, right, gap int, badge string, muted color.NRGBA, pres float32) int {
	if badge == "" {
		return right
	}
	padX := gtx.Dp(theme.SpaceSM)
	sz := measureLabel(env, gtx, theme.TypeSmall(), badge)
	w := max(sz.X+2*padX, h*3/4)
	ph := min(sz.Y+gtx.Dp(theme.SpaceXS), h)
	box := image.Rect(right-w, pill.Min.Y+(h-ph)/2, right, pill.Min.Y+(h+ph)/2)
	fillLayer(gtx, box, ph/2, env.Th.Chrome().FillSoft, pres)
	pt := image.Pt(box.Min.X+(w-sz.X)/2, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
	drawRunAt(env, gtx, pt, 0, h, theme.TypeSmall(), muted, badge, false)
	return box.Min.X - gap
}

// secondaryDot returns the faded status-dot color of the secondary text, or
// nil when the row draws none.
func (s *ListRowState) secondaryDot(env *Env, p *ListRowProps, pres float32) *color.NRGBA {
	if !p.SecondaryDot || p.Secondary == "" || p.Disabled {
		return nil
	}
	c := fade(s.toneColor(env, p.SecondaryTone), pres)
	return &c
}

// drawSecondaryDot paints the status dot before the secondary text and
// returns the x where the text starts.
func drawSecondaryDot(gtx *layout.Context, pill image.Rectangle, h, x int, c color.NRGBA) int {
	d := gtx.Dp(theme.SpaceSM)
	r := image.Rect(x, pill.Min.Y+(h-d)/2, x+d, pill.Min.Y+(h+d)/2)
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, d/2).Op(gtx.Ops))
	return x + d + gtx.Dp(theme.SpaceXS)
}
