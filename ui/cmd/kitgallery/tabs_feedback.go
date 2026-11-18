package main

import (
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type tabsFeedbackPage struct {
	docTabs  *kit.TabsState
	docItems []kit.TabItem
	docAct   string
	modeTabs *kit.TabsState
	modeAct  string
	ovTabs   *kit.TabsState
	ovAct    string
	banners  [3]*kit.BannerState
	empty    *kit.EmptyStateState
	emptyNR  *kit.EmptyStateState
}

func newTabsFeedbackPage() tabsFeedbackPage {
	return tabsFeedbackPage{
		docTabs: kit.NewTabsState(),
		docItems: []kit.TabItem{
			{ID: "pinned", Label: "Pinned", Pinned: true, Icon: kit.IconPin},
			{ID: "mainTab", Label: "app.go", Closable: true, Dirty: true},
			{ID: "test", Label: "tabs_test.go", Closable: true},
		},
		docAct:   "mainTab",
		modeTabs: kit.NewTabsState(),
		modeAct:  "chat",
		ovTabs:   kit.NewTabsState(),
		ovAct:    "t1",
		banners:  [3]*kit.BannerState{kit.NewBannerState(), kit.NewBannerState(), kit.NewBannerState()},
		empty:    kit.NewEmptyStateState(),
		emptyNR:  kit.NewEmptyStateState(),
	}
}

func (p *pages) layoutTabsFeedback(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Tabs §6.22 (document + mode + overflow Menu), Banner §6.24 (info/warning/danger), EmptyState §6.25 (default + no-results).") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{
		p.tabsBlock,
		p.bannerBlock,
		p.emptyStateBlock,
	})
}

func (p *pages) tabsBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := gtx.Dp(unit.Dp(360))
	y := line + gap

	docH := gtx.Dp(theme.Tab)
	modeH := gtx.Dp(theme.ControlMD)
	if !dry {
		p.caption(gtx, image.Point{}, "Tabs (document / mode / overflow)", c.FgMuted)
		p.placeDocTabs(gtx, w, docH, y)
		p.placeModeTabs(gtx, w, modeH, y+docH+gap)
		p.placeOverflowTabs(gtx, w, docH, y+docH+gap+modeH+gap)
	}
	return image.Pt(w, y+2*docH+modeH+3*gap)
}

func (p *pages) placeDocTabs(gtx *layout.Context, w, h, y int) {
	docPr := kit.TabsProps{Variant: kit.TabsDocument, Items: p.tf.docItems, ActiveID: p.tf.docAct}
	cd := cellDraw{
		key: cell{compTabs, 0, 0, stRest},
		tag: p.tf.docTabs.Tag(),
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(w, h))
			_, ev := p.tf.docTabs.Layout(p.env, &g, docPr)
			if ev.Err != nil {
				p.err = ev.Err
			}
			if ev.Selected != "" {
				p.tf.docAct = ev.Selected
			}
			if ev.Closed != "" {
				p.tf.docAct = p.tf.docTabs.NextOnClose(p.tf.docItems, p.tf.docAct, ev.Closed)
			}
		},
	}
	p.place(gtx, image.Pt(0, y), &cd)
}

func (p *pages) placeModeTabs(gtx *layout.Context, w, h, y int) {
	modeItems := []kit.TabItem{{ID: "chat", Label: "Chat"}, {ID: "team", Label: "Team"}, {ID: "files", Label: "Files"}}
	modePr := kit.TabsProps{Variant: kit.TabsMode, Items: modeItems, ActiveID: p.tf.modeAct}
	cd := cellDraw{
		key: cell{compTabs, 1, 0, stRest},
		tag: p.tf.modeTabs.Tag(),
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(w, h))
			_, ev := p.tf.modeTabs.Layout(p.env, &g, modePr)
			if ev.Err != nil {
				p.err = ev.Err
			}
			if ev.Selected != "" {
				p.tf.modeAct = ev.Selected
			}
		},
	}
	p.place(gtx, image.Pt(0, y), &cd)
}

func (p *pages) placeOverflowTabs(gtx *layout.Context, w, h, y int) {
	ovItems := []kit.TabItem{
		{ID: "t1", Label: "session_01.go", Closable: true},
		{ID: "t2", Label: "session_02.go", Closable: true},
		{ID: "t3", Label: "session_03.go", Closable: true},
		{ID: "t4", Label: "session_04.go", Closable: true},
		{ID: "t5", Label: "session_05.go", Closable: true},
	}
	ovPr := kit.TabsProps{Variant: kit.TabsDocument, Items: ovItems, ActiveID: p.tf.ovAct}
	cd := cellDraw{
		key: cell{compTabs, 2, 0, "overflow"},
		tag: p.tf.ovTabs.Tag(),
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(w, h))
			_, ev := p.tf.ovTabs.Layout(p.env, &g, ovPr)
			if ev.Err != nil {
				p.err = ev.Err
			}
			if ev.Selected != "" {
				p.tf.ovAct = ev.Selected
			}
		},
	}
	p.place(gtx, image.Pt(0, y), &cd)
}

func (p *pages) bannerBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := gtx.Dp(unit.Dp(360))
	kinds := []kit.BannerKind{kit.BannerInfo, kit.BannerWarning, kit.BannerDanger}
	titles := []string{"Info: host connected", "Warning: slow response", "Danger: vault locked"}

	y := line + gap
	for i, k := range kinds {
		pr := kit.BannerProps{
			Kind:        k,
			Title:       titles[i],
			Message:     "Status detail carried in text.",
			ActionLabel: "Retry",
			Dismissible: true,
			Visible:     true,
		}
		sz := measure(gtx, func(gtx *layout.Context) image.Point {
			g := *gtx
			g.Constraints.Max.X = w
			d, _, err := p.tf.banners[i].Layout(p.env, &g, pr)
			if err != nil {
				p.err = err
			}
			return d.Size
		})
		if !dry {
			cd := cellDraw{
				key: cell{compBanner, i, 0, stRest},
				box: sz,
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints.Max.X = w
					if _, _, err := p.tf.banners[i].Layout(p.env, &g, pr); err != nil {
						p.err = err
					}
				},
			}
			p.place(gtx, image.Pt(0, y), &cd)
		}
		y += sz.Y + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "Banner (info / warning / danger)", c.FgMuted)
	}
	return image.Pt(w, y)
}

func (p *pages) emptyStateBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := gtx.Dp(unit.Dp(360))

	pr1 := kit.EmptyStateProps{
		Icon:          kit.IconSearch,
		Title:         "Start a session",
		Description:   "Choose a workspace to begin.",
		PrimaryAction: "New session",
	}
	pr2 := kit.EmptyStateProps{
		Icon:              kit.IconSearch,
		Title:             "No matching results",
		Description:       "Keep your filter or clear it.",
		NoResults:         true,
		ClearFilterAction: "Clear filter",
	}

	y := line + gap
	for i, pr := range []kit.EmptyStateProps{pr1, pr2} {
		st := p.tf.empty
		if i == 1 {
			st = p.tf.emptyNR
		}
		sz := measure(gtx, func(gtx *layout.Context) image.Point {
			g := *gtx
			g.Constraints.Max.X = w
			d, _, err := st.Layout(p.env, &g, &pr)
			if err != nil {
				p.err = err
			}
			return d.Size
		})
		if !dry {
			cd := cellDraw{
				key: cell{compEmptyState, i, 0, stRest},
				box: sz,
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints.Max.X = w
					if _, _, err := st.Layout(p.env, &g, &pr); err != nil {
						p.err = err
					}
				},
			}
			p.place(gtx, image.Pt(0, y), &cd)
		}
		y += sz.Y + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "EmptyState (default / no results)", c.FgMuted)
	}
	return image.Pt(w, y)
}
