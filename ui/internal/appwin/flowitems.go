package appwin

import (
	"image/color"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Items for kit.FlowRow: the header and footer rows of companion slots wrap
// to a second row instead of running past the slot's right edge (FUG-40).

// flowButton is rigidInboxButton as a FlowRow item.
func flowButton(env *kit.Env, gtx *layout.Context, onErr func(error), btn *kit.ButtonState, props kit.ButtonProps, onClick func()) kit.FlowItem {
	return kit.FlowItem{
		Size: btn.Measure(env, gtx, props).Size,
		Draw: func(g *layout.Context) {
			clicked, err := btn.Layout(env, g, props)
			if err != nil && onErr != nil {
				onErr(err)
			}
			if clicked && !props.Disabled && !props.Busy && onClick != nil {
				onClick()
			}
		},
	}
}

// flowTag is a tag as a FlowRow item.
func flowTag(env *kit.Env, gtx *layout.Context, onErr func(error), st *kit.TagState, props kit.TagProps) kit.FlowItem {
	dims, err := st.Measure(env, gtx, props)
	if err != nil && onErr != nil {
		onErr(err)
	}
	return kit.FlowItem{
		Size: dims.Size,
		Draw: func(g *layout.Context) {
			if _, err := st.Layout(env, g, props); err != nil && onErr != nil {
				onErr(err)
			}
		},
	}
}

// flowTitle is a one-line title as a FlowRow item; on a row narrower than
// the text it ends in an ellipsis rather than being clipped.
func flowTitle(env *kit.Env, gtx *layout.Context, tp theme.Type, fg color.NRGBA, txt string) kit.FlowItem {
	return kit.FlowItem{
		Size: kit.MeasureLabel(env, gtx, tp, txt),
		Draw: func(g *layout.Context) { kit.EllipsisLabel(env, g, tp, fg, txt) },
	}
}

// flowGap is the horizontal and vertical gap of every header and footer row.
func flowGap(gtx *layout.Context) (gap, rowGap int) {
	return gtx.Dp(theme.SpaceSM), gtx.Dp(theme.SpaceSM)
}

// atEnd marks it to sit at the right end of its row.
func atEnd(it kit.FlowItem) kit.FlowItem {
	it.End = true
	return it
}

// flowHeader lays a slot header: items wrap instead of running past the edge.
func flowHeader(gtx *layout.Context, items ...kit.FlowItem) layout.Dimensions {
	gap, rowGap := flowGap(gtx)
	return kit.FlowRow(gtx, gap, rowGap, items)
}
