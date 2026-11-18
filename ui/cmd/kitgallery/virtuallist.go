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
	vlDefault = "default"
	vlFollow  = "stick-to-bottom"
	vlHover   = "bar-visible"
	vlLive    = "live-10k"
)

type virtualListPage struct {
	restSt   *kit.VirtualListState
	followSt *kit.VirtualListState
	hoverSt  *kit.VirtualListState
	liveSt   *kit.VirtualListState
	insertB  *kit.ButtonState
	followB  *kit.ButtonState
	followOn bool
	prepends int
	items    []kit.VirtualItem
}

func newVirtualListPage() virtualListPage {
	items := make([]kit.VirtualItem, 10000)
	for i := range items {
		items[i] = kit.VirtualItem{
			Key:      fmt.Sprintf("item-%d", i+1),
			Revision: 1,
			Estimate: theme.RowDense,
		}
	}
	return virtualListPage{
		restSt:   kit.NewVirtualListState(),
		followSt: kit.NewVirtualListState(),
		hoverSt:  kit.NewVirtualListState(),
		liveSt:   kit.NewVirtualListState(),
		insertB:  kit.NewButtonState(),
		followB:  kit.NewButtonState(),
		items:    items,
	}
}

func (p *pages) layoutVirtualLists(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "VirtualList §6.26: generalized from transcriptview; bounded LRU height cache by (key, revision), render window + buffer, anchor kept on insert above, opt-in FollowBottom, shared ScrollArea bar.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.virtualListSpecBlock(gtx, dry, "default (FollowBottom off)", 0, vlDefault, p.vl.restSt, false, stRest)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.virtualListSpecBlock(gtx, dry, "stick to bottom", 1, vlFollow, p.vl.followSt, true, stRest)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.virtualListSpecBlock(gtx, dry, "bar visible", 2, vlHover, p.vl.hoverSt, false, stHover)
		},
		p.virtualListLiveBlock,
	})
}

func (p *pages) virtualListSpecBlock(gtx *layout.Context, dry bool, title string, variant int, cellState string, st *kit.VirtualListState, follow bool, preview string) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	w, h := gtx.Dp(unit.Dp(260)), gtx.Dp(unit.Dp(160))
	y := line + gap
	specItems := p.vl.items[:40]
	if !dry {
		p.caption(gtx, image.Point{}, title, c.FgMuted)
		cd := cellDraw{
			key: cell{compVirtualList, variant, 0, cellState},
			tag: st.Tag(),
			box: image.Pt(w, h),
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Exact(image.Pt(w, h))
				st.Layout(p.env, &g, kit.VirtualListProps{FollowBottom: follow}, specItems, func(env *kit.Env, gtx *layout.Context, idx int, item kit.VirtualItem, _ *kit.VirtualItemState) layout.Dimensions {
					return drawVirtualGalleryRow(env, gtx, item.Key, idx)
				})
			},
		}
		if preview == stHover {
			cd.key.state = stHover
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(w, y+h)
}

func (p *pages) virtualListLiveBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	w, h := gtx.Dp(unit.Dp(340)), gtx.Dp(unit.Dp(200))

	insPr := kit.ButtonProps{Text: "Insert above", Variant: kit.Secondary, Size: kit.SizeSM}
	folText := "Stick to bottom: off"
	if p.vl.followOn {
		folText = "Stick to bottom: on"
	}
	folPr := kit.ButtonProps{Text: folText, Variant: kit.Bordered, Size: kit.SizeSM}
	insSz := measure(gtx, func(gtx *layout.Context) image.Point { return p.vl.insertB.Measure(p.env, gtx, insPr).Size })
	folSz := measure(gtx, func(gtx *layout.Context) image.Point { return p.vl.followB.Measure(p.env, gtx, folPr).Size })
	btnH := max(insSz.Y, folSz.Y)

	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, fmt.Sprintf("10,000 items (%d laid out)", p.vl.liveSt.Rendered()), c.FgMuted)
		p.layoutLiveVLButtons(gtx, y, gap, insPr, folPr, insSz.X)
		listY := y + btnH + gap
		cd := cellDraw{
			key: cell{compVirtualList, 3, 0, vlLive},
			tag: p.vl.liveSt.Tag(),
			box: image.Pt(w, h),
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Exact(image.Pt(w, h))
				p.vl.liveSt.Layout(p.env, &g, kit.VirtualListProps{FollowBottom: p.vl.followOn}, p.vl.items, func(env *kit.Env, gtx *layout.Context, idx int, item kit.VirtualItem, _ *kit.VirtualItemState) layout.Dimensions {
					return drawVirtualGalleryRow(env, gtx, item.Key, idx)
				})
			},
		}
		p.place(gtx, image.Pt(0, listY), &cd)
	}
	return image.Pt(w, y+btnH+gap+h)
}

func (p *pages) layoutLiveVLButtons(gtx *layout.Context, y, gap int, insPr, folPr kit.ButtonProps, insW int) {
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) {
		if clicked, err := p.vl.insertB.Layout(p.env, gtx, insPr); err != nil {
			p.err = err
		} else if clicked {
			p.vl.prepends++
			prep := kit.VirtualItem{Key: fmt.Sprintf("prepended-%d", p.vl.prepends), Revision: 1, Estimate: theme.RowDense}
			p.vl.items = append([]kit.VirtualItem{prep}, p.vl.items...)
		}
	})
	at(gtx, image.Pt(insW+gap, y), func(gtx *layout.Context) {
		if clicked, err := p.vl.followB.Layout(p.env, gtx, folPr); err != nil {
			p.err = err
		} else if clicked {
			p.vl.followOn = !p.vl.followOn
		}
	})
}

func drawVirtualGalleryRow(env *kit.Env, gtx *layout.Context, key string, idx int) layout.Dimensions {
	c := env.Th.Chrome()
	h := gtx.Dp(theme.RowDense)
	w := gtx.Constraints.Max.X
	pt := image.Pt(gtx.Dp(theme.SpaceMD), (h-gtx.Sp(theme.TypeSmall().Line))/2)
	at(gtx, pt, func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(w-2*pt.X, 0), h)}
		dims := kit.Label(env, &g, theme.TypeSmall(), c.Fg, fmt.Sprintf("#%d  %s", idx+1, key))
		if env.RunRecorder != nil {
			env.RunRecorder(image.Rectangle{Max: dims.Size}.Add(pt))
		}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}
