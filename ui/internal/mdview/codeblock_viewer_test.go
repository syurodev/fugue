package mdview

import (
	"fmt"
	"image"
	"strings"
	"testing"
)

// TestCodeBlockHighlightsAndScrollsToTheRequestedLines pins the file viewer's
// jump-and-highlight props (F10.4e): ScrollToLine moves the offset once to the
// exact measured row and clamps to the bounds, later frames keep the reader's
// scroll, and diffs ignore the jump.
func TestCodeBlockHighlightsAndScrollsToTheRequestedLines(t *testing.T) {
	t.Parallel()
	props := &CodeBlockProps{
		Format: "go", Raw: numberedLines(200), ShowLineNumbers: true, MaxLines: -1,
		HighlightLine: 100, HighlightEndLine: 104,
	}
	h := newBlockHarness(t, 480, props)
	h.st.Expanded = true

	h.props.ScrollToLine = 100
	h.frame()
	if h.st.ScrollY <= 0 {
		t.Fatalf("ScrollToLine 100 left ScrollY at %d", h.st.ScrollY)
	}
	scrolled := h.st.ScrollY

	// A later frame without the jump keeps the reader's offset.
	h.props.ScrollToLine = 0
	h.frame()
	if h.st.ScrollY != scrolled {
		t.Fatalf("the jump re-applied itself: %d -> %d", scrolled, h.st.ScrollY)
	}
}

// TestCodeBlockScrollToLineClampsToBounds pins the clamp: a jump past the end
// lands on the last viewport, and a diff never jumps.
func TestCodeBlockScrollToLineClampsToBounds(t *testing.T) {
	t.Parallel()
	props := &CodeBlockProps{
		Format: "go", Raw: numberedLines(200), ShowLineNumbers: true, MaxLines: -1, ScrollToLine: 100000,
	}
	h := newBlockHarness(t, 480, props)
	h.st.Expanded = true
	h.frame()
	if h.st.ScrollY != h.st.maxScrollY {
		t.Fatalf("overshooting jump = %d, want clamped to %d", h.st.ScrollY, h.st.maxScrollY)
	}

	diffProps := &CodeBlockProps{
		Title: "x.go", IsDiff: true, ShowLineNumbers: true, MaxLines: -1,
		Raw: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", ScrollToLine: 2,
	}
	dh := newBlockHarness(t, 480, diffProps)
	dh.frame()
	if dh.st.ScrollY != 0 {
		t.Fatalf("diff jumped to %d, want 0", dh.st.ScrollY)
	}
}

// TestCollectHighlightRectCoversExactlyTheRange pins the tint geometry over
// uniform rows: rows first..last, no more, across the whole body width even
// when a row is wider than the body (horizontal scroll).
func TestCollectHighlightRectCoversExactlyTheRange(t *testing.T) {
	t.Parallel()
	lineH, pad, bodyTop, bodyW := 20, 12, 100, 300
	flows := make([]*flow, 10)
	for i := range flows {
		flows[i] = &flow{width: 200, height: lineH}
	}
	flows[3].width = 900
	props := &CodeBlockProps{HighlightLine: 3, HighlightEndLine: 5}
	rect := collectHighlightRect(props, flows, len(flows), pad, bodyTop, bodyW, 0, lineH)
	if rect == nil {
		t.Fatal("no rect for rows 3..5")
	}
	want := image.Rect(pad, bodyTop+2*lineH, pad+bodyW, bodyTop+5*lineH)
	if *rect != want {
		t.Fatalf("rect = %+v, want %+v", *rect, want)
	}

	props.HighlightLine, props.HighlightEndLine = 0, 0
	if unset := collectHighlightRect(props, flows, len(flows), pad, bodyTop, bodyW, 0, lineH); unset != nil {
		t.Fatalf("unset range drew %+v", unset)
	}
	props.HighlightLine, props.HighlightEndLine = 11, 12
	if outside := collectHighlightRect(props, flows, len(flows), pad, bodyTop, bodyW, 0, lineH); outside != nil {
		t.Fatalf("out-of-range range drew %+v", outside)
	}
	props.HighlightLine, props.HighlightEndLine = 7, 0
	rect = collectHighlightRect(props, flows, len(flows), pad, bodyTop, bodyW, 0, lineH)
	if rect == nil || rect.Min.Y != bodyTop+6*lineH || rect.Max.Y != bodyTop+7*lineH {
		t.Fatalf("open-ended range rect = %+v, want exactly row 7", rect)
	}
}

func numberedLines(n int) string {
	rows := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, fmt.Sprintf("line%d := \"row %d\"", i, i))
	}
	return strings.Join(rows, "\n")
}
