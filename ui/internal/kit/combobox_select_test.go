package kit

import (
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
)

func runFrame(t *testing.T, h *harness, fn func(gtx *layout.Context)) {
	t.Helper()
	h.frameSize(t, 800, 600)
	h.env.TrackInput(&h.gtx)
	fn(&h.gtx)
	h.env.FrameEnd(&h.gtx)
	h.end()
}

func comboboxTestItems() []SuggestItem {
	return []SuggestItem{
		{ID: "alpha", Label: "A-item", Secondary: "primary"},
		{ID: "doi", Label: "Đặt tên", Secondary: "rename"},
		{ID: "beta", Label: "B-item", Disabled: true},
		{ID: "gamma", Label: "G-item a", Shortcut: Shortcut{Mods: ModPrimary, Key: Char('g')}},
	}
}

func comboboxFrame(t *testing.T, h *harness, cb *ComboboxState, p *ComboboxProps) ComboboxResult {
	t.Helper()
	var res ComboboxResult
	runFrame(t, h, func(gtx *layout.Context) {
		var err error
		res, err = cb.Layout(h.env, gtx, p)
		if err != nil {
			t.Fatalf("combobox layout: %v", err)
		}
	})
	return res
}

// TestComboboxArrowsEnterAndEscapeTwice covers §6.14 and UK-E05:
// arrows move the highlight while the caret stays in the field, Enter
// chooses, Escape closes the list without clearing, and a second Escape
// clears the text.
func TestComboboxArrowsEnterAndEscapeTwice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cb := NewComboboxState()
	p := &ComboboxProps{Label: "Picker", Placeholder: "Filter", Items: comboboxTestItems()}

	comboboxFrame(t, h, cb, p)
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := cb.Layout(h.env, gtx, p); err != nil {
			t.Fatal(err)
		}
		gtx.Execute(key.FocusCmd{Tag: cb.Tag()})
	})
	comboboxFrame(t, h, cb, p)

	assertComboboxFilterAndEscape(t, h, cb, p)
	assertComboboxTabAndDisabledOpen(t, h, cb, p)

	// Re-open with Down, then Enter chooses the highlighted item.
	h.tap(key.NameDownArrow, 0)
	comboboxFrame(t, h, cb, p)
	if !cb.IsOpen() {
		t.Fatal("Down did not reopen combobox")
	}
	h.tap(key.NameReturn, 0)
	res := comboboxFrame(t, h, cb, p)
	if res.Chosen != "alpha" || cb.IsOpen() {
		t.Fatalf("Enter chose %q (open=%v), want alpha (closed)", res.Chosen, cb.IsOpen())
	}
}

func assertComboboxTabAndDisabledOpen(t *testing.T, h *harness, cb *ComboboxState, p *ComboboxProps) {
	t.Helper()
	// Typing "a" opens list at 0 without userMoved; Tab must NOT choose.
	h.router.Queue(key.EditEvent{Text: "a"})
	comboboxFrame(t, h, cb, p)
	h.tap(key.NameTab, 0)
	if res := comboboxFrame(t, h, cb, p); res.Chosen != "" {
		t.Fatalf("Tab without arrow move chose %q", res.Chosen)
	}
	// Moving with Down enables Tab choice.
	h.tap(key.NameDownArrow, 0)
	comboboxFrame(t, h, cb, p)
	h.tap(key.NameTab, 0)
	if res := comboboxFrame(t, h, cb, p); res.Chosen != "doi" {
		t.Fatalf("Tab after Down chose %q, want doi", res.Chosen)
	}
	cb.SetText("")
	comboboxFrame(t, h, cb, p)

	// Open() when item 0 is disabled highlights first enabled (1), or -1 if none.
	disFirst := &ComboboxProps{Items: []SuggestItem{{ID: "x", Label: "X", Disabled: true}, {ID: "y", Label: "Y"}}}
	cb.Open()
	comboboxFrame(t, h, cb, disFirst)
	if got := cb.Highlight(); got != 1 {
		t.Fatalf("Open with disabled first highlighted %d, want 1", got)
	}
	cb.Close()
	comboboxFrame(t, h, cb, disFirst)
	allDis := &ComboboxProps{Items: []SuggestItem{{ID: "x", Label: "X", Disabled: true}}}
	cb.Open()
	comboboxFrame(t, h, cb, allDis)
	if got := cb.Highlight(); got != -1 {
		t.Fatalf("Open with all disabled highlighted %d, want -1", got)
	}
	cb.Close()
	comboboxFrame(t, h, cb, allDis)
}

func assertComboboxFilterAndEscape(t *testing.T, h *harness, cb *ComboboxState, p *ComboboxProps) {
	t.Helper()
	// Type "a" -> opens list, filters to Alpha / Gamma (Alpha is prefix-ranked first).
	h.router.Queue(key.EditEvent{Text: "a"})
	res := comboboxFrame(t, h, cb, p)
	if !res.Changed || !cb.IsOpen() {
		t.Fatalf("typing did not open combobox: changed=%v open=%v", res.Changed, cb.IsOpen())
	}
	if got := cb.Highlight(); got != 0 {
		t.Fatalf("initial highlight = %d, want 0", got)
	}

	// Down arrow moves highlight to G-item a (1), while caret stays in the field.
	h.tap(key.NameDownArrow, 0)
	comboboxFrame(t, h, cb, p)
	if got := cb.Highlight(); got != 1 {
		t.Fatalf("after Down highlight = %d, want 1", got)
	}
	if !h.gtx.Focused(cb.Tag()) {
		t.Fatal("caret focus left the field on Down")
	}

	// First Escape closes the list but keeps the text "a".
	h.tap(key.NameEscape, 0)
	comboboxFrame(t, h, cb, p)
	if cb.IsOpen() {
		t.Fatal("first Escape did not close the list")
	}
	if got := cb.Text(); got != "a" {
		t.Fatalf("first Escape cleared text to %q, want %q", got, "a")
	}

	// Second Escape clears the text.
	h.tap(key.NameEscape, 0)
	res = comboboxFrame(t, h, cb, p)
	if !res.Changed || cb.Text() != "" {
		t.Fatalf("second Escape did not clear text: changed=%v text=%q", res.Changed, cb.Text())
	}
}

// TestComboboxShowsLoadingEmptyAndNoResultStates verifies the three status
// rows and the 150 ms spinner delay (§6.5, §6.14).
func TestComboboxShowsLoadingEmptyAndNoResultStates(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cb := NewComboboxState()
	cb.Open()

	// Loading before 150 ms: spinner not yet visible, WakeAt armed.
	p := &ComboboxProps{Loading: true}
	comboboxFrame(t, h, cb, p)
	if cb.list.status != suggestLoading {
		t.Fatalf("status = %v, want suggestLoading", cb.list.status)
	}
	if cb.list.spinnerVisible(&h.gtx, false) {
		t.Fatal("spinner appeared before 150 ms")
	}

	// Advance fake clock past 150 ms: spinner becomes visible.
	h.now = h.now.Add(200 * time.Millisecond)
	comboboxFrame(t, h, cb, p)
	if !cb.list.spinnerVisible(&h.gtx, false) {
		t.Fatal("spinner did not appear after 150 ms")
	}

	// Empty state (not loading, empty query, no items).
	p.Loading = false
	comboboxFrame(t, h, cb, p)
	if cb.list.status != suggestEmpty {
		t.Fatalf("status = %v, want suggestEmpty", cb.list.status)
	}

	// No-results state (non-empty query, no matching items).
	cb.SetText("zzz")
	comboboxFrame(t, h, cb, p)
	if cb.list.status != suggestNoResults {
		t.Fatalf("status = %v, want suggestNoResults", cb.list.status)
	}
}

// TestComboboxFiltersFoldedDiacriticsAndNeverChoosesWhileComposing checks
// Vietnamese diacritic folding (prefix ranked first) and IME composition.
func TestComboboxFiltersFoldedDiacriticsAndNeverChoosesWhileComposing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const prefID = "doi-moi"
	items := []SuggestItem{
		{ID: "sub", Label: "Tùy chọn đổi"},
		{ID: prefID, Label: "Đổi mới"},
	}
	got := DefaultComboboxFilter("doi", items)
	if len(got) != 2 || got[0].ID != prefID || got[1].ID != "sub" {
		t.Fatalf("folded filter order = %+v, want [%s sub]", got, prefID)
	}

	cb := NewComboboxState()
	p := &ComboboxProps{Items: items}
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := cb.Layout(h.env, gtx, p); err != nil {
			t.Fatal(err)
		}
		gtx.Execute(key.FocusCmd{Tag: cb.Tag()})
	})
	comboboxFrame(t, h, cb, p)

	// Start IME composition with "d": text includes composing text and
	// filters the suggestion list in place (on macOS, interpretKeyEvents +
	// convertCommandKey consumes Return/Tab before Gio delivers key.Event).
	h.router.Queue(key.CompositionEvent{Start: 0, End: 1}, key.EditEvent{Text: "d"})
	comboboxFrame(t, h, cb, p)
	if cb.Text() != "d" || !cb.IsOpen() {
		t.Fatalf("composing text=%q open=%v, want 'd' and open", cb.Text(), cb.IsOpen())
	}
	if cb.Highlight() != 0 {
		t.Fatalf("highlight while composing = %d, want 0", cb.Highlight())
	}

	// Commit composition, then Enter chooses.
	h.router.Queue(key.CompositionEvent{Start: -1, End: -1}, key.EditEvent{Text: "đ", Range: key.Range{Start: 0, End: 1}})
	comboboxFrame(t, h, cb, p)
	h.tap(key.NameReturn, 0)
	res := comboboxFrame(t, h, cb, p)
	if res.Chosen != prefID {
		t.Fatalf("after commit Enter chose %q, want %s", res.Chosen, prefID)
	}
}

func TestSuggestListAnchorsToAnyRectAndNeverTakesFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sl := NewSuggestListState()
	sl.Open()
	items := []SuggestItem{{ID: "one", Label: "One"}, {ID: "two", Label: "Two"}}
	anchor := image.Rect(100, 560, 260, 588)

	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := sl.Layout(h.env, gtx, &SuggestProps{Items: items, Anchor: anchor}); err != nil {
			t.Fatalf("suggest layout: %v", err)
		}
	})
	r := sl.Rect()
	if r.Empty() || r.Max.Y > anchor.Min.Y {
		t.Fatalf("list at %v did not flip above bottom anchor %v", r, anchor)
	}
	if sl.dir.Y <= 0 {
		t.Fatalf("flipped dir.Y = %d, want > 0", sl.dir.Y)
	}
	assertSuggestListClickAbsorbed(t, h, sl, items, anchor, r)
}

func assertSuggestListClickAbsorbed(t *testing.T, h *harness, sl *SuggestListState, items []SuggestItem, anchor, r image.Rectangle) {
	t.Helper()
	btn := NewButtonState()
	var btnClicked bool
	clickPt := image.Pt(r.Min.X+20, r.Min.Y+sl.pad+sl.rows[0].y+sl.rows[0].h/2)
	layBoth := func() SuggestResult {
		var res SuggestResult
		runFrame(t, h, func(gtx *layout.Context) {
			at(gtx, r.Min, func(gtx *layout.Context) {
				var err error
				btnClicked, err = btn.Layout(h.env, gtx, ButtonProps{Text: "Behind"})
				if err != nil {
					t.Fatal(err)
				}
			})
			var err error
			res, err = sl.Layout(h.env, gtx, &SuggestProps{Items: items, Anchor: anchor})
			if err != nil {
				t.Fatal(err)
			}
		})
		return res
	}
	layBoth()
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: layout.FPt(clickPt), Buttons: pointer.ButtonPrimary})
	res := layBoth()
	if res.Chosen != "one" {
		t.Fatalf("click chose %q, want one", res.Chosen)
	}
	if btnClicked {
		t.Fatal("click on SuggestList fell through to the button behind it")
	}
	if h.gtx.Focused(sl) {
		t.Fatal("SuggestList took keyboard focus")
	}
}

// TestSelectOpensAMenuAndKeepsAnUnselectedState covers §6.13 and UK-E05.
func TestSelectOpensAMenuAndKeepsAnUnselectedState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sel := NewSelectState()
	p := &SelectProps{
		Label:       "Choice",
		Placeholder: "Choose model",
		Options:     []SelectOption{{ID: "sonnet", Label: "Claude Sonnet"}, {ID: "opus", Label: "Claude Opus"}},
	}
	if disp, ok := selectDisplayText(p); ok || disp != "Choose model" {
		t.Fatalf("unselected display = (%q, %v), want ('Choose model', false)", disp, ok)
	}
	lay := func() SelectResult {
		var res SelectResult
		runFrame(t, h, func(gtx *layout.Context) {
			var err error
			res, err = sel.Layout(h.env, gtx, p)
			if err != nil {
				t.Fatalf("select layout: %v", err)
			}
		})
		return res
	}
	lay()
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := sel.Layout(h.env, gtx, p); err != nil {
			t.Fatal(err)
		}
		gtx.Execute(key.FocusCmd{Tag: sel.Tag()})
	})
	lay()
	assertSelectOpenAndEscape(t, h, sel, lay)
}

func assertSelectOpenAndEscape(t *testing.T, h *harness, sel *SelectState, lay func() SelectResult) {
	t.Helper()
	if got := sel.chevronF.Value(); got != 0 {
		t.Fatalf("rest chevron = %v, want 0", got)
	}
	h.tap(key.NameSpace, 0)
	for range 60 {
		lay()
	}
	if !sel.IsOpen() {
		t.Fatal("Space did not open Select")
	}
	if got := sel.chevronF.Value(); got < 0.99 {
		t.Fatalf("open chevron = %v, want 1 (180 deg)", got)
	}
	h.tap(key.NameEscape, 0)
	for range 60 {
		lay()
	}
	if sel.IsOpen() || !h.gtx.Focused(sel.Tag()) {
		t.Fatalf("Escape open=%v focused=%v, want closed and focused", sel.IsOpen(), h.gtx.Focused(sel.Tag()))
	}
	if got := sel.chevronF.Value(); got > 0.01 {
		t.Fatalf("closed chevron = %v, want 0", got)
	}
	h.tap(key.NameDownArrow, 0)
	lay()
	if !sel.IsOpen() {
		t.Fatal("Down did not open Select")
	}
	h.tap(key.NameReturn, 0)
	res := lay()
	if !res.Changed || res.Value != "sonnet" {
		t.Fatalf("chosen = (%v, %q), want (true, 'sonnet')", res.Changed, res.Value)
	}
}

// TestSuggestListMotionSettlesAndRetargets covers UK-E11 and UK-E07.
func TestSuggestListMotionSettlesAndRetargets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sl := NewSuggestListState()
	sl.Open()
	items := []SuggestItem{{ID: "a", Label: "One"}, {ID: "b", Label: "Two"}}
	p := &SuggestProps{Items: items, Anchor: image.Rect(40, 40, 200, 68)}

	quiet := 0
	for range 120 {
		runFrame(t, h, func(gtx *layout.Context) {
			if _, err := sl.Layout(h.env, gtx, p); err != nil {
				t.Fatal(err)
			}
		})
		if _, ok := h.env.Driver.Next(); !ok {
			quiet++
			if quiet == 2 {
				break
			}
		} else {
			quiet = 0
		}
	}
	if quiet < 2 {
		t.Fatal("SuggestList open motion did not settle")
	}

	h.env.ReducedMotion = true
	sl.Close()
	runFrame(t, h, func(gtx *layout.Context) {
		if _, err := sl.Layout(h.env, gtx, p); err != nil {
			t.Fatal(err)
		}
	})
	if sl.open.Visible() {
		t.Fatal("reduced motion Close did not snap invisible")
	}
}

// TestSuggestListTextContrastOnSurface covers UK-E04 for SuggestList text
// and Kbd chips on SurfaceRaised and the highlight pill.
func TestSuggestListTextContrastOnSurface(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			env := newModeEnv(t, mode.th)
			c := env.Th.Chrome()
			checkContrastSurface(t, "SurfaceRaised", c.Fg, c.FgMuted, c.SurfaceRaised, c.FillSoft)
			checkContrastSurface(t, "highlight pill", c.Fg, c.FgMuted, over(c.SurfaceRaised, c.HoverWash), c.FillSoft)
		})
	}
}

func checkContrastSurface(t *testing.T, name string, fg, muted, bg, kbdFill color.NRGBA) {
	t.Helper()
	if got := contrastRatio(fg, bg); got < 4.5 {
		t.Errorf("SuggestList label on %s: contrast %.2f < 4.5", name, got)
	}
	if got := contrastRatio(muted, bg); got < 4.5 {
		t.Errorf("SuggestList secondary on %s: contrast %.2f < 4.5", name, got)
	}
	if got := contrastRatio(muted, over(bg, kbdFill)); got < 4.5 {
		t.Errorf("SuggestList Kbd on %s: contrast %.2f < 4.5", name, got)
	}
}
