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
	barH := sc.titleBar.layout(env, gtx)
	pad := gtx.Dp(theme.SpaceXS)
	failGtx := *gtx
	failGtx.Constraints.Min = image.Point{}
	failH := sc.failure.layout(env, &failGtx, sc).Size.Y
	w := gtx.Dp(theme.RailWidth)
	btnW := gtx.Dp(theme.ControlLG)
	gap := gtx.Dp(theme.SpaceXS)
	return f32.Pt(float32(w)/2, float32(barH+pad+failH+gap+index*(btnW+gap))+float32(btnW)/2)
}

func sidebarTogglePts(gtx *layout.Context, size image.Point) (collapsePt, expandPt f32.Point) {
	railW := gtx.Dp(theme.RailWidth)
	footerH := gtx.Dp(theme.ControlLG)
	collapsePt = f32.Pt(float32(railW)/2, float32(size.Y)-float32(footerH)/2)
	expandPt = f32.Pt(float32(railW)/2, float32(size.Y-gtx.Dp(theme.SpaceXXS)-gtx.Dp(theme.ControlSM)/2))
	return collapsePt, expandPt
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
	gap, btnW := float32(gtx.Dp(theme.SpaceXS)), float32(gtx.Dp(theme.ControlLG))
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
		now = now.Add(16 * time.Millisecond)
		clickShellAt(t, env, sc, router, size, now, st.pt)
		assertRailConsistent(t, sc, st.wantRail, st.wantKind)
		if !wantsImmediateFrame(env) {
			t.Fatalf("after clicking %v, expected a new frame to be requested", st.wantRail)
		}
		advanceShellFrames(t, env, sc, router, size, &now, 6)
		if curY := sc.grid.railY.Value(); curY == st.targetY {
			t.Fatalf("expected rail indicator mid-slide toward %v, already at %v", st.targetY, curY)
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
	pad := gtx.Dp(theme.SpaceXS)
	totalW := max(size.X-pad, 0)
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	gutter := gtx.Dp(theme.SpaceXS)
	maxSideW := min(max(totalW-railW-gutter, 0), gtx.Dp(theme.SidebarWidth))
	return scaledPx(maxSideW, sc.grid.sidebarOpenF.Value())
}

func drawnColumnHeight(gtx *layout.Context, sc *screens, size image.Point) int {
	pad := gtx.Dp(theme.SpaceXS)
	totalH := max(size.Y-2*pad, 0)
	showLock := !sc.vault.Locked() && sc.vaultWorker != nil
	return max(totalH-shellFooterHeight(gtx, showLock, sc.grid.sidebarOpenF.Value()), 0)
}

func assertSidebarSettled(t *testing.T, env *kit.Env, sc *screens, gtx *layout.Context, size image.Point, wantW int, wantCollapsed bool) {
	t.Helper()
	sideW := drawnSidebarWidth(gtx, sc, size)
	if sc.grid.doc.SidebarCollapsed != wantCollapsed || sideW != wantW || wantsImmediateFrame(env) {
		t.Fatalf("collapsed=%v (want %v) sideW=%d (want %d) immediate=%v",
			sc.grid.doc.SidebarCollapsed, wantCollapsed, sideW, wantW, wantsImmediateFrame(env))
	}
	pad := gtx.Dp(theme.SpaceXS)
	totalW := max(size.X-pad, 0)
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	wantGridW := max(totalW-(railW+sideW+scaledPx(gtx.Dp(theme.SpaceXS), sc.grid.sidebarOpenF.Value())), 0)
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
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	fullW := gtx.Dp(theme.SidebarWidth)
	collapsePt, expandPt := sidebarTogglePts(&gtx, size)

	settleShellFrames(t, env, sc, router, size, &now)
	assertSidebarSettled(t, env, sc, &gtx, size, fullW, false)

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, collapsePt)
	if !sc.grid.doc.SidebarCollapsed || !wantsImmediateFrame(env) {
		t.Fatal("collapse should flip SidebarCollapsed immediately and request a frame")
	}

	advanceShellFrames(t, env, sc, router, size, &now, 10)
	if midW := drawnSidebarWidth(&gtx, sc, size); midW <= 0 || midW >= fullW || !wantsImmediateFrame(env) {
		t.Fatalf("mid-collapse sideW = %d (want 0..%d), immediate = %v", midW, fullW, wantsImmediateFrame(env))
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
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	collapsePt, expandPt := sidebarTogglePts(&gtx, size)
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

	checkSmooth("collapse", collapsePt)
	if got, want := drawnColumnHeight(&gtx, sc, size), size.Y-2*gtx.Dp(theme.SpaceXS)-gtx.Dp(theme.ControlLG)-lockJumpPx; got != want {
		t.Fatalf("collapsed colH = %d, want %d", got, want)
	}
	checkSmooth("expand", expandPt)
	if got, want := drawnColumnHeight(&gtx, sc, size), size.Y-2*gtx.Dp(theme.SpaceXS)-gtx.Dp(theme.ControlLG); got != want {
		t.Fatalf("expanded colH = %d, want %d", got, want)
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
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	fullW := gtx.Dp(theme.SidebarWidth)
	collapsePt, expandPt := sidebarTogglePts(&gtx, size)

	runShellEnvFrame(t, env, sc, router, size, now)

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, collapsePt)
	if sideW := drawnSidebarWidth(&gtx, sc, size); sideW != 0 || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion collapse sideW=%d immediate=%v", sideW, wantsImmediateFrame(env))
	}

	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, expandPt)
	if sideW := drawnSidebarWidth(&gtx, sc, size); sideW != fullW || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion expand sideW=%d immediate=%v", sideW, wantsImmediateFrame(env))
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
