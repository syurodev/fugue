package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Combobox page (UK-09, ui-kit §6.14): field states (rest, hover,
// focus, disabled), inline SuggestList specimens (open with results,
// loading, empty, no results), and a live interactive Combobox.

const (
	cbOpenResults = "open-results"
	cbLoading     = "loading"
	cbEmpty       = "empty"
	cbNoResults   = "no-results"
	cbLive        = "live"
)

func comboboxFieldStates() []string { return []string{stRest, stHover, stFocus, stDisabled} }
func comboboxListStates() []string {
	return []string{cbOpenResults, cbLoading, cbEmpty, cbNoResults}
}

type comboboxPage struct {
	fields [4]*kit.ComboboxState
	lists  [4]*kit.SuggestListState
	live   *kit.ComboboxState
}

func newComboboxPage() comboboxPage {
	var cp comboboxPage
	for i := range cp.fields {
		cp.fields[i] = kit.NewComboboxState()
	}
	for i := range cp.lists {
		cp.lists[i] = kit.NewSuggestListState()
		cp.lists[i].Open()
	}
	cp.live = kit.NewComboboxState()
	return cp
}

const cbSampleLabel = "Model"

func comboboxSampleItems() []kit.SuggestItem {
	return []kit.SuggestItem{
		{ID: "cb-sonnet", Label: "Claude Sonnet", Secondary: "balanced", Icon: kit.IconSearch, Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('1')}},
		{ID: "cb-opus", Label: "Claude Opus", Secondary: "deep", Icon: kit.IconEye, Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('2')}},
		{ID: "cb-haiku", Label: "Đổi model", Secondary: "fast", Disabled: true},
	}
}

func (p *pages) layoutComboboxes(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Combobox §6.14: TextField search core plus SuggestList on the window overlay. Arrows move the highlight while the caret stays in the field; Enter chooses; Escape closes the list and a second Escape clears.") + gtx.Dp(theme.SpaceXL)

	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "field states", comboboxFieldStates(), [][]cellDraw{p.comboboxFieldRow(gtx)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "suggestion list states", comboboxListStates(), [][]cellDraw{p.comboboxListRow(gtx)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "interactive", []string{cbLive}, [][]cellDraw{{p.comboboxLiveCell(gtx)}}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

func (p *pages) comboboxFieldRow(gtx *layout.Context) []cellDraw {
	states := comboboxFieldStates()
	row := make([]cellDraw, 0, len(states))
	for ci, st := range states {
		cb := p.combobox.fields[ci]
		pr := &kit.ComboboxProps{
			Label:       cbSampleLabel,
			Placeholder: "Search models...",
			Items:       comboboxSampleItems(),
			Disabled:    st == stDisabled,
		}
		row = append(row, cellDraw{
			col: ci, key: cell{compCombobox, 0, 0, st}, tag: cb.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return cb.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				if _, err := cb.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}

func (p *pages) comboboxListRow(gtx *layout.Context) []cellDraw {
	states := comboboxListStates()
	row := make([]cellDraw, 0, len(states))
	items := comboboxSampleItems()
	for ci, st := range states {
		sl := p.combobox.lists[ci]
		pr := &kit.SuggestProps{Inline: true}
		switch st {
		case cbOpenResults:
			pr.Items = items
			sl.SetHighlight(0)
		case cbLoading:
			pr.Loading = true
		case cbEmpty:
			pr.EmptyText = "No items"
		case cbNoResults:
			pr.Query = "zzz"
			pr.NoResText = "No results"
		}
		row = append(row, cellDraw{
			col: ci, key: cell{compCombobox, 1, 0, st},
			box: measure(gtx, func(gtx *layout.Context) image.Point {
				res, err := sl.Layout(p.env, gtx, pr)
				if err != nil {
					p.err = err
				}
				return res.Size
			}),
			lay: func(gtx *layout.Context) {
				if _, err := sl.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}

func (p *pages) comboboxLiveCell(gtx *layout.Context) cellDraw {
	cb := p.combobox.live
	pr := &kit.ComboboxProps{
		Label:       "Pick model",
		Placeholder: "Type to filter...",
		Items:       comboboxSampleItems(),
	}
	return cellDraw{
		col: 0, key: cell{compCombobox, 2, 0, cbLive}, tag: cb.Tag(),
		box: measure(gtx, func(gtx *layout.Context) image.Point { return cb.Measure(p.env, gtx, pr).Size }),
		lay: func(gtx *layout.Context) {
			if _, err := cb.Layout(p.env, gtx, pr); err != nil {
				p.err = err
			}
		},
	}
}
