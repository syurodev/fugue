package appwin

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gioui.org/layout"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/vault"
)

var errLayoutConflictScripted = errors.New("the scripted layout conflict")

// conflictErr builds the coded Connect error a conflicting PutWindow carries:
// appstate.revision_conflict with the stored revision (host-api §5.2).
func conflictErr(current uint64) error {
	cerr := connect.NewError(connect.CodeFailedPrecondition, errLayoutConflictScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.AppStateConflictCode, CurrentRevision: current})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(detail)
	return cerr
}

// The window-level persistence evidence (DA-E10 one window): the stored
// document restores the shell across a restart while the vault is locked,
// a save goes out under CAS and a conflict retries against the stored
// revision, and a slot whose session is gone renders the missing-object
// state instead of vanishing.

// fakeLayoutClient records PutAppStateWindow calls and serves the stored
// document the topic snapshot would carry.
type fakeLayoutClient struct {
	mu       sync.Mutex
	store    map[string]json.RawMessage
	revs     map[string]uint64
	puts     int
	deletes  []string
	failOnce int // conflicts to answer before accepting
}

func newFakeLayoutClient() *fakeLayoutClient {
	return &fakeLayoutClient{store: map[string]json.RawMessage{}, revs: map[string]uint64{}}
}

func (f *fakeLayoutClient) PutAppStateWindow(_ context.Context, in *client.PutAppStateWindowInput) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.failOnce > 0 {
		f.failOnce--
		return 0, conflictErr(f.revs[in.WindowID])
	}
	if cur := f.revs[in.WindowID]; cur != in.ExpectedRevision {
		return 0, conflictErr(cur)
	}
	f.store[in.WindowID] = in.Layout
	f.revs[in.WindowID]++
	return f.revs[in.WindowID], nil
}

func (f *fakeLayoutClient) DeleteAppStateWindow(_ context.Context, _, windowID string, expectedRevision uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, windowID)
	if cur := f.revs[windowID]; cur == 0 || cur != expectedRevision {
		return conflictErr(cur)
	}
	delete(f.store, windowID)
	delete(f.revs, windowID)
	return nil
}

// seed stores one document directly, the way a previous run left it.
func (f *fakeLayoutClient) seed(doc LayoutDoc) uint64 {
	raw, err := EncodeLayout(doc)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[primaryWindowID] = raw
	f.revs[primaryWindowID] = 3
	return 3
}

// TestLayoutRestoresAcrossRestartWhileVaultLocked pins DA-E10: a fresh
// screens applies the stored document from the appstate snapshot — the grid,
// the sidebar flag and the main slot's session — with the vault locked, the
// way the app builds its windows before unlock (host-api §5.2).
func TestLayoutRestoresAcrossRestartWhileVaultLocked(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	// The vault is locked; appstate works regardless (host-api §5.2).
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "inc-lay"})
	stored := DefaultLayout()
	stored.SidebarCollapsed = true
	stored.Columns = append(stored.Columns,
		Column{WidthWeight: 1, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: KindInbox}}}})
	stored.Columns[0].Slots[0].Ref = SlotRef{Kind: KindSession, ID: "sess-restored"}
	fake := newFakeLayoutClient()
	rev := fake.seed(stored)

	snap, err := client.DecodeAppStateSnapshot(snapshotPayload(t, fake))
	if err != nil {
		t.Fatal(err)
	}
	sc.applyLayoutSnapshot(&snap)
	if !sc.grid.loaded || sc.grid.rev != rev {
		t.Fatalf("restore loaded=%v rev=%d, want loaded at revision %d", sc.grid.loaded, sc.grid.rev, rev)
	}
	if !sc.grid.doc.SidebarCollapsed {
		t.Fatal("the sidebar flag did not restore")
	}
	if got := sc.grid.mainRef(); got.Kind != KindSession || got.ID != "sess-restored" {
		t.Fatalf("main slot = %+v, want the stored session", got)
	}
	if got := len(sc.grid.doc.Columns); got != 2 {
		t.Fatalf("columns = %d, want the stored two", got)
	}
	if sc.grid.rail != railChat {
		t.Fatalf("rail = %v, want chat for a session main slot", sc.grid.rail)
	}
}

// TestLayoutSaveUsesCASAndRetriesTheConflict pins the write side: the edit
// saves under the stored revision, a conflict retries once against the
// stored revision, and the accepted revision clears the dirty mark.
func TestLayoutSaveUsesCASAndRetriesTheConflict(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	fake := newFakeLayoutClient()
	rev := fake.seed(DefaultLayout())
	w := newLayoutWorker(func() layoutClient { return fake }, nil)
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.layoutWorker = w

	// The shell learns the stored revision from the snapshot, then edits.
	snap, err := client.DecodeAppStateSnapshot(snapshotPayload(t, fake))
	if err != nil {
		t.Fatal(err)
	}
	sc.applyLayoutSnapshot(&snap)
	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	sc.grid.maybeSave(w)
	got := awaitLayoutResult(t, w)
	sc.applyLayoutResults([]layoutResult{got})
	if got.err != nil {
		t.Fatalf("save failed: %v", got.err)
	}
	if !sc.hasSlotKind(KindInbox) {
		t.Fatal("the edit did not stick")
	}

	// A writer between snapshot and save: the first put conflicts, the retry
	// against the stored revision accepts.
	fake.mu.Lock()
	fake.failOnce = 1
	fake.revs[primaryWindowID]++
	fake.mu.Unlock()
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	sc.grid.maybeSave(w)
	res := awaitLayoutResult(t, w)
	sc.applyLayoutResults([]layoutResult{res})
	if res.err != nil {
		t.Fatalf("the retried save failed: %v", res.err)
	}
	if fake.puts < 3 {
		t.Fatalf("puts = %d, want the conflict followed by a retry", fake.puts)
	}
	if sc.grid.dirty {
		t.Fatal("the accepted save left the document dirty")
	}
	_ = rev
}

func TestLayoutDeleteRetriesUnderCurrentRevision(t *testing.T) {
	t.Parallel()
	fake := newFakeLayoutClient()
	fake.seed(DefaultLayout())
	const secondaryID = "a99854df-6c7d-4597-aad4-a00e73f746fd"
	fake.revs[secondaryID] = 4
	req := layoutRequest{kind: opDeleteWindow, windowID: secondaryID, rev: 3}
	result := deleteLayout(t.Context(), fake, "layout-delete-command", &req)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if got := fake.deletes; len(got) != 2 || got[0] != secondaryID || got[1] != secondaryID {
		t.Fatalf("delete calls = %v, want two for the secondary window", got)
	}
	if fake.revs[secondaryID] != 0 || fake.revs[primaryWindowID] != 3 {
		t.Fatal("delete did not preserve the primary window while removing the secondary")
	}
}

func TestLayoutFlushKeepsEditMadeDuringPut(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	fake := newFakeLayoutClient()
	w := newLayoutWorker(func() layoutClient { return fake }, nil)
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.layoutWorker = w
	sc.applyWindowRecord(nil)

	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	sc.grid.maybeSave(w)
	sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
	if err := sc.finishWindowLayout(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	stored, err := DecodeLayout(fake.store[primaryWindowID])
	if err != nil {
		t.Fatal(err)
	}
	if sc.grid.rev != 2 || sc.grid.dirty || !hasKind(stored.Doc, KindAccounts) {
		t.Fatalf("revision=%d dirty=%v: close did not flush the edit made during the first put", sc.grid.rev, sc.grid.dirty)
	}
	if len(fake.deletes) != 0 {
		t.Fatalf("close deleted layouts %v", fake.deletes)
	}
}

func TestLayoutDebouncesChangesAndCopiesSplitWeights(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	fake := newFakeLayoutClient()
	w := newLayoutWorker(func() layoutClient { return fake }, nil)
	sc.applyWindowRecord(nil)
	g := sc.grid
	g.openCompanion(SlotRef{Kind: KindInbox})

	gtx := layout.Context{Now: time.Unix(0, 0)}
	g.saveLayoutFrame(&gtx, w)
	gtx.Now = gtx.Now.Add(layoutSaveDelay / 2)
	g.setColumnRatio(0, 2, 0.25)
	g.edited(g.doc)
	g.saveLayoutFrame(&gtx, w)
	gtx.Now = gtx.Now.Add(layoutSaveDelay / 2)
	g.saveLayoutFrame(&gtx, w)
	if len(w.reqs) != 0 {
		t.Fatal("layout save did not debounce the second edit")
	}
	gtx.Now = gtx.Now.Add(layoutSaveDelay / 2)
	g.saveLayoutFrame(&gtx, w)
	if len(w.reqs) != 1 {
		t.Fatal("layout save was not queued after the debounce")
	}
	req := <-w.reqs
	g.setColumnRatio(0, 2, 0.75)
	if ratio := columnRatio(req.doc, 0, 2); ratio != 0.25 {
		t.Fatalf("queued layout shares live weights: ratio=%f", ratio)
	}
}

// TestMissingSessionRendersEmptyStateNotVanish pins DA-E10's missing-object
// rule at the model boundary: the slot keeps its place and its ref; the
// body dispatch sees the missing target the EmptyState renders for.
func TestMissingSessionRendersEmptyStateNotVanish(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: testGoneSess, Vendor: client.VendorClaude, State: stateReady},
	}})
	sc.grid.openCompanion(SlotRef{Kind: KindSession, ID: testGoneSess})
	sc.grid.openCompanion(SlotRef{Kind: KindInbox})
	// The host reports the session gone.
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "sess-other", Vendor: client.VendorClaude, State: stateReady},
	}})
	sc.prune()
	if !sc.hasSlotKind(KindSession) {
		t.Fatal("the slot of a missing session vanished on its own")
	}
	if !sc.missingTarget(SlotRef{Kind: KindSession, ID: testGoneSess}) {
		t.Fatal("the missing session was not detected as a missing target")
	}
	if sc.missingTarget(SlotRef{Kind: KindInbox}) {
		t.Fatal("a window-level kind was treated as a missing object")
	}
	// An empty list means the list read has not landed: nothing is missing yet.
	sc.sidebar.ApplyList(client.SessionList{})
	if sc.missingTarget(SlotRef{Kind: KindSession, ID: testGoneSess}) {
		t.Fatal("an unloaded list was read as a missing object")
	}
}

// TestShortcutsFocusCloseAndFill pins the shell's keyboard rules (§5.3):
// Ctrl/Cmd+1 always names the main slot, Ctrl/Cmd+W closes the focused
// regular slot but not the main, and Ctrl/Cmd+Shift+Enter toggles the fill.
func TestShortcutsFocusCloseAndFill(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = kindsFixture(KindInbox, KindAccounts)
	g.setFocus(Pos{Col: 0, Row: 0})
	g.closeFocused()
	if len(g.doc.Columns[0].Slots) == 0 {
		t.Fatal("Ctrl/Cmd+W closed the main slot")
	}
	g.setFocus(Pos{Col: 1, Row: 0})
	g.closeFocused()
	if hasKind(g.doc, KindInbox) {
		t.Fatal("Ctrl/Cmd+W did not close the focused regular slot")
	}
	g.setFocus(Pos{Col: 1, Row: 0})
	g.toggleFillFocused()
	if g.doc.FilledSlot != fillKeyCol1Row0 {
		t.Fatalf("fill toggle set %q, want the focused cell", g.doc.FilledSlot)
	}
	g.toggleFillFocused()
	if g.doc.FilledSlot != "" {
		t.Fatalf("second fill toggle left %q, want the grid back", g.doc.FilledSlot)
	}
	// The focus order column-then-row with the main slot first (§5.3).
	g.doc = gridFixture()
	order := g.focusOrder()
	want := posList(Pos{}, Pos{Col: 0, Row: 1}, Pos{Col: 1, Row: 0}, Pos{Col: 2, Row: 0})
	if len(order) != len(want) {
		t.Fatalf("focus order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("focus order[%d] = %v, want %v", i, order[i], want[i])
		}
	}
}

func awaitLayoutResult(t *testing.T, w *layoutWorker) layoutResult {
	t.Helper()
	select {
	case res := <-w.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for the layout worker: %v", t.Context().Err())
		return layoutResult{}
	}
}

// testGoneSess is the session id the missing-object test deletes.
const testGoneSess = "sess-live"

// snapshotPayload marshals the fake's store into the snapshot payload the
// topic delivers, in the wire shape the decoder expects.
func snapshotPayload(t *testing.T, fake *fakeLayoutClient) []byte {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	type winWire struct {
		WindowID string          `json:"window_id"`
		Revision uint64          `json:"revision"`
		Layout   json.RawMessage `json:"layout"`
	}
	snap := struct {
		Windows []winWire `json:"windows"`
	}{}
	for id, layout := range fake.store {
		snap.Windows = append(snap.Windows, winWire{WindowID: id, Revision: fake.revs[id], Layout: layout})
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
