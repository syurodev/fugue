package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// FUG-108: a control that spans its scroll content's column is drawn flush
// with the ScrollArea's clip edge, so its §3.4 outer focus ring lost the sides
// to the cross-axis clip. The paint clip now widens on the cross axis by the
// ring allowance while the scroll axis keeps its exact clip: rings show in the
// margin, content never moves, and scrolling clips exactly where it did.

// ringFrameLay is the ScrollArea under test: a 200×100 viewport at (20, 20)
// in a 240×140 AppBg frame, so ring ink past the side edges lands on pixels
// the shot can see. content runs under the scroll content's constraints.
func ringFrameLay(t *testing.T, h *harness, sa *ScrollAreaState, content func(*layout.Context) layout.Dimensions) {
	t.Helper()
	h.frameSize(t, 240, 140)
	paint.FillShape(h.gtx.Ops, h.env.Th.Chrome().AppBg, clip.Rect{Max: image.Pt(240, 140)}.Op())
	at(&h.gtx, image.Pt(20, 20), func(gtx *layout.Context) {
		gtx.Constraints = layout.Exact(image.Pt(200, 100))
		sa.Layout(h.env, gtx, ScrollAreaProps{Axis: layout.Vertical}, content)
	})
	h.end()
}

// ringContent spans the column with control and keeps the area scrolling with
// a filler below it.
func ringContent(control func(*layout.Context) layout.Dimensions) func(*layout.Context) layout.Dimensions {
	return func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return control(&gtx) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(0, 300)}
			}),
		)
	}
}

// ringDiffCounts the pixels a and b differ on inside r.
func ringDiffCount(a, b *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				n++
			}
		}
	}
	return n
}

func TestScrollAreaShowsSpanningControlsFullSideFocusRings(t *testing.T) {
	t.Parallel()
	// assertSideRings expects the §3.4 outer ring: its side segments land in
	// the margin past both clip edges. assertInsetRings expects the inset ring
	// of a control that draws inside its own box (the tabs strip): its full
	// ink sits just inside both edges.
	assertSideRings := func(t *testing.T, base, focused *image.RGBA) {
		t.Helper()
		const ringPx = int(theme.FocusStroke) + int(theme.FocusOffset)
		vp := image.Rect(20, 20, 220, 120)
		left := image.Rect(vp.Min.X-ringPx, vp.Min.Y, vp.Min.X-2, vp.Max.Y)
		right := image.Rect(vp.Max.X+2, vp.Min.Y, vp.Max.X+ringPx, vp.Max.Y)
		if ringDiffCount(base, focused, left) == 0 {
			t.Fatal("no focus ring ink left of the scroll clip edge; the cross-axis clip cut it")
		}
		if ringDiffCount(base, focused, right) == 0 {
			t.Fatal("no focus ring ink right of the scroll clip edge; the cross-axis clip cut it")
		}
	}
	assertInsetRings := func(t *testing.T, base, focused *image.RGBA) {
		t.Helper()
		vp := image.Rect(20, 20, 220, 120)
		left := image.Rect(vp.Min.X+1, vp.Min.Y, vp.Min.X+4, vp.Max.Y)
		right := image.Rect(vp.Max.X-4, vp.Min.Y, vp.Max.X-1, vp.Max.Y)
		if ringDiffCount(base, focused, left) == 0 {
			t.Fatal("no inset focus ring ink just inside the left edge")
		}
		if ringDiffCount(base, focused, right) == 0 {
			t.Fatal("no inset focus ring ink just inside the right edge")
		}
	}
	assertExactScrollClip := func(t *testing.T, base, focused *image.RGBA) {
		t.Helper()
		const ringPx = int(theme.FocusStroke) + int(theme.FocusOffset)
		above := image.Rect(0, 20-ringPx, 240, 20)
		if n := ringDiffCount(base, focused, above); n != 0 {
			t.Fatalf("%d changed pixels above the viewport; the scroll axis clip is not exact", n)
		}
	}
	for _, tc := range []struct {
		name    string
		control func(t *testing.T, h *harness) (func(*layout.Context) layout.Dimensions, event.Tag)
		assert  func(t *testing.T, base, focused *image.RGBA)
	}{
		{"block select", func(t *testing.T, h *harness) (func(*layout.Context) layout.Dimensions, event.Tag) {
			st := NewSelectState()
			props := SelectProps{Block: true, Value: "a", Options: []SelectOption{{ID: "a", Label: "Alpha"}}}
			return func(gtx *layout.Context) layout.Dimensions {
				if _, err := st.Layout(h.env, gtx, &props); err != nil {
					t.Fatalf("select layout: %v", err)
				}
				return st.Measure(h.env, gtx, &props)
			}, st.Tag()
		}, assertSideRings},
		{"text field", func(t *testing.T, h *harness) (func(*layout.Context) layout.Dimensions, event.Tag) {
			st := NewTextFieldState()
			props := TextFieldProps{Placeholder: "Repo name"}
			return func(gtx *layout.Context) layout.Dimensions {
				if _, err := st.Layout(h.env, gtx, &props); err != nil {
					t.Fatalf("text field layout: %v", err)
				}
				return st.Measure(h.env, gtx, &props)
			}, st.Tag()
		}, assertSideRings},
		{"tabs", func(t *testing.T, h *harness) (func(*layout.Context) layout.Dimensions, event.Tag) {
			st := NewTabsState()
			props := TabsProps{
				Variant:  TabsMode,
				Items:    []TabItem{{ID: "forms", Label: "Forms"}, {ID: "raw", Label: "Raw TOML"}},
				ActiveID: "forms",
			}
			return func(gtx *layout.Context) layout.Dimensions {
				dims, _ := st.Layout(h.env, gtx, props)
				return dims
			}, st.Tag()
		}, assertInsetRings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.env.ReducedMotion = true
			sa := NewScrollAreaState()
			control, tag := tc.control(t, h)

			h.env.Preview = Preview{Focus: tag}
			ringFrameLay(t, h, sa, ringContent(control))
			ringFrameLay(t, h, sa, ringContent(control))
			focused := h.shot(t, 240, 140)

			h.env.Preview = Preview{}
			ringFrameLay(t, h, sa, ringContent(control))
			base := h.shot(t, 240, 140)

			tc.assert(t, base, focused)
			assertExactScrollClip(t, base, focused)
		})
	}
}

func TestScrollAreaScrollsAndClipsExactlyOnTheScrollAxis(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	sa := NewScrollAreaState()
	st := NewSelectState()
	props := SelectProps{Block: true, Value: "a", Options: []SelectOption{{ID: "a", Label: "Alpha"}}}
	control := func(gtx *layout.Context) layout.Dimensions {
		if _, err := st.Layout(h.env, gtx, &props); err != nil {
			t.Fatalf("select layout: %v", err)
		}
		return st.Measure(h.env, gtx, &props)
	}

	ringFrameLay(t, h, sa, ringContent(control))
	atRest := h.shot(t, 240, 140)

	if sa.Offset() != 0 {
		t.Fatalf("initial offset = %d, want 0", sa.Offset())
	}
	sa.SetOffset(50)
	if sa.Offset() != 50 {
		t.Fatalf("offset after SetOffset(50) = %d, want 50", sa.Offset())
	}
	ringFrameLay(t, h, sa, ringContent(control))
	scrolled := h.shot(t, 240, 140)

	// The content moved by the offset: the select's box at the top of the
	// content sits 50 px higher now.
	if atRest.RGBAAt(100, 30) == scrolled.RGBAAt(100, 30) {
		t.Fatal("the content did not move with the scroll offset")
	}
	// Both edges of the scroll axis still clip exactly: the viewport bands
	// just past the top and the bottom stay bare AppBg.
	surface := h.env.Th.Chrome().AppBg
	for x := 20; x < 220; x++ {
		if got := scrolled.RGBAAt(x, 19); got != (color.RGBA{R: surface.R, G: surface.G, B: surface.B, A: 255}) {
			t.Fatalf("content leaked above the scroll-axis clip at x=%d: %v", x, got)
		}
		if got := scrolled.RGBAAt(x, 120); got != (color.RGBA{R: surface.R, G: surface.G, B: surface.B, A: 255}) {
			t.Fatalf("content leaked below the scroll-axis clip at x=%d: %v", x, got)
		}
	}
}
