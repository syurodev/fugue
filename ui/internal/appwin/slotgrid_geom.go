package appwin

import (
	"image"
	"math"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The grid's geometry (§5.3): weights to pixels with the same minimum
// clamping the nested SplitPanes apply, so the drawn cells and the handles
// agree to the pixel.

// cellGeom is one cell's laid-out rectangle.
type cellGeom struct {
	pos  Pos
	Rect image.Rectangle
}

// computeRects lays out the visible columns' cells into area: columns split
// horizontally by their weights, rows vertically inside each column, leaving
// IslandGap (2 dp) between adjacent islands (FUG-92).
func (g *slotGrid) computeRects(gtx *layout.Context, area image.Rectangle, visible int) []cellGeom {
	if visible <= 0 || area.Empty() {
		return nil
	}
	gap := gtx.Dp(theme.IslandGap)
	weights := columnWeights(g.doc, visible)
	minsPx := dpToPx(gtx, colMins(0, visible))
	availW := max(area.Dx()-(visible-1)*gap, 0)
	widths := splitPx(weights, availW, minsPx)
	geom := make([]cellGeom, 0, visible*maxRowsPerColumn)
	x := area.Min.X
	for c := range visible {
		col := g.doc.Columns[c]
		colRect := image.Rect(x, area.Min.Y, x+widths[c], area.Max.Y)
		availH := max(colRect.Dy()-(len(col.Slots)-1)*gap, 0)
		heights := splitPx(rowWeights(col), availH, dpToPx(gtx, rowMins(len(col.Slots))))
		y := colRect.Min.Y
		for r := range col.Slots {
			geom = append(geom, cellGeom{pos: Pos{Col: c, Row: r}, Rect: image.Rect(colRect.Min.X, y, colRect.Max.X, y+heights[r])})
			y += heights[r] + gap
		}
		x += widths[c] + gap
	}
	return geom
}

// dpToPx converts dp sizes to pixels at the frame's metric.
func dpToPx(gtx *layout.Context, vals []unit.Dp) []int {
	out := make([]int, len(vals))
	for i, v := range vals {
		out[i] = gtx.Dp(v)
	}
	return out
}

// splitPx turns weights into pixel sizes that respect each side's minimum:
// boundary i takes its weight's share of the remaining span — the same
// nested ratio SplitPane applies — so boundaries and handles land on the
// same pixel.
func splitPx(weights []float32, total int, mins []int) []int {
	out := make([]int, len(weights))
	if len(weights) == 0 || total <= 0 {
		return out
	}
	rest := total
	restWeights := weights
	for i := range len(weights) - 1 {
		sum := float32(0)
		for _, w := range restWeights {
			sum += max(w, 0)
		}
		share := rest / (len(weights) - i)
		if sum > 0 {
			share = int(math.Round(float64(max(restWeights[0], 0) / sum * float32(rest))))
		}
		lo := min(max(mins[i], 0), rest)
		hi := max(rest-minSum(mins[i+1:]), 0)
		px := min(max(share, lo), hi)
		out[i] = px
		rest -= px
		restWeights = restWeights[1:]
	}
	out[len(out)-1] = max(rest, 0)
	return out
}

// minSum is the px sum of the minimums after a boundary.
func minSum(mins []int) int {
	total := 0
	for _, m := range mins {
		total += m
	}
	return total
}

// columnWeights lists the visible columns' width weights.
func columnWeights(doc LayoutDoc, visible int) []float32 {
	out := make([]float32, 0, visible)
	for i := 0; i < visible && i < len(doc.Columns); i++ {
		out = append(out, doc.Columns[i].WidthWeight)
	}
	return out
}

// rowWeights lists a column's slots' height weights.
func rowWeights(col Column) []float32 {
	out := make([]float32, len(col.Slots))
	for i, slot := range col.Slots {
		out[i] = slot.HeightWeight
	}
	return out
}

// rowMins lists a column's rows' minimum heights.
func rowMins(rows int) []unit.Dp {
	out := make([]unit.Dp, rows)
	for i := range out {
		out[i] = theme.SlotMinHeight
	}
	return out
}

// weightSum is the visible columns' weight sum from first on.
func weightSum(doc LayoutDoc, first, visible int) float32 {
	sum := float32(0)
	for i := first; i < visible && i < len(doc.Columns); i++ {
		sum += max(doc.Columns[i].WidthWeight, 0)
	}
	return max(sum, 0.001)
}

// columnRatio is the boundary after column first's share of the columns it
// divides: first's weight over the sum from first to visible.
func columnRatio(doc LayoutDoc, first, visible int) float32 {
	// first may name a column a close inside the frame took away (FUG-64).
	if first < 0 || first >= len(doc.Columns) {
		return 0
	}
	visible = min(visible, len(doc.Columns))
	firstW := max(doc.Columns[first].WidthWeight, 0)
	return firstW / weightSum(doc, first, visible)
}

// defaultColumnRatio is the equal share of the columns a boundary divides.
func defaultColumnRatio(first, visible int) float32 {
	return 1 / float32(visible-first)
}

// setColumnRatio writes a boundary's ratio back into the weights: first's
// weight takes the ratio's share, the rest re-scale to the remainder.
func (g *slotGrid) setColumnRatio(first, visible int, ratio float32) {
	// first may name a column a close inside the frame took away (FUG-64).
	if first < 0 || first >= len(g.doc.Columns) {
		return
	}
	visible = min(visible, len(g.doc.Columns))
	sum := weightSum(g.doc, first, visible)
	g.doc.Columns[first].WidthWeight = ratio * sum
	restOthers := weightSum(g.doc, first+1, visible)
	if restOthers <= 0 {
		return
	}
	scale := (1 - ratio) * sum / restOthers
	for i := first + 1; i < visible; i++ {
		g.doc.Columns[i].WidthWeight = max(g.doc.Columns[i].WidthWeight, 0) * scale
	}
}

// rowRatio is a column's top row's share of its two rows.
func rowRatio(col Column) float32 {
	w0 := max(col.Slots[0].HeightWeight, 0)
	sum := max(w0+max(col.Slots[1].HeightWeight, 0), 0.001)
	return w0 / sum
}

// rectOf returns the laid-out rect of p.
func (g *slotGrid) rectOf(geom []cellGeom, p Pos) image.Rectangle {
	for _, cg := range geom {
		if cg.pos == p {
			return cg.Rect
		}
	}
	return image.Rectangle{}
}

// recordRects notes this frame's cells; the animations an edit armed read
// their slide target from here.
func (g *slotGrid) recordRects(geom []cellGeom) {
	rects := make(map[string]image.Rectangle, len(geom))
	refRects := make(map[string]image.Rectangle, len(geom))
	for _, cg := range geom {
		ref := refAt(g.doc, cg.pos)
		rects[cg.pos.key()] = cg.Rect
		refRects[keyOfRef(ref)] = cg.Rect
	}
	g.rects, g.refRects = rects, refRects
}

// overlaySplits lays the SplitPane handles over the boundaries, keeping handle
// state and keyboard focus alive while animating and tracking moving rects.
func (g *slotGrid) overlaySplits(gtx *layout.Context, env *kit.Env, geom []cellGeom, area image.Rectangle, visible int) {
	visible = min(visible, len(g.doc.Columns))
	if visible <= 0 {
		return
	}
	animating := len(g.anims) > 0
	for i := range visible - 1 {
		g.overlayColSplit(gtx, env, geom, area, visible, i, animating)
	}
	for c := range visible {
		g.overlayRowSplit(gtx, env, geom, visible, c)
	}
}

func (g *slotGrid) overlayColSplit(gtx *layout.Context, env *kit.Env, geom []cellGeom, area image.Rectangle, visible, i int, animating bool) {
	state, ok := g.splitC[i]
	if !ok {
		state = kit.NewSplitPaneState()
		g.splitC[i] = state
	}
	left, props := g.colSplitProps(gtx, geom, area, visible, i, animating)
	sub := image.Rect(left.Min.X, area.Min.Y, area.Max.X, area.Max.Y)
	local := *gtx
	local.Constraints = layout.Exact(sub.Size())
	atPoint(&local, sub.Min, func(gtx *layout.Context) {
		local := *gtx
		next, changed, _ := state.Layout(env, &local, props, emptyPane, emptyPane)
		if changed && !animating {
			gap := gtx.Dp(theme.IslandGap)
			avail := float32(max(sub.Dx()-(visible-1-i)*gap, 1))
			r := min(max((next*float32(sub.Dx())-float32(gap)/2)/avail, 0), 1)
			g.setColumnRatio(i, visible, r)
			g.edited(g.doc)
		}
	})
}

func (g *slotGrid) colSplitProps(gtx *layout.Context, geom []cellGeom, area image.Rectangle, visible, i int, animating bool) (image.Rectangle, kit.SplitPaneProps) {
	left := g.rectOf(geom, Pos{Col: i, Row: 0})
	gap := gtx.Dp(theme.IslandGap)
	gapHalf := theme.IslandGap / 2
	subW := area.Max.X - left.Min.X
	availW := float32(max(subW-(visible-1-i)*gap, 1))
	defRatio := defaultColumnRatio(i, visible)
	ratio := columnRatio(g.doc, i, visible)
	if subW > 0 {
		ratio = (float32(left.Dx()) + float32(gap)/2) / float32(subW)
		defRatio = (defRatio*availW + float32(gap)/2) / float32(subW)
	}
	props := kit.SplitPaneProps{
		Orientation:  kit.Horizontal,
		Ratio:        ratio,
		DefaultRatio: defRatio,
		MinFirst:     colMin(i) + gapHalf,
		MinSecond:    sumDp(colMins(i+1, visible)) + unit.Dp(visible-1-i)*theme.IslandGap - gapHalf,
		HideRestLine: true,
	}
	if !animating {
		return left, props
	}
	left = g.drawnCellRect(geom, Pos{Col: i, Row: 0})
	if totalW := area.Max.X - left.Min.X; totalW > 0 {
		props.Ratio = (float32(left.Dx()) + float32(gap)/2) / float32(totalW)
	}
	props.MinFirst, props.MinSecond = 0, 0
	return left, props
}

// overlayRowSplit lays one two-row column's vertical SplitPane over its
// cells and writes a moved ratio back into the row weights.
func (g *slotGrid) overlayRowSplit(gtx *layout.Context, env *kit.Env, geom []cellGeom, visible, c int) {
	if c < 0 || c >= visible || c >= len(g.doc.Columns) || len(g.doc.Columns[c].Slots) < maxRowsPerColumn {
		return
	}
	state, ok := g.splitR[c]
	if !ok {
		state = kit.NewSplitPaneState()
		g.splitR[c] = state
	}
	animating := len(g.anims) > 0
	colRect, props, ok := g.rowSplitProps(gtx, geom, c, animating)
	if !ok {
		return
	}
	local := *gtx
	local.Constraints = layout.Exact(colRect.Size())
	atPoint(&local, colRect.Min, func(gtx *layout.Context) {
		local := *gtx
		next, changed, _ := state.Layout(env, &local, props, emptyPane, emptyPane)
		if changed && !animating {
			gap := gtx.Dp(theme.IslandGap)
			avail := float32(max(colRect.Dy()-gap, 1))
			r := min(max((next*float32(colRect.Dy())-float32(gap)/2)/avail, 0), 1)
			col := g.doc.Columns[c]
			col.Slots[0].HeightWeight = r
			col.Slots[1].HeightWeight = 1 - r
			g.edited(g.doc)
		}
	})
}

func (g *slotGrid) rowSplitProps(gtx *layout.Context, geom []cellGeom, c int, animating bool) (image.Rectangle, kit.SplitPaneProps, bool) {
	top := g.rectOf(geom, Pos{Col: c, Row: 0})
	bottom := g.rectOf(geom, Pos{Col: c, Row: 1})
	if top.Empty() || (bottom.Empty() && !animating) {
		return image.Rectangle{}, kit.SplitPaneProps{}, false
	}
	colRect := image.Rect(top.Min.X, top.Min.Y, top.Max.X, max(bottom.Max.Y, top.Max.Y))
	gap := gtx.Dp(theme.IslandGap)
	gapHalf := theme.IslandGap / 2
	ratio := rowRatio(g.doc.Columns[c])
	if colRect.Dy() > 0 {
		ratio = (float32(top.Dy()) + float32(gap)/2) / float32(colRect.Dy())
	}
	props := kit.SplitPaneProps{
		Orientation:  kit.Vertical,
		Ratio:        ratio,
		DefaultRatio: 0.5,
		MinFirst:     theme.SlotMinHeight + gapHalf,
		MinSecond:    theme.SlotMinHeight + theme.IslandGap - gapHalf,
		HideRestLine: true,
	}
	if animating {
		top = g.drawnCellRect(geom, Pos{Col: c, Row: 0})
		bottom = g.drawnCellRect(geom, Pos{Col: c, Row: 1})
		colRect = image.Rect(top.Min.X, top.Min.Y, top.Max.X, max(bottom.Max.Y, top.Max.Y))
		if totalH := colRect.Dy(); totalH > 0 {
			props.Ratio = (float32(top.Dy()) + float32(gap)/2) / float32(totalH)
		}
		props.MinFirst, props.MinSecond = 0, 0
	}
	return colRect, props, true
}

func emptyPane(layout.Context) layout.Dimensions { return layout.Dimensions{} }
