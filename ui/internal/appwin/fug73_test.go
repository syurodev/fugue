package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

func newFUG73ShotScreens(t *testing.T) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-fug73"})
	sc.accounts.ApplyListAccounts("inc-fug73", []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateNeedsLogin, QuotaKnown: true, QuotaGate: 0.85,
			Windows: []client.QuotaWindow{{Name: testWinFiveHour, Utilization: 0.42, Status: client.QuotaStatusAllowed}},
		},
		{Vendor: client.VendorCodex, State: client.AccountStateOK, QuotaKnown: false},
	})
	sess := client.Session{
		ID: shotSlotSessID, Vendor: client.VendorClaude, State: stateReady,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339),
	}
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{sess}})
	sc.workspaces.ApplyList([]workspace.Summary{{ID: "ws-fug73", Name: testRawWSName, ConfigRevision: 1}})
	sc.open = shotSlotSessID
	sc.grid.openMain(SlotRef{Kind: KindSession, ID: shotSlotSessID})
	tr := session.NewTranscript(shotSlotSessID)
	items, _ := transcriptShotFixtures("")
	tr.ApplyNewestPage(items)
	sc.transcripts[shotSlotSessID] = tr
	sc.viewFor(shotSlotSessID).BackToTop()
	return sc
}

func renderFUG73Frame(t *testing.T, win *headless.Window, env *kit.Env, sc *screens, router *input.Router, size image.Point, now time.Time) *image.RGBA {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size),
		Source:      router.Source(),
		Now:         now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
	if err := win.Frame(&ops); err != nil {
		t.Fatalf("render frame: %v", err)
	}
	router.Frame(&ops)
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	return img
}

type fug73RenderHarness struct {
	win    *headless.Window
	env    *kit.Env
	sc     *screens
	router *input.Router
	size   image.Point
	now    time.Time
}

func verifyMidOpenCycle(t *testing.T, h *fug73RenderHarness, imgClosed *image.RGBA, cycle int) {
	t.Helper()
	h.sc.toggleCompanion(KindAccounts)
	h.now = h.now.Add(16 * time.Millisecond)
	renderFUG73Frame(t, h.win, h.env, h.sc, h.router, h.size, h.now)
	accRef := SlotRef{Kind: KindAccounts}
	anim := h.sc.grid.animOf(accRef)
	if anim == nil || !anim.entering {
		t.Fatalf("cycle %d: expected enter animation on Accounts", cycle)
	}
	finalAcc := h.sc.grid.rects[Pos{Col: 1, Row: 0}.key()]
	if w := lerpRect(anim.from, finalAcc, anim.spring.Value).Dx(); w >= finalAcc.Dx() {
		t.Fatalf("cycle %d: first frame width %d >= final %d", cycle, w, finalAcc.Dx())
	}
	for range 3 {
		h.now = h.now.Add(16 * time.Millisecond)
		renderFUG73Frame(t, h.win, h.env, h.sc, h.router, h.size, h.now)
	}
	assertMidOpenFrame(t, h, imgClosed, finalAcc, cycle)
	settleShellFrames(t, h.env, h.sc, h.router, h.size, &h.now)
	if len(h.sc.grid.anims) != 0 {
		t.Fatalf("cycle %d: animations not cleared at rest", cycle)
	}
}

func assertMidOpenFrame(t *testing.T, h *fug73RenderHarness, imgClosed *image.RGBA, finalAcc image.Rectangle, cycle int) {
	t.Helper()
	anim := h.sc.grid.animOf(SlotRef{Kind: KindAccounts})
	if anim == nil {
		t.Fatalf("cycle %d: enter animation finished too early", cycle)
	}
	midRect := lerpRect(anim.from, finalAcc, anim.spring.Value)
	if midRect.Dx() <= 0 || midRect.Dx() >= finalAcc.Dx() {
		t.Fatalf("cycle %d: mid-open width %d not strictly in (0, %d)", cycle, midRect.Dx(), finalAcc.Dx())
	}
	if alpha := anim.presence.Progress(); alpha <= 0 || alpha >= 1 {
		t.Fatalf("cycle %d: mid-open opacity %v not strictly in (0, 1)", cycle, alpha)
	}
	mainAnim := h.sc.grid.animOf(h.sc.grid.mainRef())
	if mainAnim == nil {
		t.Fatalf("cycle %d: main slot did not slide on open", cycle)
	}
	finalMain := h.sc.grid.rects[Pos{Col: 0, Row: 0}.key()]
	if midMain := lerpRect(mainAnim.from, finalMain, mainAnim.spring.Value); midMain.Max.X != midRect.Min.X {
		t.Fatalf("cycle %d: main right edge %d != new slot left edge %d", cycle, midMain.Max.X, midRect.Min.X)
	}
	h.now = h.now.Add(16 * time.Millisecond)
	imgMidOpen := renderFUG73Frame(t, h.win, h.env, h.sc, h.router, h.size, h.now)
	if diff := countDiffPixels(imgMidOpen, imgClosed, midRect); diff == 0 {
		t.Fatalf("cycle %d: mid-open slot region drew nothing", cycle)
	}
}

func verifyMidCloseCycle(t *testing.T, h *fug73RenderHarness, imgClosed *image.RGBA, cycle int) {
	t.Helper()
	h.sc.toggleCompanion(KindAccounts)
	if len(h.sc.grid.closing) != 1 {
		t.Fatalf("cycle %d: expected 1 closing slot, got %d", cycle, len(h.sc.grid.closing))
	}
	var imgMidClose *image.RGBA
	for range 4 {
		h.now = h.now.Add(16 * time.Millisecond)
		imgMidClose = renderFUG73Frame(t, h.win, h.env, h.sc, h.router, h.size, h.now)
	}
	if len(h.sc.grid.closing) != 1 {
		t.Fatalf("cycle %d: closing slot vanished before frame 4", cycle)
	}
	cs := h.sc.grid.closing[0]
	if alpha := cs.presence.Progress(); alpha <= 0 || alpha >= 1 {
		t.Fatalf("cycle %d: mid-close opacity %v not strictly in (0, 1)", cycle, alpha)
	}
	bodyRect := image.Rect(cs.rect.Min.X+20, cs.rect.Min.Y+80, cs.rect.Max.X-20, cs.rect.Min.Y+240)
	if diff := countDiffPixels(imgMidClose, imgClosed, bodyRect); diff == 0 {
		t.Fatalf("cycle %d: closing slot body was covered or missing in %v", cycle, bodyRect)
	}
	settleShellFrames(t, h.env, h.sc, h.router, h.size, &h.now)
	if len(h.sc.grid.closing) != 0 || len(h.sc.grid.anims) != 0 {
		t.Fatalf("cycle %d: expected idle grid after close, got closing=%d anims=%d",
			cycle, len(h.sc.grid.closing), len(h.sc.grid.anims))
	}
}

// TestFUG73SlotOpenCloseTwiceAndMidFrameRendered verifies with a headless GL
// window and fake clock that opening a slot grows it from 0 width while fading
// in, closing a slot draws its header and body fading out above its sliding
// neighbours, and repeating open/close twice behaves identically.
func TestFUG73SlotOpenCloseTwiceAndMidFrameRendered(t *testing.T) {
	t.Parallel()
	size := image.Pt(1200, 800)
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	h := &fug73RenderHarness{
		win: win, env: newFUG65Env(t, true), sc: newFUG73ShotScreens(t),
		router: new(input.Router), size: size, now: time.Unix(1_700_000_000, 0),
	}
	h.env.ReducedMotion = false
	settleShellFrames(t, h.env, h.sc, h.router, size, &h.now)
	imgClosed := renderFUG73Frame(t, win, h.env, h.sc, h.router, size, h.now)

	for cycle := 1; cycle <= 2; cycle++ {
		verifyMidOpenCycle(t, h, imgClosed, cycle)
		verifyMidCloseCycle(t, h, imgClosed, cycle)
	}
}

// TestFUG73ClosingSlotPassesInputThrough verifies that a closing slot rendered
// on top of an underlying slot ignores input on its own controls and lets clicks
// reach the slot sliding underneath.
func TestFUG73ClosingSlotPassesInputThrough(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1500, 800)
	now := time.Unix(1_700_000_000, 0)

	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	settleShellFrames(t, env, sc, router, size, &now)

	inboxRect := sc.grid.rects[Pos{Col: 1, Row: 0}.key()]
	sc.grid.edited(CloseSlot(sc.grid.doc, Pos{Col: 1, Row: 0}))
	advanceShellFrames(t, env, sc, router, size, &now, 3)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	pad, btnSide := gtx.Dp(theme.SpaceXS), gtx.Dp(theme.ControlSM)
	gridOrigin := image.Pt(size.X-pad-sc.grid.lastArea.Dx(), pad)
	accDraw := sc.grid.drawnCellRect([]cellGeom{{pos: Pos{Col: 1, Row: 0}, Rect: sc.grid.rects[Pos{Col: 1, Row: 0}.key()]}}, Pos{Col: 1, Row: 0})
	closePt := f32.Pt(
		float32(gridOrigin.X+accDraw.Max.X-pad-btnSide/2),
		float32(gridOrigin.Y+accDraw.Min.Y+gtx.Dp(theme.SlotHeaderHeight)/2),
	)
	if !image.Pt(int(closePt.X)-gridOrigin.X, int(closePt.Y)-gridOrigin.Y).In(inboxRect) {
		t.Fatalf("Accounts close button %v not under closing Inbox rect %v", closePt, inboxRect)
	}
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, closePt)
	if hasKind(sc.grid.doc, KindAccounts) {
		t.Fatal("click through closing Inbox slot did not reach Accounts close button underneath")
	}
	assertClosingAccountsIgnoresClick(t, env, sc, router, size, &now, gridOrigin, pad, btnSide, gtx.Dp(theme.SlotHeaderHeight))
}

func assertClosingAccountsIgnoresClick(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now *time.Time, gridOrigin image.Point, pad, btnSide, headerH int) {
	t.Helper()
	var accClosing *closingSlot
	for _, cs := range sc.grid.closing {
		if cs.ref.Kind == KindAccounts {
			accClosing = cs
			break
		}
	}
	if accClosing == nil {
		t.Fatal("expected Accounts in closing slots")
	}
	refreshPt := f32.Pt(
		float32(gridOrigin.X+accClosing.rect.Max.X-pad-2*btnSide-btnSide/2),
		float32(gridOrigin.Y+accClosing.rect.Min.Y+headerH/2),
	)
	*now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, *now, refreshPt)
	if sc.accountsView.refreshing {
		t.Fatal("closing Accounts slot must not trigger refresh on click")
	}
	for _, btn := range sc.grid.actionBtns {
		if btn != nil && btn.Hovered() {
			t.Fatal("closing Accounts slot must not receive hover on ghost header icon")
		}
	}
}

func assertEnterMidSize(t *testing.T, sc *screens, p Pos, ref SlotRef, checkWidth bool) {
	t.Helper()
	anim := sc.grid.animOf(ref)
	if anim == nil || !anim.entering {
		t.Fatalf("slot %v at %v did not arm enter animation", ref.Kind, p)
	}
	finalRect := sc.grid.rects[p.key()]
	curRect := lerpRect(anim.from, finalRect, anim.spring.Value)
	if checkWidth && (curRect.Dx() <= 0 || curRect.Dx() >= finalRect.Dx()) {
		t.Fatalf("mid-width = %d, want in (0, %d)", curRect.Dx(), finalRect.Dx())
	}
	if !checkWidth && (curRect.Dy() <= 0 || curRect.Dy() >= finalRect.Dy()) {
		t.Fatalf("mid-height = %d, want in (0, %d)", curRect.Dy(), finalRect.Dy())
	}
	if alpha := anim.presence.Progress(); alpha <= 0 || alpha >= 1 {
		t.Fatalf("mid-opacity = %v, want in (0, 1)", alpha)
	}
}

// TestFUG73AllOpenPathsAnimate verifies enter motion across all openCompanion
// paths: new column (PlaceNearest), new row in existing column (PlaceNearest),
// kindPos remembered row, and confirms openMain/selectRail do not enter from 0.
func TestFUG73AllOpenPathsAnimate(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1400, 800)
	now := time.Unix(1_700_000_000, 0)
	settleShellFrames(t, env, sc, router, size, &now)

	// 1. PlaceNearest new column (1,0).
	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	advanceShellFrames(t, env, sc, router, size, &now, 4)
	assertEnterMidSize(t, sc, Pos{Col: 1, Row: 0}, SlotRef{Kind: KindInbox}, true)
	settleShellFrames(t, env, sc, router, size, &now)

	// 2. PlaceNearest new row (1,1).
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	advanceShellFrames(t, env, sc, router, size, &now, 4)
	assertEnterMidSize(t, sc, Pos{Col: 1, Row: 1}, SlotRef{Kind: KindAccounts}, false)
	settleShellFrames(t, env, sc, router, size, &now)

	// 3. Drag Accounts to (0,1), close it, then reopen via kindPos.
	sc.grid.drag = dragState{active: true, pos: Pos{Col: 1, Row: 1}}
	sc.grid.drop(Pos{Col: 1, Row: 1}, DropTarget{Kind: DropCellBottom, Pos: Pos{Col: 0, Row: 0}})
	settleShellFrames(t, env, sc, router, size, &now)
	sc.toggleCompanion(KindAccounts)
	settleShellFrames(t, env, sc, router, size, &now)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	if got := refAt(sc.grid.doc, Pos{Col: 0, Row: 1}); got.Kind != KindAccounts {
		t.Fatalf("expected kindPos reopen at (0,1), got %+v", got)
	}
	advanceShellFrames(t, env, sc, router, size, &now, 4)
	assertEnterMidSize(t, sc, Pos{Col: 0, Row: 1}, SlotRef{Kind: KindAccounts}, false)
	settleShellFrames(t, env, sc, router, size, &now)

	// 4. Switching main slot content must not enter from 0 or spawn a closing ghost.
	sc.grid.selectRail(railTeam)
	if len(sc.grid.closing) != 0 {
		t.Fatalf("selectRail spawned %d closing ghosts", len(sc.grid.closing))
	}
	if mainAnim := sc.grid.animOf(sc.grid.mainRef()); mainAnim != nil && mainAnim.entering {
		t.Fatal("selectRail must not animate main slot in from 0")
	}
}

// TestFUG73MidFlightReversalNoJump verifies that toggling close during open and
// toggling open during close continue from current rect and opacity without jumping.
func TestFUG73MidFlightReversalNoJump(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)
	settleShellFrames(t, env, sc, router, size, &now)

	verifyMidOpenToCloseReversal(t, env, sc, router, size, &now)
	verifyMidCloseToOpenReversal(t, env, sc, router, size, &now)
}

func verifyMidOpenToCloseReversal(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now *time.Time) {
	t.Helper()
	sc.toggleCompanion(KindAccounts)
	advanceShellFrames(t, env, sc, router, size, now, 4)
	accRef := SlotRef{Kind: KindAccounts}
	anim := sc.grid.animOf(accRef)
	if anim == nil {
		t.Fatal("missing enter anim")
	}
	midW := lerpRect(anim.from, sc.grid.rects[Pos{Col: 1, Row: 0}.key()], anim.spring.Value).Dx()
	midAlpha := anim.presence.Progress()

	sc.toggleCompanion(KindAccounts)
	if len(sc.grid.closing) != 1 {
		t.Fatalf("expected 1 closing slot on mid-open reversal, got %d", len(sc.grid.closing))
	}
	cs := sc.grid.closing[0]
	if cs.rect.Dx() != midW || cs.presence.Progress() != midAlpha {
		t.Fatalf("mid-open -> close jumped rect/opacity (%d,%v) -> (%d,%v)", midW, midAlpha, cs.rect.Dx(), cs.presence.Progress())
	}
	advanceShellFrames(t, env, sc, router, size, now, 4)
	if cs.presence.Progress() >= midAlpha {
		t.Fatalf("closing opacity did not turn around and decrease from %v (got %v)", midAlpha, cs.presence.Progress())
	}
	settleShellFrames(t, env, sc, router, size, now)
}

func verifyMidCloseToOpenReversal(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now *time.Time) {
	t.Helper()
	accRef := SlotRef{Kind: KindAccounts}
	sc.toggleCompanion(KindAccounts)
	settleShellFrames(t, env, sc, router, size, now)
	sc.toggleCompanion(KindAccounts)
	advanceShellFrames(t, env, sc, router, size, now, 4)
	if len(sc.grid.closing) != 1 {
		t.Fatal("expected closing slot before reversal")
	}
	closeAlpha := sc.grid.closing[0].presence.Progress()
	mainRef := sc.grid.mainRef()
	mainAnim := sc.grid.animOf(mainRef)
	if mainAnim == nil {
		t.Fatal("expected main slide anim before reversal")
	}
	mainMidW := lerpRect(mainAnim.from, sc.grid.rects[Pos{Col: 0, Row: 0}.key()], mainAnim.spring.Value).Dx()

	sc.toggleCompanion(KindAccounts)
	if len(sc.grid.closing) != 0 {
		t.Fatalf("mid-close -> open left %d closing slots", len(sc.grid.closing))
	}
	reAnim := sc.grid.animOf(accRef)
	if reAnim == nil || !reAnim.entering || reAnim.presence.Progress() != closeAlpha {
		t.Fatalf("mid-close -> open jumped or missing re-enter anim (alpha=%v, want=%v)", reAnim, closeAlpha)
	}
	if mainReAnim := sc.grid.animOf(mainRef); mainReAnim == nil || mainReAnim.from.Dx() != mainMidW {
		t.Fatalf("mid-close -> open jumped main width (want from %d, got %+v)", mainMidW, mainReAnim)
	}
	advanceShellFrames(t, env, sc, router, size, now, 8)
	if reAnim.presence.Progress() <= closeAlpha {
		t.Fatalf("re-enter opacity did not turn around and increase from %v (got %v)", closeAlpha, reAnim.presence.Progress())
	}
	settleShellFrames(t, env, sc, router, size, now)
}

// TestFUG73ReducedMotionInstantNoFrame verifies that under ReducedMotion,
// opening and closing a slot reach their final state on the very first frame
// and request zero animation frames.
func TestFUG73ReducedMotionInstantNoFrame(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = true
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	runShellEnvFrame(t, env, sc, router, size, now)
	if wantsImmediateFrame(env) {
		t.Fatal("idle frame under ReducedMotion requested a frame")
	}

	now = now.Add(16 * time.Millisecond)
	sc.toggleCompanion(KindAccounts)
	runShellEnvFrame(t, env, sc, router, size, now)
	if len(sc.grid.anims) != 0 || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion open: anims=%d immediate=%v", len(sc.grid.anims), wantsImmediateFrame(env))
	}
	if r := sc.grid.rects[Pos{Col: 1, Row: 0}.key()]; r.Empty() {
		t.Fatal("ReducedMotion open did not lay out new slot on first frame")
	}

	now = now.Add(16 * time.Millisecond)
	sc.toggleCompanion(KindAccounts)
	runShellEnvFrame(t, env, sc, router, size, now)
	if len(sc.grid.closing) != 0 || len(sc.grid.anims) != 0 || wantsImmediateFrame(env) {
		t.Fatalf("ReducedMotion close: closing=%d anims=%d immediate=%v",
			len(sc.grid.closing), len(sc.grid.anims), wantsImmediateFrame(env))
	}
}

func placeholderCloseBtnPt(t *testing.T, env *kit.Env, sc *screens, size image.Point, slotRect image.Rectangle, ref SlotRef, title, desc string) f32.Point {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	pad := gtx.Dp(theme.SpaceXS)
	headerH := min(gtx.Dp(theme.SlotHeaderHeight), slotRect.Dy())
	bodyW := slotRect.Dx()
	measure := layout.Context{
		Ops:         &ops,
		Metric:      gtx.Metric,
		Constraints: layout.Constraints{Max: image.Pt(bodyW, max(slotRect.Dy()-headerH, 0))},
	}
	tmp := kit.NewEmptyStateState()
	tmp.SnapShown()
	dims, _, err := tmp.Layout(env, &measure, &kit.EmptyStateProps{
		Icon: slotIcon(ref.Kind), Title: title, Description: desc, PrimaryAction: closeLabel,
	})
	if err != nil {
		t.Fatalf("measure EmptyState: %v", err)
	}
	gridOrigin := image.Pt(size.X-pad-sc.grid.lastArea.Dx(), pad)
	btnY := slotRect.Min.Y + headerH + dims.Size.Y - gtx.Dp(theme.SpaceXXL) - gtx.Dp(theme.ControlMD)/2
	return f32.Pt(float32(gridOrigin.X+slotRect.Min.X+bodyW/2), float32(gridOrigin.Y+btnY))
}

// TestFUG73TwoPlaceholdersCloseSeparately verifies that two simultaneously open
// placeholder slots keep separate EmptyState instances keyed by ref (kind:id),
// clicking Close on B closes only B, and B's state is pruned once settled.
func TestFUG73TwoPlaceholdersCloseSeparately(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1500, 800)
	now := time.Unix(1_700_000_000, 0)

	refA := SlotRef{Kind: KindModule, ID: "mod-a"}
	refB := SlotRef{Kind: KindModule, ID: "mod-b"}
	doc, _, _ := PlaceNearest(sc.grid.doc, refA)
	doc, _, _ = PlaceNearest(doc, refB)
	sc.grid.edited(doc)
	settleShellFrames(t, env, sc, router, size, &now)

	rectB := sc.grid.rects[Pos{Col: 1, Row: 1}.key()]
	ptB := placeholderCloseBtnPt(t, env, sc, size, rectB, refB, "Module: mod-b", "The module's panel opens here with its module item.")
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, ptB)

	if _, bStill := findCell(sc.grid.doc, refB); bStill {
		t.Fatal("clicking Close on placeholder B did not close B")
	}
	if _, aStill := findCell(sc.grid.doc, refA); !aStill {
		t.Fatal("clicking Close on placeholder B also closed A")
	}
	settleShellFrames(t, env, sc, router, size, &now)
	if sc.slotEmpty[keyOfRef(refB)] != nil {
		t.Fatal("closed placeholder B state was not pruned")
	}
	if sc.slotEmpty[keyOfRef(refA)] == nil {
		t.Fatal("live placeholder A state was unexpectedly pruned")
	}
}

// TestFUG73ClosingPlaceholderGhostPreservesIconAndDoesNotTriggerLiveSlot
// verifies that a closing placeholder ghost keeps its own icon (not the
// neighbour sliding into its old pos) and registers no shared Close button hit
// area that could close the live placeholder sliding underneath.
func TestFUG73ClosingPlaceholderGhostPreservesIconAndDoesNotTriggerLiveSlot(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1500, 800)
	now := time.Unix(1_700_000_000, 0)

	diffRef := SlotRef{Kind: KindDiff}
	modRef := SlotRef{Kind: KindModule, ID: "mod-live"}
	doc, _, _ := PlaceNearest(sc.grid.doc, diffRef)
	doc, _, _ = PlaceNearest(doc, modRef)
	sc.grid.edited(doc)
	settleShellFrames(t, env, sc, router, size, &now)

	oldDiffRect := sc.grid.rects[Pos{Col: 1, Row: 0}.key()]
	oldDiffClosePt := placeholderCloseBtnPt(t, env, sc, size, oldDiffRect, diffRef, "File viewer", "A file badge or an Edited row opens its view here.")

	sc.grid.edited(CloseSlot(sc.grid.doc, Pos{Col: 1, Row: 0}))
	advanceShellFrames(t, env, sc, router, size, &now, 2)
	if len(sc.grid.closing) != 1 {
		t.Fatalf("expected 1 closing ghost, got %d", len(sc.grid.closing))
	}
	cs := sc.grid.closing[0]
	if cs.ref != diffRef || slotIcon(cs.ref.Kind) != kit.IconFileText {
		t.Fatalf("ghost ref=%+v icon=%v, want KindDiff/IconFileText", cs.ref, slotIcon(cs.ref.Kind))
	}
	if cs.empty == sc.slotEmpty[keyOfRef(modRef)] {
		t.Fatal("closing ghost shared EmptyState with live placeholder")
	}

	// Click where the closing Diff ghost's Close button sat while live Module
	// slides upward underneath: live Module must remain open on this and the next frame.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, oldDiffClosePt)
	now = now.Add(16 * time.Millisecond)
	runShellEnvFrame(t, env, sc, router, size, now)
	if _, live := findCell(sc.grid.doc, modRef); !live {
		t.Fatal("clicking closing ghost area closed the live placeholder underneath")
	}
}

// TestFUG73SplitHandlePreservesFocusDuringAnimation verifies that SplitPane
// handles remain laid out during slot animations so keyboard focus is never
// dropped mid-transition.
func TestFUG73SplitHandlePreservesFocusDuringAnimation(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1500, 800)
	now := time.Unix(1_700_000_000, 0)

	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	settleShellFrames(t, env, sc, router, size, &now)

	splitTag := sc.grid.splitC[0].Tag()
	focusSplitFrame := func() bool {
		var ops op.Ops
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{
			Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
			Constraints: layout.Exact(size), Source: router.Source(), Now: now,
		}
		env.Begin(now)
		env.TrackInput(&gtx)
		drawWindow(&gtx, env, sc)
		focused := gtx.Focused(splitTag)
		env.FrameEnd(&gtx)
		router.Frame(&ops)
		return focused
	}

	// Grant focus to splitC[0] and verify it holds before animation.
	var ops op.Ops
	now = now.Add(16 * time.Millisecond)
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size), Source: router.Source(), Now: now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	gtx.Execute(key.FocusCmd{Tag: splitTag})
	env.FrameEnd(&gtx)
	router.Frame(&ops)
	if !focusSplitFrame() {
		t.Fatal("split handle did not receive initial focus")
	}

	// Open a 3rd column to animate while splitC[0] stays focused.
	sc.grid.openCompanion(SlotRef{Kind: KindSettings})
	if len(sc.grid.anims) == 0 {
		t.Fatal("expected slot animations to start")
	}
	for frame := 1; len(sc.grid.anims) > 0 && frame <= 100; frame++ {
		if !focusSplitFrame() {
			t.Fatalf("split handle lost focus on animation frame %d", frame)
		}
	}
	if len(sc.grid.anims) > 0 {
		t.Fatal("slot animation did not settle within 100 frames")
	}
}

// TestFUG73SlotEnterOpacityLeadsSize verifies FUG-74: on slot enter, Snappy
// opacity leads Smooth width growth so content is already >0.8 opaque around
// mid-open before the slot finishes expanding.
func TestFUG73SlotEnterOpacityLeadsSize(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	env.ReducedMotion = false
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)
	settleShellFrames(t, env, sc, router, size, &now)

	accRef := SlotRef{Kind: KindAccounts}
	sc.toggleCompanion(KindAccounts)
	advanceShellFrames(t, env, sc, router, size, &now, 5)
	anim := sc.grid.animOf(accRef)
	if anim == nil || !anim.entering {
		t.Fatal("missing enter anim on Accounts")
	}
	if size5, alpha5 := anim.spring.Value, anim.presence.Progress(); size5 < 0.45 || size5 > 0.60 || alpha5 <= size5+0.15 {
		t.Fatalf("at ~50%% size frame (5): size=%v alpha=%v, want size in [0.45,0.60] and alpha > size+0.15", size5, alpha5)
	}
	advanceShellFrames(t, env, sc, router, size, &now, 1)
	if size6, alpha6 := anim.spring.Value, anim.presence.Progress(); size6 < 0.55 || size6 >= 0.70 || alpha6 <= 0.8 {
		t.Fatalf("at mid-open frame (6): size=%v alpha=%v, want size in [0.55,0.70) and alpha > 0.8", size6, alpha6)
	}
	settleShellFrames(t, env, sc, router, size, &now)
}
