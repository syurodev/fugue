package kit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
)

// FlowItem is one child of a FlowRow: its measured size and the function
// that draws it at the origin of its slot. Size is measured by the caller
// (a button's Measure), so no widget is laid out twice in a frame.
type FlowItem struct {
	Size image.Point
	Draw func(gtx *layout.Context)
	// End packs the item against the right edge of the row it lands on;
	// consecutive End items keep their order.
	End bool
}

// FlowRow lays items left to right with gap between them and starts a new
// row, rowGap lower, when the next item would pass gtx.Constraints.Max.X. An
// item wider than the row sits alone on its row, narrowed to the row's
// width, and must truncate or wrap itself. It is the footer and header row
// of companion slots, where a plain horizontal Flex would push actions past
// the slot edge.
func FlowRow(gtx *layout.Context, gap, rowGap int, items []FlowItem) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	rects, size := FlowPlace(maxW, gap, rowGap, items)
	for i, it := range items {
		func() {
			defer op.Offset(rects[i].Min).Push(gtx.Ops).Pop()
			g := *gtx
			g.Constraints = layout.Constraints{Max: rects[i].Size()}
			it.Draw(&g)
		}()
	}
	return layout.Dimensions{Size: size}
}

// FlowPlace returns the rectangle of every item and the size of the whole
// row group for a row width of maxW. FlowRow draws from it, so a test of the
// rectangles is a test of what is drawn.
func FlowPlace(maxW, gap, rowGap int, items []FlowItem) ([]image.Rectangle, image.Point) {
	maxW = max(maxW, 0)
	sizes := make([]image.Point, len(items))
	for i, it := range items {
		sizes[i] = image.Pt(min(it.Size.X, maxW), it.Size.Y)
	}
	starts := flowRowStarts(sizes, maxW, gap)
	rects := make([]image.Rectangle, len(items))
	y, width := 0, 0
	for ri, lo := range starts {
		hi := len(items)
		if ri+1 < len(starts) {
			hi = starts[ri+1]
		}
		if ri > 0 {
			y += rowGap
		}
		rowH, rowW := placeFlowRow(items[lo:hi], sizes[lo:hi], rects[lo:hi], maxW, gap, y)
		y += rowH
		width = max(width, rowW)
	}
	return rects, image.Pt(width, y)
}

// flowRowStarts returns the index of the first item of every row.
func flowRowStarts(sizes []image.Point, maxW, gap int) []int {
	starts := make([]int, 0, 1+len(sizes)/2)
	rowW := 0
	for i, s := range sizes {
		switch {
		case i == 0:
			starts = append(starts, 0)
		case rowW+gap+s.X > maxW:
			starts = append(starts, i)
			rowW = 0
		default:
			rowW += gap
		}
		rowW += s.X
	}
	return starts
}

// placeFlowRow writes one row's rectangles at y and returns its height and
// the right edge of its last item.
func placeFlowRow(items []FlowItem, sizes []image.Point, rects []image.Rectangle, maxW, gap, y int) (rowH, right int) {
	endW := 0
	for i, it := range items {
		rowH = max(rowH, sizes[i].Y)
		if it.End {
			endW += sizes[i].X + gap
		}
	}
	x, endX := 0, max(maxW-endW+gap, 0)
	for i, it := range items {
		px := x
		if it.End {
			px, endX = endX, endX+sizes[i].X+gap
		} else {
			x += sizes[i].X + gap
		}
		rects[i] = image.Rectangle{Min: image.Pt(px, y+(rowH-sizes[i].Y)/2)}
		rects[i].Max = rects[i].Min.Add(sizes[i])
		right = max(right, rects[i].Max.X)
	}
	return rowH, right
}
