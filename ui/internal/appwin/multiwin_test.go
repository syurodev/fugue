package appwin

import (
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/pkg/client"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

const (
	testMultiSessionOne = "sess-1"
	testMultiSessionTwo = "sess-2"
	testMultiUpdatedTxt = "updated live"
)

func attachTestLayoutWorker(t *testing.T, sc *screens, fake layoutClient) {
	t.Helper()
	w := newLayoutWorker(func() layoutClient { return fake }, nil)
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.layoutWorker = w
}

func TestMultiWindowOpenSlotInNewWindowPromotesToMainAndCollapsesSidebar(t *testing.T) {
	t.Parallel()
	sc1, _ := newTestScreens(t)
	sc2, _ := newTestScreens(t)
	fake := newFakeLayoutClient()
	attachTestLayoutWorker(t, sc1, fake)
	attachTestLayoutWorker(t, sc2, fake)
	sc1.applyWindowRecord(nil)

	popped := SlotRef{Kind: KindSession, ID: testMultiSessionTwo}
	sc1.grid.openMain(SlotRef{Kind: KindSession, ID: testMultiSessionOne})
	sc1.grid.openCompanion(popped)
	sc1.grid.arrange(Pos{Col: 1, Row: 0}, arrangeNewWindow)

	coord := newMultiWindow(nil, nil)
	var secondaryID string
	if !sc1.openSlotWindow(func(doc LayoutDoc) error {
		secondaryID = coord.nextWindowID()
		installNewLayout(sc2, secondaryID, doc)
		return nil
	}) {
		t.Fatal("openSlotWindow did not open a secondary window")
	}

	flushTestWindowLayout(t, sc1, false)
	flushTestWindowLayout(t, sc2, false)
	assertOpenedSlotWindow(t, sc1, sc2, fake, secondaryID, popped)
}

func flushTestWindowLayout(t *testing.T, sc *screens, remove bool) {
	t.Helper()
	if err := sc.finishWindowLayout(t.Context(), remove); err != nil {
		t.Fatal(err)
	}
}

func assertOpenedSlotWindow(t *testing.T, sc1, sc2 *screens, fake *fakeLayoutClient, secondaryID string, popped SlotRef) {
	t.Helper()
	if len(regularCells(sc1.grid.doc)) != 0 {
		t.Fatalf("source window still has regular slots: %+v", sc1.grid.doc.Columns)
	}
	if secondaryID == "" || secondaryID == sc1.grid.windowID {
		t.Fatalf("secondary windowID = %q, want distinct from primary %q", secondaryID, sc1.grid.windowID)
	}
	if got := sc2.grid.mainRef(); got != popped || sc2.sidebarVisible() {
		t.Fatalf("secondary main=%+v sidebarVisible=%v, want %+v and collapsed", got, sc2.sidebarVisible(), popped)
	}
	stored1, err := DecodeLayout(fake.store[sc1.grid.windowID])
	if err != nil || len(regularCells(stored1.Doc)) != 0 {
		t.Fatalf("persisted source layout err=%v doc=%+v", err, stored1.Doc)
	}
	stored2, err := DecodeLayout(fake.store[secondaryID])
	if err != nil || stored2.Doc.Columns[0].Slots[0].Ref != popped || !stored2.Doc.SidebarCollapsed {
		t.Fatalf("persisted secondary layout err=%v doc=%+v", err, stored2.Doc)
	}
}

func TestMultiWindowRestoreAllWindowsAndCloseSemantics(t *testing.T) {
	t.Parallel()
	manager := hostconn.NewManager(&hostconn.Config{})
	fake := newFakeLayoutClient()
	doc1, doc2, snapBytes := seedMultiWindowRestore(t, fake)

	sc1, _ := newTestScreens(t)
	sc1.vault.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "locked"})
	attachTestLayoutWorker(t, sc1, fake)
	u1 := &windowUpdates{queue: &updateQueue{}}

	var sc2 *screens
	var u2 *windowUpdates
	var coord *multiWindow
	coord = newMultiWindow(manager, func(_ string, record *client.AppStateWindow, _ *LayoutDoc) {
		sc2, _ = newTestScreens(t)
		sc2.vault.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "locked"})
		attachTestLayoutWorker(t, sc2, fake)
		sc2.applyWindowRecord(record)
		u2 = &windowUpdates{queue: &updateQueue{}}
		coord.register(u2)
	})
	coord.register(u1)

	coord.deliver(hostconn.Update{
		Topic:    topicAppState,
		Snapshot: &client.Snapshot{Sequence: 1, State: snapBytes},
	})
	sc1.update(u1.queue.drain(), nil, nil, nil)

	assertRestoredWindows(t, sc1, sc2, doc1, doc2)
	assertMultiWindowCloseSemantics(t, coord, sc1, sc2, u1, u2, fake)
}

func seedMultiWindowRestore(t *testing.T, fake *fakeLayoutClient) (LayoutDoc, LayoutDoc, []byte) {
	t.Helper()
	doc1 := LayoutDoc{
		SchemaVersion: LayoutSchemaVersion,
		Columns: []Column{
			{WidthWeight: 0.65, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: KindSession, ID: testMultiSessionOne}}}},
			{WidthWeight: 0.35, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: KindSession, ID: "sess-missing"}}}},
		},
		Window: WindowGeometry{WidthDp: 1100, HeightDp: 760},
	}
	doc2 := LayoutDoc{
		SchemaVersion:    LayoutSchemaVersion,
		SidebarCollapsed: true,
		Columns: []Column{
			{WidthWeight: 0.55, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: KindSession, ID: testMultiSessionTwo}}}},
			{WidthWeight: 0.45, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: KindInbox}}}},
		},
		Window: WindowGeometry{WidthDp: 820, HeightDp: 640},
	}
	raw1, err := EncodeLayout(doc1)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := EncodeLayout(doc2)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.store["win-1"], fake.revs["win-1"] = raw1, 3
	fake.store["win-2"], fake.revs["win-2"] = raw2, 5
	fake.mu.Unlock()

	type wireWindow struct {
		WindowID string          `json:"window_id"`
		Revision uint64          `json:"revision"`
		Layout   json.RawMessage `json:"layout"`
	}
	snapBytes, err := json.Marshal(struct {
		Windows []wireWindow `json:"windows"`
	}{
		Windows: []wireWindow{
			{WindowID: "win-2", Revision: 5, Layout: raw2},
			{WindowID: "win-1", Revision: 3, Layout: raw1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc1, doc2, snapBytes
}

func assertRestoredWindows(t *testing.T, sc1, sc2 *screens, doc1, doc2 LayoutDoc) {
	t.Helper()
	if sc2 == nil {
		t.Fatal("coordinator did not spawn secondary window from appstate snapshot")
	}
	if sc1.grid.windowID != "win-1" || sc1.grid.rev != 3 {
		t.Fatalf("primary window restored (%q, rev=%d), want (win-1, 3)", sc1.grid.windowID, sc1.grid.rev)
	}
	if sc2.grid.windowID != "win-2" || sc2.grid.rev != 5 {
		t.Fatalf("secondary window restored (%q, rev=%d), want (win-2, 5)", sc2.grid.windowID, sc2.grid.rev)
	}
	if sc1.grid.doc.Columns[0].WidthWeight != 0.65 || sc2.grid.doc.Columns[0].WidthWeight != 0.55 {
		t.Fatal("restored windows did not preserve split ratios")
	}
	if sc1.grid.doc.Window != doc1.Window || sc2.grid.doc.Window != doc2.Window {
		t.Fatal("restored windows did not preserve window geometry")
	}
	if !sc1.vault.Locked() || !sc2.vault.Locked() {
		t.Fatal("windows did not restore while vault was locked")
	}
}

func assertMultiWindowCloseSemantics(t *testing.T, coord *multiWindow, sc1, sc2 *screens, u1, u2 *windowUpdates, fake *fakeLayoutClient) {
	t.Helper()
	sc1.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: testMultiSessionOne, State: stateRunning},
		{ID: testMultiSessionTwo, State: stateReady},
	}})
	sc1.sessionsLoaded = true
	if !sc1.missingTarget(SlotRef{Kind: KindSession, ID: "sess-missing"}) {
		t.Fatal("missing session slot did not report missingTarget for EmptyState")
	}

	if coord.beginClose(u2) {
		t.Fatal("secondary window close treated as last window")
	}
	if sc2.failure.closeDlg.dialog.IsOpen() {
		t.Fatal("closing secondary window opened running-turn close dialog")
	}
	flushTestWindowLayout(t, sc2, true)
	if !slices.Contains(fake.deletes, "win-2") {
		t.Fatalf("secondary close did not call DeleteAppStateWindow for win-2: %v", fake.deletes)
	}

	if !coord.beginClose(u1) {
		t.Fatal("last remaining window not treated as last")
	}
	if requestWindowClose(sc1, func() {}) || !sc1.failure.closeDlg.dialog.IsOpen() {
		t.Fatal("last window close did not intercept while turn is active")
	}
	sc1.failure.closeDlg.confirmed()
	if !requestWindowClose(sc1, func() {}) {
		t.Fatal("confirmed last window close did not proceed")
	}
	flushTestWindowLayout(t, sc1, false)
	if slices.Contains(fake.deletes, "win-1") {
		t.Fatalf("last window close deleted win-1 layout: %v", fake.deletes)
	}
	if _, ok := fake.store["win-1"]; !ok {
		t.Fatal("last window layout was not preserved for next launch")
	}
}

func TestMultiWindowSameSessionStaysSynchronized(t *testing.T) {
	t.Parallel()
	manager := hostconn.NewManager(&hostconn.Config{})
	coord := newMultiWindow(manager, nil)
	sc1, _ := newTestScreens(t)
	sc2, _ := newTestScreens(t)
	u1, u2 := &windowUpdates{queue: &updateQueue{}}, &windowUpdates{queue: &updateQueue{}}
	coord.register(u1)
	coord.register(u2)
	sc1.setTopic = func(topic string, active bool) { coord.setTopic(u1, topic, active) }
	sc2.setTopic = func(topic string, active bool) { coord.setTopic(u2, topic, active) }

	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	light1, light2 := theme.Light(sh), theme.Light(sh)
	if newEnv(sh, &light1) == newEnv(sh, &light2) {
		t.Fatal("windows share the same kit.Env pointer")
	}

	sc1.openSession(testMultiSessionOne)
	sc2.openSession(testMultiSessionOne)
	sc2.grid.openCompanion(SlotRef{Kind: KindInbox})

	page := session.Result{
		Kind: session.ResultPage, Session: testMultiSessionOne,
		Items: []client.TranscriptItem{
			{ItemID: "item-1", Kind: testMsgKindAssistant, Sequence: 1, Revision: 1, View: client.TranscriptView{Text: "initial"}},
		},
	}
	sc1.applyResult(page)
	sc2.applyResult(page)

	payload, err := proto.Marshal(&transcriptv1.TranscriptDelta{
		ItemId: "item-1", BaseRevision: 1, View: &transcriptv1.TranscriptView{Text: testMultiUpdatedTxt},
	})
	if err != nil {
		t.Fatal(err)
	}
	coord.deliver(hostconn.Update{Topic: topicTranscriptDlt + testMultiSessionOne, Event: &client.Event{Payload: payload}})
	sc1.update(u1.queue.drain(), nil, nil, nil)
	sc2.update(u2.queue.drain(), nil, nil, nil)

	items1, items2 := sc1.transcripts[testMultiSessionOne].Items(), sc2.transcripts[testMultiSessionOne].Items()
	if len(items1) != 1 || len(items2) != 1 || items1[0].Display().Text != testMultiUpdatedTxt || items2[0].Display().Text != testMultiUpdatedTxt {
		t.Fatalf("synced items = (%+v, %+v), want %q", items1, items2, testMultiUpdatedTxt)
	}
	if len(regularCells(sc1.grid.doc)) != 0 || len(regularCells(sc2.grid.doc)) != 1 {
		t.Fatal("windows did not maintain independent grid layout state")
	}
}
