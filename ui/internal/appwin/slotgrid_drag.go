package appwin

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The drag, the drop zones and the narrow-window fold's visuals (desktop-app
// §5.3, DA-E09): a drag tracks the pointer without a spring, the grid shows
// only the valid drop zones — never a fourth column, a third row, left of
// the main slot, or the main slot's own cell — Escape cancels, and the
// folded columns live in an edge tab strip whose tab slides the slot out as
// an overlay drawer on Smooth.

// zoneRect pairs a drop target with its rectangle in grid coordinates.
type zoneRect struct {
	target DropTarget
	Rect   image.Rectangle
}

// dropZoneRects gives every valid target its rectangle: column edges are
// narrow strips on the boundary, cell halves are the cell's halves, and a
// swap is the center of the cell.
func dropZoneRects(gtx *layout.Context, geom []cellGeom, area image.Rectangle, targets []DropTarget) []zoneRect {
	out := make([]zoneRect, 0, len(targets))
	for _, t := range targets {
		zr := zoneRect{target: t, Rect: dropZoneRect(gtx, geom, area, t)}
		if !zr.Rect.Empty() {
			out = append(out, zr)
		}
	}
	return out
}

// dropZoneRect computes one target's rectangle; empty when the target's
// column or cell is not on screen.
func dropZoneRect(gtx *layout.Context, geom []cellGeom, area image.Rectangle, t DropTarget) image.Rectangle {
	switch t.Kind {
	case DropColumnLeft, DropColumnRight:
		return columnEdgeRect(geom, area, gtx.Dp(theme.DropEdgeWidth), t)
	case DropCellTop, DropCellBottom:
		return cellHalfRect(geom, t)
	case DropSwap:
		return swapRect(geom, t)
	}
	return image.Rectangle{}
}

// columnEdgeRect is the strip across the whole grid at a column's edge,
// edge pixels wide.
func columnEdgeRect(geom []cellGeom, area image.Rectangle, edge int, t DropTarget) image.Rectangle {
	colRect := columnRect(geom, t.Pos.Col, area)
	if colRect.Empty() {
		return image.Rectangle{}
	}
	x := colRect.Min.X
	if t.Kind == DropColumnRight {
		x = colRect.Max.X
	}
	return image.Rect(x-edge/2, area.Min.Y, x-edge/2+edge, area.Max.Y)
}

// cellHalfRect is the target cell's top or bottom half.
func cellHalfRect(geom []cellGeom, t DropTarget) image.Rectangle {
	cell := rectOfGeom(geom, t.Pos)
	if cell.Empty() {
		return image.Rectangle{}
	}
	mid := (cell.Min.Y + cell.Max.Y) / 2
	if t.Kind == DropCellTop {
		return image.Rect(cell.Min.X, cell.Min.Y, cell.Max.X, mid)
	}
	return image.Rect(cell.Min.X, mid, cell.Max.X, cell.Max.Y)
}

// swapRect is the target cell's center quarter, the "drop onto this slot"
// region (§5.3).
func swapRect(geom []cellGeom, t DropTarget) image.Rectangle {
	cell := rectOfGeom(geom, t.Pos)
	if cell.Empty() {
		return image.Rectangle{}
	}
	return shrinkRect(cell, min(cell.Dx(), cell.Dy())/4)
}

// columnRect is a whole column's span within the grid area.
func columnRect(geom []cellGeom, col int, area image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for _, cg := range geom {
		if cg.pos.Col != col {
			continue
		}
		if out.Empty() {
			out = image.Rect(cg.Rect.Min.X, area.Min.Y, cg.Rect.Max.X, area.Max.Y)
			break
		}
	}
	return out
}

// rectOfGeom is a cell's rect by position.
func rectOfGeom(geom []cellGeom, p Pos) image.Rectangle {
	for _, cg := range geom {
		if cg.pos == p {
			return cg.Rect
		}
	}
	return image.Rectangle{}
}

// shrinkRect insets a rect by n on each side, keeping it non-empty.
func shrinkRect(r image.Rectangle, n int) image.Rectangle {
	n = min(n, min(r.Dx(), r.Dy())/2-1)
	if n <= 0 {
		return r
	}
	return image.Rect(r.Min.X+n, r.Min.Y+n, r.Max.X-n, r.Max.Y-n)
}

// wash fades a theme color by alpha, for the drop-zone washes and the dimmed
// origin cell.
func wash(c color.NRGBA, alpha float32) color.NRGBA {
	c.A = uint8(float32(c.A) * min(max(alpha, 0), 1))
	return c
}

// previewRect is the dragged slot's ghost box: the pointer minus the grab,
// clamped into the grid area.
func (g *slotGrid) previewRect(area image.Rectangle, size image.Point) image.Rectangle {
	topLeft := g.drag.pointer.Sub(g.drag.grab)
	clamped := image.Pt(
		min(max(topLeft.X, area.Min.X), max(area.Max.X-size.X, area.Min.X)),
		min(max(topLeft.Y, area.Min.Y), max(area.Max.Y-size.Y, area.Min.Y)))
	return image.Rectangle{Min: clamped, Max: clamped.Add(size)}
}

// dragState is one header drag (§5.3, DA-E09): armed on press, active once
// the pointer passes DragSlop, tracking the pointer without a spring. The
// pointer position and the grab offset are grid coordinates — the filter
// registers at the grid level.
type dragState struct {
	armed  bool
	active bool
	// pressed marks a header press of this drag; a Cancel without one never
	// belonged to it.
	pressed bool
	pos     Pos
	origin  image.Point
	// pressAt is the press point, grab the offset inside the slot the ghost
	// keeps, pointer the live pointer.
	pressAt image.Point
	grab    image.Point
	pointer image.Point
}

// layoutDrag draws the active drag: the valid zones as rounded translucent
// panels — idle a faint info wash, hovered a stronger one with a crisper
// outline — the dragged slot's ghost under the pointer, and the origin cell
// dimmed (§5.3, DA-E09).
func (g *slotGrid) layoutDrag(gtx *layout.Context, env *kit.Env, sc *screens, geom []cellGeom, area image.Rectangle) {
	zones := dropZoneRects(gtx, geom, area, ValidDropTargets(g.doc, g.drag.pos))
	hovered := -1
	for i, z := range zones {
		if g.drag.pointer.In(z.Rect) {
			hovered = i
			break
		}
	}
	inset := gtx.Dp(theme.SpaceXS)
	radius := gtx.Dp(theme.RadiusIsland)
	info := env.Th.Status().Info
	for i, z := range zones {
		hover := i == hovered
		inner := shrinkRect(z.Rect, inset)
		if inner.Empty() {
			continue
		}
		alpha := float32(0.08)
		line := float32(0.50)
		if hover {
			alpha, line = 0.18, 0.85
		}
		rr := clip.UniformRRect(inner, radius)
		paint.FillShape(gtx.Ops, wash(info, alpha), rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, wash(info, line),
			clip.Stroke{Path: rr.Path(gtx.Ops), Width: 1.5}.Op())
	}
	if origin := rectOfGeom(geom, g.drag.pos); !origin.Empty() {
		paint.FillShape(gtx.Ops, wash(env.Th.Chrome().CanvasBg, 0.6), clip.UniformRRect(origin, radius).Op(gtx.Ops))
		ghost := g.previewRect(area, image.Pt(min(origin.Dx(), area.Dx()/2), gtx.Dp(theme.SlotHeaderHeight)*3))
		g.drawGhost(gtx, env, sc, ghost)
	}
}

// drawGhost draws the dragged slot's preview card: a rounded, floating
// chrome-only card under its own clip, tracking the pointer (§5.3 — no
// spring).
func (g *slotGrid) drawGhost(gtx *layout.Context, env *kit.Env, sc *screens, rect image.Rectangle) {
	c := env.Th.Chrome()
	h := gtx.Dp(theme.SlotHeaderHeight)
	if rect.Dy() < h {
		return
	}
	radius := gtx.Dp(theme.RadiusIsland)
	kit.DrawFloatShadow(gtx, rect, radius)
	defer clip.UniformRRect(rect, radius).Push(gtx.Ops).Pop()
	rr := clip.UniformRRect(rect, radius)
	paint.FillShape(gtx.Ops, c.SurfaceRaised, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, c.SurfaceBase, clip.Rect(image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+h)).Op())
	paint.FillShape(gtx.Ops, c.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1))}.Op())
	ref := refAt(g.doc, g.drag.pos)
	atPoint(gtx, rect.Min, func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: rect.Size()}
		layout.Inset{Left: theme.SpaceMD, Right: theme.SpaceMD}.Layout(local, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(local,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					local := gtx
					dims, err := env.Icon(&local, slotIcon(ref.Kind), theme.IconSM, c.FgMuted)
					sc.fail(err)
					return layout.Inset{Right: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
						return dims
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					local := gtx
					return kit.Label(env, &local, theme.TypeSmall(), c.Fg, slotTitle(sc, ref))
				}),
			)
		})
	})
}

// finishDrag ends an active drag: the drop target under the pointer
// applies, anything else slides the slot back to its cell (§5.3). The zones
// and cells recompute against the frame's stored area.
func (g *slotGrid) finishDrag(gtx *layout.Context) {
	geom := g.computeRects(gtx, g.lastArea, g.lastVisible)
	zones := dropZoneRects(gtx, geom, g.lastArea, ValidDropTargets(g.doc, g.drag.pos))
	for _, z := range zones {
		if g.drag.pointer.In(z.Rect) {
			dragged := g.drag.pos
			g.drag = dragState{}
			g.drop(dragged, z.target)
			return
		}
	}
	g.cancelDrag()
}

// foldedCells lists the folded regular columns' cells, right to left, in
// tab order.
func (g *slotGrid) foldedCells(plan PlanFoldResult) []Pos {
	first := len(g.doc.Columns) - plan.FoldedCols
	var out []Pos
	for c := len(g.doc.Columns) - 1; c >= first; c-- {
		for r := range g.doc.Columns[c].Slots {
			out = append(out, Pos{Col: c, Row: r})
		}
	}
	return out
}

// layoutDrawer slides the drawer slot out over the grid on Smooth (§5.3,
// FUG-94) right next to the permanent Right Stripe as an 8 dp rounded island.
func (g *slotGrid) layoutDrawer(gtx *layout.Context, env *kit.Env, sc *screens, area image.Rectangle) {
	if env.ReducedMotion {
		g.drawerPresence.Snap(g.drawerPresence.Shown())
	} else if g.drawerPresence.Advance(env.DT()) {
		env.Keep()
	}
	if !g.drawerPresence.Visible() {
		if !g.drawerPresence.Shown() {
			g.drawer = ""
		}
		return
	}
	c, r, ok := parseSlotKey(g.drawer)
	if !ok {
		return
	}
	w := min(gtx.Dp(theme.SlotMinWidth), area.Dx())
	p := g.drawerPresence.Progress()
	x := area.Max.X - int(float32(w)*p+0.5)
	rect := image.Rect(x, area.Min.Y, x+w, area.Max.Y)
	kit.DrawFloatShadow(gtx, rect, gtx.Dp(theme.RadiusIsland))
	g.drawSlot(gtx, env, sc, Pos{Col: c, Row: r}, rect)
}
