package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// LineSize selects the height of a LineRow or Progress line row (ui-kit §3.9).
type LineSize uint8

const (
	// LineMD is the default 14 dp line height.
	LineMD LineSize = iota
	// LineSM is the compact 10 dp line height.
	LineSM
)

// LineRowProps configures one frame of a LineRow (ui-kit §3.9). ColorAt returns
// the solid color for line i of count; Fallback fills any line when ColorAt
// is nil or returns a zero-alpha color.
type LineRowProps struct {
	Size     LineSize
	Fallback color.NRGBA
	ColorAt  func(i, count int) color.NRGBA
}

// LineRowCount returns floor((width + gap) / (lineW + gap)), the number of
// lines of width lineW separated by gap that fit inside width (ui-kit §3.9).
func LineRowCount(width, lineW, gap int) int {
	if width <= 0 || lineW <= 0 || gap < 0 {
		return 0
	}
	return (width + gap) / (lineW + gap)
}

func lineHeightToken(sz LineSize) unit.Dp {
	if sz == LineSM {
		return theme.LineHeightSM
	}
	return theme.LineHeight
}

func lineRowBox(gtx *layout.Context, sz LineSize) image.Point {
	h := max(gtx.Dp(lineHeightToken(sz)), 2)
	w := gtx.Constraints.Max.X
	if w <= 0 {
		w = gtx.Constraints.Min.X
	}
	return gtx.Constraints.Constrain(image.Pt(w, h))
}

// LineRowMeasure returns the LineRow's natural box without drawing.
func LineRowMeasure(gtx *layout.Context, sz LineSize) layout.Dimensions {
	return layout.Dimensions{Size: lineRowBox(gtx, sz)}
}

// LineRow draws separate vertical rounded lines anchored to the left edge
// (ui-kit §3.9), with no track, no outer corner radius, and no animation.
func LineRow(_ *Env, gtx *layout.Context, p LineRowProps) layout.Dimensions {
	box := lineRowBox(gtx, p.Size)
	lineW := max(gtx.Dp(theme.LineWidth), 1)
	gap := max(gtx.Dp(theme.LineGap), 0)
	count := LineRowCount(box.X, lineW, gap)
	radius := max(min(lineW, box.Y)/2, 1)
	step := lineW + gap
	for i := range count {
		c := p.Fallback
		if p.ColorAt != nil {
			if chosen := p.ColorAt(i, count); chosen.A != 0 {
				c = chosen
			}
		}
		if c.A == 0 {
			continue
		}
		x0 := i * step
		fillRRect(gtx.Ops, image.Rect(x0, 0, x0+lineW, box.Y), radius, c)
	}
	return layout.Dimensions{Size: box}
}
