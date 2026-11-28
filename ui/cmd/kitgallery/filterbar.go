package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type filterBarPage struct {
	bars     [2]*kit.FilterBarState
	kindSel  string
	stateSel string
}

func newFilterBarPage() filterBarPage {
	b1 := kit.NewFilterBarState()
	b1.SetQuery("auth")
	return filterBarPage{
		bars:     [2]*kit.FilterBarState{kit.NewFilterBarState(), b1},
		kindSel:  "fact",
		stateSel: "unverified",
	}
}

func (p *pages) layoutFilterBars(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "FilterBar: search TextField + N Select filters + Clear filters action, debouncing search input via Env.Now.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{p.filterBarSpecBlock})
}

func (p *pages) filterBarSpecBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := min(gtx.Constraints.Max.X, gtx.Dp(theme.MainSlotMinWidth))

	propsList := []struct {
		label string
		props kit.FilterBarProps
		state *kit.FilterBarState
	}{
		{
			label: "default (empty)",
			state: p.fb.bars[0],
			props: kit.FilterBarProps{
				SearchPlaceholder: "Search entries…",
				Filters: []kit.FilterSelect{
					{ID: "kind", Placeholder: "Kind", Options: []kit.SelectOption{{ID: "", Label: "All"}, {ID: "fact", Label: "Fact"}, {ID: "decision", Label: "Decision"}}},
					{ID: "verified", Placeholder: "Status", Options: []kit.SelectOption{{ID: "", Label: "All"}, {ID: "unverified", Label: "Unverified"}}},
				},
			},
		},
		{
			label: "active filters",
			state: p.fb.bars[1],
			props: kit.FilterBarProps{
				SearchPlaceholder: "Search entries…",
				Filters: []kit.FilterSelect{
					{ID: "kind", Placeholder: "Kind", Selected: p.fb.kindSel, Options: []kit.SelectOption{{ID: "", Label: "All"}, {ID: "fact", Label: "Fact"}, {ID: "decision", Label: "Decision"}}},
					{ID: "verified", Placeholder: "Status", Selected: p.fb.stateSel, Options: []kit.SelectOption{{ID: "", Label: "All"}, {ID: "unverified", Label: "Unverified"}}},
				},
			},
		},
	}

	y := line + gap
	for i, sp := range propsList {
		if !dry {
			p.caption(gtx, image.Pt(0, y), sp.label, c.FgSubtle)
		}
		y += line + gtx.Dp(theme.SpaceXS)
		g := *gtx
		g.Constraints.Max.X = w
		sz := sp.state.Measure(p.env, &g, sp.props)
		if !dry {
			idx := i
			cd := cellDraw{
				key: cell{compFilterBar, idx, 0, stRest},
				box: sz,
				lay: func(gtx *layout.Context) {
					lg := *gtx
					lg.Constraints.Max.X = w
					_, res, err := sp.state.Layout(p.env, &lg, sp.props)
					if err != nil && p.err == nil {
						p.err = err
					}
					if idx == 1 && res.Changed {
						p.fb.kindSel = res.State.Selected["kind"]
						p.fb.stateSel = res.State.Selected["verified"]
					}
				},
			}
			p.place(gtx, image.Pt(0, y), &cd)
		}
		y += sz.Y + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "FilterBar", c.FgMuted)
	}
	return image.Pt(w, y)
}
