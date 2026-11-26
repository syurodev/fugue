package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestSlotGridIslandGapsAndHideRestLine verifies FUG-92: computeRects leaves
// IslandGap (4 dp) between adjacent columns and stacked rows, and split pane
// overlays center on the gutter with HideRestLine enabled.
func TestSlotGridIslandGapsAndHideRestLine(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = gridFixture()

	var ops op.Ops
	area := image.Rect(0, 0, 1200, 800)
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(area.Size()),
	}
	gap := gtx.Dp(theme.IslandGap)
	if gap != 4 {
		t.Fatalf("IslandGap = %d px, want 4", gap)
	}

	geom := g.computeRects(&gtx, area, len(g.doc.Columns))
	c0r0 := g.rectOf(geom, Pos{Col: 0, Row: 0})
	c0r1 := g.rectOf(geom, Pos{Col: 0, Row: 1})
	c1r0 := g.rectOf(geom, Pos{Col: 1, Row: 0})
	c2r0 := g.rectOf(geom, Pos{Col: 2, Row: 0})

	if got := c1r0.Min.X - c0r0.Max.X; got != gap {
		t.Fatalf("col 0->1 gap = %d px, want %d", got, gap)
	}
	if got := c2r0.Min.X - c1r0.Max.X; got != gap {
		t.Fatalf("col 1->2 gap = %d px, want %d", got, gap)
	}
	if got := c0r1.Min.Y - c0r0.Max.Y; got != gap {
		t.Fatalf("row 0->1 gap = %d px, want %d", got, gap)
	}

	_, colProps := g.colSplitProps(&gtx, geom, area, len(g.doc.Columns), 0, false)
	if !colProps.HideRestLine {
		t.Fatal("colSplitProps.HideRestLine = false, want true")
	}
	subW := area.Max.X - c0r0.Min.X
	if gotPx := int(colProps.Ratio*float32(subW) + 0.5); gotPx != c0r0.Dx()+gap/2 {
		t.Fatalf("col split center = %d, want %d", gotPx, c0r0.Dx()+gap/2)
	}

	colRect, rowProps, ok := g.rowSplitProps(&gtx, geom, 0, false)
	if !ok || !rowProps.HideRestLine {
		t.Fatalf("rowSplitProps ok=%v HideRestLine=%v, want true, true", ok, rowProps.HideRestLine)
	}
	if gotPy := int(rowProps.Ratio*float32(colRect.Dy()) + 0.5); gotPy != c0r0.Dy()+gap/2 {
		t.Fatalf("row split center = %d, want %d", gotPy, c0r0.Dy()+gap/2)
	}
}

// TestResetShellMotionPreservesButtonHoverPressAndFocus verifies that
// resetShellMotion (called when applying stored layouts) preserves existing
// rail and Right Stripe button states so hover, active press, and keyboard
// focus survive layout reloads.
func TestResetShellMotionPreservesButtonHoverPressAndFocus(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	runShellEnvFrame(t, env, sc, router, size, now)

	railBtn := sc.grid.railBtn(railChat)
	stripeBtn := sc.grid.stripeBtn(KindInbox)
	stripePt := rightStripeCenter(env, sc, &gtx, size, 0)

	// Focus railBtn and press/hover stripeBtn before resetShellMotion runs.
	runWindowFrameWithFocus(env, sc, router, size, now, railBtn.Tag())

	now = now.Add(16 * time.Millisecond)
	router.Queue(
		pointer.Event{Kind: pointer.Move, Position: stripePt},
		pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: stripePt},
	)
	runShellEnvFrame(t, env, sc, router, size, now)
	if !stripeBtn.Hovered() {
		t.Fatal("expected stripeBtn to be hovered before applyStored")
	}

	// Applying a stored layout triggers resetShellMotion().
	sc.grid.applyStored(DecodeLayoutResult{Doc: DefaultLayout()}, 2)
	if sc.grid.railBtn(railChat) != railBtn || sc.grid.stripeBtn(KindInbox) != stripeBtn {
		t.Fatal("resetShellMotion replaced rail or stripe IconButtonState instances")
	}
	if !stripeBtn.Hovered() {
		t.Fatal("resetShellMotion cleared stripeBtn hover state")
	}

	// Releasing on stripeBtn must complete the click, and keyboard focus must stay on railBtn.
	now = now.Add(16 * time.Millisecond)
	router.Queue(pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonPrimary, Position: stripePt})
	checkGtx := runWindowFrameWithFocus(env, sc, router, size, now, nil)
	if !checkGtx.Focused(railBtn.Tag()) {
		t.Fatal("resetShellMotion cleared keyboard focus from railBtn")
	}
	if !sc.hasSlotKind(KindInbox) {
		t.Fatal("releasing pressed stripeBtn after resetShellMotion did not open KindInbox")
	}
}

func runWindowFrameWithFocus(env *kit.Env, sc *screens, router *input.Router, size image.Point, now time.Time, focusTag any) layout.Context {
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size), Source: router.Source(), Now: now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	if focusTag != nil {
		gtx.Execute(key.FocusCmd{Tag: focusTag})
	}
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
	return gtx
}

// TestResetShellMotionNarrowRestoreDoesNotFlashRail verifies that when
// applyStored runs while the window is narrow (TempSidebarFolded) and the
// overlay sidebar is closed, the active rail button's selF stays at 0 on the
// next frame with no fade animation or extra frame requested.
func TestResetShellMotionNarrowRestoreDoesNotFlashRail(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	narrow := image.Pt(500, 800)
	now := time.Unix(1_700_000_000, 0)

	settleShellFrames(t, env, sc, router, narrow, &now)
	railBtn := sc.grid.railBtn(railChat)
	if got := railBtn.ToggledProgress(); got != 0 {
		t.Fatalf("initial narrow selF = %v, want 0", got)
	}

	// Restore layout while still narrow and overlay closed.
	sc.grid.applyStored(DecodeLayoutResult{Doc: DefaultLayout()}, 2)
	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, narrow, now)
	if got := railBtn.ToggledProgress(); got != 0 {
		t.Fatalf("after narrow applyStored, active rail selF = %v, want 0", got)
	}
	if wantsImmediateFrame(env) {
		t.Fatal("narrow applyStored triggered an unexpected rail fade animation frame")
	}
}
