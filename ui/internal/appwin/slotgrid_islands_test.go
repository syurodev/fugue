package appwin

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestSlotGridIslandGapsAndHideRestLine verifies FUG-92: computeRects leaves
// IslandGap (4 dp) between adjacent columns and stacked rows, and split pane
// overlays center on the gutter with HideRestLine enabled.
func TestSlotGridIslandGapsAndHideRestLine(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = gridFixture()

	var ops op.Ops
	area := image.Rect(0, 0, 1200, 800)
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(area.Size()),
	}
	gap := gtx.Dp(theme.IslandGap)
	if gap != 4 {
		t.Fatalf("IslandGap = %d px, want 4", gap)
	}

	geom := g.computeRects(&gtx, area, len(g.doc.Columns))
	c0r0 := g.rectOf(geom, Pos{Col: 0, Row: 0})
	c0r1 := g.rectOf(geom, Pos{Col: 0, Row: 1})
	c1r0 := g.rectOf(geom, Pos{Col: 1, Row: 0})
	c2r0 := g.rectOf(geom, Pos{Col: 2, Row: 0})

	if got := c1r0.Min.X - c0r0.Max.X; got != gap {
		t.Fatalf("col 0->1 gap = %d px, want %d", got, gap)
	}
	if got := c2r0.Min.X - c1r0.Max.X; got != gap {
		t.Fatalf("col 1->2 gap = %d px, want %d", got, gap)
	}
	if got := c0r1.Min.Y - c0r0.Max.Y; got != gap {
		t.Fatalf("row 0->1 gap = %d px, want %d", got, gap)
	}

	_, colProps := g.colSplitProps(&gtx, geom, area, len(g.doc.Columns), 0, false)
	if !colProps.HideRestLine {
		t.Fatal("colSplitProps.HideRestLine = false, want true")
	}
	subW := area.Max.X - c0r0.Min.X
	if gotPx := int(colProps.Ratio*float32(subW) + 0.5); gotPx != c0r0.Dx()+gap/2 {
		t.Fatalf("col split center = %d, want %d", gotPx, c0r0.Dx()+gap/2)
	}

	colRect, rowProps, ok := g.rowSplitProps(&gtx, geom, 0, false)
	if !ok || !rowProps.HideRestLine {
		t.Fatalf("rowSplitProps ok=%v HideRestLine=%v, want true, true", ok, rowProps.HideRestLine)
	}
	if gotPy := int(rowProps.Ratio*float32(colRect.Dy()) + 0.5); gotPy != c0r0.Dy()+gap/2 {
		t.Fatalf("row split center = %d, want %d", gotPy, c0r0.Dy()+gap/2)
	}
}
