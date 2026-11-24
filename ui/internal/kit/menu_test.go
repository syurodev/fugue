package kit

import (
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Evidence for Menu and the window overlay (F10.11a, ui-kit §6.15, UK-07).

// testMenuItems has every kind of row: an item with icon and shortcut, a
// disabled one, a separator, an item with a submenu, and a plain one.
func testMenuItems() []MenuItem {
	return []MenuItem{
		{ID: "open", Label: "Open", Icon: IconSearch, Shortcut: Shortcut{Mods: ModPrimary, Key: Char('o')}},
		{ID: "paste", Label: "Paste", Disabled: true},
		MenuSeparator(),
		{ID: "share", Label: "Share", Sub: []MenuItem{
			{ID: "link", Label: "Copy link"},
			{ID: "mail", Label: "Email"},
			{ID: "more", Label: "More"},
		}},
		{ID: "delete", Label: "Delete"},
	}
}

// The window every menu test lays out in.
const (
	menuWinW = 400
	menuWinH = 300
)

const (
	itemOpen   = 0
	itemPaste  = 1
	itemShare  = 3
	itemDelete = 4
)

// menuFrame lays one frame of the menu out in a window of w×hgt, the way a
// window would: TrackInput first, then the menu, then the end of the frame.
func (h *harness) menuFrame(t *testing.T, m *MenuState, p MenuProps) MenuResult {
	t.Helper()
	return h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
		res, err := m.Layout(h.env, gtx, p)
		if err != nil {
			t.Fatalf("lay the menu out: %v", err)
		}
		return res
	})
}

// menuFrameWith is menuFrame around any layout: the window's frame.
func (h *harness) menuFrameWith(t *testing.T, lay func(*layout.Context) MenuResult) MenuResult {
	t.Helper()
	h.frameSize(t, menuWinW, menuWinH)
	h.env.TrackInput(&h.gtx)
	res := lay(&h.gtx)
	h.end()
	return res
}

// run lays n frames of the same menu out.
func (h *harness) run(t *testing.T, m *MenuState, p MenuProps, n int) {
	t.Helper()
	for range n {
		h.menuFrame(t, m, p)
	}
}

// moveTo queues a pointer move to a window position.
func (h *harness) moveTo(p image.Point) {
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(float32(p.X), float32(p.Y))})
}

// itemCentre is the middle of item i of panel k, from the last frame.
func itemCentre(m *MenuState, k, i int) image.Point {
	lv := &m.lv[k]
	in := lv.rect.Inset(lv.pad)
	r := lv.rows[i]
	return image.Pt(in.Min.X+in.Dx()/2, in.Min.Y+r.y+r.h/2)
}

// pointOutside is a window point outside every panel of the menu.
func pointOutside(m *MenuState) image.Point {
	for _, p := range []image.Point{{menuWinW - 3, menuWinH - 3}, {3, menuWinH - 3}, {menuWinW - 3, 3}, {3, 3}} {
		inside := false
		for _, r := range m.Panels() {
			inside = inside || p.In(r.Inset(-8))
		}
		if !inside {
			return p
		}
	}
	return image.Point{}
}

// quiet reports whether the last frame asked for no next frame.
func (h *harness) quiet() bool { _, moving := h.env.Driver.Next(); return !moving }

// settle lays frames out until two quiet ones in a row, then checks it stays
// quiet: the frame that handles a command only changes a spring's target, so
// one quiet frame proves little (ui-kit §9.1).
func (h *harness) settle(t *testing.T, m *MenuState, p MenuProps, what string) {
	t.Helper()
	frames, streak := 0, 0
	for streak < 2 {
		h.menuFrame(t, m, p)
		frames++
		streak++
		if !h.quiet() {
			streak = 0
		}
		if frames > 90 {
			t.Fatalf("%s: still moving after %d frames", what, frames)
		}
	}
	for range 20 {
		h.menuFrame(t, m, p)
		if !h.quiet() {
			t.Fatalf("%s: asked for a frame after settling", what)
		}
	}
}

// TestMenuSettlesAfterTwoQuietFrames covers UK-E07 for the menu: it asks for
// frames while it opens, moves its highlight and closes, and stops asking —
// two quiet frames in a row, then quiet for good — and a closed menu never
// asks (§3.7).
func TestMenuSettlesAfterTwoQuietFrames(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}

	h.menuFrame(t, m, p)
	if !h.quiet() {
		t.Fatal("a closed menu asked for a frame")
	}
	m.Open()
	h.menuFrame(t, m, p)
	if h.quiet() {
		t.Fatal("an opening menu asked for no frame")
	}
	h.settle(t, m, p, "opening")
	h.moveTo(itemCentre(m, 0, itemOpen))
	h.menuFrame(t, m, p)
	if h.quiet() {
		t.Fatal("a highlight that starts to slide asked for no frame")
	}
	h.settle(t, m, p, "highlight")
	m.Close()
	h.menuFrame(t, m, p)
	if h.quiet() {
		t.Fatal("a closing menu asked for no frame")
	}
	h.settle(t, m, p, "closing")
	if got := m.Panels(); len(got) != 0 {
		t.Fatalf("a closed menu still has panels %v", got)
	}
}

// TestMenuReducedMotionOpensAtOnce covers UK-04 for the menu: under reduced
// motion the panel is there, fully, in the frame that opens it, and nothing
// asks for a frame.
func TestMenuReducedMotionOpensAtOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.ReducedMotion = true
	m := NewMenuState()
	m.Open()
	h.menuFrame(t, m, MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)})
	if got := m.lv[0].open.Progress(); got != 1 {
		t.Fatalf("the panel is at %v in the first frame, want 1", got)
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("a reduced-motion menu asked for a frame")
	}
}

// TestMenuClickOutsideClosesAndDoesNotFallThrough covers §6.15: the root
// menu's window-wide area takes a press that lands on no panel, closes the
// menu, and the control underneath — a backdrop here, laid out first — gets
// nothing; a press on a disabled item or on the padding keeps it open; once
// the menu is gone the same press reaches the backdrop.
func TestMenuClickOutsideClosesAndDoesNotFallThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	var back widget.Clickable
	backdropClicks := 0
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	frame := func() MenuResult {
		return h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
			if back.Clicked(*gtx) {
				backdropClicks++
			}
			back.Layout(*gtx, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Max} })
			res, err := m.Layout(h.env, gtx, p)
			if err != nil {
				t.Fatalf("lay the menu out: %v", err)
			}
			return res
		})
	}
	m.Open()
	for range 30 {
		frame()
	}
	out := pointOutside(m)

	// A press on a disabled item stays in its panel.
	c := itemCentre(m, 0, itemPaste)
	h.click(float32(c.X), float32(c.Y))
	frame()
	frame()
	if !m.IsOpen() || backdropClicks != 0 {
		t.Fatalf("a press on a disabled item: open %v, backdrop clicks %d", m.IsOpen(), backdropClicks)
	}

	h.click(float32(out.X), float32(out.Y))
	res := frame()
	frame()
	if !res.Closed || m.IsOpen() {
		t.Fatalf("a press outside: closed %v, still open %v", res.Closed, m.IsOpen())
	}
	if backdropClicks != 0 {
		t.Fatalf("the press outside fell through to the control underneath (%d clicks)", backdropClicks)
	}

	for range 40 {
		frame()
	}
	h.click(float32(out.X), float32(out.Y))
	frame()
	frame()
	if backdropClicks != 1 {
		t.Fatalf("with the menu gone the press reached the backdrop %d times, want 1", backdropClicks)
	}
}

// TestMenuChoosesAnItemAndReportsItsID covers UK-06: a press on an enabled
// item reports its ID in that frame and closes the menu.
func TestMenuChoosesAnItemAndReportsItsID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	m.Open()
	h.run(t, m, p, 30)
	c := itemCentre(m, 0, itemDelete)
	h.click(float32(c.X), float32(c.Y))
	res := h.menuFrame(t, m, p)
	if res.Chosen != "delete" || !res.Closed || m.IsOpen() {
		t.Fatalf("result %+v, open %v, want delete chosen and the menu closed", res, m.IsOpen())
	}
}

// laidMenu lays the menu out three frames, its anchor at window position at
// (placed with Env.Offset), opened along path, and returns its panels in
// window coordinates, after checking every one is inside the window.
func laidMenu(t *testing.T, at image.Point, anchor image.Rectangle, path ...int) []image.Rectangle {
	t.Helper()
	h := newHarness(t)
	m := NewMenuState()
	m.Open(path...)
	for range 3 {
		h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
			h.env.Offset(gtx, at, func(gtx *layout.Context) {
				if _, err := m.Layout(h.env, gtx, MenuProps{Items: testMenuItems(), Anchor: anchor}); err != nil {
					t.Fatalf("lay the menu out: %v", err)
				}
			})
			return MenuResult{}
		})
	}
	win := image.Rect(0, 0, menuWinW, menuWinH)
	for _, r := range m.Panels() {
		if !r.In(win) {
			t.Fatalf("panel %v is outside the window %v", r, win)
		}
	}
	return m.Panels()
}

// TestMenuFlipsAtTheWindowEdge covers §6.15 and UK-07: with room the panel
// sits under its anchor, left edges together; at the bottom-right corner of
// the window it flips above the anchor and to its right edge; a submenu
// opens to the right of its parent, and to the left where the window runs
// out. Every panel stays inside the window. The anchor is placed with
// Env.Offset, and the panels come back in window coordinates.
func TestMenuFlipsAtTheWindowEdge(t *testing.T) {
	t.Parallel()
	trigger := image.Rect(0, 0, 50, 20)
	cases := []struct {
		name  string
		at    image.Point
		path  []int
		check func(p []image.Rectangle) bool
	}{
		{
			"room: under the anchor, left edges together", image.Pt(20, 20), nil,
			func(p []image.Rectangle) bool { return p[0].Min.X == 20 && p[0].Min.Y >= 40 },
		},
		{
			"corner: above the anchor, at its right edge", image.Pt(340, 270), nil,
			func(p []image.Rectangle) bool { return p[0].Max.Y <= 270 && p[0].Max.X == 390 },
		},
		{
			"submenu with room: right of its parent", image.Pt(20, 20),
			[]int{itemShare},
			func(p []image.Rectangle) bool { return len(p) == 2 && p[1].Min.X >= p[0].Max.X },
		},
		{
			"submenu at the edge: left of its parent", image.Pt(290, 20),
			[]int{itemShare},
			func(p []image.Rectangle) bool { return len(p) == 2 && p[1].Max.X <= p[0].Min.X },
		},
		{
			"submenu at margin-shifted root: left of its parent", image.Pt(330, 20),
			[]int{itemShare},
			func(p []image.Rectangle) bool { return len(p) == 2 && p[1].Max.X <= p[0].Min.X && p[1].Min.X >= 8 },
		},
	}
	for _, c := range cases {
		if panels := laidMenu(t, c.at, trigger, c.path...); !c.check(panels) {
			t.Errorf("%s: got panels %v", c.name, panels)
		}
	}
}

// TestMenuDiagonalTravelKeepsSubmenu covers §6.15: the pointer on the parent
// item opens its submenu; crossing the items below the parent on a diagonal
// toward the submenu keeps it open, and arriving in it highlights its item;
// leaving the parent in any other direction closes it.
func TestMenuDiagonalTravelKeepsSubmenu(t *testing.T) {
	t.Parallel()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(20, 20, 70, 40)}
	open := func() (*harness, *MenuState) {
		h := newHarness(t)
		m := NewMenuState()
		m.Open()
		h.run(t, m, p, 30)
		h.moveTo(itemCentre(m, 0, itemShare))
		h.run(t, m, p, 30)
		if !m.lv[1].open.Shown() || m.lv[0].sub != itemShare {
			t.Fatal("the pointer on the parent item did not open its submenu")
		}
		return h, m
	}

	h, m := open()
	root, sub := m.lv[0].rect, m.lv[1].rect
	// The diagonal: down over the next item, near the root's right edge,
	// then into the submenu.
	below := itemCentre(m, 0, itemDelete)
	below.X = root.Max.X - m.lv[0].pad - 2
	h.moveTo(below)
	h.run(t, m, p, 3)
	if !m.lv[1].open.Shown() {
		t.Fatalf("crossing the item below at %v closed the submenu %v", below, sub)
	}
	into := itemCentre(m, 1, 0)
	h.moveTo(into)
	h.run(t, m, p, 3)
	if !m.lv[1].open.Shown() || m.lv[1].hi != 0 {
		t.Fatalf("arriving in the submenu: open %v, highlighted item %d, want item 0", m.lv[1].open.Shown(), m.lv[1].hi)
	}

	// Leaving the parent away from the submenu closes it.
	h, m = open()
	away := itemCentre(m, 0, itemDelete)
	away.X = m.lv[0].rect.Min.X + m.lv[0].pad + 4
	h.moveTo(away)
	h.run(t, m, p, 3)
	if m.lv[1].open.Shown() {
		t.Fatalf("moving away to %v left the submenu open", away)
	}
}

// TestOnlyOneMenuHoldsTheOverlay covers UK-07: the window has one overlay,
// and the menu opened last holds it; the other closes.
func TestOnlyOneMenuHoldsTheOverlay(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a, b := NewMenuState(), NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	both := func() {
		h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
			for _, m := range []*MenuState{a, b} {
				if _, err := m.Layout(h.env, gtx, p); err != nil {
					t.Fatalf("lay the menu out: %v", err)
				}
			}
			return MenuResult{}
		})
	}
	a.Open()
	both()
	b.Open()
	both()
	both()
	if a.IsOpen() || !b.IsOpen() {
		t.Fatalf("after b opened: a open %v, b open %v, want only b", a.IsOpen(), b.IsOpen())
	}
}

// TestAClosingMenuTakesNoInput covers §3.7: a menu fading out is not
// clickable, and the press goes to what is underneath.
func TestAClosingMenuTakesNoInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	var back widget.Clickable
	clicks := 0
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	frame := func() MenuResult {
		return h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
			if back.Clicked(*gtx) {
				clicks++
			}
			back.Layout(*gtx, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Max} })
			res, err := m.Layout(h.env, gtx, p)
			if err != nil {
				t.Fatalf("lay the menu out: %v", err)
			}
			return res
		})
	}
	m.Open()
	for range 30 {
		frame()
	}
	c := itemCentre(m, 0, itemDelete)
	m.Close()
	frame()
	if len(m.Panels()) == 0 {
		t.Fatal("the menu vanished in one frame: nothing is fading to test")
	}
	h.click(float32(c.X), float32(c.Y))
	res := frame()
	if res.Chosen != "" || clicks != 1 {
		t.Fatalf("a press on a fading item: chosen %q, backdrop clicks %d, want none chosen and the press on the backdrop", res.Chosen, clicks)
	}
}

// TestMenuPanelCastsTheFloatShadow covers §3.6 and Q-91: the floating panel
// is lit by elevation.float — translucent rects stacked under it, no blur —
// so the ink just below its edge is darker than the page far from it, and
// the panel itself is SurfaceRaised.
func TestMenuPanelCastsTheFloatShadow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	th := theme.Light(h.env.Sh)
	h.env.Th = &th
	m := NewMenuState()
	m.Open()
	h.frameSize(t, 400, 300)
	paint.Fill(&h.ops, th.Chrome().AppBg)
	at(&h.gtx, image.Pt(40, 40), func(gtx *layout.Context) {
		if _, err := m.Layout(h.env, gtx, MenuProps{Items: testMenuItems(), Inline: true}); err != nil {
			t.Fatalf("lay the menu out: %v", err)
		}
	})
	img := h.shot(t, 400, 300)
	r := m.lv[0].rect.Add(image.Pt(40, 40))
	lum := func(p image.Point) int {
		c := img.RGBAAt(p.X, p.Y)
		return int(c.R) + int(c.G) + int(c.B)
	}
	far := lum(image.Pt(390, 290))
	below := lum(image.Pt(r.Min.X+r.Dx()/2, r.Max.Y+2))
	if below >= far {
		t.Fatalf("the ink 2 px below the panel (%d) is not darker than the page (%d): no shadow", below, far)
	}
	pad, want := img.RGBAAt(r.Min.X+r.Dx()/2, r.Min.Y+m.lv[0].pad/2), th.Chrome().SurfaceRaised
	if pad.R != want.R || pad.G != want.G || pad.B != want.B {
		t.Fatalf("the panel's padding is %v, want SurfaceRaised %v", pad, want)
	}
}
