package appwin

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func rightStripeCenter(env *kit.Env, sc *screens, gtx *layout.Context, size image.Point, index int) f32.Point {
	barH := sc.titleBar.layout(env, gtx, sc)
	failGtx := *gtx
	failGtx.Constraints.Min = image.Point{}
	failH := sc.failure.layout(env, &failGtx, sc).Size.Y
	rightW := gtx.Dp(theme.RightStripeWidth)
	btnW := gtx.Dp(theme.ControlMD)
	gap := gtx.Dp(theme.SpaceXS)
	x := float32(size.X) - float32(rightW)/2
	y := float32(barH+failH+gap+index*(btnW+gap)) + float32(btnW)/2
	return f32.Pt(x, y)
}

func rightStripeFoldedCenter(env *kit.Env, sc *screens, gtx *layout.Context, size image.Point, foldedCount, index int) f32.Point {
	rightW := gtx.Dp(theme.RightStripeWidth)
	btnW := gtx.Dp(theme.ControlMD)
	gap := gtx.Dp(theme.SpaceXS)
	barH := sc.titleBar.layout(env, gtx, sc)
	statusH := gtx.Dp(theme.StatusBarHeight)
	failGtx := *gtx
	failGtx.Constraints.Min = image.Point{}
	failH := sc.failure.layout(env, &failGtx, sc).Size.Y
	totalH := max(size.Y-barH-statusH-failH, 0)
	groupH := foldedCount*btnW + max(foldedCount-1, 0)*gap
	startY := totalH - gap - groupH
	x := float32(size.X) - float32(rightW)/2
	y := float32(barH+failH+startY+index*(btnW+gap)) + float32(btnW)/2
	return f32.Pt(x, y)
}

// TestRightStripeTogglesCompanionsAndSyncsShortcuts verifies FUG-94: clicking
// Approvals, Accounts, and Secrets icons in the 40 dp Right Stripe toggles the
// corresponding companion slots, and Ctrl/Cmd+Shift+I / A stay synced.
func TestRightStripeTogglesCompanionsAndSyncsShortcuts(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	runShellEnvFrame(t, env, sc, router, size, now)

	cases := []struct {
		idx  int
		kind SlotKind
	}{
		{0, KindInbox},
		{1, KindAccounts},
		{2, KindSecrets},
	}
	for _, tc := range cases {
		pt := rightStripeCenter(env, sc, &gtx, size, tc.idx)
		now = now.Add(16 * time.Millisecond)
		clickShellAt(t, env, sc, router, size, now, pt)
		if !sc.hasSlotKind(tc.kind) {
			t.Fatalf("clicking Right Stripe index %d did not open %v", tc.idx, tc.kind)
		}
		if r := sc.grid.refRects[keyOfRef(SlotRef{Kind: tc.kind})]; r.Empty() {
			t.Fatalf("clicking Right Stripe index %d did not lay out %v in the same frame", tc.idx, tc.kind)
		}
		if wantsImmediateFrame(env) {
			t.Fatalf("ReducedMotion Right Stripe open of %v requested extra frame", tc.kind)
		}
		now = now.Add(16 * time.Millisecond)
		clickShellAt(t, env, sc, router, size, now, pt)
		if sc.hasSlotKind(tc.kind) {
			t.Fatalf("clicking Right Stripe index %d a second time did not close %v", tc.idx, tc.kind)
		}
		if _, stillLaidOut := sc.grid.refRects[keyOfRef(SlotRef{Kind: tc.kind})]; stillLaidOut {
			t.Fatalf("clicking Right Stripe index %d a second time left %v laid out in the same frame", tc.idx, tc.kind)
		}
	}

	// Keyboard shortcut Ctrl/Cmd+Shift+I opens Inbox; another toggles it closed.
	router.Queue(key.Event{Name: key.Name("I"), Modifiers: key.ModShortcut | key.ModShift, State: key.Press})
	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, size, now)
	if !sc.hasSlotKind(KindInbox) {
		t.Fatal("Ctrl/Cmd+Shift+I did not open Approvals slot")
	}
	router.Queue(key.Event{Name: key.Name("A"), Modifiers: key.ModShortcut | key.ModShift, State: key.Press})
	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, size, now)
	if !sc.hasSlotKind(KindAccounts) || sc.hasSlotKind(KindInbox) {
		t.Fatalf("Ctrl/Cmd+Shift+A should switch to Accounts (inbox=%v accounts=%v)",
			sc.hasSlotKind(KindInbox), sc.hasSlotKind(KindAccounts))
	}
}

// TestRightStripeTooltipsAndAttentionStates verifies FUG-94: pending approvals,
// accounts attention alerts, and locked vault states surface clearly in tooltips.
func TestRightStripeTooltipsAndAttentionStates(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.inbox.ApplyListPending("inc-fug65", client.PendingInteractionList{
		Sequence: 1,
		Requests: []client.PendingInteraction{{SessionID: "sess-1", RequestID: "apr-1", Incarnation: "inc-fug65"}},
	})
	sc.secrets.ApplyList([]client.SecretMeta{{SecretID: "sec-1", ScopeKind: "global", Name: "KEY"}})

	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}

	runShellEnvFrame(t, env, sc, router, size, now)

	// Hover Approvals -> "Approvals (1 pending)"
	pt0 := rightStripeCenter(env, sc, &gtx, size, 0)
	router.Queue(pointer.Event{Kind: pointer.Move, Position: pt0})
	runShellEnvFrame(t, env, sc, router, size, now)
	runShellEnvFrame(t, env, sc, router, size, now.Add(time.Second))
	if got := env.TooltipText(); !strings.Contains(got, "1 pending") {
		t.Fatalf("Approvals tooltip = %q, want 1 pending", got)
	}

	// Hover Accounts -> "Accounts (1 alert)"
	pt1 := rightStripeCenter(env, sc, &gtx, size, 1)
	router.Queue(pointer.Event{Kind: pointer.Move, Position: pt1})
	runShellEnvFrame(t, env, sc, router, size, now.Add(2*time.Second))
	runShellEnvFrame(t, env, sc, router, size, now.Add(3*time.Second))
	if got := env.TooltipText(); !strings.Contains(got, "1 alert") {
		t.Fatalf("Accounts tooltip = %q, want 1 alert", got)
	}

	// Hover Secrets -> "Secrets (1)"
	pt2 := rightStripeCenter(env, sc, &gtx, size, 2)
	router.Queue(pointer.Event{Kind: pointer.Move, Position: pt2})
	runShellEnvFrame(t, env, sc, router, size, now.Add(4*time.Second))
	runShellEnvFrame(t, env, sc, router, size, now.Add(5*time.Second))
	if got := env.TooltipText(); !strings.Contains(got, "(1)") {
		t.Fatalf("Secrets tooltip = %q, want count (1)", got)
	}
}

// TestRightStripeFoldedSlotsToggleDrawer verifies FUG-94: when columns fold in a
// narrow window, folded slot tabs appear at the bottom of the Right Stripe and
// clicking one toggles the drawer overlay.
func TestRightStripeFoldedSlotsToggleDrawer(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})

	env := newFUG65Env(t, true)
	router := new(input.Router)
	narrow := image.Pt(700, 700)
	now := time.Unix(1_700_000_000, 0)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(narrow)}

	runShellEnvFrame(t, env, sc, router, narrow, now)
	plan := PlanFold(pxToDp(&gtx, narrow.X), sc.sidebarVisible(), len(sc.grid.doc.Columns))
	if plan.FoldedCols == 0 {
		t.Fatalf("expected columns to fold at %dpx, got %+v", narrow.X, plan)
	}
	folded := sc.grid.foldedCells(plan)
	pt := rightStripeFoldedCenter(env, sc, &gtx, narrow, len(folded), 0)
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, narrow, now, pt)
	if sc.grid.drawer != folded[0].key() || !sc.grid.drawerPresence.Shown() || sc.grid.drawerPresence.Progress() != 1 {
		t.Fatalf("clicking Right Stripe folded slot %q did not open drawer in same frame (drawer=%q shown=%v prog=%v)",
			folded[0].key(), sc.grid.drawer, sc.grid.drawerPresence.Shown(), sc.grid.drawerPresence.Progress())
	}
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, narrow, now, pt)
	if sc.grid.drawer != "" || sc.grid.drawerPresence.Shown() || sc.grid.drawerPresence.Progress() != 0 {
		t.Fatalf("clicking Right Stripe folded slot %q again did not close drawer in same frame (drawer=%q shown=%v prog=%v)",
			folded[0].key(), sc.grid.drawer, sc.grid.drawerPresence.Shown(), sc.grid.drawerPresence.Progress())
	}
}
