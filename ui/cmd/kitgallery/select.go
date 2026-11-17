package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Select page (UK-09, ui-kit §6.13): unselected and selected variants
// across rest, hover, focus, open and disabled, plus a live interactive
// Select.

const (
	selOpen = "open"
	selLive = "live"
)

func selectStates() []string { return []string{stRest, stHover, stFocus, selOpen, stDisabled} }
func selectVariantNames() []string {
	return []string{"unselected", "selected"}
}

type selectPage struct {
	cells    [2][5]*kit.SelectState
	openMenu [2]*kit.MenuState
	live     *kit.SelectState
	liveVal  string
}

func newSelectPage() selectPage {
	var sp selectPage
	for vi := range sp.cells {
		for ci := range sp.cells[vi] {
			sp.cells[vi][ci] = kit.NewSelectState()
		}
		sp.openMenu[vi] = kit.NewMenuState()
		sp.openMenu[vi].Open(0)
	}
	sp.live = kit.NewSelectState()
	return sp
}

const selDefaultID = "sel-sonnet"

func selectSampleOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: selDefaultID, Label: "Claude Sonnet", Icon: kit.IconSearch},
		{ID: "sel-opus", Label: "Claude Opus", Icon: kit.IconEye},
		{ID: "sel-haiku", Label: "Claude Haiku"},
	}
}

func (p *pages) layoutSelects(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Select §6.13: read-only field with a clear unselected state and a chevron that rotates 180° on open (Snappy). Enter, Space or Down open its Menu; Escape closes and returns focus.") + gtx.Dp(theme.SpaceXL)

	blocks := make([]block, 0, 4)
	for vi, name := range selectVariantNames() {
		blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, name, selectStates(), [][]cellDraw{p.selectRow(gtx, vi)}, dry)
		})
	}
	blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
		return p.matrix(gtx, "open menu specimen", []string{selOpen}, [][]cellDraw{{p.selectOpenMenuCell(gtx)}}, dry)
	})
	blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
		return p.matrix(gtx, "interactive", []string{selLive}, [][]cellDraw{{p.selectLiveCell(gtx)}}, dry)
	})
	p.flow(gtx, y, blocks)
}

func (p *pages) selectRow(gtx *layout.Context, vi int) []cellDraw {
	states := selectStates()
	opts := selectSampleOptions()
	val := ""
	if vi == 1 {
		val = selDefaultID
	}
	row := make([]cellDraw, 0, len(states))
	for ci, st := range states {
		sel := p.sel.cells[vi][ci]
		pr := &kit.SelectProps{
			Label:       cbSampleLabel,
			Placeholder: "Choose model",
			Options:     opts,
			Value:       val,
			Disabled:    st == stDisabled,
		}
		row = append(row, cellDraw{
			col: ci, key: cell{compSelect, vi, 0, st}, tag: sel.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return sel.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				if _, err := sel.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}

func (p *pages) selectOpenMenuCell(gtx *layout.Context) cellDraw {
	m := p.sel.openMenu[0]
	opts := selectSampleOptions()
	items := make([]kit.MenuItem, len(opts))
	for i, o := range opts {
		items[i] = kit.MenuItem{ID: o.ID, Label: o.Label, Icon: o.Icon}
	}
	pr := kit.MenuProps{Items: items, Inline: true}
	return cellDraw{
		col: 0, key: cell{compSelect, 2, 0, selOpen},
		box: measure(gtx, func(gtx *layout.Context) image.Point {
			res, err := m.Layout(p.env, gtx, pr)
			if err != nil {
				p.err = err
			}
			return res.Size
		}),
		lay: func(gtx *layout.Context) {
			if _, err := m.Layout(p.env, gtx, pr); err != nil {
				p.err = err
			}
		},
	}
}

func (p *pages) selectLiveCell(gtx *layout.Context) cellDraw {
	sel := p.sel.live
	pr := &kit.SelectProps{
		Label:       cbSampleLabel,
		Placeholder: "Choose model",
		Options:     selectSampleOptions(),
		Value:       p.sel.liveVal,
	}
	return cellDraw{
		col: 0, key: cell{compSelect, 3, 0, selLive}, tag: sel.Tag(),
		box: measure(gtx, func(gtx *layout.Context) image.Point { return sel.Measure(p.env, gtx, pr).Size }),
		lay: func(gtx *layout.Context) {
			res, err := sel.Layout(p.env, gtx, pr)
			if err != nil {
				p.err = err
			}
			if res.Changed {
				p.sel.liveVal = res.Value
			}
		},
	}
}
