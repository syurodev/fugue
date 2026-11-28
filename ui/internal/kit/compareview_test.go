package kit

import (
	"strings"
	"testing"
)

func TestCompareViewMarksChangedAndWrapsLongValues(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cv := NewCompareViewState()

	props := CompareViewProps{
		LeftTitle:  "On this machine",
		RightTitle: "In the file",
		Rows: []CompareRow{
			{Name: "Summary", Left: "Short local summary", Right: "Short local summary", Changed: false},
			{
				Name:    "Body",
				Left:    strings.Repeat("local prose words ", 12),
				Right:   strings.Repeat("imported prose words ", 12),
				Changed: true,
			},
			{Name: "Keywords", Left: "", Right: "auth, token", Changed: true},
		},
	}

	h.frameSize(t, 480, 900)
	dims, err := cv.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("CompareView.Layout: %v", err)
	}
	if dims.Size.X != 480 || dims.Size.Y <= 0 {
		t.Fatalf("unexpected CompareView dims: %+v", dims)
	}
	if len(cv.tags) < 2 {
		t.Fatalf("expected changed TagStates allocated, got %d", len(cv.tags))
	}

	// Narrower width wraps taller.
	h.frameSize(t, 280, 900)
	narrowDims, err := cv.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("CompareView.Layout narrow: %v", err)
	}
	if narrowDims.Size.Y <= dims.Size.Y {
		t.Fatalf("expected narrower CompareView to wrap taller (%d <= %d)", narrowDims.Size.Y, dims.Size.Y)
	}
}
