package main

import (
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	tabDocActive     = "doc-active"
	tabDocHover      = "doc-hover"
	tabDocFocus      = "doc-focus"
	tabDocDrag       = "doc-drag"
	tabDocPinned     = "doc-pinned"
	tabDocDirty      = "doc-dirty"
	tabDocDirtyHover = "doc-dirty-hover"
	tabDocIcon       = "doc-icon"
	tabDocNoIcon     = "doc-no-icon"
	tabDocOverflow   = "doc-overflow"
	tabModeFocus     = "mode-focus"
)

type tabsFeedbackPage struct {
	docTabs  *kit.TabsState
	docItems []kit.TabItem
	docAct   string
	specTabs [6]*kit.TabsState
	modeTabs *kit.TabsState
	modeAct  string
	modeSpec *kit.TabsState
	ovTabs   *kit.TabsState
	ovAct    string
	banners  [3]*kit.BannerState
	empty    *kit.EmptyStateState
	emptyNR  *kit.EmptyStateState
}

func newTabsFeedbackPage() tabsFeedbackPage {
	var specTabs [6]*kit.TabsState
	for i := range specTabs {
		specTabs[i] = kit.NewTabsState()
	}
	return tabsFeedbackPage{
		docTabs: kit.NewTabsState(),
		docItems: []kit.TabItem{
			{ID: "pinned", Label: "Pinned", Pinned: true, Icon: kit.IconPin},
			{ID: "mainTab", Label: "app.go", Icon: kit.IconSearch, Closable: true, Dirty: true},
			{ID: "test", Label: "tabs_test.go", Closable: true},
		},
		docAct:   "mainTab",
		specTabs: specTabs,
		modeTabs: kit.NewTabsState(),
		modeAct:  "chat",
		modeSpec: kit.NewTabsState(),
		ovTabs:   kit.NewTabsState(),
		ovAct:    "t1",
		banners:  [3]*kit.BannerState{kit.NewBannerState(), kit.NewBannerState(), kit.NewBannerState()},
		empty:    kit.NewEmptyStateState(),
		emptyNR:  kit.NewEmptyStateState(),
	}
}

func docTabSpecimenStates() []string {
	return []string{tabDocActive, tabDocHover, tabDocFocus, tabDocDrag, tabDocPinned, tabDocDirty, tabDocDirtyHover, tabDocIcon, tabDocNoIcon}
}

func (p *pages) layoutTabsFeedback(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Tabs §6.22 (document + mode + overflow Menu, static Preview specimens), Banner §6.24, EmptyState §6.25.") + gtx.Dp(theme.SpaceXL)
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

	h := gtx.Dp(theme.ControlMD)
	if !dry {
		p.caption(gtx, image.Point{}, "Tabs (document / specimens / mode / overflow)", c.FgMuted)
		p.placeDocTabs(gtx, w, h, y)
		y += h + gap
		y = p.placeDocSpecimens(gtx, w, h, y, gap)
		p.placeModeTabs(gtx, w, h, y, stRest, p.tf.modeTabs)
		y += h + gap
		p.placeModeTabs(gtx, w, h, y, tabModeFocus, p.tf.modeSpec)
		y += h + gap
		p.placeOverflowTabs(gtx, w, h, y)
		return image.Pt(w, y+h+gap)
	}
	return image.Pt(w, y+9*(h+gap))
}

func (p *pages) placeDocSpecimens(gtx *layout.Context, w, h, y, gap int) int {
	const (
		actID = "act"
		othID = "oth"
	)
	// 1. active + hover + pinned + icon + no-icon
	s0 := p.tf.specTabs[0]
	p.placeTabSpecimen(gtx, w, h, y, s0, kit.TabsProps{
		Variant: kit.TabsDocument,
		Items: []kit.TabItem{
			{ID: "pin", Label: "Pinned", Pinned: true},
			{ID: actID, Label: "app.go", Icon: kit.IconSearch, Closable: true},
			{ID: "hov", Label: "plain.go", Closable: true},
		},
		ActiveID: actID,
	}, s0.TabTag("hov"), stHover, []string{tabDocPinned, tabDocActive, tabDocHover, tabDocIcon, tabDocNoIcon})
	y += h + gap

	// 2. keyboard focus
	s1 := p.tf.specTabs[1]
	p.placeTabSpecimen(gtx, w, h, y, s1, kit.TabsProps{
		Variant: kit.TabsDocument,
		Items: []kit.TabItem{
			{ID: "foc", Label: "focused.go", Closable: true},
			{ID: othID, Label: "other.go", Closable: true},
		},
		ActiveID: "foc",
	}, s1.Tag(), stFocus, []string{tabDocFocus})
	y += h + gap

	// 3. dragging
	s2 := p.tf.specTabs[2]
	p.placeTabSpecimen(gtx, w, h, y, s2, kit.TabsProps{
		Variant: kit.TabsDocument,
		Items: []kit.TabItem{
			{ID: "drg", Label: "dragging.go", Closable: true},
			{ID: othID, Label: "other.go", Closable: true},
		},
		ActiveID:   othID,
		DraggingID: "drg",
	}, nil, "", []string{tabDocDrag})
	y += h + gap

	// 4. dirty + dirty+hover
	s3 := p.tf.specTabs[3]
	p.placeTabSpecimen(gtx, w, h, y, s3, kit.TabsProps{
		Variant: kit.TabsDocument,
		Items: []kit.TabItem{
			{ID: actID, Label: "index.go", Closable: true},
			{ID: "drt", Label: "dirty.go", Closable: true, Dirty: true},
			{ID: "drh", Label: "dirty_hov.go", Closable: true, Dirty: true},
		},
		ActiveID: actID,
	}, s3.TabTag("drh"), stHover, []string{tabDocDirty, tabDocDirtyHover})
	return y + h + gap
}

func (p *pages) placeTabSpecimen(gtx *layout.Context, w, h, y int, st *kit.TabsState, pr kit.TabsProps, pvTag interface{}, pvKind string, keys []string) {
	cd := cellDraw{
		key: cell{compTabs, 3, 0, keys[0]},
		tag: pvTag,
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			switch pvKind {
			case stHover:
				p.env.Preview.Hover = pvTag
				defer func() { p.env.Preview.Hover = nil }()
			case stFocus:
				p.env.Preview.Focus = pvTag
				defer func() { p.env.Preview.Focus = nil }()
			}
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(w, h))
			_, ev := st.Layout(p.env, &g, pr)
			if ev.Err != nil {
				p.err = ev.Err
			}
			for _, k := range keys[1:] {
				p.cells[cell{compTabs, 3, 0, k}] = true
			}
		},
	}
	p.place(gtx, image.Pt(0, y), &cd)
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

func (p *pages) placeModeTabs(gtx *layout.Context, w, h, y int, stKey string, st *kit.TabsState) {
	modeItems := []kit.TabItem{{ID: "chat", Label: "Chat"}, {ID: "team", Label: "Team"}, {ID: "files", Label: "Files"}}
	modePr := kit.TabsProps{Variant: kit.TabsMode, Items: modeItems, ActiveID: p.tf.modeAct}
	var pvTag interface{}
	if stKey == tabModeFocus {
		pvTag = st.Tag()
	}
	cd := cellDraw{
		key: cell{compTabs, 1, 0, stKey},
		tag: pvTag,
		box: image.Pt(w, h),
		lay: func(gtx *layout.Context) {
			if stKey == tabModeFocus {
				p.env.Preview.Focus = st.Tag()
				defer func() { p.env.Preview.Focus = nil }()
			}
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(w, h))
			_, ev := st.Layout(p.env, &g, modePr)
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
		{ID: "t1", Label: "session_01.go", Icon: kit.IconSearch, Closable: true},
		{ID: "t2", Label: "session_02.go", Icon: kit.IconCopy, Closable: true},
		{ID: "t3", Label: "session_03.go", Icon: kit.IconHelp, Closable: true},
		{ID: "t4", Label: "session_04.go", Icon: kit.IconPin, Closable: true},
		{ID: "t5", Label: "session_05.go", Icon: kit.IconRestart, Closable: true},
	}
	ovPr := kit.TabsProps{Variant: kit.TabsDocument, Items: ovItems, ActiveID: p.tf.ovAct}
	cd := cellDraw{
		key: cell{compTabs, 2, 0, tabDocOverflow},
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
