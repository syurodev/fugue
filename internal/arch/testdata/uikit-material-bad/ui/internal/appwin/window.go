package appwin

import (
	"gioui.org/layout"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// drawWindow imports material directly, which only kit, theme and mdview may.
func drawWindow(gtx *layout.Context, th theme.Theme) {
	l := material.Label(th.Material(), 13, "status")
	_ = l
}
