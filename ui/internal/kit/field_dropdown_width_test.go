package kit

import (
	"image"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func openSelectInColumn(t *testing.T, h *harness, sel *SelectState, p *SelectProps, atPt image.Point, colW int) {
	t.Helper()
	lay := func(focus bool) {
		runFrame(t, h, func(gtx *layout.Context) {
			h.env.Offset(gtx, atPt, func(gtx *layout.Context) {
				g := *gtx
				g.Constraints.Min.X = 0
				g.Constraints.Max.X = colW
				if _, err := sel.Layout(h.env, &g, p); err != nil {
					t.Fatalf("select layout: %v", err)
				}
				if focus {
					g.Execute(key.FocusCmd{Tag: sel.Tag()})
				}
			})
		})
	}
	lay(true)
	lay(false)
	h.tap(key.NameDownArrow, 0)
	lay(false)
	if !sel.IsOpen() {
		t.Fatal("Down did not open Select")
	}
}

// TestBlockSelectMenuMatchesBoxWidthAndLeftEdge verifies §6.13: a Block Select
// in a 400 dp column opens a menu matching the box's exact width and left edge,
// with item text aligned to the field's value text, both standalone and inside
// a Dialog.
func TestBlockSelectMenuMatchesBoxWidthAndLeftEdge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const (
		colW       = 400
		vendorName = "claude"
	)
	sel := NewSelectState()
	p := &SelectProps{
		Label:   "Vendor",
		Options: []SelectOption{{ID: vendorName, Label: vendorName}},
		Value:   vendorName,
		Block:   true,
	}
	offset := image.Pt(40, 50)
	openSelectInColumn(t, h, sel, p, offset, colW)

	panels := sel.menu.Panels()
	if len(panels) != 1 {
		t.Fatalf("got %d panels, want 1", len(panels))
	}
	panel := panels[0]
	if panel.Dx() != colW {
		t.Fatalf("menu width = %d, want %d", panel.Dx(), colW)
	}
	if panel.Min.X != offset.X {
		t.Fatalf("menu left = %d, want %d", panel.Min.X, offset.X)
	}
	itemTextX := panel.Min.X + sel.menu.lv[0].pad + sel.menu.lv[0].itemInset
	fieldTextX := offset.X + h.gtx.Dp(theme.SpaceLG)
	if itemTextX != fieldTextX {
		t.Fatalf("item text X = %d, want field value text X = %d", itemTextX, fieldTextX)
	}

	assertDialogSelectMatchesBody(t, h)
}

func assertDialogSelectMatchesBody(t *testing.T, h *harness) {
	t.Helper()
	dlg := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	const vendorName = "claude"
	sel := NewSelectState()
	p := &SelectProps{
		Label:   "Vendor",
		Options: []SelectOption{{ID: vendorName, Label: vendorName}},
		Value:   vendorName,
		Block:   true,
	}
	dlg.Open()
	var boxes DialogBoxes
	var selW int
	layDlg := func(focusSel bool) {
		runFrame(t, h, func(gtx *layout.Context) {
			res, err := dlg.Layout(h.env, gtx, &DialogProps{
				Variant:      DialogForm,
				Title:        "Create session",
				Cancel:       cancel,
				Primary:      primary,
				PrimaryLabel: "Create",
				Body: func(gtx *layout.Context, bands *DialogBands) image.Point {
					selW = selectWidth(h.env, gtx, p)
					if _, err := sel.Layout(h.env, gtx, p); err != nil {
						t.Fatalf("dialog select layout: %v", err)
					}
					if focusSel {
						gtx.Execute(key.FocusCmd{Tag: sel.Tag()})
					}
					dims := sel.Measure(h.env, gtx, p)
					bands.Focusable(sel.Tag(), 0, dims.Size.Y)
					return dims.Size
				},
			})
			if err != nil {
				t.Fatalf("dialog layout: %v", err)
			}
			boxes = res.Boxes
		})
	}
	layDlg(false)
	layDlg(true)
	layDlg(false)
	h.tap(key.NameDownArrow, 0)
	layDlg(false)

	panels := sel.menu.Panels()
	if len(panels) != 1 {
		t.Fatalf("dialog select got %d panels, want 1", len(panels))
	}
	ring := dialogRing(&h.gtx)
	wantLeft := boxes.Body.Min.X + ring
	if panels[0].Dx() != selW || panels[0].Min.X != wantLeft {
		t.Fatalf("dialog select panel = %v (w=%d), want left=%d w=%d", panels[0], panels[0].Dx(), wantLeft, selW)
	}
}

// TestSelectMenuGrowsWiderWhenItemExceedsBox verifies §6.13: when an option's
// label is wider than the Select box, the menu widens beyond the box width.
func TestSelectMenuGrowsWiderWhenItemExceedsBox(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sel := NewSelectState()
	p := &SelectProps{
		Placeholder: "Pick",
		Options: []SelectOption{
			{ID: "short", Label: "a"},
			{ID: "long", Label: "A very long option label that clearly exceeds the 180 dp select box width"},
		},
		Value: "short",
		Block: true,
	}
	const boxW = 180
	openSelectInColumn(t, h, sel, p, image.Pt(20, 20), boxW)

	panel := sel.menu.Panels()[0]
	if panel.Dx() <= boxW {
		t.Fatalf("menu width = %d, want > box width %d", panel.Dx(), boxW)
	}
	if panel.Min.X != 20 {
		t.Fatalf("menu left = %d, want 20", panel.Min.X)
	}
}

// TestSelectMenuNearRightEdgeFlipsAndStaysInWindow verifies §6.13: a Select near
// the right edge of the window flips its menu so the right edges align and the
// panel stays inside the window.
func TestSelectMenuNearRightEdgeFlipsAndStaysInWindow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sel := NewSelectState()
	p := &SelectProps{
		Placeholder: "Pick",
		Options: []SelectOption{
			{ID: "long", Label: "An extra long option label forcing horizontal flip near the right edge"},
		},
		Block: true,
	}
	const boxW = 160
	offset := image.Pt(620, 40) // right edge at 780 in 800-wide window
	openSelectInColumn(t, h, sel, p, offset, boxW)

	panel := sel.menu.Panels()[0]
	win := image.Rect(0, 0, 800, 600)
	if !panel.In(win) {
		t.Fatalf("menu panel %v escaped window %v", panel, win)
	}
	wantRight := offset.X + boxW
	if panel.Max.X != wantRight {
		t.Fatalf("flipped menu right = %d, want field right %d (panel=%v)", panel.Max.X, wantRight, panel)
	}
}

// TestComboboxInFormColumnListMatchesFieldWidth verifies §6.14: a Combobox in a
// 400 dp form column (via Block or stretched Min.X) opens a suggestion list as
// wide as the field, with item text aligned to the field's value text.
func TestComboboxInFormColumnListMatchesFieldWidth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		block bool
		minX  int
	}{
		{"block", true, 0},
		{"stretched_min_x", false, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			cb := NewComboboxState()
			p := &ComboboxProps{
				Label:       "Repository",
				Placeholder: "Pick a repository",
				Items:       []SuggestItem{{ID: "fsap", Label: "fsap"}},
				Block:       tc.block,
			}
			const colW = 400
			offset := image.Pt(32, 48)
			cb.Open()
			runFrame(t, h, func(gtx *layout.Context) {
				h.env.Offset(gtx, offset, func(gtx *layout.Context) {
					g := *gtx
					g.Constraints.Min.X = tc.minX
					g.Constraints.Max.X = colW
					if _, err := cb.Layout(h.env, &g, p); err != nil {
						t.Fatalf("combobox layout: %v", err)
					}
				})
			})
			r := cb.list.Rect()
			if r.Dx() != colW {
				t.Fatalf("suggest list width = %d, want %d", r.Dx(), colW)
			}
			if r.Min.X != offset.X {
				t.Fatalf("suggest list left = %d, want %d", r.Min.X, offset.X)
			}
			lead, _ := textFieldSlots(h.env, &h.gtx, p.fieldProps())
			fieldTextX := offset.X + h.gtx.Dp(theme.SpaceLG) + lead
			itemTextX := r.Min.X + cb.list.pad + cb.list.itemInset + h.gtx.Dp(theme.IconMD) + h.gtx.Dp(theme.SpaceSM)
			if itemTextX != fieldTextX {
				t.Fatalf("item text X = %d, want field value text X = %d", itemTextX, fieldTextX)
			}
		})
	}
}

// TestButtonAnchoredMenuKeepsMenuMinWidth verifies §6.15: a menu opened from a
// button keeps its MenuMinWidth floor rather than stretching to a wide trigger.
func TestButtonAnchoredMenuKeepsMenuMinWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	m.Open()
	p := MenuProps{
		Items:  []MenuItem{{ID: "compact-item", Label: "Compact item"}},
		Anchor: image.Rect(20, 20, 420, 52),
	}
	h.menuFrame(t, m, p)
	panels := m.Panels()
	if len(panels) != 1 {
		t.Fatalf("got %d panels, want 1", len(panels))
	}
	wantW := h.gtx.Dp(theme.MenuMinWidth)
	if panels[0].Dx() != wantW {
		t.Fatalf("button menu width = %d, want MenuMinWidth %d", panels[0].Dx(), wantW)
	}
	if m.lv[0].itemInset != h.gtx.Dp(theme.SpaceMD) {
		t.Fatalf("button menu item inset = %d, want SpaceMD %d", m.lv[0].itemInset, h.gtx.Dp(theme.SpaceMD))
	}
}
