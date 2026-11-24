package kit

import (
	"image"
	"image/color"
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

// TestSegmentedSelectedSegmentContrast renders a SegmentedControl in both light
// and dark modes and verifies that the drawn selected pill pixels differ from
// the FillSoft track and preserve >= 4.5:1 text contrast (FUG-65).
func TestSegmentedSelectedSegmentContrast(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	opts := []SegmentedOption{
		{ID: "a", Label: "Light"},
		{ID: "b", Label: "Dark"},
	}
	for _, light := range []bool{true, false} {
		th := theme.Dark(sh)
		mode := modeDark
		if light {
			th = theme.Light(sh)
			mode = modeLight
		}
		h := newHarness(t)
		h.env.Th = &th
		h.env.ReducedMotion = true
		st := NewSegmentedState()
		const w, hgt = 240, 60
		h.frameSize(t, w, hgt)
		if _, _, err := st.Layout(h.env, &h.gtx, SegmentedProps{Options: opts, Selected: 0}); err != nil {
			t.Fatalf("%s: SegmentedControl.Layout: %v", mode, err)
		}
		h.env.FrameEnd(&h.gtx)
		img := h.shot(t, w, hgt)
		pill := st.PillRect()
		if pill.Empty() || len(st.rects) < 2 {
			t.Fatalf("%s: expected pill and segment rects, got pill=%v rects=%v", mode, pill, st.rects)
		}
		pillPx := img.RGBAAt(pill.Min.X+3, pill.Min.Y+3)
		trackPx := img.RGBAAt(st.rects[1].Min.X+3, st.rects[1].Min.Y+3)
		if pillPx == trackPx {
			t.Errorf("%s: selected pill pixel %v matches unselected track pixel %v", mode, pillPx, trackPx)
		}
		pillNRGBA := color.NRGBA(pillPx)
		if got := contrastRatio(th.Chrome().Fg, pillNRGBA); got < 4.5 {
			t.Errorf("%s: Fg on drawn pill %v contrast %.2f < 4.5", mode, pillPx, got)
		}
	}
}
