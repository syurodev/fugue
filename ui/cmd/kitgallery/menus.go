package main

import (
	"image"
	"slices"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Menu page (UK-09, ui-kit §6.15): two specimens of a menu — labels
// only, and icons with Kbd shortcuts, a separator and a disabled item — each
// drawn inline, open and still: at rest, with the pointer's item
// highlighted, with the keyboard-active item highlighted (the same sliding
// pill, one row down), and with its submenu open; and two live triggers that
// open the real menu on the window's overlay. The right-hand trigger sits at
// the window's edge, so its menu and its submenu flip. The specimens use the
// same drawing as the overlay and the matrix, bounds recording and cell
// inventory of the other pages.

// The Menu page's state columns: the specimens', then the live triggers'.
const (
	mstKeyboard = "keyboard"
	mstSubmenu  = "submenu"
	mstDropdown = "dropdown"
	mstFlip     = "flip-right"
)

// numMenuSpecimens is how many specimen variants the page has; the live
// triggers are the variant after them.
const numMenuSpecimens = 2

// menuStates are the specimen columns.
func menuStates() []string { return []string{stRest, stHover, mstKeyboard, mstSubmenu} }

// liveStates are the live triggers.
func liveStates() []string { return []string{mstDropdown, mstFlip} }

// menuSpecimenNames label the specimen blocks.
func menuSpecimenNames() []string {
	return []string{"labels only", "icons, shortcuts, separator, disabled"}
}

// menuPage is the Menu page's state: one menu per specimen cell, and a
// button and a menu per live trigger.
type menuPage struct {
	specimens [numMenuSpecimens][4]*kit.MenuState
	trigger   [2]*kit.ButtonState
	live      [2]*kit.MenuState
	chosen    string
}

// menuItems are the items of a specimen variant and the index of its
// submenu item.
func menuItems(variant int) ([]kit.MenuItem, int) {
	share := []kit.MenuItem{{ID: "link", Label: "Copy link"}, {ID: "mail", Label: "Email"}}
	if variant == 0 {
		return []kit.MenuItem{
			{ID: "open", Label: "Open"},
			{ID: "rename", Label: "Rename"},
			{ID: "dup", Label: "Duplicate"},
			{ID: "share", Label: "Share", Sub: share},
			{ID: "delete", Label: "Delete"},
		}, 3
	}
	return []kit.MenuItem{
		{ID: "open", Label: "Open", Icon: kit.IconSearch, Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('o')}},
		{ID: "copy", Label: "Copy", Icon: kit.IconCopy, Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('c')}},
		{ID: "paste", Label: "Paste", Icon: kit.IconRestart, Shortcut: kit.Shortcut{Mods: kit.ModPrimary, Key: kit.Char('v')}, Disabled: true},
		kit.MenuSeparator(),
		{ID: "share", Label: "Share", Icon: kit.IconEye, Sub: share},
		{ID: "delete", Label: "Delete", Icon: kit.IconClose, Shortcut: kit.Shortcut{Key: kit.KeyDelete}},
	}, 4
}

// newMenuPage builds the page's states. A specimen is opened once, in the
// state its column shows: nothing highlighted, the first item highlighted,
// or the submenu open with its first item highlighted.
func newMenuPage() menuPage {
	var mp menuPage
	for v := range mp.specimens {
		_, sub := menuItems(v)
		for c, path := range [][]int{nil, {0}, {1}, {sub, 0}} {
			mp.specimens[v][c] = kit.NewMenuState()
			mp.specimens[v][c].Open(path...)
		}
	}
	for i := range mp.trigger {
		mp.trigger[i], mp.live[i] = kit.NewButtonState(), kit.NewMenuState()
	}
	return mp
}

// layoutMenus draws the Menu page: a block of specimens per variant, then
// the live triggers.
func (p *pages) layoutMenus(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Menu §6.15: the highlight is the pointer's item, a submenu opens sideways and survives a diagonal move toward it, a disabled item shows no interaction. A press outside closes the menu and goes no further. The right-hand trigger is at the window edge: its menu flips to the trigger's right edge and its submenu to the left.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, numMenuSpecimens+2)
	for vi, name := range menuSpecimenNames() {
		// The icon variant is wide with its shortcut chips, so its submenu
		// state is a block of its own that wraps when the row is full.
		groups := [][]string{menuStates()}
		if vi == 1 {
			groups = [][]string{{stRest, stHover, mstKeyboard}, {mstSubmenu}}
		}
		for gi, states := range groups {
			title := name
			if gi > 0 {
				title += " (open submenu)"
			}
			blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
				return p.matrix(gtx, title, states, [][]cellDraw{p.specimenRow(gtx, vi, states)}, dry)
			})
		}
	}
	blocks = append(blocks, p.liveBlock)
	p.flow(gtx, y, blocks)
}

// specimenRow is the row of one specimen variant: a cell per state, each as
// big as the menu and its open submenu.
func (p *pages) specimenRow(gtx *layout.Context, vi int, states []string) []cellDraw {
	items, _ := menuItems(vi)
	row := make([]cellDraw, 0, len(states))
	for col, st := range states {
		m := p.menu.specimens[vi][slices.Index(menuStates(), st)]
		props := kit.MenuProps{Items: items, Inline: true}
		row = append(row, cellDraw{
			col: col, key: cell{compMenu, vi, 0, st}, tag: m,
			box: measure(gtx, func(gtx *layout.Context) image.Point {
				res, err := m.Layout(p.env, gtx, props)
				if err != nil {
					p.err = err
				}
				return res.Size
			}),
			lay: func(gtx *layout.Context) {
				if _, err := m.Layout(p.env, gtx, props); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}

// liveBlock is the row of live triggers: the first at the left, the second
// at the right edge of the column, under a caption naming the last choice.
func (p *pages) liveBlock(gtx *layout.Context, dry bool) image.Point {
	cells := []cellDraw{p.liveCell(gtx, 0), p.liveCell(gtx, 1)}
	line := gtx.Sp(theme.TypeSmall().Line)
	w, y := gtx.Constraints.Max.X, line+gtx.Dp(theme.SpaceSM)
	if dry {
		return image.Pt(w, y+cells[0].box.Y)
	}
	label := "live: click a trigger, then an item (chosen: " + p.menu.chosen + ")"
	if p.menu.chosen == "" {
		label = "live: click a trigger, then an item"
	}
	p.caption(gtx, image.Point{}, label, p.env.Th.Chrome().FgMuted)
	p.place(gtx, image.Pt(0, y), &cells[0])
	p.place(gtx, image.Pt(w-cells[1].box.X, y), &cells[1])
	return image.Pt(w, y+cells[0].box.Y)
}

// liveCell is one live trigger: a button that opens a menu under itself.
func (p *pages) liveCell(gtx *layout.Context, i int) cellDraw {
	btn, m := p.menu.trigger[i], p.menu.live[i]
	pr := kit.ButtonProps{Text: "Open menu", Icon: kit.IconChevronDown, Variant: kit.Bordered}
	items, _ := menuItems(1)
	box := measure(gtx, func(gtx *layout.Context) image.Point { return btn.Measure(p.env, gtx, pr).Size })
	return cellDraw{
		col: i, key: cell{compMenu, numMenuSpecimens, 0, liveStates()[i]}, tag: btn.Tag(), box: box,
		lay: func(gtx *layout.Context) {
			clicked, err := btn.Layout(p.env, gtx, pr)
			if err != nil {
				p.err = err
			}
			if clicked {
				if btn.KeyClicked() {
					m.OpenKeyboard()
				} else {
					m.Open()
				}
			}
			m.SetOpener(btn.Tag())
			res, err := m.Layout(p.env, gtx, kit.MenuProps{Items: items, Anchor: image.Rectangle{Max: box}, Inline: false})
			if err != nil {
				p.err = err
			}
			if res.Chosen != "" {
				p.menu.chosen = res.Chosen
			}
		},
	}
}
