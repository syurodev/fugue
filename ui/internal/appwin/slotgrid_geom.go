package appwin

import (
	"image"
	"math"

	"gioui.org/layout"
	"gioui.org/unit"

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
// horizontally by their weights, rows vertically inside each column.
func (g *slotGrid) computeRects(gtx *layout.Context, area image.Rectangle, visible int) []cellGeom {
	if visible <= 0 || area.Empty() {
		return nil
	}
	weights := columnWeights(g.doc, visible)
	minsPx := dpToPx(gtx, colMins(0, visible))
	widths := splitPx(weights, area.Dx(), minsPx)
	geom := make([]cellGeom, 0, visible*maxRowsPerColumn)
	x := area.Min.X
	for c := range visible {
		col := g.doc.Columns[c]
		colRect := image.Rect(x, area.Min.Y, x+widths[c], area.Max.Y)
		heights := splitPx(rowWeights(col), colRect.Dy(), dpToPx(gtx, rowMins(len(col.Slots))))
		y := colRect.Min.Y
		for r := range col.Slots {
			geom = append(geom, cellGeom{pos: Pos{Col: c, Row: r}, Rect: image.Rect(colRect.Min.X, y, colRect.Max.X, y+heights[r])})
			y += heights[r]
		}
		x += widths[c]
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
