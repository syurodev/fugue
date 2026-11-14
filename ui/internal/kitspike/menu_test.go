package kitspike

import (
	"image"
	"testing"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"
)

// menuItems is the test menu: three leaves and one submenu at index 1.
func menuItems() []MenuItem {
	return []MenuItem{
		{Label: "Alpha"},
		{Label: "Beta", Submenu: []MenuItem{{Label: "Beta one"}, {Label: "Beta two"}}},
		{Label: "Gamma"},
		{Label: "", Separator: true},
		{Label: "Delete", Danger: true},
	}
}

// menuScene is a menu open over a laid-out opener button: the opener stays
// in every frame, the way a real window keeps its controls alive.
type menuScene struct {
	h      *harness
	o      *Overlay
	opener *widget.Clickable
}

func newMenuScene(t *testing.T) *menuScene {
	s := &menuScene{h: newHarness(t), o: NewOverlay(), opener: new(widget.Clickable)}
	s.h.frame(400, 300)
	s.opener.Layout(s.h.gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(60, 30)}
	})
	s.o.Open(menuItems(), image.Rect(10, 10, 60, 30), s.opener)
	s.h.end()
	// Let the open spring settle.
	for i := 0; i < 40; i++ {
		s.frame()
		if !s.o.Shown() || (s.o.Top() != nil && s.o.Top().presence.Progress() >= 1) {
			break
		}
	}
	return s
}

// frame lays one frame out: opener, overlay, router handoff.
func (s *menuScene) frame() {
	s.h.frame(400, 300)
	s.opener.Layout(s.h.gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(60, 30)}
	})
	s.o.Layout(s.h.env, &s.h.gtx)
	s.h.end()
}

// key presses and processes one key.
func (s *menuScene) key(n key.Name, mods key.Modifiers) {
	s.h.press(n, mods)
	s.frame()
}

// pressKey queues and processes one key press.
func pressKey(h *harness, o *Overlay, n key.Name) {
	h.press(n, 0)
	h.frame(400, 300)
	o.Layout(h.env, &h.gtx)
	h.end()
}

// TestMenuKeyboardWalkAndSelect covers §6.15: arrows wrap, Enter selects and
// reports the index path, Escape closes and returns focus to the opener.
func TestMenuKeyboardWalkAndSelect(t *testing.T) {
	sc := newMenuScene(t)
	o := sc.o

	// Focus lands on the panel and the first arrow highlights item 0.
	sc.key(key.NameDownArrow, 0)
	if got := o.Top().kbd; got != 0 {
		t.Fatalf("first Down highlighted %d, want 0", got)
	}
	// Walk up wraps to the last selectable item (the Delete at 4).
	sc.key(key.NameUpArrow, 0)
	if got := o.Top().kbd; got != 4 {
		t.Fatalf("Up from 0 wrapped to %d, want 4", got)
	}
	// One Down returns to 0 (wrap), then Enter selects it.
	sc.key(key.NameDownArrow, 0)
	sc.key(key.NameReturn, 0)
	sel := o.Selected()
	if len(sel) != 1 || len(sel[0]) != 1 || sel[0][0] != 0 {
		t.Fatalf("Enter selected %v, want [[0]]", sel)
	}
	// Closed, and focus returned to the opener: give the frame that
	// consumed the focus command a chance.
	sc.frame()
	if !sc.h.gtx.Focused(sc.opener) {
		t.Fatal("focus did not return to the opener")
	}
	if o.Shown() {
		t.Fatal("menu still shown after selection")
	}
}

// TestMenuSubmenuAndEscape covers Right opening the submenu, its selection
// reporting the two-index path, and Escape closing level by level.
func TestMenuSubmenuAndEscape(t *testing.T) {
	sc := newMenuScene(t)
	o := sc.o

	sc.key(key.NameDownArrow, 0) // 0
	sc.key(key.NameDownArrow, 0) // 1: Beta
	if o.Top().Sub() != nil {
		t.Fatal("submenu opened before Right")
	}
	sc.key(key.NameRightArrow, 0)
	if o.Top().Sub() == nil {
		t.Fatal("Right did not open the submenu")
	}
	sc.key(key.NameDownArrow, 0)
	sc.key(key.NameReturn, 0)
	sel := o.Selected()
	if len(sel) != 1 || len(sel[0]) != 2 || sel[0][0] != 1 || sel[0][1] != 0 {
		t.Fatalf("submenu selection = %v, want [[1 0]]", sel)
	}
}

// TestMenuTypeahead covers the typeahead jump: "g" lands on Gamma, past the
// separator.
func TestMenuTypeahead(t *testing.T) {
	sc := newMenuScene(t)
	o := sc.o
	sc.key(key.Name("G"), 0)
	if got := o.Top().kbd; got != 2 {
		t.Fatalf("typeahead G highlighted %d, want 2 (Gamma)", got)
	}
}

// TestMenuClickOutsideCovers covers the scrim: a pointer press outside the
// panel closes the menu.
func TestMenuClickOutsideCovers(t *testing.T) {
	sc := newMenuScene(t)
	o := sc.o
	sc.h.click(390, 290)
	sc.frame()
	if o.Shown() {
		t.Fatal("outside click did not close the menu")
	}
}

// TestMenuDiagonalTravelKeepsSubmenu covers §6.15: hovering a different
// parent item closes the submenu, but the pointer leaving both panels does
// not (so the diagonal move from item to submenu survives).
func TestMenuDiagonalTravelKeepsSubmenu(t *testing.T) {
	sc := newMenuScene(t)
	o := sc.o
	sc.key(key.NameDownArrow, 0)
	sc.key(key.NameDownArrow, 0)
	sc.key(key.NameRightArrow, 0)
	if o.Top().Sub() == nil {
		t.Fatal("submenu did not open")
	}
	// Pointer drifts into the corridor between the panels: nothing closes.
	sc.h.router.Queue(pointer.Event{Kind: pointer.Move, Position: imageF32Pt(200, 120)})
	sc.frame()
	if o.Top().Sub() == nil || !o.Top().Sub().presence.Shown() {
		t.Fatal("submenu closed while the pointer moved across")
	}
}
