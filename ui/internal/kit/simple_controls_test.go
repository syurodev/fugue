package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func focusTag(t *testing.T, h *harness, tag event.Tag, step func(func())) {
	t.Helper()
	step(func() {
		h.gtx.Execute(key.FocusCmd{Tag: tag})
	})
}

func composite(fg, bg color.NRGBA) color.NRGBA {
	a := float32(fg.A) / 255
	return color.NRGBA{
		R: uint8(float32(fg.R)*a + float32(bg.R)*(1-a) + 0.5),
		G: uint8(float32(fg.G)*a + float32(bg.G)*(1-a) + 0.5),
		B: uint8(float32(fg.B)*a + float32(bg.B)*(1-a) + 0.5),
		A: 255,
	}
}

// TestCheckboxCyclesThreeStatesWithSpace covers F10.19, §6.10, UK-E05 and UK-E11:
// Space cycles off -> on -> mixed -> off, clicking the label activates it,
// disabled ignores input, and check draw-on + press scale settle cleanly.
func TestCheckboxCyclesThreeStatesWithSpace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewCheckboxState()
	val := CheckOff
	disabled := false

	step := func(extra ...func()) (bool, CheckState) {
		h.frameSize(t, 240, 48)
		ch, next, err := st.Layout(h.env, &h.gtx, CheckboxProps{Label: "Accept all", Value: val, Disabled: disabled})
		if err != nil {
			t.Fatalf("Checkbox.Layout: %v", err)
		}
		for _, fn := range extra {
			fn()
		}
		h.end()
		return ch, next
	}

	step()
	focusTag(t, h, st.Tag(), func(fn func()) { step(fn) })

	// Space cycles off -> on -> mixed -> off.
	wantOrder := []CheckState{CheckOn, CheckMixed, CheckOff}
	for _, want := range wantOrder {
		h.tap(key.NameSpace, 0)
		ch, next := step()
		if !ch || next != want {
			t.Fatalf("Space cycle: got changed=%v next=%v, want true %v", ch, next, want)
		}
		val = next
	}

	// Label is part of the hit area: click near the right end of the control.
	sz := st.Measure(h.env, &h.gtx, CheckboxProps{Label: "Accept all"}).Size
	h.click(float32(sz.X-4), float32(sz.Y/2))
	ch, next := step()
	if !ch || next != CheckOn {
		t.Fatalf("label click: got changed=%v next=%v, want true CheckOn", ch, next)
	}
	val = next

	// Disabled ignores Space.
	disabled = true
	h.tap(key.NameSpace, 0)
	if ch, _ := step(); ch {
		t.Fatal("disabled Checkbox must not activate on Space")
	}

	// Settle motion (UK-E11).
	disabled = false
	for range 60 {
		step()
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("Checkbox motion did not settle within 60 frames")
	}
}

// TestSwitchShowsPendingAndNeverFakesOn covers F10.19, §6.11 and UK-E11:
// while SwitchPending, the thumb stays at the confirmed Checked value and
// never fakes "on"; SwitchError settles back to the confirmed state.
func TestSwitchShowsPendingAndNeverFakesOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewSwitchState()
	checked := false
	status := SwitchPending

	step := func() bool {
		h.frameSize(t, 240, 48)
		toggled, err := st.Layout(h.env, &h.gtx, SwitchProps{Label: "Auto-sync", Checked: checked, Status: status})
		if err != nil {
			t.Fatalf("Switch.Layout: %v", err)
		}
		h.end()
		return toggled
	}

	step()
	h.click(12, 12)
	if toggled := step(); toggled {
		t.Fatal("SwitchPending must ignore clicks and never fake on")
	}
	for range 20 {
		step()
		if st.ThumbProgress() > 0.01 {
			t.Fatalf("SwitchPending moved thumb to %v before confirmation", st.ThumbProgress())
		}
	}

	// Caller reports error: still at confirmed false, settles cleanly.
	status = SwitchError
	for range 60 {
		step()
	}
	if st.ThumbProgress() > 0.01 {
		t.Fatalf("SwitchError moved thumb to %v while checked=false", st.ThumbProgress())
	}

	// Caller confirms true: Bouncy thumb travels to 1 and settles.
	status = SwitchIdle
	checked = true
	for range 60 {
		step()
	}
	_, moving := h.env.Driver.Next()
	if st.ThumbProgress() < 0.99 || moving {
		t.Fatalf("confirmed Switch did not settle at 1: thumb=%v moving=%v", st.ThumbProgress(), moving)
	}
}

// TestSegmentedControlArrowsMoveTheSlidingPill covers F10.19, §6.12, UK-E05 and UK-E11:
// single tab stop, Left/Right arrows move selection (skipping disabled) and
// slide the Smooth pill.
func TestSegmentedControlArrowsMoveTheSlidingPill(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewSegmentedState()
	opts := []SegmentedOption{
		{ID: "day", Label: "Day"},
		{ID: "week", Label: "Week", Disabled: true},
		{ID: "month", Label: "Month"},
	}
	sel := 0

	step := func(extra ...func()) (bool, int) {
		h.frameSize(t, 320, 48)
		ch, next, err := st.Layout(h.env, &h.gtx, SegmentedProps{Options: opts, Selected: sel})
		if err != nil {
			t.Fatalf("SegmentedControl.Layout: %v", err)
		}
		for _, fn := range extra {
			fn()
		}
		h.end()
		return ch, next
	}

	step()
	pill0 := st.PillRect()
	focusTag(t, h, st.Tag(), func(fn func()) { step(fn) })

	// Right arrow skips disabled "week" and selects "month" (index 2).
	h.tap(key.NameRightArrow, 0)
	ch, next := step()
	if !ch || next != 2 {
		t.Fatalf("Right arrow: got changed=%v next=%d, want true 2", ch, next)
	}
	sel = next

	for range 120 {
		step()
	}
	pill2 := st.PillRect()
	if pill2.Min.X <= pill0.Min.X {
		t.Fatalf("sliding pill did not move right: pill0=%v pill2=%v", pill0, pill2)
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("SegmentedControl pill did not settle within 120 frames")
	}

	// Left arrow returns to "day" (index 0).
	h.tap(key.NameLeftArrow, 0)
	ch, next = step()
	if !ch || next != 0 {
		t.Fatalf("Left arrow: got changed=%v next=%d, want true 0", ch, next)
	}
}

// TestTabsCtrlTabCyclesDocumentTabsAndOverflowListsHidden covers F10.19, §6.22,
// UK-E05 and UK-E11: Ctrl+Tab / Ctrl+Shift+Tab cycle document tabs, MRU close
// picks the most recent tab, mode tabs use arrows, and overflow lists hidden tabs.
func TestTabsCtrlTabCyclesDocumentTabsAndOverflowListsHidden(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewTabsState()
	items := []TabItem{
		{ID: "a", Label: "alpha.go", Closable: true},
		{ID: "b", Label: "beta.go", Closable: true},
		{ID: "c", Label: "gamma.go", Closable: true},
		{ID: "d", Label: "delta.go", Closable: true},
	}
	act := "a"

	step := func(w int, extra ...func()) TabsEvents {
		h.frameSize(t, w, 40)
		_, ev := st.Layout(h.env, &h.gtx, TabsProps{Variant: TabsDocument, Items: items, ActiveID: act})
		if ev.Err != nil {
			t.Fatalf("Tabs.Layout: %v", ev.Err)
		}
		for _, fn := range extra {
			fn()
		}
		h.end()
		return ev
	}

	// Narrow width forces overflow and populates HiddenIDs.
	step(180)
	if len(st.HiddenIDs()) == 0 {
		t.Fatal("expected HiddenIDs on narrow tab strip")
	}

	focusTag(t, h, st.Tag(), func(fn func()) { step(400, fn) })

	// Ctrl+Tab -> b, Ctrl+Tab -> c, Ctrl+Shift+Tab -> b.
	h.tap(key.NameTab, key.ModCtrl)
	if ev := step(400); ev.Selected != "b" {
		t.Fatalf("Ctrl+Tab: got %q, want %q", ev.Selected, "b")
	}
	act = "b"
	h.tap(key.NameTab, key.ModCtrl)
	if ev := step(400); ev.Selected != "c" {
		t.Fatalf("second Ctrl+Tab: got %q, want %q", ev.Selected, "c")
	}
	act = "c"
	h.tap(key.NameTab, key.ModCtrl|key.ModShift)
	if ev := step(400); ev.Selected != "b" {
		t.Fatalf("Ctrl+Shift+Tab: got %q, want %q", ev.Selected, "b")
	}
	act = "b"

	// MRU close: closing active "b" returns to "c" (most recent), closing
	// background "a" keeps "b".
	if got := st.NextOnClose(items, act, "a"); got != "b" {
		t.Fatalf("closing background tab: got %q, want %q", got, "b")
	}
	if got := st.NextOnClose(items, act, "b"); got != "c" {
		t.Fatalf("closing active tab: got %q, want MRU %q", got, "c")
	}

	checkModeTabs(t, h)
}

func checkModeTabs(t *testing.T, h *harness) {
	t.Helper()
	mode := NewTabsState()
	const teamID = "teamMode"
	modeItems := []TabItem{{ID: "chat", Label: "Chat"}, {ID: teamID, Label: "Team"}}
	modeAct := "chat"
	stepMode := func(extra ...func()) TabsEvents {
		h.frameSize(t, 240, 36)
		_, ev := mode.Layout(h.env, &h.gtx, TabsProps{Variant: TabsMode, Items: modeItems, ActiveID: modeAct})
		for _, fn := range extra {
			fn()
		}
		h.end()
		return ev
	}
	stepMode()
	ind0 := mode.IndicatorRect()
	focusTag(t, h, mode.Tag(), func(fn func()) { stepMode(fn) })
	h.tap(key.NameRightArrow, 0)
	if ev := stepMode(); ev.Selected != teamID {
		t.Fatalf("mode tabs Right arrow: got %q, want %q", ev.Selected, teamID)
	}
	modeAct = teamID
	for range 60 {
		stepMode()
	}
	if mode.IndicatorRect().Min.X <= ind0.Min.X {
		t.Fatalf("mode tab pill did not slide right: %v -> %v", ind0, mode.IndicatorRect())
	}
}

// TestBannerAndEmptyStateUseTheirTokens covers F10.19, §6.24, §6.25, UK-E04
// and UK-E11: Banner uses status tones (≥4.5:1 in both modes), dismissible
// collapses on Smooth/Exit, and EmptyState no-results offers clear-filter and
// settles its fade/rise.
func TestBannerAndEmptyStateUseTheirTokens(t *testing.T) {
	t.Parallel()
	sh := newHarness(t).env.Sh
	for _, th := range []theme.Theme{theme.Light(sh), theme.Dark(sh)} {
		for _, k := range []BannerKind{BannerInfo, BannerWarning, BannerDanger} {
			tone := th.StatusTone(k.statusKind())
			if r := contrastRatio(tone.Fg, composite(tone.Fill, th.Chrome().CanvasBg)); r < 4.5 {
				t.Errorf("Banner kind %d contrast %0.2f < 4.5", k, r)
			}
		}
	}

	h := newHarness(t)
	b := NewBannerState()
	visible := true
	stepBanner := func() (layout.Dimensions, BannerEvents) {
		h.frameSize(t, 360, 120)
		d, ev, err := b.Layout(h.env, &h.gtx, BannerProps{
			Kind:        BannerWarning,
			Title:       "Warning",
			Message:     "Slow host connection.",
			ActionLabel: "Retry",
			Dismissible: true,
			Visible:     visible,
		})
		if err != nil {
			t.Fatalf("Banner.Layout: %v", err)
		}
		h.end()
		return d, ev
	}
	d0, _ := stepBanner()
	if d0.Size.Y == 0 {
		t.Fatal("visible Banner had zero height")
	}
	visible = false
	for range 60 {
		stepBanner()
	}
	if b.Presence() > 0.01 {
		t.Fatalf("dismissed Banner did not settle to 0: %v", b.Presence())
	}

	es := NewEmptyStateState()
	for range 60 {
		h.frameSize(t, 360, 240)
		_, _, err := es.Layout(h.env, &h.gtx, &EmptyStateProps{
			Icon:              IconSearch,
			Title:             "No results",
			Description:       "Try another query.",
			NoResults:         true,
			ClearFilterAction: "Clear filter",
		})
		if err != nil {
			t.Fatalf("EmptyState.Layout: %v", err)
		}
		h.end()
	}
	if es.Presence() < 0.99 {
		t.Fatalf("EmptyState did not settle to 1: %v", es.Presence())
	}
}

// TestSimpleControlsEventAreasAndContrast covers UK-E04 and UK-E10 for Card
// and the new simple controls.
func TestSimpleControlsEventAreasAndContrast(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	card := NewCardState()
	clicked := false
	stepCard := func() {
		h.frameSize(t, 240, 120)
		_, c, err := card.Layout(h.env, &h.gtx, CardProps{Clickable: true}, func(gtx layout.Context) layout.Dimensions {
			return Label(h.env, &gtx, theme.TypeBody(), h.env.Th.Chrome().Fg, "Card body")
		})
		if err != nil {
			t.Fatalf("Card.Layout: %v", err)
		}
		clicked = c
		h.end()
	}
	stepCard()
	h.click(20, 20)
	stepCard()
	if !clicked {
		t.Fatal("clickable Card did not fire on interior click")
	}

	// Verify Card SurfaceRaised text contrast and FocusBorder 3:1 in both modes.
	for _, th := range []theme.Theme{theme.Light(h.env.Sh), theme.Dark(h.env.Sh)} {
		c := th.Chrome()
		if r := contrastRatio(c.Fg, composite(c.SurfaceRaised, c.AppBg)); r < 4.5 {
			t.Errorf("Card Fg on SurfaceRaised contrast %0.2f < 4.5", r)
		}
		if r := contrastRatio(c.FocusBorder, c.CanvasBg); r < 3.0 {
			t.Errorf("FocusBorder contrast %0.2f < 3.0", r)
		}
	}
	_ = image.Point{}
}
