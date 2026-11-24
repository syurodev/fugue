package appwin

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The arrange popover (desktop-app §5.3, ui-kit §6.16): Move in the four
// directions — invalid directions disabled, moving toward the main slot
// first of all — Fill across the whole grid, Split beside the main slot,
// Make main, and Open in new window for F10.21 to attach.

// labelArrange names the popover and its heading row.
const labelArrange = "Arrange"

// arrangeRow is one popover row: a heading, a divider, or a command button.
type arrangeRow struct {
	heading  bool
	divider  bool
	label    string
	disabled bool
	do       arrangeCmd
}

// layoutArrange runs the popover for the slot at p while it is open.
func (g *slotGrid) layoutArrange(env *kit.Env, gtx *layout.Context, sc *screens, p Pos, anchor image.Rectangle) {
	if g.popFor != p.key() || !g.popover.IsOpen() {
		return
	}
	g.popover.SetOpener(g.headerTagFor(p))
	res, err := g.popover.Layout(env, gtx, &kit.PopoverProps{
		Title:  labelArrange,
		Anchor: anchor,
		Measure: func(gtx *layout.Context) image.Point {
			return g.measureArrangeContent(env, gtx, p)
		},
		Content: func(gtx *layout.Context) {
			g.layoutArrangeContent(gtx, env, sc, p)
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Closed {
		g.popFor = ""
	}
}

// arrangeRows builds the popover's rows for the slot at p: the four moves
// — disabled when the direction leaves the grid or would enter the main
// slot — then fill, split, make main and open in a new window.
func (g *slotGrid) arrangeRows(p Pos) []arrangeRow {
	labels := [4]string{"Move left", "Move right", "Move up", "Move down"}
	dirs := [4]Direction{DirLeft, DirRight, DirUp, DirDown}
	cmds := [4]arrangeCmd{arrangeMoveLeft, arrangeMoveRight, arrangeMoveUp, arrangeMoveDown}
	rows := []arrangeRow{{heading: true, label: "Move"}}
	for i := range dirs {
		_, ok := neighborCell(g.doc, p, dirs[i])
		rows = append(rows, arrangeRow{label: labels[i], disabled: !ok, do: cmds[i]})
	}
	splitable := len(g.doc.Columns[0].Slots) < maxRowsPerColumn
	return append(rows,
		arrangeRow{divider: true},
		arrangeRow{heading: true, label: labelArrange},
		arrangeRow{label: fillLabel(g.doc, p), do: arrangeFill},
		arrangeRow{label: "Split", disabled: !splitable, do: arrangeSplit},
		arrangeRow{label: "Make main", do: arrangeMakeMain},
		arrangeRow{label: "Open in new window", do: arrangeNewWindow},
	)
}

// measureArrangeContent sizes the rows without touching an input source:
// the popover measures into a discarded macro, so painting labels is fine.
func (g *slotGrid) measureArrangeContent(env *kit.Env, gtx *layout.Context, p Pos) image.Point {
	c := env.Th.Chrome()
	rows := g.arrangeRows(p)
	children := make([]layout.FlexChild, 0, len(rows))
	for i := range rows {
		row := rows[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			cell := gtx
			if row.divider {
				return arrangeDividerDims(cell.Dp(theme.SpaceXS))
			}
			if row.heading {
				return kit.Label(env, &cell, theme.TypeSmall(), c.FgMuted, row.label)
			}
			return g.arrPop.rowState(row).Measure(env, &cell, kit.ButtonProps{
				Text: row.label, Variant: kit.Ghost, Size: kit.SizeSM, Disabled: row.disabled,
			})
		}))
	}
	var box layout.Dimensions
	layout.Inset{
		Top: theme.SpaceXS, Bottom: theme.SpaceXS, Left: theme.SpaceXS, Right: theme.SpaceXS,
	}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		box = layout.Flex{Axis: layout.Vertical}.Layout(local, children...)
		return layout.Dimensions{}
	})
	return box.Size
}

// arrangeDividerDims is the divider row's size: gap, hairline, gap.
func arrangeDividerDims(gap int) layout.Dimensions {
	return layout.Dimensions{Size: image.Pt(0, 2*gap+max(int(theme.Hairline), 1))}
}

// layoutArrangeContent draws the rows; the popover calls it once per frame.
func (g *slotGrid) layoutArrangeContent(gtx *layout.Context, env *kit.Env, sc *screens, p Pos) {
	c := env.Th.Chrome()
	rows := g.arrangeRows(p)
	children := make([]layout.FlexChild, 0, len(rows))
	for i := range rows {
		row := rows[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Min = image.Pt(0, 0)
			if row.divider {
				return layout.Inset{Top: theme.SpaceXS, Bottom: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
					cell := gtx
					h := max(cell.Dp(theme.Hairline), 1)
					paint.FillShape(cell.Ops, c.BorderSubtle, clip.Rect{Max: image.Pt(cell.Constraints.Max.X, h)}.Op())
					return layout.Dimensions{Size: image.Pt(cell.Constraints.Max.X, h)}
				})
			}
			if row.heading {
				return kit.Label(env, &local, theme.TypeSmall(), c.FgMuted, row.label)
			}
			st := g.arrPop.rowState(row)
			props := kit.ButtonProps{Text: row.label, Variant: kit.Ghost, Size: kit.SizeSM, Disabled: row.disabled}
			clicked, err := st.Layout(env, &local, props)
			sc.fail(err)
			if clicked {
				g.arrange(p, row.do)
				g.popover.Close()
				g.popFor = ""
			}
			return st.Measure(env, &local, props)
		}))
	}
	layout.Inset{
		Top: theme.SpaceXS, Bottom: theme.SpaceXS, Left: theme.SpaceXS, Right: theme.SpaceXS,
	}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		local.Constraints.Min = image.Pt(0, 0)
		return layout.Flex{Axis: layout.Vertical}.Layout(local, children...)
	})
}

// rowState returns the row's button state.
func (a arrangeActions) rowState(row arrangeRow) *kit.ButtonState {
	switch row.do {
	case arrangeMoveLeft, arrangeMoveRight, arrangeMoveUp, arrangeMoveDown:
		return a.move[row.do]
	case arrangeFill:
		return a.fill
	case arrangeSplit:
		return a.split
	case arrangeMakeMain:
		return a.main
	case arrangeNewWindow:
		return a.newWin
	}
	return a.newWin
}

// fillLabel names the fill row: Fill, or Back to grid while filled.
func fillLabel(doc LayoutDoc, p Pos) string {
	if doc.FilledSlot == p.key() {
		return "Back to grid"
	}
	return "Fill"
}
