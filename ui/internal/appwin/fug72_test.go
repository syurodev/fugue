package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

func wantsImmediateFrame(env *kit.Env) bool {
	at, ok := env.Driver.Next()
	return ok && at.IsZero()
}

func settleShellFrames(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now *time.Time) {
	t.Helper()
	for range 100 {
		*now = now.Add(16 * time.Millisecond)
		runShellEnvFrame(t, env, sc, router, size, *now)
		if !wantsImmediateFrame(env) {
			return
		}
	}
	t.Fatal("shell did not settle within 100 frames")
}

func railCenter(env *kit.Env, sc *screens, gtx *layout.Context, index int) f32.Point {
	barH := sc.titleBar.layout(env, gtx, sc)
	failGtx := *gtx
	failGtx.Constraints.Min = image.Point{}
	failH := sc.failure.layout(env, &failGtx, sc).Size.Y
	w := gtx.Dp(theme.RailWidth)
	btnW := gtx.Dp(theme.ControlSM)
	gap := gtx.Dp(theme.SpaceXS)
	return f32.Pt(float32(w)/2, float32(barH+failH+gap+index*(btnW+gap))+float32(btnW)/2)
}

func sidebarTogglePts(env *kit.Env, sc *screens, gtx *layout.Context) (collapsePt, expandPt f32.Point) {
	pt := railCenter(env, sc, gtx, 0)
	return pt, pt
}

func clickShellAt(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now time.Time, pt f32.Point) {
	t.Helper()
	router.Queue(
		pointer.Event{Kind: pointer.Move, Position: pt},
		pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: pt},
		pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonPrimary, Position: pt},
	)
	runShellEnvFrame(t, env, sc, router, size, now)
}

func assertRailConsistent(t *testing.T, sc *screens, wantRail railItem, wantKind SlotKind) {
	t.Helper()
	if sc.grid.rail != wantRail {
		t.Fatalf("selected rail = %v, want %v", sc.grid.rail, wantRail)
	}
	if got := sc.grid.mainRef().Kind; got != wantKind {
		t.Fatalf("main slot kind = %v, want %v", got, wantKind)
	}
	if sc.grid.railBtns[wantRail] == nil {
		t.Fatalf("rail button for %v was not laid out", wantRail)
	}
}

func advanceShellFrames(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now *time.Time, n int) {
	t.Helper()
	for range n {
		*now = now.Add(16 * time.Millisecond)
		runShellEnvFrame(t, env, sc, router, size, *now)
	}
}

// TestFUG72RailHighlightFollowsClickAndSlides verifies that clicking rail items
// updates Toggled, selected rail, and main slot kind in the same frame without
// extra input, slides the indicator on Smooth, and snaps under ReducedMotion.
func TestFUG72RailHighlightFollowsClickAndSlides(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.applyConnectionUpdate(hostconn.StateConnected, "")
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	settleShellFrames(t, env, sc, router, size, &now)
	assertRailConsistent(t, sc, railChat, KindSession)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	gap, btnW := float32(gtx.Dp(theme.SpaceXS)), float32(gtx.Dp(theme.ControlSM))
	steps := []struct {
		pt       f32.Point
		wantRail railItem
		wantKind SlotKind
		targetY  float32
	}{
		{railCenter(env, sc, &gtx, 2), railModules, KindModule, gap + 2*(btnW+gap)},
		{railCenter(env, sc, &gtx, 1), railTeam, KindTeam, gap + btnW + gap},
		{railCenter(env, sc, &gtx, 0), railChat, KindSession, gap},
	}
	for _, st := range steps {
		startY := sc.grid.railY.Value()
		now = now.Add(16 * time.Millisecond)
		clickShellAt(t, env, sc, router, size, now, st.pt)
		assertRailConsistent(t, sc, st.wantRail, st.wantKind)
		if !wantsImmediateFrame(env) {
			t.Fatalf("after clicking %v, expected a new frame to be requested", st.wantRail)
		}
		advanceShellFrames(t, env, sc, router, size, &now, 5)
		curY := sc.grid.railY.Value()
		lo, hi := min(startY, st.targetY), max(startY, st.targetY)
		if curY <= lo || curY >= hi {
			t.Fatalf("expected rail indicator mid-slide strictly between %v and %v, got %v", lo, hi, curY)
		}
		settleShellFrames(t, env, sc, router, size, &now)
	}

	env.ReducedMotion = true
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, steps[0].pt)
	assertRailConsistent(t, sc, railModules, KindModule)
	if got := sc.grid.railY.Value(); got != steps[0].targetY {
		t.Fatalf("ReducedMotion railY = %v, want %v", got, steps[0].targetY)
	}
}

func drawnSidebarWidth(gtx *layout.Context, sc *screens, size image.Point) int {
	totalW := size.X
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	rightW := min(gtx.Dp(theme.RightStripeWidth), max(totalW-railW, 0))
	gutter := gtx.Dp(theme.IslandGap)
	maxSideW := min(max(totalW-railW-rightW-gutter, 0), gtx.Dp(theme.SidebarWidth))
	return scaledPx(maxSideW, sc.grid.sidebarOpenF.Value())
}

func drawnColumnHeight(gtx *layout.Context, sc *screens, size image.Point) int {
	return max(size.Y-sc.titleBar.height(gtx)-gtx.Dp(theme.StatusBarHeight), 0)
}

func assertSidebarSettled(t *testing.T, env *kit.Env, sc *screens, gtx *layout.Context, size image.Point, wantW int, wantCollapsed bool) {
	t.Helper()
	sideW := drawnSidebarWidth(gtx, sc, size)
	if sc.grid.doc.SidebarCollapsed != wantCollapsed || sideW != wantW || wantsImmediateFrame(env) {
		t.Fatalf("collapsed=%v (want %v) sideW=%d (want %d) immediate=%v",
			sc.grid.doc.SidebarCollapsed, wantCollapsed, sideW, wantW, wantsImmediateFrame(env))
	}
	totalW := size.X
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	rightW := min(gtx.Dp(theme.RightStripeWidth), max(totalW-railW, 0))
	wantGridW := max(totalW-(railW+sideW+scaledPx(gtx.Dp(theme.IslandGap), sc.grid.sidebarOpenF.Value()))-rightW, 0)
	if gotGridW := sc.grid.lastArea.Dx(); gotGridW != wantGridW {
		t.Fatalf("grid area width = %d, want %d (sideW=%d)", gotGridW, wantGridW, sideW)
	}
}

// TestFUG72SidebarCollapseExpandMotion verifies Smooth sidebar width motion at
// start, middle, and end, mid-flight reversal, and zero frames requested at rest.
func TestFUG72SidebarCollapseExpandMotion(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	fullW := gtx.Dp(theme.SidebarWidth)
	collapsePt, expandPt := sidebarTogglePts(env, sc, &gtx)

	settleShellFrames(t, env, sc, router, size, &now)
	assertSidebarSettled(t, env, sc, &gtx, size, fullW, false)

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, collapsePt)
	if !sc.grid.doc.SidebarCollapsed || !wantsImmediateFrame(env) {
		t.Fatal("collapse should flip SidebarCollapsed immediately and request a frame")
	}

	advanceShellFrames(t, env, sc, router, size, &now, 4)
	if midW := drawnSidebarWidth(&gtx, sc, size); midW <= 0 || midW >= fullW || !wantsImmediateFrame(env) {
		t.Fatalf("mid-collapse sideW = %d (want 0..%d), immediate = %v", midW, fullW, wantsImmediateFrame(env))
	}
	if sc.grid.sidebarFadeF.Value() >= sc.grid.sidebarOpenF.Value() {
		t.Fatalf("collapse fade %v should exit faster than width %v", sc.grid.sidebarFadeF.Value(), sc.grid.sidebarOpenF.Value())
	}

	// Reverse mid-flight and settle back to fullW.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, expandPt)
	settleShellFrames(t, env, sc, router, size, &now)
	assertSidebarSettled(t, env, sc, &gtx, size, fullW, false)

	// Collapse to end and verify 0 width and idle at rest.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, collapsePt)
	settleShellFrames(t, env, sc, router, size, &now)
	assertSidebarSettled(t, env, sc, &gtx, size, 0, true)

	checkSidebarExpandFadeLeadsWidth(t, env, sc, router, &gtx, size, &now, expandPt, fullW)
}

func checkSidebarExpandFadeLeadsWidth(t *testing.T, env *kit.Env, sc *screens, router *input.Router, gtx *layout.Context, size image.Point, now *time.Time, expandPt f32.Point, fullW int) {
	t.Helper()
	*now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, *now, expandPt)
	advanceShellFrames(t, env, sc, router, size, now, 4)
	if midW := drawnSidebarWidth(gtx, sc, size); midW <= 0 || midW >= fullW {
		t.Fatalf("mid-expand sideW = %d (want 0..%d)", midW, fullW)
	}
	if sc.grid.sidebarFadeF.Value() <= sc.grid.sidebarOpenF.Value() {
		t.Fatalf("expand fade %v should lead width %v", sc.grid.sidebarFadeF.Value(), sc.grid.sidebarOpenF.Value())
	}
	advanceShellFrames(t, env, sc, router, size, now, 1)
	if sc.grid.sidebarFadeF.Value() <= 0.8 || sc.grid.sidebarOpenF.Value() >= 0.7 {
		t.Fatalf("frame 6 expand fade=%v width=%v, want fade>0.8 while width<0.7",
			sc.grid.sidebarFadeF.Value(), sc.grid.sidebarOpenF.Value())
	}
	settleShellFrames(t, env, sc, router, size, now)
	assertSidebarSettled(t, env, sc, gtx, size, fullW, false)
}

// TestFUG72SidebarFooterColumnHeightSmooth verifies that with an unlocked vault
// (Lock button in the footer), the rail/sidebar column height never jumps in a
// single frame during collapse or expand.
func TestFUG72SidebarFooterColumnHeightSmooth(t *testing.T) {
	t.Parallel()
	vw := vault.NewWorker(func() vault.Client { return newFakeAppVaultClient() })
	vw.Start(t.Context())
	t.Cleanup(vw.Stop)

	sc := newFUG65TestScreens(t)
	sc.vaultWorker = vw
	sc.applyConnectionUpdate(hostconn.StateConnected, "")
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	collapsePt, expandPt := sidebarTogglePts(env, sc, &gtx)
	lockJumpPx := gtx.Dp(theme.ControlSM) + gtx.Dp(theme.SpaceSM) + gtx.Dp(theme.SpaceXS)

	settleShellFrames(t, env, sc, router, size, &now)
	prevH := drawnColumnHeight(&gtx, sc, size)

	checkSmooth := func(label string, pt f32.Point) {
		now = now.Add(16 * time.Millisecond)
		clickShellAt(t, env, sc, router, size, now, pt)
		for range 100 {
			curH := drawnColumnHeight(&gtx, sc, size)
			delta := absInt(curH - prevH)
			if delta > lockJumpPx/4 {
				t.Fatalf("%s: column height jumped by %d px in one frame (%d -> %d, lock row=%d)",
					label, delta, prevH, curH, lockJumpPx)
			}
			prevH = curH
			if !wantsImmediateFrame(env) {
				return
			}
			now = now.Add(16 * time.Millisecond)
			runShellEnvFrame(t, env, sc, router, size, now)
		}
		t.Fatalf("%s: did not settle within 100 frames", label)
	}

	wantH := size.Y - gtx.Dp(theme.TopBarHeight) - gtx.Dp(theme.StatusBarHeight)
	checkSmooth("collapse", collapsePt)
	if got := drawnColumnHeight(&gtx, sc, size); got != wantH {
		t.Fatalf("collapsed colH = %d, want %d", got, wantH)
	}
	checkSmooth("expand", expandPt)
	if got := drawnColumnHeight(&gtx, sc, size); got != wantH {
		t.Fatalf("expanded colH = %d, want %d", got, wantH)
	}
}

// TestFUG72SidebarReducedMotionInstant verifies ReducedMotion flips sidebar
// width in a single frame with no extra frames requested.
func TestFUG72SidebarReducedMotionInstant(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = true
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	fullW := gtx.Dp(theme.SidebarWidth)
	collapsePt, expandPt := sidebarTogglePts(env, sc, &gtx)

	runShellEnvFrame(t, env, sc, router, size, now)

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, collapsePt)
	if sideW := drawnSidebarWidth(&gtx, sc, size); sideW != 0 || sc.grid.sidebarFadeF.Value() != 0 || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion collapse sideW=%d fade=%v immediate=%v", sideW, sc.grid.sidebarFadeF.Value(), wantsImmediateFrame(env))
	}

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, expandPt)
	if sideW := drawnSidebarWidth(&gtx, sc, size); sideW != fullW || sc.grid.sidebarFadeF.Value() != 1 || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion expand sideW=%d fade=%v immediate=%v", sideW, sc.grid.sidebarFadeF.Value(), wantsImmediateFrame(env))
	}
}

// TestFUG72SidebarNoAnimOnTempFoldAndRestore verifies narrow-window fold
// (TempSidebarFolded) and layout restore snap without animating.
func TestFUG72SidebarNoAnimOnTempFoldAndRestore(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	wide, narrow := image.Pt(1200, 800), image.Pt(500, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	fullW := gtx.Dp(theme.SidebarWidth)

	settleShellFrames(t, env, sc, router, wide, &now)
	if sideW := drawnSidebarWidth(&gtx, sc, wide); sideW != fullW {
		t.Fatalf("wide sideW = %d, want %d", sideW, fullW)
	}

	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, narrow, now)
	if sideW := drawnSidebarWidth(&gtx, sc, narrow); sideW != 0 || wantsImmediateFrame(env) {
		t.Fatalf("TempSidebarFolded sideW=%d immediate=%v", sideW, wantsImmediateFrame(env))
	}

	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, wide, now)
	if sideW := drawnSidebarWidth(&gtx, sc, wide); sideW != fullW || wantsImmediateFrame(env) {
		t.Fatalf("unfold sideW=%d immediate=%v", sideW, wantsImmediateFrame(env))
	}

	restored := DefaultLayout()
	restored.SidebarCollapsed = true
	sc.grid.applyStored(DecodeLayoutResult{Doc: restored}, 2)
	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, wide, now)
	if sideW := drawnSidebarWidth(&gtx, sc, wide); sideW != 0 || wantsImmediateFrame(env) {
		t.Fatalf("restored collapsed sideW=%d immediate=%v", sideW, wantsImmediateFrame(env))
	}
}

// TestFUG72ClickingInactiveRailExpandsCollapsedSidebar verifies the FUG-93
// JetBrains rail toggle rule: clicking the active rail item collapses the
// sidebar, and clicking an inactive rail item switches sections and expands it.
func TestFUG72ClickingInactiveRailExpandsCollapsedSidebar(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = true
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	runShellEnvFrame(t, env, sc, router, size, now)

	// Clicking active Chat collapses the sidebar.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, railCenter(env, sc, &gtx, 0))
	if !sc.grid.doc.SidebarCollapsed {
		t.Fatal("clicking active rail item should collapse sidebar")
	}

	// Clicking inactive Team switches rail to Team AND re-expands sidebar.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, railCenter(env, sc, &gtx, 1))
	assertRailConsistent(t, sc, railTeam, KindTeam)
	if sc.grid.doc.SidebarCollapsed {
		t.Fatal("clicking inactive rail item while collapsed should expand sidebar")
	}
}
