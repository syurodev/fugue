package appwin

import (
	"image/color"

	"gioui.org/layout"
	"gioui.org/unit"
)

// layout writes dp, sp and colors directly, which only the theme may.
func layout(gtx layout.Context) layout.Dimensions {
	inset := layout.Inset{Top: unit.Dp(8)}
	label := unit.Sp(13)
	paint := color.NRGBA{R: 0x10}
	_, _, _ = inset, label, paint
	return layout.Dimensions{}
}
