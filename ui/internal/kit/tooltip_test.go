package kit

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestTooltipWaitsOnHoverAndShowsAtOnceOnFocus verifies the 500 ms hover
// delay (via Driver.WakeAt on the fake clock) and immediate show on keyboard
// focus (§6.7).
func TestTooltipWaitsOnHoverAndShowsAtOnceOnFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var tagA, tagB int
	anchorA := image.Rect(100, 200, 140, 228)
	anchorB := image.Rect(200, 200, 240, 228)

	const tipCopy = "Duplicate"
	// Hovering tagA does not show immediately.
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: tipCopy})
	})
	if !h.env.TooltipRect().Empty() {
		t.Fatalf("tooltip appeared immediately on hover: %v", h.env.TooltipRect())
	}
	if _, ok := h.env.Driver.Next(); !ok {
		t.Fatal("hover did not schedule WakeAt")
	}

	// At 400 ms still hidden; at 500 ms it opens.
	h.now = h.now.Add(400 * time.Millisecond)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: tipCopy})
	})
	if !h.env.TooltipRect().Empty() {
		t.Fatal("tooltip appeared before 500 ms")
	}
	h.now = h.now.Add(100 * time.Millisecond)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: tipCopy})
	})
	if h.env.TooltipRect().Empty() {
		t.Fatal("tooltip did not appear at 500 ms")
	}

	// Escape hides it.
	h.tap(key.NameEscape, 0)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: tipCopy})
	})
	if h.env.tip.open.Shown() {
		t.Fatal("Escape did not hide tooltip")
	}

	// Keyboard focus on tagB shows at once.
	h.now = h.now.Add(time.Second)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagB, anchorB, false, true, false, image.Point{}, TooltipProps{Text: "Lookup"})
	})
	if h.env.TooltipRect().Empty() {
		t.Fatal("keyboard focus did not show tooltip at once")
	}
}

// TestTooltipHopsBetweenNeighboursWithoutClosing checks that moving to a
// neighbouring control within 300 ms keeps the tooltip open and slides it on
// Snappy instead of closing and waiting 500 ms again (§6.7).
func TestTooltipHopsBetweenNeighboursWithoutClosing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var tagA, tagB int
	anchorA := image.Rect(100, 200, 132, 232)
	anchorB := image.Rect(240, 200, 272, 232)

	// Open on A after 500 ms.
	const hopTextA = "Copy item"
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: hopTextA})
	})
	h.now = h.now.Add(500 * time.Millisecond)
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagA, anchorA, true, false, false, image.Point{}, TooltipProps{Text: hopTextA})
	})
	rectA := h.env.TooltipRect()
	if rectA.Empty() {
		t.Fatal("tooltip did not open on A")
	}

	// Leave A for 100 ms (< 300 ms), then hover B: opens immediately and
	// starts sliding from A's X toward B's X.
	h.now = h.now.Add(100 * time.Millisecond)
	runFrame(t, h, func(gtx *layout.Context) {})
	runFrame(t, h, func(gtx *layout.Context) {
		h.env.TooltipAnchor(&tagB, anchorB, true, false, false, image.Point{}, TooltipProps{Text: "Next action"})
	})
	rectHop := h.env.TooltipRect()
	if rectHop.Empty() || !h.env.tip.open.Shown() {
		t.Fatal("tooltip did not hop to B within 300 ms")
	}
	if rectHop.Min.X >= anchorB.Min.X {
		t.Fatalf("tooltip snapped to B (%v) instead of sliding from A (%v)", rectHop, rectA)
	}
}

// TestTooltipTakesNoInput covers UK-E10: the tooltip registers no event area
// of its own, so a click where the tooltip is drawn reaches the control
// underneath it.
func TestTooltipTakesNoInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	btn := NewButtonState()
	ib := NewIconButtonState()
	var btnClicked bool

	// Lay IconButton at (200, 200) and Button right above it at (180, 160),
	// where the IconButton's tooltip will appear.
	lay := func(focusIB bool) {
		runFrame(t, h, func(gtx *layout.Context) {
			h.env.Offset(gtx, image.Pt(180, 160), func(gtx *layout.Context) {
				var err error
				btnClicked, err = btn.Layout(h.env, gtx, ButtonProps{Text: "Under tooltip"})
				if err != nil {
					t.Fatal(err)
				}
			})
			h.env.Offset(gtx, image.Pt(200, 200), func(gtx *layout.Context) {
				if _, err := ib.Layout(h.env, gtx, &IconButtonProps{Icon: IconCopy, Label: "Copy item"}); err != nil {
					t.Fatal(err)
				}
			})
			if focusIB {
				gtx.Execute(key.FocusCmd{Tag: ib.Tag()})
			}
		})
	}

	lay(false)
	h.env.KeyboardOnly = true
	lay(true)
	lay(false)
	tipRect := h.env.TooltipRect()
	if tipRect.Empty() {
		t.Fatal("tooltip did not open")
	}

	// Click inside the tooltip's rect over the Button underneath: Button must receive it.
	h.click(float32(tipRect.Min.X+tipRect.Dx()/2), 160+10)
	lay(false)
	if !btnClicked {
		t.Fatal("click through tooltip did not reach the Button beneath it")
	}
}

// TestIconButtonShowsItsLabelAsTooltipWithKbd verifies IconButton wires its
// required Label and optional Shortcut into Tooltip, and flips below at the
// top edge.
func TestIconButtonShowsItsLabelAsTooltipWithKbd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.Platform = PlatformMac
	h.env.KeyboardOnly = true
	ib := NewIconButtonState()
	pr := &IconButtonProps{
		Icon:     IconSearch,
		Label:    "Quick find",
		Shortcut: Shortcut{Mods: ModPrimary, Key: Char('k')},
	}

	lay := func(focus bool) {
		runFrame(t, h, func(gtx *layout.Context) {
			h.env.Offset(gtx, image.Pt(200, 4), func(gtx *layout.Context) {
				if _, err := ib.Layout(h.env, gtx, pr); err != nil {
					t.Fatal(err)
				}
			})
			if focus {
				gtx.Execute(key.FocusCmd{Tag: ib.Tag()})
			}
		})
	}
	lay(false)
	lay(true)
	lay(false)

	if h.env.TooltipRect().Empty() {
		t.Fatal("IconButton focus did not show its tooltip")
	}
	if !h.env.TooltipFlipped() {
		t.Fatal("tooltip at top edge did not flip below IconButton")
	}
	if got := h.env.tip.activeProps.Text; got != "Quick find" {
		t.Fatalf("tooltip text = %q, want Quick find", got)
	}
	if got := h.env.tip.activeProps.Shortcut; got != pr.Shortcut {
		t.Fatalf("tooltip shortcut = %+v, want %+v", got, pr.Shortcut)
	}
}

// TestTooltipTextContrastOnSurface covers UK-E04 for Tooltip text and Kbd.
func TestTooltipTextContrastOnSurface(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			env := newModeEnv(t, mode.th)
			c := env.Th.Chrome()
			if got := contrastRatio(c.Fg, c.SurfaceRaised); got < 4.5 {
				t.Errorf("Tooltip text on SurfaceRaised: contrast %.2f < 4.5", got)
			}
			if got := contrastRatio(c.FgMuted, over(c.SurfaceRaised, c.FillSoft)); got < 4.5 {
				t.Errorf("Tooltip Kbd on SurfaceRaised: contrast %.2f < 4.5", got)
			}
			_ = theme.RadiusMD
		})
	}
}
