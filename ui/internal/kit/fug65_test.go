package kit

import (
	"image"
	"testing"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestPopoverStaysInsideWindowNearEdges verifies that Popover flips and shifts
// to stay inside the window with SpaceMD margins near every edge (FUG-65).
func TestPopoverStaysInsideWindowNearEdges(t *testing.T) {
	t.Parallel()
	winSize := image.Pt(800, 600)
	cases := []struct {
		name   string
		anchor image.Rectangle
	}{
		{name: "top-right", anchor: image.Rect(740, 10, 780, 38)},
		{name: "bottom-right", anchor: image.Rect(740, 560, 780, 588)},
		{name: "top-left", anchor: image.Rect(4, 10, 44, 38)},
		{name: "bottom-left", anchor: image.Rect(4, 560, 44, 588)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.env.ReducedMotion = true
			h.env.win = winSize
			pop := NewPopoverState()
			pop.Open()
			h.frameSize(t, winSize.X, winSize.Y)
			_, err := pop.Layout(h.env, &h.gtx, &PopoverProps{
				Title:  "Arrange",
				Anchor: tc.anchor,
				Measure: func(*layout.Context) image.Point {
					return image.Pt(180, 200)
				},
				Content: func(*layout.Context) {},
			})
			if err != nil {
				t.Fatalf("Popover.Layout: %v", err)
			}
			h.env.FrameEnd(&h.gtx)
			margin := h.gtx.Dp(theme.SpaceMD)
			bounds := image.Rectangle{Max: winSize}.Inset(margin)
			got := pop.Rect()
			if got.Empty() {
				t.Fatal("popover rect is empty")
			}
			if !got.In(bounds) {
				t.Fatalf("popover rect %v escaped window bounds %v", got, bounds)
			}
			if got.Overlaps(tc.anchor) {
				t.Fatalf("popover rect %v overlapped its anchor %v", got, tc.anchor)
			}
		})
	}
}

// TestSegmentedSelectedSegmentContrast verifies that SegmentedControl's
// selected pill has clear visual separation from its FillSoft track and
// >= 4.5:1 text contrast in both light and dark modes (FUG-65).
func TestSegmentedSelectedSegmentContrast(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	for _, light := range []bool{true, false} {
		th := theme.Dark(sh)
		mode := modeDark
		if light {
			th = theme.Light(sh)
			mode = modeLight
		}
		c := th.Chrome()
		if got := contrastRatio(c.Fg, c.CanvasBg); got < 4.5 {
			t.Errorf("%s: Fg on CanvasBg contrast %.2f < 4.5", mode, got)
		}
		if c.CanvasBg == c.FillSoft {
			t.Errorf("%s: selected pill fill equals FillSoft track %v", mode, c.CanvasBg)
		}
		if got := contrastRatio(c.Border, c.CanvasBg); got < 1.2 {
			t.Errorf("%s: pill border %v lacks separation on %v (%.2f)", mode, c.Border, c.CanvasBg, got)
		}
	}
}
