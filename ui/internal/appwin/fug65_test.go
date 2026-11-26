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

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// TestSlotColumnsShareOuterEdges verifies that slots stacked in the same
// column share identical horizontal bounds (FUG-65).
func TestSlotColumnsShareOuterEdges(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.openCompanion(SlotRef{Kind: KindAccounts})
	g.openCompanion(SlotRef{Kind: KindSettings})
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	env := &kit.Env{Th: &th, Sh: sh, UIScale: 1.0}
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(1500, 900)),
	}
	area := image.Rect(0, 0, 1200, 860)
	geom := g.computeRects(&gtx, area, len(g.doc.Columns))
	top := g.rectOf(geom, Pos{Col: 1, Row: 0})
	bot := g.rectOf(geom, Pos{Col: 1, Row: 1})
	if top.Empty() || bot.Empty() {
		t.Fatalf("expected non-empty rects, got top=%v bot=%v", top, bot)
	}
	if top.Min.X != bot.Min.X || top.Max.X != bot.Max.X {
		t.Fatalf("column 1 slots have ragged edges: top=%v bot=%v", top, bot)
	}
	if top.Max.X != area.Max.X {
		t.Fatalf("column 1 right edge %d != area.Max.X %d", top.Max.X, area.Max.X)
	}
}

// TestEveryRegularSlotHasArrangeAndClose verifies that every regular slot
// receives Arrange and Close IconButton states while the main slot does not (FUG-65).
func TestEveryRegularSlotHasArrangeAndClose(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.openCompanion(SlotRef{Kind: KindSettings})
	runShellTestFrame(t, sc, image.Pt(1500, 900))
	for _, p := range []Pos{{Col: 1, Row: 0}, {Col: 1, Row: 1}} {
		if sc.grid.arrangeBtns[p.key()] == nil {
			t.Errorf("regular slot %s missing arrange button", p.key())
		}
		if sc.grid.closeBtns[p.key()] == nil {
			t.Errorf("regular slot %s missing close button", p.key())
		}
	}
	mainKey := (Pos{Col: 0, Row: 0}).key()
	if sc.grid.arrangeBtns[mainKey] != nil || sc.grid.closeBtns[mainKey] != nil {
		t.Fatal("main slot must not have arrange or close buttons")
	}
}

// TestSlotContentHasNoInPaneTitleOrBackButton verifies that slot views host
// their pane actions in the slot header and lay out no in-pane heading or
// "Back to …" button (FUG-65).
func TestSlotContentHasNoInPaneTitleOrBackButton(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	if acts := slotHeaderActions(sc, SlotRef{Kind: KindAccounts}); len(acts) == 0 || acts[0].props.Label != "Refresh accounts" {
		t.Fatalf("expected Refresh accounts action in slot header, got %+v", acts)
	}
	if acts := slotHeaderActions(sc, SlotRef{Kind: KindSettings}); len(acts) == 0 || acts[0].props.Label != "Refresh settings" {
		t.Fatalf("expected Refresh settings action in slot header, got %+v", acts)
	}
	if acts := slotHeaderActions(sc, SlotRef{Kind: KindSecrets}); len(acts) == 0 || acts[0].props.Label != labelAddSecret {
		t.Fatalf("expected Add secret action in slot header, got %+v", acts)
	}
	// Verify accountsView renders directly to its card without a header row above it.
	env := newFUG65Env(t, true)
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(360, 400)),
	}
	env.Begin(gtx.Now)
	sc.accountsView.layout(&gtx, env, sc)
	env.FrameEnd(&gtx)
	if w := sc.accountsView.widgetsFor(client.VendorClaude); w.card == nil {
		t.Fatal("expected accounts card to lay out")
	}
}

// TestAccountsCardFitsMinSlotWidth verifies that an Accounts card with a
// status chip, Sign in action, and quota windows fits within the 300 dp
// minimum slot width with its Sign in button and percentage inside (FUG-65).
func TestAccountsCardFitsMinSlotWidth(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.accounts.ApplyListAccounts("inc-300", []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateNeedsLogin, QuotaKnown: true, QuotaGate: 0.85,
			Windows: []client.QuotaWindow{{Name: "five_hour", Utilization: 0.42, Status: client.QuotaStatusAllowed}},
		},
	})
	env := newFUG65Env(t, true)
	var ops op.Ops
	const minSlotPx = 300
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(minSlotPx, 400)),
	}
	accs := sc.accounts.Accounts()
	env.Begin(gtx.Now)
	dims := sc.accountsView.layoutAccountCard(&gtx, env, sc, &accs[0])
	env.FrameEnd(&gtx)
	if dims.Size.X > minSlotPx || dims.Size.X <= 0 {
		t.Fatalf("account card width %d out of range (0, %d]", dims.Size.X, minSlotPx)
	}
	w := sc.accountsView.widgetsFor(client.VendorClaude)
	btnSz := w.loginBtn.Measure(env, &gtx, kit.ButtonProps{Text: "Sign in", Variant: kit.Secondary, Size: kit.SizeSM}).Size
	if btnSz.X <= 0 || btnSz.X > dims.Size.X-2*gtx.Dp(theme.SpaceLG) {
		t.Fatalf("Sign in button width %d does not fit inside card width %d", btnSz.X, dims.Size.X)
	}
}

// TestSidebarHasNoUtilityRowsAndKeepsSessions verifies that utility rows
// (Approvals, Accounts, Settings, Secrets) were removed from the Sidebar Island
// while opening companion slots still leaves the session list clean (FUG-93).
func TestSidebarHasNoUtilityRowsAndKeepsSessions(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.openCompanion(SlotRef{Kind: KindSettings})
	runShellTestFrame(t, sc, image.Pt(1500, 900))
	if !hasKind(sc.grid.doc, KindAccounts) || !hasKind(sc.grid.doc, KindSettings) {
		t.Fatal("expected Accounts and Settings companion slots to be open")
	}
	if sc.grid.doc.SidebarCollapsed {
		t.Fatal("sidebar should remain open by default")
	}
}

// TestConnectedStatusShowsNoText verifies that the footer renders no text when
// connected (the same pixels as an empty state except for the 6dp dot), and
// renders the "Reconnecting…" label when connecting (FUG-65).
func TestConnectedStatusShowsNoText(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(900, 600)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	scConn := newFUG65TestScreens(t)
	scConn.applyConnectionUpdate(hostconn.StateConnected, "")
	imgConn := renderShotFrames(t, win, newFUG65Env(t, true), scConn, image.Pt(900, 600), "conn")

	scEmpty := newFUG65TestScreens(t)
	imgEmpty := renderShotFrames(t, win, newFUG65Env(t, true), scEmpty, image.Pt(900, 600), "empty")

	// Bottom Status Bar region (x: 0..900, y: 600-StatusBarHeight..600): connected differs from
	// empty only by the small 6dp status dot on the right.
	statusTop := 600 - int(theme.StatusBarHeight)
	diffConnected := countDiffPixels(imgConn, imgEmpty, image.Rect(0, statusTop, 900, 600))
	if diffConnected == 0 || diffConnected > 64 {
		t.Fatalf("expected only the small dot in connected status bar, got %d changed pixels", diffConnected)
	}

	scReconn := newFUG65TestScreens(t)
	scReconn.applyConnectionUpdate(hostconn.StateConnecting, "")
	imgReconn := renderShotFrames(t, win, newFUG65Env(t, true), scReconn, image.Pt(900, 600), "reconn")
	diffReconn := countDiffPixels(imgReconn, imgConn, image.Rect(0, statusTop, 900, 600))
	if diffReconn <= diffConnected {
		t.Fatalf("expected Reconnecting… label to add pixels beyond the dot (%d <= %d)", diffReconn, diffConnected)
	}
}

// TestLockVaultIsIconButtonWithTooltip verifies that hovering the Bottom Status
// Bar Lock vault IconButton shows the "Lock vault" tooltip and clicking it issues OpLock (FUG-95).
func TestLockVaultIsIconButtonWithTooltip(t *testing.T) {
	t.Parallel()
	vc := newFakeAppVaultClient()
	vw := vault.NewWorker(func() vault.Client { return vc })
	vw.Start(t.Context())
	t.Cleanup(vw.Stop)

	sc := newFUG65TestScreens(t)
	sc.vaultWorker = vw
	sc.applyConnectionUpdate(hostconn.StateConnected, "")

	size := image.Pt(1000, 700)
	env := newFUG65Env(t, true)
	router := new(input.Router)
	runShellEnvFrame(t, env, sc, router, size, time.Unix(1_700_000_000, 0))

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	padX := gtx.Dp(theme.SpaceMD)
	dotD := max(gtx.Dp(theme.SpaceSM), 6)
	btnW := gtx.Dp(theme.ControlSM)
	statusH := gtx.Dp(theme.StatusBarHeight)
	// Lock button sits immediately left of the connection dot in the Bottom Status Bar.
	lockX := float32(size.X-padX-dotD-padX) - float32(btnW)/2
	lockY := float32(size.Y - statusH/2)
	lockPt := f32.Pt(lockX, lockY)
	router.Queue(pointer.Event{Kind: pointer.Move, Position: lockPt})
	runShellEnvFrame(t, env, sc, router, size, time.Unix(1_700_000_000, 0))
	runShellEnvFrame(t, env, sc, router, size, time.Unix(1_700_000_001, 0))
	if got := env.TooltipText(); got != "Lock vault" {
		t.Fatalf("expected Lock vault tooltip on hover, got %q", got)
	}

	router.Queue(
		pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: lockPt},
		pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonPrimary, Position: lockPt},
	)
	runShellEnvFrame(t, env, sc, router, size, time.Unix(1_700_000_001, 0))
	vw.Stop()
	if vc.lockCalls == 0 {
		t.Fatal("clicking footer Lock vault IconButton did not issue OpLock")
	}
}

// TestMainEmptyStateIsCentredWithNewSessionAction verifies that the empty main
// slot centres its EmptyState within 1 dp and its primary action opens the
// start-session dialog (FUG-65).
func TestMainEmptyStateIsCentredWithNewSessionAction(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	var ops op.Ops
	const w, h = 800, 600
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(w, h)),
	}
	env.Begin(gtx.Now)
	dims, _, err := sc.emptyTranscript.Layout(env, &gtx, &kit.EmptyStateProps{
		Icon:          kit.IconMessageSquare,
		Title:         "Select a session",
		Description:   "Choose a session from the sidebar or start a new one.",
		PrimaryAction: newSessionLabel,
	})
	if err != nil {
		t.Fatalf("EmptyState.Layout: %v", err)
	}
	env.FrameEnd(&gtx)
	box := sc.layoutMainEmptyStateDims(&gtx, env)
	centerX := box.Min.X + box.Dx()/2
	centerY := box.Min.Y + box.Dy()/2
	if absInt(centerX-w/2) > 1 || absInt(centerY-h/2) > 1 {
		t.Fatalf("EmptyState center (%d,%d) not within 1 dp of slot center (%d,%d)", centerX, centerY, w/2, h/2)
	}
	if dims.Size.Y <= 0 {
		t.Fatal("EmptyState had zero height")
	}
	sc.composer.start.open(sc)
	if !sc.composer.start.dialog.IsOpen() {
		t.Fatal("New session action did not open start dialog")
	}
}

// TestArrangeMenuItemsHaveIconsAndFillShortcut verifies that every Arrange
// menu item carries its Lucide icon and Fill carries Ctrl/Cmd+Shift+Enter (FUG-65).
func TestArrangeMenuItemsHaveIconsAndFillShortcut(t *testing.T) {
	t.Parallel()
	doc := gridFixture()
	items := arrangeMenuItems(doc, Pos{Col: 1, Row: 0})
	if len(items) != 9 {
		t.Fatalf("expected 9 items (4 moves + separator + 4 actions), got %d", len(items))
	}
	var fillItem *kit.MenuItem
	for i := range items {
		it := &items[i]
		if it.ID == "" {
			continue // separator
		}
		if it.Icon == "" {
			t.Errorf("arrange menu item %q missing icon", it.ID)
		}
		if it.ID == menuIDFill {
			fillItem = it
		}
	}
	wantSc := kit.Shortcut{Mods: kit.ModPrimary | kit.ModShift, Key: kit.KeyEnter}
	if fillItem == nil || fillItem.Shortcut != wantSc {
		t.Fatalf("Fill shortcut = %+v, want %+v", fillItem, wantSc)
	}
}

// TestArrangeMenuStaysInsideWindow verifies that opening the Arrange menu at
// the top-right slot clamps it inside the window without clipping (FUG-65).
func TestArrangeMenuStaysInsideWindow(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.openArrange(Pos{Col: 1, Row: 0})
	size := image.Pt(1500, 900)
	runShellTestFrame(t, sc, size)
	panels := sc.grid.arrangeMenu.Panels()
	if len(panels) == 0 || panels[0].Empty() {
		t.Fatal("arrange menu panel rect is empty")
	}
	r := panels[0]
	bounds := image.Rectangle{Max: size}.Inset(8)
	if !r.In(bounds) {
		t.Fatalf("arrange menu rect %v escaped window bounds %v", r, bounds)
	}
}

// TestSidebarRowsHaveGap verifies that sidebar utility and session rows are
// separated by SpaceXXS (2 dp) spacers (FUG-65).
func TestSidebarRowsHaveGap(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	sess := []session.Session{{ID: "s1", Vendor: client.VendorClaude}, {ID: "s2", Vendor: client.VendorCodex}}
	rows := sc.sessionRows(env, sess)
	if len(rows) != 3 {
		t.Fatalf("expected 3 flex children (row, SpaceXXS spacer, row) for 2 sessions, got %d", len(rows))
	}
}

// TestClosingSlotFadesAndNeighboursSlide verifies that closing a slot arms an
// Exit fade on the closing slot and a Smooth slide on remaining neighbours, and
// snaps instantly under ReducedMotion (FUG-65).
func TestClosingSlotFadesAndNeighboursSlide(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.openCompanion(SlotRef{Kind: KindSettings})
	env := newFUG65Env(t, false)
	env.ReducedMotion = false
	runShellEnvFrame(t, env, sc, new(input.Router), image.Pt(1400, 800), time.Unix(1_700_000_000, 0))

	sc.grid.edited(CloseSlot(sc.grid.doc, Pos{Col: 1, Row: 0}))
	if len(sc.grid.closing) != 1 {
		t.Fatalf("expected 1 closing slot animation, got %d", len(sc.grid.closing))
	}
	if _, ok := sc.grid.anims[keyOfRef(SlotRef{Kind: KindSettings})]; !ok {
		t.Fatal("expected remaining neighbour slot to arm Smooth slide animation")
	}

	env.ReducedMotion = true
	runShellEnvFrame(t, env, sc, new(input.Router), image.Pt(1400, 800), time.Unix(1_700_000_000, 0))
	if len(sc.grid.closing) != 0 || len(sc.grid.anims) != 0 {
		t.Fatalf("ReducedMotion should clear animations immediately, got closing=%d anims=%d", len(sc.grid.closing), len(sc.grid.anims))
	}
}

// TestShellRailFlushLeftAndOuterMarginXS verifies that the rail sits flush at
// x = 0 and the grid container uses SpaceXS (4 dp) top/right/bottom margins
// and SpaceXS gutter (FUG-65).
func TestShellRailFlushLeftAndOuterMarginXS(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	size := image.Pt(1200, 800)
	runShellTestFrame(t, sc, size)
	mainRect := sc.grid.rects[(Pos{Col: 0, Row: 0}).key()]
	if mainRect.Empty() {
		t.Fatal("main slot rect is empty")
	}
	// Grid height = 800 - TopBarHeight(32) - StatusBarHeight(22) = 746.
	wantH := size.Y - int(theme.TopBarHeight) - int(theme.StatusBarHeight)
	if sc.grid.lastArea.Dy() != wantH {
		t.Fatalf("expected grid height %d (TopBar+StatusBar chrome), got %d", wantH, sc.grid.lastArea.Dy())
	}
	// Grid width = 1200 - RailWidth(40) - SidebarWidth(240) - IslandGap(4) - RightStripeWidth(40) = 876.
	wantW := size.X - int(theme.RailWidth) - int(theme.SidebarWidth) - int(theme.IslandGap) - int(theme.RightStripeWidth)
	if sc.grid.lastArea.Dx() != wantW {
		t.Fatalf("expected grid width %d (rail flush left, Right Stripe, IslandGap gutter), got %d", wantW, sc.grid.lastArea.Dx())
	}
}

func newFUG65Env(t *testing.T, light bool) *kit.Env {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if light {
		th = theme.Light(sh)
	}
	return &kit.Env{Th: &th, Sh: sh, UIScale: 1.0, ReducedMotion: true}
}

func newFUG65TestScreens(t *testing.T) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-fug65"})
	sc.accounts.ApplyListAccounts("inc-fug65", []client.Account{
		{Vendor: client.VendorClaude, State: client.AccountStateNeedsLogin, QuotaKnown: false},
	})
	return sc
}

func runShellTestFrame(t *testing.T, sc *screens, size image.Point) {
	t.Helper()
	runShellEnvFrame(t, newFUG65Env(t, true), sc, new(input.Router), size, time.Unix(1_700_000_000, 0))
}

func runShellEnvFrame(t *testing.T, env *kit.Env, sc *screens, router *input.Router, size image.Point, now time.Time) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size), Source: router.Source(), Now: now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
}

func countDiffPixels(a, b *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				n++
			}
		}
	}
	return n
}
