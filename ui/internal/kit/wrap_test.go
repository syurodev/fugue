package kit

import (
	"image"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestParagraphBreaksATokenLongerThanTheLine is FUG-40: verbatim text (an
// approval's command) holds words that never fit a 300 dp slot. The
// paragraph must break them at the line edge, so every rune is laid out
// inside the width, instead of letting one line run past it.
func TestParagraphBreaksATokenLongerThanTheLine(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.frame(t)
	tok := theme.TypeCode()
	one := MeasureLabel(h.env, &h.gtx, tok, "X")
	const width = 120
	const runes = 300
	g := h.gtx
	g.Constraints = layout.Constraints{Max: image.Pt(width, 1<<20)}
	m := op.Record(g.Ops)
	dims := Paragraph(h.env, &g, tok, h.env.Th.Chrome().Fg, strings.Repeat("X", runes))
	m.Stop()
	perLine := width / one.X
	wantLines := (runes + perLine - 1) / perLine
	if got := dims.Size.Y / one.Y; got < wantLines {
		t.Fatalf("a %d-rune token laid out in %d lines of %d px, want at least %d: it overflowed the width", runes, got, one.Y, wantLines)
	}
	if dims.Size.X > width {
		t.Fatalf("paragraph is %d px wide in a %d px slot", dims.Size.X, width)
	}
}

// TestCenteredParagraphWrapsAndFillsTheWidth covers the empty states' text.
func TestCenteredParagraphWrapsAndFillsTheWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.frame(t)
	g := h.gtx
	g.Constraints = layout.Constraints{Max: image.Pt(100, 1<<20)}
	m := op.Record(g.Ops)
	dims := CenteredParagraph(h.env, &g, theme.TypeBody(), h.env.Th.Chrome().Fg, "Approval requests and agent questions from sessions will appear here.")
	m.Stop()
	line := MeasureLabel(h.env, &h.gtx, theme.TypeBody(), "A").Y
	if dims.Size.X != 100 || dims.Size.Y < 3*line {
		t.Fatalf("centered paragraph is %v, want 100 px wide and at least 3 lines of %d px", dims.Size, line)
	}
}

// TestEmptyStateKeepsItsTextInsideANarrowSlot: the title and description
// wrap inside the slot, the buttons stack, and nothing is laid out wider than
// the slot.
func TestEmptyStateKeepsItsTextInsideANarrowSlot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var runs []image.Rectangle
	h.env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }
	st := NewEmptyStateState()
	var dims layout.Dimensions
	h.frameSize(t, 276, 400)
	g := h.gtx
	var err error
	dims, _, err = st.Layout(h.env, &g, &EmptyStateProps{
		Icon: IconCheck, Title: "No pending approvals",
		Description:   "Approval requests and agent questions from sessions will appear here.",
		PrimaryAction: "Open the approvals inbox now", NoResults: true, ClearFilterAction: "Clear every filter",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) < 2 {
		t.Fatalf("recorded %d text runs, want the title and the description", len(runs))
	}
	for _, r := range runs {
		if r.Min.X < 0 || r.Max.X > 276 {
			t.Fatalf("text run %v leaves the 276 px slot", r)
		}
	}
	if dims.Size.X != 276 {
		t.Fatalf("empty state is %d px wide, want 276", dims.Size.X)
	}
}

func TestFlowPlaceWrapsAndKeepsEveryItemInsideTheRow(t *testing.T) {
	t.Parallel()
	items := []FlowItem{
		{Size: image.Pt(60, 20)}, {Size: image.Pt(100, 24)}, {Size: image.Pt(120, 20)}, {Size: image.Pt(500, 20)},
	}
	rects, size := FlowPlace(200, 8, 6, items)
	want := []image.Rectangle{
		image.Rect(0, 2, 60, 22), image.Rect(68, 0, 168, 24),
		image.Rect(0, 30, 120, 50), image.Rect(0, 56, 200, 76),
	}
	for i := range want {
		if rects[i] != want[i] {
			t.Fatalf("item %d at %v, want %v", i, rects[i], want[i])
		}
	}
	if size != image.Pt(200, 76) {
		t.Fatalf("size %v, want 200x76", size)
	}
}

func TestFlowPlacePacksEndItemsAgainstTheRightEdge(t *testing.T) {
	t.Parallel()
	items := []FlowItem{
		{Size: image.Pt(50, 20)},
		{Size: image.Pt(20, 20), End: true},
		{Size: image.Pt(40, 20), End: true},
	}
	rects, _ := FlowPlace(200, 8, 6, items)
	if rects[1].Min.X != 200-8-20-40 || rects[2].Max.X != 200 {
		t.Fatalf("end items at %v and %v, want them packed to x=200", rects[1], rects[2])
	}
	if rects[1].Max.X+8 != rects[2].Min.X {
		t.Fatalf("end items %v and %v are not one gap apart", rects[1], rects[2])
	}
	// An end item that does not fit beside the rest wraps alone and still
	// ends at the right edge.
	rects, _ = FlowPlace(100, 8, 6, items)
	if rects[1].Max.X != 100 || rects[2].Max.X != 100 || rects[2].Min.Y <= rects[1].Min.Y {
		t.Fatalf("wrapped end items at %v and %v", rects[1], rects[2])
	}
}
