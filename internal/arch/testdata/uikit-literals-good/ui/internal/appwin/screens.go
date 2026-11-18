package appwin

import (
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// layout sizes everything from the tokens; the window size arrives computed.
func layout(gtx layout.Context, th theme.Theme, size unit.Dp) layout.Dimensions {
	inset := layout.Inset{Top: theme.SpaceMD, Left: theme.SpaceLG}
	return inset.Layout(gtx, func(layout.Context) layout.Dimensions { return layout.Dimensions{} })
}
