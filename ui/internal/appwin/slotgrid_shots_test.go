package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The slot grid's screenshots (F10.20b, desktop-app §5.3): the shell with
// its rail, sidebar, main slot and regular slots in light and dark, the
// arrange popover and a drag's drop zones, and the narrow-window fold into
// the edge tab strip. Rendered headless like every other shot; the
// regenerate-tagged companion writes the goldens.

// The shot scene names and the scene's session id.
const (
	shotSlotGrid   = "slotgrid"
	shotSlotGridA  = "slotgrid-arrange"
	shotSlotGridD  = "slotgrid-drag"
	shotSlotGridN  = "slotgrid-narrow"
	shotSlotGridNS = "slotgrid-narrow-sidebar"
	shotSlotSessID = "sess-shot-grid"
)

// slotGridShotSpecs lists the shell's screenshots.
func slotGridShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotSlotGrid + "-light-ui100", scene: shotSlotGrid, light: true, uiScale: 1.0},
		{name: shotSlotGrid + "-dark-ui100", scene: shotSlotGrid, light: false, uiScale: 1.0},
		{name: shotSlotGridA + "-light-ui100", scene: shotSlotGridA, light: true, uiScale: 1.0},
		{name: shotSlotGridD + "-dark-ui100", scene: shotSlotGridD, light: false, uiScale: 1.0},
		{name: shotSlotGridN + "-light-ui100", scene: shotSlotGridN, light: true, uiScale: 1.0},
		{name: shotSlotGridNS + "-light-ui100", scene: shotSlotGridNS, light: true, uiScale: 1.0},
		{name: shotSlotGridNS + "-dark-ui100", scene: shotSlotGridNS, light: false, uiScale: 1.0},
	}
}

// buildSlotGridShotScreens builds the shell's scene: the chat rail with a
// session in the main slot, an inbox slot beside it and an accounts slot
// under it.
func buildSlotGridShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-grid-shot"})
	sess := client.Session{
		ID: shotSlotSessID, Vendor: client.VendorClaude, State: stateReady,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339),
	}
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{sess}})
	sc.workspaces.ApplyList([]workspace.Summary{{ID: "ws-shot", Name: testRawWSName, ConfigRevision: 1}})
	sc.open = shotSlotSessID
	sc.grid.openMain(SlotRef{Kind: KindSession, ID: shotSlotSessID})
	tr := session.NewTranscript(shotSlotSessID)
	items, _ := transcriptShotFixtures("")
	tr.ApplyNewestPage(items)
	sc.transcripts[shotSlotSessID] = tr
	sc.viewFor(shotSlotSessID).BackToTop()

	// A second session in the list gives the companion slot its title.
	other := sess
	other.ID = shotSlotSessID + "-2"
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{sess, other}})

	switch scene {
	case shotSlotGrid:
		sc.grid.openCompanion(SlotRef{Kind: KindInbox})
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	case shotSlotGridA:
		// Three columns, the popover open from the middle column's slot, so
		// the panel sits inside the window (§5.3, §6.16).
		sc.grid.openCompanion(SlotRef{Kind: KindInbox})
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		sc.grid.openCompanion(SlotRef{Kind: KindSession, ID: other.ID})
		sc.grid.openArrange(Pos{Col: 1, Row: 0})
	case shotSlotGridD:
		// A drag of the third column's session slot, mid-flight over the
		// second column's lower cell: the valid zones wash and the ghost
		// follows the pointer (DA-E09).
		sc.grid.openCompanion(SlotRef{Kind: KindInbox})
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		sc.grid.openCompanion(SlotRef{Kind: KindSession, ID: other.ID})
		sc.grid.drag = dragState{
			active: true, pos: Pos{Col: 2, Row: 0},
			origin: image.Pt(1310, 60), pressAt: image.Pt(1320, 70), grab: image.Pt(10, 10),
			pointer: image.Pt(920, 620),
		}
	case shotSlotGridN:
		// Two regular columns on a narrow grid: both fold into the edge tab
		// strip, one drawer open (§5.3).
		sc.grid.openCompanion(SlotRef{Kind: KindInbox})
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		sc.grid.drawer = fillKeyCol1Row0
		sc.grid.drawerPresence.Snap(true)
	case shotSlotGridNS:
		// Narrow window temporarily folds the sidebar; clicking the active rail
		// icon opens the floating Sidebar Island overlay over the grid.
		sc.grid.tempSidebarPresence.Snap(true)
	}
	return sc
}

// renderSlotGridShot renders one shell scene at the spec's size; the narrow
// scene squeezes the window so the fold plan does its work.
func renderSlotGridShot(t *testing.T, _ *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if spec.light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: spec.uiScale, ReducedMotion: true}
	sc := buildSlotGridShotScreens(t, spec.scene)
	// The wide scenes show the multi-column grid, which needs room for the
	// rail, the sidebar, the main column and two regular columns at their
	// minimums; the narrow scene is under the two-column minimum, so the
	// regular columns fold right to left (§5.3, DA-E09). Each scene opens
	// its own window at the scene's size, the way the shell really runs.
	size := image.Pt(1800, 900)
	switch spec.scene {
	case shotSlotGridN:
		size = image.Pt(700, shotWindowH)
	case shotSlotGridNS:
		size = image.Pt(500, shotWindowH)
	}
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window for %s: %v", spec.name, err)
	}
	img := renderShotFrames(t, win, env, sc, size, spec.name)
	if sc.lastErr != "" {
		t.Fatalf("slot grid shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	return img
}

// TestSlotGridShellRendersAndShowsTheEvidence verifies the shell's shots:
// every scene renders, light and dark differ, and the narrow window folded
// both regular columns while the main slot stayed.
func TestSlotGridShellRendersAndShowsTheEvidence(t *testing.T) {
	t.Parallel()
	// The model behind the narrow scene: a 700 dp window cannot fit two regular columns at their 300 dp
	// minimums, and the main slot's column never folds (DA-E09).
	plan := PlanFold(700, true, 3)
	if plan.FoldedCols != 2 {
		t.Fatalf("narrow plan folded %d columns, want both regular ones", plan.FoldedCols)
	}
	if wide := PlanFold(1800, true, 3); wide.FoldedCols != 0 || wide.TempSidebarFolded {
		t.Fatalf("the wide scene folded: %+v", wide)
	}

	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range slotGridShotSpecs() {
		img := renderSlotGridShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("slot grid shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	if !imagesDifferRGBA(rendered[shotSlotGrid+"-light-ui100"], rendered[shotSlotGrid+"-dark-ui100"]) {
		t.Fatal("slot grid light and dark shots are identical")
	}
	if !imagesDifferRGBA(rendered[shotSlotGrid+"-light-ui100"], rendered[shotSlotGridA+"-light-ui100"]) {
		t.Fatal("the arrange scene drew nothing over the plain grid")
	}
	if !imagesDifferRGBA(rendered[shotSlotGrid+"-dark-ui100"], rendered[shotSlotGridD+"-dark-ui100"]) {
		t.Fatal("the drag scene drew no zones over the plain grid")
	}
	if !imagesDifferRGBA(rendered[shotSlotGridNS+"-light-ui100"], rendered[shotSlotGridNS+"-dark-ui100"]) {
		t.Fatal("narrow sidebar overlay light and dark shots are identical")
	}
}

// TestArrangePopoverCommandsTrackValidity drives the popover's command rows
// headless: the move rows toward the main slot are disabled, and choosing
// Make main keeps the main slot at (0,0) while swapping the contents.
func TestArrangePopoverCommandsTrackValidity(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = gridFixture()
	g.openArrange(Pos{Col: 1, Row: 0})
	items := arrangeMenuItems(g.doc, Pos{Col: 1, Row: 0})
	// Item 0 is move left (disabled toward main slot), item 1 is move right (enabled toward column 2).
	if !items[0].Disabled {
		t.Fatal("move left toward the main slot was not disabled")
	}
	if items[1].Disabled {
		t.Fatal("move right toward column 2 was disabled")
	}
	g.arrange(Pos{Col: 1, Row: 0}, arrangeMakeMain)
	if got := g.mainRef(); got.Kind != KindInbox {
		t.Fatalf("make main landed %+v, want the inbox in the main slot", got)
	}
	if got := refAt(g.doc, Pos{Col: 1, Row: 0}); got.Kind != KindSession {
		t.Fatalf("make main left %+v beside, want the session", got)
	}
}

// TestEscapeDrivesDragDrawerAndFill pins the Escape order over the shell's
// states: an active drag first, then the drawer, then the fill (§5.3, §7).
func TestEscapeDrivesDragDrawerAndFill(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = kindsFixture(KindInbox)
	g.lastArea = image.Rectangle{Max: image.Pt(800, 600)}
	// Fill, then drawer, then a drag: Escape unwinds them in order.
	g.setFocus(Pos{Col: 1, Row: 0})
	g.toggleFillFocused()
	if g.doc.FilledSlot == "" {
		t.Fatal("the fill did not arm")
	}
	g.drawer = "1,0"
	g.drawerPresence.Snap(true)
	g.drag = dragState{active: true, pos: Pos{Col: 1, Row: 0}}
	ref := refAt(g.doc, Pos{Col: 1, Row: 0})

	env := newInboxTestEnv(t)
	router := new(input.Router)
	pressEscape(env, g, router)
	if g.drag.active {
		t.Fatal("Escape did not cancel the active drag first (§7: the topmost layer owns Escape)")
	}
	if g.drawer == "" || g.doc.FilledSlot == "" {
		t.Fatalf("cancelling the drag also unwound drawer=%q fill=%q; Escape acts one layer at a time", g.drawer, g.doc.FilledSlot)
	}
	if _, still := g.anims[keyOfRef(ref)]; !still {
		t.Fatal("the drag cancel armed no slide-back")
	}
	g.drag = dragState{}
	pressEscape(env, g, router)
	if g.drawer != "" {
		t.Fatalf("Escape left the drawer %q open with no drag running", g.drawer)
	}
	pressEscape(env, g, router)
	if g.doc.FilledSlot != "" {
		t.Fatalf("Escape left the fill on %q", g.doc.FilledSlot)
	}
}

// pressEscape delivers one Escape press: a warm-up frame registers the
// shell's filter, the queued press lands in the next frame — Gio hands key
// events to the filters the previous frame's ops carried.
func pressEscape(env *kit.Env, g *slotGrid, router *input.Router) {
	runGridEscapeFrame(env, g, router)
	router.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	runGridEscapeFrame(env, g, router)
}

// runGridEscapeFrame runs one frame whose only grid input is the handler
// under test, the way the shell's frame drives it.
func runGridEscapeFrame(env *kit.Env, g *slotGrid, router *input.Router) {
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
	gtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(800, 600)), Source: router.Source(), Now: now,
	}
	env.Begin(now)
	env.TrackInput(&gtx)
	g.handleEscape(&gtx)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
}
