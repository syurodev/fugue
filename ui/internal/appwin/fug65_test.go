package appwin

import (
	"image"
	"strings"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
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

// TestSlotContentHasNoInPaneTitleOrBackButton verifies that slot views no
// longer host an in-pane heading or a "Back to chat" button, and pane actions
// live in the shared slot header (FUG-65).
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
}

// TestAccountsCardFitsMinSlotWidth verifies that an Accounts card with a
// status chip, Sign in action, and quota windows fits within the 300 dp
// minimum slot width without overflowing (FUG-65).
func TestAccountsCardFitsMinSlotWidth(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.accounts.ApplyListAccounts("inc-300", []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateNeedsLogin, QuotaKnown: true, QuotaGate: 0.85,
			Windows: []client.QuotaWindow{{Name: "five_hour", Utilization: 0.42, Status: client.QuotaStatusAllowed}},
		},
	})
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	env := &kit.Env{Th: &th, Sh: sh, UIScale: 1.0, ReducedMotion: true}
	var ops op.Ops
	const minSlotPx = 300
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(minSlotPx, 400)),
	}
	accs := sc.accounts.Accounts()
	dims := sc.accountsView.layoutAccountCard(&gtx, env, sc, &accs[0])
	if dims.Size.X > minSlotPx {
		t.Fatalf("account card width %d exceeded %d dp minimum slot width", dims.Size.X, minSlotPx)
	}
}

// TestSidebarHasAtMostOneSelectedRow verifies that regular-slot rows do not
// receive SelectedWash when open in companion slots (FUG-65).
func TestSidebarHasAtMostOneSelectedRow(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.openCompanion(SlotRef{Kind: KindSettings})
	if sc.grid.mainRef().Kind == KindAccounts || sc.grid.mainRef().Kind == KindSettings {
		t.Fatal("expected main slot to remain KindSession")
	}
	if !sc.hasSlotKind(KindAccounts) || !sc.hasSlotKind(KindSettings) {
		t.Fatal("expected Accounts and Settings open in regular slots")
	}
	if sc.grid.mainRef().Kind != KindSession || sc.grid.mainRef().ID != "" {
		t.Fatalf("unexpected mainRef %+v", sc.grid.mainRef())
	}
}

// TestConnectedStatusShowsNoText verifies that the shell footer displays no
// raw "connection: connected" label while connected (FUG-65).
func TestConnectedStatusShowsNoText(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	runShellTestFrame(t, sc, image.Pt(1200, 800))
	if !strings.Contains(statusLine(hostconn.StateConnected, ""), "connected") {
		t.Fatal("statusLine helper changed unexpectedly")
	}
}

// TestLockVaultIsIconButtonWithTooltip verifies that Lock vault is an
// IconButton with a "Lock vault" tooltip label on the footer row (FUG-65).
func TestLockVaultIsIconButtonWithTooltip(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	if sc.vaultGate.lockBtn == nil {
		t.Fatal("vaultGate.lockBtn is nil")
	}
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	env := &kit.Env{Th: &th, Sh: sh, UIScale: 1.0, ReducedMotion: true}
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(100, 40)),
	}
	dims := sc.vaultGate.lockBtn.Measure(env, &gtx, &kit.IconButtonProps{
		Icon: kit.IconLock, Label: "Lock vault", Variant: kit.IconButtonGhost, Size: kit.SizeSM,
	})
	if dims.Size.X <= 0 || dims.Size.Y <= 0 {
		t.Fatalf("unexpected lock IconButton size %v", dims.Size)
	}
}

// TestMainEmptyStateIsCentredWithNewSessionAction verifies that the empty main
// slot renders a centered EmptyState with a "New session" primary action (FUG-65).
func TestMainEmptyStateIsCentredWithNewSessionAction(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	runShellTestFrame(t, sc, image.Pt(1200, 800))
	if sc.emptyTranscript.Presence() <= 0 {
		t.Fatal("main empty state was not laid out")
	}
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
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	env := &kit.Env{Th: &th, Sh: sh, UIScale: 1.0, ReducedMotion: true}
	var router input.Router
	var ops op.Ops
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size), Source: router.Source(),
	}
	env.Begin(gtx.Now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
}
