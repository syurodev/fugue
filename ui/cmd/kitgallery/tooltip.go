package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Tooltip page (UK-09, ui-kit §6.7): specimens with and without Kbd,
// flipped at the edge (anchor above the tooltip), and live IconButton
// neighbours demonstrating the 500 ms hover delay, immediate focus, and
// 300 ms hop.

const (
	tipPlain   = "plain"
	tipKbd     = "kbd"
	tipFlipped = "flipped"
	tipLiveA   = "live-a"
	tipLiveB   = "live-b"
)

type tooltipPage struct {
	live [2]*kit.IconButtonState
}

func newTooltipPage() tooltipPage {
	return tooltipPage{
		live: [2]*kit.IconButtonState{kit.NewIconButtonState(), kit.NewIconButtonState()},
	}
}

func (p *pages) layoutTooltips(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Tooltip §6.7: type.small with optional Kbd on SurfaceRaised. Appears after 500 ms on hover or at once on keyboard focus, hops between neighbours within 300 ms, flips below near the edge, and takes no input.") + gtx.Dp(theme.SpaceXL)

	blocks := []block{
		p.tooltipSpecimensBlock,
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "interactive neighbours (hover or Tab)", []string{tipLiveA, tipLiveB}, [][]cellDraw{p.tooltipLiveRow(gtx)}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

func (p *pages) tooltipSpecimensBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap, colGap := gtx.Dp(theme.SpaceLG), gtx.Dp(theme.SpaceLG)
	specs := []struct {
		st    string
		props kit.TooltipProps
	}{
		{tipPlain, kit.TooltipProps{Text: "Copy", Inline: true}},
		{tipKbd, kit.TooltipProps{Text: "Search", Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('k')}, Inline: true}},
		{tipFlipped, kit.TooltipProps{Text: "Flipped below edge", Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('d')}, Inline: true}},
	}
	x, maxH := 0, 0
	for i, sp := range specs {
		cw := p.captionWidth(gtx, sp.st)
		sz := measure(gtx, func(gtx *layout.Context) image.Point {
			dims, err := kit.TooltipLayout(p.env, gtx, sp.props)
			if err != nil {
				p.err = err
			}
			return dims.Size
		})
		colW := max(cw, sz.X)
		if !dry {
			p.caption(gtx, image.Pt(x, line+gap), sp.st, c.FgSubtle)
			pt := image.Pt(x, line+gap+line+gap)
			at(gtx, pt, func(gtx *layout.Context) {
				if _, err := kit.TooltipLayout(p.env, gtx, sp.props); err != nil {
					p.err = err
				}
			})
			p.runs = append(p.runs, image.Rectangle{Min: p.base.Add(pt), Max: p.base.Add(pt).Add(sz)})
			p.cells[cell{compTooltip, 0, 0, sp.st}] = true
		}
		maxH = max(maxH, sz.Y)
		x += colW
		if i+1 < len(specs) {
			x += colGap
		}
	}
	if !dry {
		p.caption(gtx, image.Point{}, "specimens", c.FgMuted)
	}
	return image.Pt(x, line+gap+line+gap+maxH)
}

func (p *pages) tooltipLiveRow(gtx *layout.Context) []cellDraw {
	btns := []struct {
		st   string
		icon kit.IconName
		lbl  string
		sc   kit.Shortcut
	}{
		{tipLiveA, kit.IconCopy, "Copy message", kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('c')}},
		{tipLiveB, kit.IconSearch, "Search session", kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('f')}},
	}
	row := make([]cellDraw, 0, len(btns))
	for ci, b := range btns {
		ib := p.tip.live[ci]
		pr := &kit.IconButtonProps{Icon: b.icon, Label: b.lbl, Shortcut: b.sc, Variant: kit.IconButtonSecondary}
		row = append(row, cellDraw{
			col: ci, key: cell{compTooltip, 1, 0, b.st}, tag: ib.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return ib.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				if _, err := ib.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}
