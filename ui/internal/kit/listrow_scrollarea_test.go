package kit

import (
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func layListFrames(t *testing.T, h *harness, l *ListState, items []ListItem, w, hgt, n int) ListEvents {
	t.Helper()
	var ev ListEvents
	for range n {
		h.frameSize(t, w, hgt)
		h.env.TrackInput(&h.gtx)
		_, ev = l.Layout(h.env, &h.gtx, items)
		h.end()
	}
	return ev
}

func TestListRowSelectionSlidesAndStaysVisibleUnderHover(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := NewListState()
	l.SetSelected("a")
	items := []ListItem{
		{Key: "a", Props: ListRowProps{Label: "First", Secondary: "1st"}},
		{Key: "b", Props: ListRowProps{Label: "Second", Secondary: "2nd"}},
		{Key: "c", Props: ListRowProps{Label: "Third", Secondary: "3rd"}},
	}
	layListFrames(t, h, l, items, 240, 140, 40)
	rowH := h.gtx.Dp(theme.Row)
	if l.pill.alpha != 1 || l.pill.rect.Min.Y != 0 {
		t.Fatalf("settled pill = alpha %v y %d, want 1, 0", l.pill.alpha, l.pill.rect.Min.Y)
	}

	// Hover row "b" while row "a" is selected: selection stays visible, row "b" hovers.
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(100, float32(rowH)+10)})
	layListFrames(t, h, l, items, 240, 140, 30)
	if l.pill.alpha != 1 || l.pill.rect.Min.Y != 0 {
		t.Fatalf("under hover, selected pill = alpha %v y %d, want 1, 0", l.pill.alpha, l.pill.rect.Min.Y)
	}
	if l.Row("b").hoverF.Value() < 0.9 {
		t.Fatalf("row b hoverF = %v, want settled near 1", l.Row("b").hoverF.Value())
	}

	// Move selection to row 2: pill slides on Smooth, then retarget back to row 0 (UK-E11).
	h.click(100, float32(2*rowH)+10)
	layListFrames(t, h, l, items, 240, 140, 2)
	if midY := l.pill.rect.Min.Y; midY <= 0 || midY >= 2*rowH {
		t.Fatalf("mid-slide pill y = %d, want strictly between 0 and %d", midY, 2*rowH)
	}
	h.click(100, 10)
	layListFrames(t, h, l, items, 240, 140, 60)
	if l.pill.rect.Min.Y != 0 {
		t.Fatalf("retargeted pill y = %d, want 0", l.pill.rect.Min.Y)
	}

	// ReducedMotion snaps immediately.
	h.env.ReducedMotion = true
	h.click(100, float32(2*rowH)+10)
	layListFrames(t, h, l, items, 240, 140, 3)
	if l.pill.rect.Min.Y != 2*rowH {
		t.Fatalf("ReducedMotion pill y = %d (selected=%q), want %d", l.pill.rect.Min.Y, l.Selected(), 2*rowH)
	}
}

func TestListRowActionsShowOnHoverAndOnKeyboardFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := NewListState()
	l.SetSelected("a")
	const actID = "clone"
	const actLabel = "Clone"
	items := []ListItem{
		{Key: "a", Props: ListRowProps{
			Label:       "Alpha",
			Actions:     []ListAction{{ID: actID, Icon: IconCopy, Label: actLabel}},
			ContextMenu: []MenuItem{{ID: actID, Label: actLabel}},
		}},
		{Key: "b", Props: ListRowProps{
			Label:       "Beta",
			Actions:     []ListAction{{ID: actID, Icon: IconCopy, Label: actLabel}},
			ContextMenu: []MenuItem{{ID: actID, Label: actLabel}},
		}},
	}
	layListFrames(t, h, l, items, 260, 120, 1)

	// At rest, action alpha is 0 and clicking the right action slot does not fire.
	if l.Row("a").actionF.Value() != 0 {
		t.Fatalf("resting actionF = %v, want 0", l.Row("a").actionF.Value())
	}
	h.click(235, 14)
	if ev := layListFrames(t, h, l, items, 260, 120, 2); ev.ActionID != "" {
		t.Fatalf("resting click fired action %q", ev.ActionID)
	}

	// Hover row "a": action fades in and clicking it fires ev.ActionID.
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(100, 14)})
	layListFrames(t, h, l, items, 260, 120, 30)
	if l.Row("a").actionF.Value() < 0.99 {
		t.Fatalf("hovered actionF = %v, want ~1", l.Row("a").actionF.Value())
	}
	h.click(235, 14)
	ev := layListFrames(t, h, l, items, 260, 120, 1)
	if ev.ActionID == "" {
		ev = layListFrames(t, h, l, items, 260, 120, 1)
	}
	if ev.ActionID != actID {
		t.Fatalf("hovered action click = %q, want %s", ev.ActionID, actID)
	}

	verifyListRowKeyboard(t, h, l, items)
}

func verifyListRowKeyboard(t *testing.T, h *harness, l *ListState, items []ListItem) {
	t.Helper()
	h.router.Queue(pointer.Event{Kind: pointer.Leave, Position: f32.Pt(-10, -10)})
	h.env.KeyboardOnly = true
	h.frameSize(t, 260, 120)
	l.Layout(h.env, &h.gtx, items)
	h.gtx.Execute(key.FocusCmd{Tag: l.Row("a").Tag()})
	h.end()
	layListFrames(t, h, l, items, 260, 120, 30)
	if l.Row("a").actionF.Value() < 0.99 {
		t.Fatalf("focused actionF = %v, want ~1", l.Row("a").actionF.Value())
	}
	h.tap(key.NameDownArrow, 0)
	if ev := layListFrames(t, h, l, items, 260, 120, 1); !ev.Changed || ev.Selected != "b" {
		t.Fatalf("Down arrow selected = %q (changed=%v), want b", ev.Selected, ev.Changed)
	}
	h.tap(key.NameReturn, 0)
	if ev := layListFrames(t, h, l, items, 260, 120, 1); ev.Opened != "b" {
		t.Fatalf("Enter opened = %q, want b", ev.Opened)
	}
	h.tap(key.NameF10, key.ModShift)
	layListFrames(t, h, l, items, 260, 120, 1)
	if !l.Row("b").menu.IsOpen() {
		t.Fatal("Shift+F10 did not open row context menu")
	}
}

func TestListSelectionAndRowStateFollowTheKeyOnInsertAbove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := NewListState()
	l.SetSelected("b")
	items := []ListItem{
		{Key: "a", Props: ListRowProps{Label: "A"}},
		{Key: "b", Props: ListRowProps{Label: "B"}},
		{Key: "c", Props: ListRowProps{Label: "C"}},
	}
	layListFrames(t, h, l, items, 240, 160, 40)
	rowH := h.gtx.Dp(theme.Row)
	if l.Selected() != "b" || l.pill.rect.Min.Y != rowH {
		t.Fatalf("before insert: selected=%q pill y=%d, want b, %d", l.Selected(), l.pill.rect.Min.Y, rowH)
	}

	// Insert "x" above "b": "b" stays selected, only "x" plays enter motion, pill ends on "b".
	withX := append([]ListItem{{Key: "x", Props: ListRowProps{Label: "X"}}}, items...)
	l.Enter("x")
	layListFrames(t, h, l, withX, 240, 160, 2)
	if l.Selected() != "b" {
		t.Fatalf("after insert: selected=%q, want b", l.Selected())
	}
	if l.Row("x").presence.Progress() >= 1 || l.Row("b").presence.Progress() != 1 {
		t.Fatalf("presence x=%v b=%v, want x<1 and b=1", l.Row("x").presence.Progress(), l.Row("b").presence.Progress())
	}
	layListFrames(t, h, l, withX, 240, 160, 60)
	if l.pill.rect.Min.Y != 2*rowH {
		t.Fatalf("settled pill after insert: y=%d, want %d", l.pill.rect.Min.Y, 2*rowH)
	}

	// Remove "a" above "b": pill slides smoothly with the collapse to rowH without jumping.
	l.Remove("a")
	layListFrames(t, h, l, withX, 240, 160, 4)
	if mid := l.pill.rect.Min.Y; mid <= rowH || mid >= 2*rowH {
		t.Fatalf("during collapse: pill y=%d, want strictly between %d and %d", mid, rowH, 2*rowH)
	}
	layListFrames(t, h, l, withX, 240, 160, 60)
	if l.pill.rect.Min.Y != rowH {
		t.Fatalf("settled pill after collapse: y=%d, want %d", l.pill.rect.Min.Y, rowH)
	}
}

func TestListRowStateIsNeverTheRowBackground(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rs := NewListRowState()
	pr := ListRowProps{
		Label:      "Deploy",
		StateLabel: "failed",
		StateTone:  ToneDanger,
	}
	const w, hgt = 260, 40
	for range 5 {
		h.frameSize(t, w, hgt)
		g := h.gtx
		g.Constraints = layout.Exact(image.Pt(w, hgt))
		rs.Layout(h.env, &g, &pr)
		h.end()
	}
	h.frameSize(t, w, hgt)
	g := h.gtx
	g.Constraints = layout.Exact(image.Pt(w, hgt))
	rs.Layout(h.env, &g, &pr)
	img := h.shot(t, w, hgt)
	h.end()

	danger := h.env.Th.Status().Danger
	bg := color.NRGBA(img.RGBAAt(12, 24))
	if colorClose(bg, danger) {
		t.Fatalf("row background %v matches Status.Danger %v; state must never be row background", bg, danger)
	}
}

func colorClose(a, b color.NRGBA) bool {
	diff := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return diff(a.R, b.R) < 12 && diff(a.G, b.G) < 12 && diff(a.B, b.B) < 12
}

func TestScrollAreaBarFadesAfterIdle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sa := NewScrollAreaState()
	lay := func() {
		h.frameSize(t, 200, 100)
		sa.Layout(h.env, &h.gtx, ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(200, 400)}
		})
		h.end()
	}
	lay()
	if sa.BarAlpha() != 0 {
		t.Fatalf("initial BarAlpha = %v, want 0", sa.BarAlpha())
	}

	h.router.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(50, 50), Scroll: f32.Pt(0, 40)})
	for range 30 {
		lay()
	}
	if sa.BarAlpha() < 0.99 {
		t.Fatalf("after scroll BarAlpha = %v, want ~1", sa.BarAlpha())
	}
	if _, ok := h.env.Driver.Next(); !ok {
		t.Fatal("expected Driver.WakeAt deadline after scroll")
	}

	h.now = h.now.Add(scrollIdleDelay + 50*time.Millisecond)
	for range 30 {
		lay()
	}
	if sa.BarAlpha() != 0 {
		t.Fatalf("after 1s idle BarAlpha = %v, want 0", sa.BarAlpha())
	}

	// Nested ScrollArea on the same axis must not double-scroll.
	outer, inner := NewScrollAreaState(), NewScrollAreaState()
	layNested := func() {
		h.frameSize(t, 200, 100)
		outer.Layout(h.env, &h.gtx, ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
			return inner.Layout(h.env, gtx, ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(200, 400)}
			})
		})
		h.end()
	}
	layNested()
	h.router.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(50, 50), Scroll: f32.Pt(0, 30)})
	layNested()
	if inner.Offset() != 0 {
		t.Fatalf("nested same-axis ScrollArea scrolled to %d, want 0", inner.Offset())
	}
}

func TestScrollAreaDoesNotYankAReaderWhoScrolledUp(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sa := NewScrollAreaState()
	contentH := 400
	lay := func() {
		h.frameSize(t, 200, 100)
		sa.Layout(h.env, &h.gtx, ScrollAreaProps{Axis: layout.Vertical, FollowBottom: true}, func(gtx *layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(200, contentH)}
		})
		h.end()
	}
	lay()
	if !sa.Following() || sa.Offset() != 300 {
		t.Fatalf("initial Following=%v Offset=%d, want true, 300", sa.Following(), sa.Offset())
	}

	h.router.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(50, 50), Scroll: f32.Pt(0, -80)})
	lay()
	lay()
	if sa.Following() || sa.Offset() != 220 {
		t.Fatalf("after scroll up Following=%v Offset=%d, want false, 220", sa.Following(), sa.Offset())
	}

	contentH = 800
	lay()
	if sa.Following() || sa.Offset() != 220 {
		t.Fatalf("after stream growth Following=%v Offset=%d, want false, 220", sa.Following(), sa.Offset())
	}

	h.router.Queue(pointer.Event{Kind: pointer.Scroll, Position: f32.Pt(50, 50), Scroll: f32.Pt(0, 600)})
	lay()
	lay()
	if !sa.Following() || sa.Offset() != 700 {
		t.Fatalf("at bottom Following=%v Offset=%d, want true, 700", sa.Following(), sa.Offset())
	}
}

func TestListRowAndSectionHeaderTextContrastOnSurface(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("shaper: %v", err)
	}
	for _, tc := range []struct {
		name string
		th   theme.Theme
	}{
		{"day", theme.Light(sh)},
		{"night", theme.Dark(sh)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := tc.th.Chrome()
			surfaces := []struct {
				name   string
				bg     color.NRGBA
				subtle bool
			}{
				{"AppBg", c.AppBg, true},
				{"CanvasBg", c.CanvasBg, true},
				{"SurfaceBase", c.SurfaceBase, true},
				{"SurfaceRaised", c.SurfaceRaised, true},
				{"SurfaceTinted", c.SurfaceTinted, true},
				{"SelectedPill/AppBg", over(c.AppBg, c.SelectedWash), false},
				{"SelectedPill/SurfaceBase", over(c.SurfaceBase, c.SelectedWash), false},
				{"HoverPill/AppBg", over(c.AppBg, c.HoverWash), false},
			}
			for _, sf := range surfaces {
				checkContrastSurface(t, fmt.Sprintf("%s/%s", tc.name, sf.name), c.Fg, c.FgMuted, sf.bg, c.FillSoft)
				if sf.subtle {
					if got := contrastRatio(c.FgSubtle, sf.bg); got < 4.5 {
						t.Errorf("%s/%s FgSubtle contrast = %.2f, want >= 4.5", tc.name, sf.name, got)
					}
				}
			}
		})
	}
}
