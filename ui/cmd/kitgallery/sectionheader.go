package main

import (
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	shPlain   = "plain"
	shAction  = "action"
	shOpen    = "open"
	shClosed  = "closed"
	shFocused = "focus"
	shLive    = "live"
)

func sectionHeaderStates() []string {
	return []string{shPlain, shAction, shOpen, shClosed, shFocused}
}

type sectionHeaderPage struct {
	specs [5]*kit.SectionHeaderState
	live  *kit.SectionHeaderState
}

func newSectionHeaderPage() sectionHeaderPage {
	var specs [5]*kit.SectionHeaderState
	for i := range specs {
		specs[i] = kit.NewSectionHeaderState(i != 3)
	}
	return sectionHeaderPage{specs: specs, live: kit.NewSectionHeaderState(true)}
}

func (p *pages) layoutSectionHeaders(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "SectionHeader §6.23: type.small in FgSubtle, optional trailing IconButton with Tooltip, and collapsible chevron rotating 90° on Snappy while content folds on Smooth.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{p.sectionHeaderSpecBlock, p.sectionHeaderLiveBlock})
}

func (p *pages) sectionHeaderSpecBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	w := gtx.Dp(unit.Dp(300))
	h := gtx.Dp(theme.HitMin)
	y := line + gap
	for i, st := range sectionHeaderStates() {
		sh := p.section.specs[i]
		pr := kit.SectionHeaderProps{
			Label:       "Section " + st,
			Collapsible: st == shOpen || st == shClosed || st == shFocused,
		}
		if st == shAction {
			pr.Action = &kit.IconButtonProps{Icon: kit.IconHelp, Label: "Help"}
		}
		if !dry {
			p.caption(gtx, image.Pt(0, y), st, c.FgSubtle)
			cd := cellDraw{
				key: cell{compSectionHeader, 0, 0, st},
				tag: sh.Tag(),
				box: image.Pt(w, h),
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints = layout.Exact(image.Pt(w, h))
					sh.Layout(p.env, &g, &pr, nil)
				},
			}
			p.place(gtx, image.Pt(0, y+line+gtx.Dp(theme.SpaceXS)), &cd)
		}
		y += line + gtx.Dp(theme.SpaceXS) + h + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "variants", c.FgMuted)
	}
	return image.Pt(w, y)
}

func (p *pages) sectionHeaderLiveBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	w := gtx.Dp(unit.Dp(300))
	pr := kit.SectionHeaderProps{
		Label:       "Recents (click to fold)",
		Collapsible: true,
		Action:      &kit.IconButtonProps{Icon: kit.IconCopy, Label: "Mirror"},
	}
	content := func(gtx *layout.Context) layout.Dimensions {
		h := gtx.Dp(theme.Row)
		pt := image.Pt(gtx.Dp(theme.SpaceMD), (h-line)/2)
		at(gtx, pt, func(gtx *layout.Context) {
			dims := kit.Label(p.env, gtx, theme.TypeSmall(), c.FgMuted, "Collapsible body item")
			if p.env.RunRecorder != nil {
				p.env.RunRecorder(image.Rectangle{Max: dims.Size}.Add(pt).Add(image.Pt(0, gtx.Dp(theme.HitMin))))
			}
		})
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	sz := measure(gtx, func(gtx *layout.Context) image.Point {
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
		dims, _ := p.section.live.Layout(p.env, &g, &pr, content)
		return dims.Size
	})
	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, "interactive fold", c.FgMuted)
		cd := cellDraw{
			key: cell{compSectionHeader, 1, 0, shLive},
			box: sz,
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
				p.section.live.Layout(p.env, &g, &pr, content)
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(w, y+sz.Y)
}
