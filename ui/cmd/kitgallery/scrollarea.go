package main

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	saVertical   = "vertical"
	saHorizontal = "horizontal"
	saStream     = "streaming"
)

type scrollAreaPage struct {
	vert   *kit.ScrollAreaState
	horiz  *kit.ScrollAreaState
	stream *kit.ScrollAreaState
}

func newScrollAreaPage() scrollAreaPage {
	return scrollAreaPage{
		vert:   kit.NewScrollAreaState(),
		horiz:  kit.NewScrollAreaState(),
		stream: kit.NewScrollAreaState(),
	}
}

func (p *pages) layoutScrollAreas(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "ScrollArea §6.29: vertical or horizontal non-virtualized scroll, thin radius.full bar fading in on scroll/hover and out after ~1 s idle, no nested same-axis scroll, non-yanking streaming.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.scrollSpecBlock(gtx, dry, saVertical, layout.Vertical, p.scroll.vert, false)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.scrollSpecBlock(gtx, dry, saHorizontal, layout.Horizontal, p.scroll.horiz, false)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.scrollSpecBlock(gtx, dry, saStream, layout.Vertical, p.scroll.stream, true)
		},
	})
}

func (p *pages) scrollSpecBlock(gtx *layout.Context, dry bool, name string, axis layout.Axis, st *kit.ScrollAreaState, follow bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	w, h := gtx.Dp(unit.Dp(260)), gtx.Dp(unit.Dp(140))
	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, name, c.FgMuted)
		cd := cellDraw{
			key: cell{compScrollArea, 0, 0, name},
			box: image.Pt(w, h),
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Exact(image.Pt(w, h))
				st.Layout(p.env, &g, kit.ScrollAreaProps{Axis: axis, FollowBottom: follow}, func(gtx *layout.Context) layout.Dimensions {
					if axis == layout.Horizontal {
						txt := "Horizontal scrollable content line 1 — keeps scrolling without wrapping past the viewport width."
						dims := kit.Label(p.env, gtx, theme.TypeBody(), c.Fg, txt)
						return layout.Dimensions{Size: image.Pt(dims.Size.X+gtx.Dp(theme.SpaceXL), h)}
					}
					cy := 0
					for i := range 14 {
						pt := image.Pt(gtx.Dp(theme.SpaceMD), cy)
						at(gtx, pt, func(gtx *layout.Context) {
							kit.Label(p.env, gtx, theme.TypeSmall(), c.Fg, fmt.Sprintf("Scrollable line %d", i+1))
						})
						cy += line + gtx.Dp(theme.SpaceXS)
					}
					return layout.Dimensions{Size: image.Pt(w, cy)}
				})
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(w, y+h)
}
