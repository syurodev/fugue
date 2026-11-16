package kit

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Evidence for Menu keyboard, IME-safe typeahead, diagonal-travel dwell, and
// highlight sliding (F10.11b, ui-kit §6.15, §7, UK-05, UK-E05, UK-E11).

func testKeyboardMenuItems() []MenuItem {
	return []MenuItem{
		{ID: "kb-open", Label: "Open", Icon: IconSearch, Shortcut: Shortcut{Mods: ModPrimary, Key: Char('o')}},
		{ID: "kb-paste", Label: "Paste", Disabled: true},
		MenuSeparator(),
		{ID: "kb-read", Label: "Đọc"},
		{ID: "kb-rename", Label: "Đổi tên"},
		{ID: "kb-share", Label: "Share", Sub: []MenuItem{
			{ID: "kb-link", Label: "Copy link"},
			{ID: "kb-mail", Label: "Email"},
		}},
		{ID: "kb-delete", Label: "Delete"},
	}
}

func testTriggerFrame(t *testing.T, h *harness, btn *ButtonState, m *MenuState, items []MenuItem) MenuResult {
	t.Helper()
	return h.menuFrameWith(t, func(gtx *layout.Context) MenuResult {
		pr := ButtonProps{Text: "Menu", Variant: Bordered}
		box := btn.Measure(h.env, gtx, pr).Size
		clicked, err := btn.Layout(h.env, gtx, pr)
		if err != nil {
			t.Fatalf("lay the trigger out: %v", err)
		}
		if clicked {
			if btn.KeyClicked() {
				m.OpenKeyboard()
			} else {
				m.Open()
			}
		}
		m.SetOpener(btn.Tag())
		res, err := m.Layout(h.env, gtx, MenuProps{
			Items:  items,
			Anchor: image.Rectangle{Max: box},
		})
		if err != nil {
			t.Fatalf("lay the menu out: %v", err)
		}
		return res
	})
}

// TestMenuKeyboardWrapsTypeaheadAndReturnsFocus covers UK-E05: opening by
// keyboard (Down, Enter, Space) activates the first enabled item while pointer
// opening activates nothing; Up/Down wrap and skip disabled/separators;
// Home/End jump to ends; typeahead matches folded labels; Enter/Space choose;
// Escape closes and returns focus to the trigger.
func TestMenuKeyboardWrapsTypeaheadAndReturnsFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	btn := NewButtonState()
	m := NewMenuState()
	items := testMenuItems()

	testTriggerFrame(t, h, btn, m, items)
	h.gtx.Execute(key.FocusCmd{Tag: btn.Tag()})
	testTriggerFrame(t, h, btn, m, items)

	assertMenuWraps(t, h, btn, m, items)
	assertMenuEscapeReturnsFocus(t, h, btn, m, items)
	assertMenuEnterSpaceAndPointerOpen(t, h, btn, m, items)
}

func assertMenuWraps(t *testing.T, h *harness, btn *ButtonState, m *MenuState, items []MenuItem) {
	t.Helper()
	h.tap(key.NameDownArrow, 0)
	testTriggerFrame(t, h, btn, m, items)
	if !m.IsOpen() || m.lv[0].hi != itemOpen {
		t.Fatalf("Down on trigger: open=%v hi=%d, want open with hi=%d", m.IsOpen(), m.lv[0].hi, itemOpen)
	}
	h.tap(key.NameDownArrow, 0)
	testTriggerFrame(t, h, btn, m, items)
	if m.lv[0].hi != itemShare {
		t.Fatalf("Down from open: hi=%d, want %d", m.lv[0].hi, itemShare)
	}
	h.tap(key.NameDownArrow, 0)
	testTriggerFrame(t, h, btn, m, items)
	h.tap(key.NameDownArrow, 0)
	testTriggerFrame(t, h, btn, m, items)
	if m.lv[0].hi != itemOpen {
		t.Fatalf("Down wrap: hi=%d, want %d", m.lv[0].hi, itemOpen)
	}
	h.tap(key.NameUpArrow, 0)
	testTriggerFrame(t, h, btn, m, items)
	if m.lv[0].hi != itemDelete {
		t.Fatalf("Up wrap: hi=%d, want %d", m.lv[0].hi, itemDelete)
	}
	h.tap(key.NameHome, 0)
	testTriggerFrame(t, h, btn, m, items)
	if m.lv[0].hi != itemOpen {
		t.Fatalf("Home: hi=%d, want %d", m.lv[0].hi, itemOpen)
	}
	h.tap(key.NameEnd, 0)
	testTriggerFrame(t, h, btn, m, items)
	if m.lv[0].hi != itemDelete {
		t.Fatalf("End: hi=%d, want %d", m.lv[0].hi, itemDelete)
	}
}

func assertMenuEscapeReturnsFocus(t *testing.T, h *harness, btn *ButtonState, m *MenuState, items []MenuItem) {
	t.Helper()
	h.tap(key.NameEscape, 0)
	res := testTriggerFrame(t, h, btn, m, items)
	if !res.Closed || m.IsOpen() {
		t.Fatalf("Escape: closed=%v open=%v, want closed", res.Closed, m.IsOpen())
	}
	testTriggerFrame(t, h, btn, m, items)
	if !h.gtx.Focused(btn.Tag()) {
		t.Fatal("Escape did not return focus to the trigger")
	}
}

func assertMenuEnterSpaceAndPointerOpen(t *testing.T, h *harness, btn *ButtonState, m *MenuState, items []MenuItem) {
	t.Helper()
	h.tap(key.NameReturn, 0)
	testTriggerFrame(t, h, btn, m, items)
	if !m.IsOpen() || m.lv[0].hi != itemOpen {
		t.Fatalf("Enter on trigger: open=%v hi=%d, want open with hi=%d", m.IsOpen(), m.lv[0].hi, itemOpen)
	}
	h.tap(key.NameSpace, 0)
	res := testTriggerFrame(t, h, btn, m, items)
	if res.Chosen != items[itemOpen].ID || !res.Closed {
		t.Fatalf("Space choose: chosen=%q closed=%v, want %q, true", res.Chosen, res.Closed, items[itemOpen].ID)
	}
	h.click(10, 10)
	testTriggerFrame(t, h, btn, m, items)
	if !m.IsOpen() || m.lv[0].hi != -1 {
		t.Fatalf("pointer open: open=%v hi=%d, want open with hi=-1", m.IsOpen(), m.lv[0].hi)
	}
}

// TestMenuTypeaheadFollowsTheInputMethod verifies typeahead reads committed
// key.EditEvent through key.FocusFilter, ignores active composition, folds
// diacritics ("do" matches "Đổi tên"), and resets the prefix after 700 ms.
func TestMenuTypeaheadFollowsTheInputMethod(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	items := testKeyboardMenuItems()
	p := MenuProps{Items: items, Anchor: image.Rect(10, 10, 60, 30)}
	m.OpenKeyboard()
	h.menuFrame(t, m, p)
	h.menuFrame(t, m, p)

	// Active composition must be ignored.
	h.router.Queue(key.CompositionEvent{Start: 0, End: 1}, key.EditEvent{Text: "d"})
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 0 {
		t.Fatalf("uncommitted composition moved highlight to %d", m.lv[0].hi)
	}

	// Plain key.Event letters without EditEvent do nothing.
	h.tap("D", 0)
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 0 {
		t.Fatalf("plain key.Event moved highlight to %d", m.lv[0].hi)
	}

	// Commit "d", "o", "i" -> jumps to "Đọc" (3), stays on "Đọc", then "Đổi tên" (4).
	h.router.Queue(key.CompositionEvent{Start: -1, End: -1}, key.EditEvent{Text: "d"})
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 3 {
		t.Fatalf("committed 'd': hi=%d, want 3 (Đọc)", m.lv[0].hi)
	}
	h.router.Queue(key.EditEvent{Text: "o"})
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 3 {
		t.Fatalf("committed 'do': hi=%d, want 3 (Đọc)", m.lv[0].hi)
	}
	h.router.Queue(key.EditEvent{Text: "i"})
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 4 {
		t.Fatalf("committed 'doi': hi=%d, want 4 (Đổi tên)", m.lv[0].hi)
	}

	// Advance fake clock past 700 ms -> prefix resets, "o" jumps to "Open" (0).
	h.now = h.now.Add(750 * time.Millisecond)
	h.router.Queue(key.EditEvent{Text: "o"})
	h.menuFrame(t, m, p)
	if m.lv[0].hi != 0 {
		t.Fatalf("after 700 ms reset 'o': hi=%d, want 0 (Open)", m.lv[0].hi)
	}

	assertTypeaheadSpace(t, h, m, p)
}

// assertTypeaheadSpace checks Space against the typeahead prefix: inside a
// live prefix it continues it, after the prefix expired it chooses.
func assertTypeaheadSpace(t *testing.T, h *harness, m *MenuState, p MenuProps) {
	t.Helper()
	// "doi ten" lands on "Đổi tên" and chooses nothing; the platform's own
	// " " edit that comes with the key is not counted twice.
	h.now = h.now.Add(750 * time.Millisecond)
	for _, txt := range []string{"d", "o", "i"} {
		h.router.Queue(key.EditEvent{Text: txt})
		h.menuFrame(t, m, p)
	}
	h.tap(key.NameSpace, 0)
	h.router.Queue(key.EditEvent{Text: " "})
	if res := h.menuFrame(t, m, p); res.Chosen != "" || !m.IsOpen() {
		t.Fatalf("Space inside the prefix chose %q (open %v), want it to extend the prefix", res.Chosen, m.IsOpen())
	}
	for _, txt := range []string{"t", "e", "n"} {
		h.router.Queue(key.EditEvent{Text: txt})
		h.menuFrame(t, m, p)
	}
	if m.lv[0].hi != 4 {
		t.Fatalf("typed 'doi ten': hi=%d, want 4 (Đổi tên)", m.lv[0].hi)
	}

	// With the prefix expired, Space chooses the active item.
	h.now = h.now.Add(750 * time.Millisecond)
	h.tap(key.NameSpace, 0)
	if res := h.menuFrame(t, m, p); res.Chosen != "kb-rename" {
		t.Fatalf("Space after the prefix expired chose %q, want kb-rename", res.Chosen)
	}
}

// TestMenuSubmenuOpensRightAndClosesLeft verifies Right opens the active
// item's submenu and moves into it, and Left / Escape closes the innermost
// submenu back to the parent item.
func TestMenuSubmenuOpensRightAndClosesLeft(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	m.OpenKeyboard()
	h.menuFrame(t, m, p)
	h.menuFrame(t, m, p)

	h.tap(key.NameDownArrow, 0)
	h.menuFrame(t, m, p)
	if m.lv[0].hi != itemShare {
		t.Fatalf("hi=%d, want %d", m.lv[0].hi, itemShare)
	}

	h.tap(key.NameRightArrow, 0)
	h.menuFrame(t, m, p)
	if !m.lv[1].open.Shown() || m.active != 1 || m.lv[1].hi != 0 {
		t.Fatalf("Right: subOpen=%v active=%d subHi=%d, want true, 1, 0", m.lv[1].open.Shown(), m.active, m.lv[1].hi)
	}

	h.tap(key.NameLeftArrow, 0)
	h.menuFrame(t, m, p)
	if m.lv[1].open.Shown() || m.active != 0 || m.lv[0].hi != itemShare {
		t.Fatalf("Left: subOpen=%v active=%d hi=%d, want false, 0, %d", m.lv[1].open.Shown(), m.active, m.lv[0].hi, itemShare)
	}

	// Escape closes innermost submenu first, then root on second Escape.
	h.tap(key.NameRightArrow, 0)
	h.menuFrame(t, m, p)
	h.tap(key.NameEscape, 0)
	h.menuFrame(t, m, p)
	if m.lv[1].open.Shown() || !m.IsOpen() {
		t.Fatalf("first Escape: subOpen=%v rootOpen=%v, want false, true", m.lv[1].open.Shown(), m.IsOpen())
	}
	h.tap(key.NameEscape, 0)
	res := h.menuFrame(t, m, p)
	if !res.Closed || m.IsOpen() {
		t.Fatalf("second Escape: closed=%v rootOpen=%v, want true, false", res.Closed, m.IsOpen())
	}
}

// TestMenuDiagonalTravelDwellClosesAStaleSubmenu verifies that stopping inside
// the diagonal-travel triangle schedules a 300 ms Driver.WakeAt deadline and
// hands over to the item under the pointer once it expires.
func TestMenuDiagonalTravelDwellClosesAStaleSubmenu(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	m.Open()
	h.run(t, m, p, 5)
	h.moveTo(itemCentre(m, 0, itemShare))
	h.run(t, m, p, 3)
	if !m.lv[1].open.Shown() {
		t.Fatal("submenu did not open on hover")
	}

	// Move diagonally into the triangle over itemDelete and stop.
	below := itemCentre(m, 0, itemDelete)
	below.X = m.lv[0].rect.Max.X - m.lv[0].pad - 2
	h.moveTo(below)
	h.run(t, m, p, 2)
	if !m.lv[1].open.Shown() {
		t.Fatal("submenu closed before the 300 ms dwell")
	}
	if _, wake := h.env.Driver.Next(); !wake {
		t.Fatal("expected Driver.WakeAt deadline while dwelling in diagonal triangle")
	}

	// Advance fake clock past 300 ms without moving the pointer.
	h.now = h.now.Add(300 * time.Millisecond)
	h.menuFrame(t, m, p)
	if m.lv[1].open.Shown() || m.lv[0].hi != itemDelete {
		t.Fatalf("after dwell: subOpen=%v hi=%d, want false, %d", m.lv[1].open.Shown(), m.lv[0].hi, itemDelete)
	}
}

// TestMenuHighlightSlidesAndSettles covers UK-E11 for the menu highlight:
// Snappy slide on keyboard/pointer change, mid-flight retargeting, two quiet
// frames at rest, and instant snap under ReducedMotion.
func TestMenuHighlightSlidesAndSettles(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	m := NewMenuState()
	p := MenuProps{Items: testMenuItems(), Anchor: image.Rect(10, 10, 60, 30)}
	m.OpenKeyboard()
	h.settle(t, m, p, "open")

	h.tap(key.NameEnd, 0)
	h.menuFrame(t, m, p)
	target := float32(m.lv[0].rows[itemDelete].y)
	if m.lv[0].hiYv <= 0 || m.lv[0].hiYv >= target {
		t.Fatalf("hiYv=%v after 1 frame, want strictly between 0 and %v", m.lv[0].hiYv, target)
	}
	if h.quiet() {
		t.Fatal("sliding highlight asked for no next frame")
	}

	// Retarget mid-flight back to top.
	h.tap(key.NameHome, 0)
	h.menuFrame(t, m, p)
	h.settle(t, m, p, "retarget")
	if m.lv[0].hiYv > 0.5 {
		t.Fatalf("after retargeting home: hiYv=%v, want ~0", m.lv[0].hiYv)
	}

	// Reduced motion snaps immediately.
	h.env.ReducedMotion = true
	h.tap(key.NameEnd, 0)
	h.menuFrame(t, m, p)
	if got := m.lv[0].hiYv; got != target {
		t.Fatalf("reduced motion hiYv=%v, want %v (diff %v)", got, target, theme.Row)
	}
	h.settle(t, m, p, "reduced motion")
}
