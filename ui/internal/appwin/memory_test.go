package appwin

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type fakeAppMemoryClient struct {
	mu sync.Mutex

	entries     []client.MemoryEntry
	searchResp  client.MemorySearchResponse
	history     []client.MemoryHistoryEntry
	previewResp client.MemoryImportPreview
	applyResp   client.MemoryImportResult

	writeErr   error
	verifyErr  error
	restoreErr error
	applyErr   error

	listReqs    []client.ListMemoryInput
	searchReqs  []client.SearchMemoryInput
	writeReqs   []client.WriteMemoryInput
	previewReqs [][]byte
	applyReqs   []client.ApplyMemoryImportInput
}

func (f *fakeAppMemoryClient) ListMemoryEntries(_ context.Context, in client.ListMemoryInput) ([]client.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReqs = append(f.listReqs, in)
	return append([]client.MemoryEntry(nil), f.entries...), nil
}

func (f *fakeAppMemoryClient) SearchMemoryEntries(_ context.Context, in client.SearchMemoryInput) (client.MemorySearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchReqs = append(f.searchReqs, in)
	return f.searchResp, nil
}

func (f *fakeAppMemoryClient) GetMemoryEntry(_ context.Context, _, id, _ string) (client.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return client.MemoryEntry{}, nil
}

func (f *fakeAppMemoryClient) GetMemoryEntryHistory(context.Context, string, string) ([]client.MemoryHistoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]client.MemoryHistoryEntry(nil), f.history...), nil
}

func (f *fakeAppMemoryClient) WriteMemoryEntry(_ context.Context, in *client.WriteMemoryInput) (client.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeReqs = append(f.writeReqs, *in)
	if f.writeErr != nil {
		return client.MemoryEntry{}, f.writeErr
	}
	return client.MemoryEntry{
		ID:          in.ID,
		WorkspaceID: in.WorkspaceID,
		Kind:        in.Kind,
		Label:       in.Label,
		Summary:     in.Summary,
		Body:        in.Body,
		Keywords:    append([]string(nil), in.Keywords...),
		Author:      client.MemoryAuthorUser,
		Verified:    true,
		Validity:    client.MemoryValidityValid,
		Revision:    in.ExpectedRevision + 1,
		UpdatedAt:   time.Date(2026, 11, 28, 13, 0, 0, 0, time.UTC),
		Freshness:   client.MemoryFreshnessFresh,
	}, nil
}

func (f *fakeAppMemoryClient) VerifyMemoryEntry(_ context.Context, wsID, id string, rev uint64) (client.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return client.MemoryEntry{}, f.verifyErr
	}
	return client.MemoryEntry{ID: id, WorkspaceID: wsID, Verified: true, Revision: rev + 1}, nil
}

func (f *fakeAppMemoryClient) RestoreMemoryEntry(_ context.Context, wsID, id string, _, rev uint64) (client.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restoreErr != nil {
		return client.MemoryEntry{}, f.restoreErr
	}
	return client.MemoryEntry{ID: id, WorkspaceID: wsID, Verified: true, Revision: rev + 1}, nil
}

func (f *fakeAppMemoryClient) ExportMemory(context.Context, string) ([]byte, error) {
	return []byte("---\nformat: fugue-memory/1\n---\n"), nil
}

func (f *fakeAppMemoryClient) PreviewMemoryImport(_ context.Context, _ string, content []byte) (client.MemoryImportPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.previewReqs = append(f.previewReqs, append([]byte(nil), content...))
	return f.previewResp, nil
}

func (f *fakeAppMemoryClient) ApplyMemoryImport(_ context.Context, in *client.ApplyMemoryImportInput) (client.MemoryImportResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyReqs = append(f.applyReqs, *in)
	if f.applyErr != nil {
		return client.MemoryImportResult{}, f.applyErr
	}
	return f.applyResp, nil
}

func buildTestMemoryScreens(t *testing.T, mc memory.Client) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	if mc != nil {
		mw := memory.NewWorker(func() memory.Client { return mc }, nil)
		mw.Start(t.Context())
		t.Cleanup(mw.Stop)
		sc.memoryWorker = mw
	}
	wsSnap, err := json.Marshal([]workspace.Summary{
		{ID: testSettingsWSID, Name: "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyWorkspaceDelivery(hostconn.Update{
		Topic:    topicWorkspaces,
		Snapshot: &client.Snapshot{State: wsSnap},
	})
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-mem"}))
	return sc
}

func awaitMemoryResult(t *testing.T, sc *screens) memory.Result {
	t.Helper()
	select {
	case res := <-sc.memoryWorker.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("waiting for memory worker: %v", t.Context().Err())
		return memory.Result{}
	}
}

func TestMemoryEditAndSaveCallsWriteAndReturnsVerified(t *testing.T) {
	t.Parallel()
	fc := &fakeAppMemoryClient{}
	sc := buildTestMemoryScreens(t, fc)
	mv := sc.memoryViewFor(testSettingsWSID)
	mv.setSelectedForTest(&client.MemoryEntry{
		ID:          "mem-1",
		WorkspaceID: testSettingsWSID,
		Kind:        client.MemoryKindFact,
		Label:       "auth-architecture",
		Summary:     "Initial agent summary",
		Body:        "Initial agent body",
		Author:      client.MemoryAuthorAgent,
		Verified:    false,
		Validity:    client.MemoryValidityValid,
		Revision:    1,
	})
	mv.startEdit()
	mv.editSummary.SetText("Updated verified summary")
	mv.editBody.SetText("Updated verified body")
	mv.saveEdit(sc)

	res := awaitMemoryResult(t, sc)
	sc.applyMemoryResults([]memory.Result{res})

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.writeReqs) != 1 || fc.writeReqs[0].Op != client.MemoryOpUpdate || fc.writeReqs[0].ExpectedRevision != 1 {
		t.Fatalf("WriteMemoryEntry calls = %+v, want 1 update at rev 1", fc.writeReqs)
	}
	if mv.selected == nil || !mv.selected.Verified || mv.selected.Revision != 2 {
		t.Fatalf("selected entry after save = %+v, want Verified=true Revision=2", mv.selected)
	}
}

func TestMemoryUnverifiedCountUpdatesOnEvent(t *testing.T) {
	t.Parallel()
	sc := buildTestMemoryScreens(t, &fakeAppMemoryClient{})

	snap, err := json.Marshal(client.MemoryTopicSnapshot{Unverified: map[string]int{testSettingsWSID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyMemoryDelivery(hostconn.Update{Topic: topicMemory, Snapshot: &client.Snapshot{State: snap}})
	if got := sc.workspaceBadge(testSettingsWSID); got != "1" {
		t.Fatalf("workspaceBadge after snapshot = %q, want \"1\"", got)
	}

	evBytes, err := json.Marshal(client.MemoryWrittenEvent{
		EntryID:         "mem-2",
		WorkspaceID:     testSettingsWSID,
		Revision:        1,
		Author:          client.MemoryAuthorAgent,
		Op:              client.MemoryOpAdd,
		UnverifiedCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyMemoryDelivery(hostconn.Update{
		Topic: topicMemory,
		Event: &client.Event{Type: client.EventMemoryEntryWritten, Payload: evBytes},
	})
	if got := sc.workspaceBadge(testSettingsWSID); got != "3" {
		t.Fatalf("workspaceBadge after event = %q, want \"3\"", got)
	}

	evZero, err := json.Marshal(client.MemoryWrittenEvent{
		EntryID:         "mem-2",
		WorkspaceID:     testSettingsWSID,
		Revision:        2,
		Author:          client.MemoryAuthorUser,
		Op:              client.MemoryOpVerify,
		UnverifiedCount: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyMemoryDelivery(hostconn.Update{
		Topic: topicMemory,
		Event: &client.Event{Type: client.EventMemoryEntryWritten, Payload: evZero},
	})
	if got := sc.workspaceBadge(testSettingsWSID); got != "" {
		t.Fatalf("workspaceBadge after zero event = %q, want \"\"", got)
	}
}

func TestMemoryImportConflictChoiceAndRenameReachApplyImport(t *testing.T) {
	t.Parallel()
	fc := &fakeAppMemoryClient{
		applyResp: client.MemoryImportResult{Added: 0, Updated: 2, Skipped: 0},
	}
	sc := buildTestMemoryScreens(t, fc)
	mv := sc.memoryViewFor(testSettingsWSID)
	mv.loadPreviewForTest(&client.MemoryImportPreview{
		PreviewID:     "prev-1",
		WorkspaceName: "alpha",
		Items: []client.MemoryImportPreviewItem{
			{
				ItemID:        "item-conflict",
				EntryID:       "mem-1",
				Label:         "auth-architecture",
				Class:         client.MemoryImportClassConflict,
				DefaultChoice: client.MemoryImportChoiceKeepLocal,
			},
			{
				ItemID:        "item-label",
				EntryID:       "mem-2",
				Label:         "build-steps",
				Class:         client.MemoryImportClassLabelConflict,
				DefaultChoice: client.MemoryImportChoiceSkip,
			},
		},
	}, []byte("raw-md"), "import.md")

	mv.setImportChoice("item-conflict", client.MemoryImportChoiceKeepBoth, "")
	mv.setImportChoice("item-label", client.MemoryImportChoiceRename, "build-steps-imported")
	mv.applyImportDiff(sc)

	res := awaitMemoryResult(t, sc)
	sc.applyMemoryResults([]memory.Result{res})

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.applyReqs) != 1 {
		t.Fatalf("ApplyMemoryImport calls = %d, want 1", len(fc.applyReqs))
	}
	got := fc.applyReqs[0].Choices
	if len(got) != 2 || got[0].Choice != client.MemoryImportChoiceKeepBoth || got[1].Choice != client.MemoryImportChoiceRename || got[1].NewLabel != "build-steps-imported" {
		t.Fatalf("ApplyMemoryImport choices = %+v, want keep_both and rename(build-steps-imported)", got)
	}
}

func TestMemoryImportStaleOffersRepreviewAction(t *testing.T) {
	t.Parallel()
	cerr := connect.NewError(connect.CodeFailedPrecondition, errors.New("stale"))
	det, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.MemoryCodeImportStale})
	if derr != nil {
		t.Fatal(derr)
	}
	cerr.AddDetail(det)

	fc := &fakeAppMemoryClient{
		applyErr: cerr,
		previewResp: client.MemoryImportPreview{
			PreviewID: "prev-2",
		},
	}
	sc := buildTestMemoryScreens(t, fc)
	mv := sc.memoryViewFor(testSettingsWSID)
	mv.loadPreviewForTest(&client.MemoryImportPreview{
		PreviewID: "prev-1",
		Items: []client.MemoryImportPreviewItem{
			{ItemID: "item-1", EntryID: "mem-1", Label: "fact-1", Class: client.MemoryImportClassAdd},
		},
	}, []byte("raw-import-bytes"), "import.md")

	mv.applyImportDiff(sc)
	res := awaitMemoryResult(t, sc)
	sc.applyMemoryResults([]memory.Result{res})

	if !mv.importStale || !strings.Contains(mv.importErr, "memory changed, preview again") {
		t.Fatalf("stale state = (importStale=%v, importErr=%q), want stale with 'memory changed, preview again'", mv.importStale, mv.importErr)
	}

	mv.repreviewImport(sc)
	prevRes := awaitMemoryResult(t, sc)
	sc.applyMemoryResults([]memory.Result{prevRes})

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.previewReqs) != 1 || string(fc.previewReqs[0]) != "raw-import-bytes" {
		t.Fatalf("repreview requests = %q, want [raw-import-bytes]", fc.previewReqs)
	}
	if mv.importStale || mv.importPreview == nil || mv.importPreview.PreviewID != "prev-2" {
		t.Fatalf("after repreview = (stale=%v, preview=%+v), want stale=false prev-2", mv.importStale, mv.importPreview)
	}
}

func TestMemorySlotDoesNotRenderWhileVaultLocked(t *testing.T) {
	t.Parallel()
	sc := buildTestMemoryScreens(t, &fakeAppMemoryClient{})
	mv := sc.memoryViewFor(testSettingsWSID)
	mv.entries = []client.MemoryEntry{
		{ID: "mem-1", WorkspaceID: testSettingsWSID, Kind: client.MemoryKindFact, Label: "SECRET_MEMORY_ENTRY_LABEL", Summary: "Secret summary"},
	}
	openVisibleSlot(sc, SlotRef{Kind: KindMemory, ID: testSettingsWSID})
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateLocked, Revision: 2, Incarnation: "inc-mem"}))

	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("shaper: %v", err)
	}
	th := theme.Dark(sh)
	var rendered []string
	env := &kit.Env{
		Th: &th,
		Sh: sh,
		RunRecorder: func(image.Rectangle) {
			rendered = append(rendered, "run")
		},
	}
	var router input.Router
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(900, 600)),
		Source:      router.Source(),
		Now:         time.Unix(1_700_000_000, 0),
	}
	env.Begin(gtx.Now)
	mv.layout(&gtx, env, sc)
	env.FrameEnd(&gtx)

	if !mv.lastRenderedLocked || len(rendered) > 0 {
		t.Fatalf("memory slot rendered content while vault locked: lastRenderedLocked=%v runs=%d", mv.lastRenderedLocked, len(rendered))
	}
}
