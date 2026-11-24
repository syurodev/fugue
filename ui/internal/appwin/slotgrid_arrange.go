package appwin

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
)

// The arrange menu (desktop-app §5.3, ui-kit §6.15): Move in the four
// directions — invalid directions disabled, moving toward the main slot
// first of all — Fill across the whole grid, Split beside the main slot,
// Make main, and Open in new window for F10.21 to attach.

// labelArrange names the arrange trigger button's accessible label and tooltip.
const labelArrange = "Arrange"

const (
	menuIDMoveLeft  = "move-left"
	menuIDMoveRight = "move-right"
	menuIDMoveUp    = "move-up"
	menuIDMoveDown  = "move-down"
	menuIDFill      = "fill"
	menuIDSplit     = "split"
	menuIDMakeMain  = "make-main"
	menuIDNewWindow = "new-window"
)

// layoutArrange runs the arrange Menu for the slot at p while it is open.
func (g *slotGrid) layoutArrange(env *kit.Env, gtx *layout.Context, sc *screens, p Pos, anchor image.Rectangle) {
	if g.popFor != p.key() || !g.arrangeMenu.IsOpen() {
		return
	}
	res, err := g.arrangeMenu.Layout(env, gtx, kit.MenuProps{
		Items:    arrangeMenuItems(g.doc, p),
		Anchor:   anchor,
		AlignEnd: true,
	})
	if err != nil {
		sc.fail(err)
	}
	if cmd, ok := arrangeCmdForID(res.Chosen); ok {
		g.arrange(p, cmd)
	}
	if res.Closed {
		g.popFor = ""
	}
}

// arrangeMenuItems builds the kit.Menu items for the slot at p: the four moves
// — disabled when the direction leaves the grid or would enter the main
// slot — separated by a Divider from fill (with shortcut), split, make main
// and open in a new window.
func arrangeMenuItems(doc LayoutDoc, p Pos) []kit.MenuItem {
	moveSpecs := []struct {
		id    string
		label string
		icon  kit.IconName
		dir   Direction
	}{
		{menuIDMoveLeft, "Move left", kit.IconArrowLeft, DirLeft},
		{menuIDMoveRight, "Move right", kit.IconArrowRight, DirRight},
		{menuIDMoveUp, "Move up", kit.IconArrowUp, DirUp},
		{menuIDMoveDown, "Move down", kit.IconArrowDown, DirDown},
	}
	items := make([]kit.MenuItem, 0, 9)
	for _, m := range moveSpecs {
		_, ok := neighborCell(doc, p, m.dir)
		items = append(items, kit.MenuItem{
			ID:       m.id,
			Label:    m.label,
			Icon:     m.icon,
			Disabled: !ok,
		})
	}
	fillIcon := kit.IconMaximize2
	if doc.FilledSlot == p.key() {
		fillIcon = kit.IconMinimize2
	}
	splitable := len(doc.Columns[0].Slots) < maxRowsPerColumn
	return append(items,
		kit.MenuSeparator(),
		kit.MenuItem{
			ID:       menuIDFill,
			Label:    fillLabel(doc, p),
			Icon:     fillIcon,
			Shortcut: kit.Shortcut{Mods: kit.ModPrimary | kit.ModShift, Key: kit.KeyEnter},
		},
		kit.MenuItem{ID: menuIDSplit, Label: "Split", Icon: kit.IconColumns2, Disabled: !splitable},
		kit.MenuItem{ID: menuIDMakeMain, Label: "Make main", Icon: kit.IconPanelLeft},
		kit.MenuItem{ID: menuIDNewWindow, Label: "Open in new window", Icon: kit.IconAppWindow},
	)
}

func arrangeCmdForID(id string) (arrangeCmd, bool) {
	switch id {
	case menuIDMoveLeft:
		return arrangeMoveLeft, true
	case menuIDMoveRight:
		return arrangeMoveRight, true
	case menuIDMoveUp:
		return arrangeMoveUp, true
	case menuIDMoveDown:
		return arrangeMoveDown, true
	case menuIDFill:
		return arrangeFill, true
	case menuIDSplit:
		return arrangeSplit, true
	case menuIDMakeMain:
		return arrangeMakeMain, true
	case menuIDNewWindow:
		return arrangeNewWindow, true
	default:
		return 0, false
	}
}

// fillLabel names the fill row: Fill, or Back to grid while filled.
func fillLabel(doc LayoutDoc, p Pos) string {
	if doc.FilledSlot == p.key() {
		return "Back to grid"
	}
	return "Fill"
}
