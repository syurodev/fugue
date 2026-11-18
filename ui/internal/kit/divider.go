package kit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Orientation selects the axis of Divider and SplitPane (§6.6, §6.27).
type Orientation uint8

const (
	// Horizontal lays out a horizontal line (Divider) or splits panes left
	// and right across a vertical handle (SplitPane).
	Horizontal Orientation = iota
	// Vertical lays out a vertical line (Divider) or splits panes top and
	// bottom across a horizontal handle (SplitPane).
	Vertical
)

// DividerProps are the per-frame properties of Divider (ui-kit §6.6).
type DividerProps struct {
	Orientation Orientation
}

// Divider draws a stroke.hairline rule in Border across the available
// width (Horizontal) or height (Vertical) (ui-kit §6.6).
//
//	kit.Divider(env, gtx, kit.DividerProps{Orientation: kit.Horizontal})
func Divider(env *Env, gtx *layout.Context, p DividerProps) layout.Dimensions {
	thick := max(gtx.Dp(theme.Hairline), 1)
	var size image.Point
	if p.Orientation == Vertical {
		h := max(gtx.Constraints.Max.Y, gtx.Constraints.Min.Y)
		if h <= 0 {
			h = thick
		}
		size = gtx.Constraints.Constrain(image.Pt(thick, h))
	} else {
		w := max(gtx.Constraints.Max.X, gtx.Constraints.Min.X)
		if w <= 0 {
			w = thick
		}
		size = gtx.Constraints.Constrain(image.Pt(w, thick))
	}
	paint.FillShape(gtx.Ops, env.Th.Chrome().Border, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}
