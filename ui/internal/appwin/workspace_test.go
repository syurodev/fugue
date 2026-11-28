package appwin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"

	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// errRootInvalid is the typed host answer the root-dialog test plays.
var errRootInvalid = errors.New("workspace.root_invalid: the root path is invalid")

const (
	testPolicyKey    = "policy.approval_mode"
	testApprovalAsk  = "ask"
	testApprovalFull = "full"
	testRootAPI      = "api"
	testRootWeb      = "web"
	testRootMainPath = "/repo/main"
	testRootWebPath  = "/repo/web"
)

// recordingReader records the workspace reads the screens arm.
type recordingReader struct {
	lists   int
	details []string
	gens    []uint64
}

func (r *recordingReader) RequestWorkspaces() { r.lists++ }
func (r *recordingReader) RequestWorkspace(id string, gen uint64) {
	r.details = append(r.details, id)
	r.gens = append(r.gens, gen)
}

// recordingMutatorClient records the workspace calls the mutator reaches.
// A create answers with createRes; each add answers with its place in
// addRes and addErrs (an index past the end succeeds), so the chain tests
// script a failure at an exact row.
type recordingMutatorClient struct {
	calls     []workspace.Mutation
	err       error
	createRes client.Workspace
	addRes    []client.Workspace
	addErrs   []error
	addN      int
}

func (r *recordingMutatorClient) CreateWorkspace(_ context.Context, _ string) (client.Workspace, error) {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutCreate})
	return r.createRes, r.err
}

func (r *recordingMutatorClient) AddRoot(_ context.Context, id, name, path, access, hash string) (client.Workspace, error) {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutAddRoot, WorkspaceID: id, Name: name, Path: path, Access: access, ExpectedHash: hash})
	res := client.Workspace{}
	if r.addN < len(r.addRes) {
		res = r.addRes[r.addN]
	}
	var err error
	if r.addN < len(r.addErrs) {
		err = r.addErrs[r.addN]
	}
	r.addN++
	return res, err
}

func (r *recordingMutatorClient) RemoveRoot(_ context.Context, id, name, hash string) error {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutRemoveRoot, WorkspaceID: id, Name: name, ExpectedHash: hash})
	return r.err
}

func (r *recordingMutatorClient) UpdateRoot(_ context.Context, id, name, newName, path, access, hash string) error {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutUpdateRoot, WorkspaceID: id, Name: name, NewName: newName, Path: path, Access: access, ExpectedHash: hash})
	return r.err
}

func (r *recordingMutatorClient) DeleteWorkspace(_ context.Context, id, hash, confirm string) error {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutDelete, WorkspaceID: id, ExpectedHash: hash, ConfirmName: confirm})
	return r.err
}

func (r *recordingMutatorClient) ConfirmWorkspaceConfig(_ context.Context, id, hash string) error {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutConfirmPolicy, WorkspaceID: id, ExpectedHash: hash})
	return r.err
}

func (r *recordingMutatorClient) RejectWorkspaceConfig(_ context.Context, id, hash string) error {
	r.calls = append(r.calls, workspace.Mutation{Kind: workspace.MutRejectPolicy, WorkspaceID: id, ExpectedHash: hash})
	return r.err
}

// waitMutResult takes the mutator's next result. The receive blocks: Start,
// Do and deliver are deterministic, and the package timeout catches a stuck
// goroutine, so no wall-clock wait is needed.
func waitMutResult(t *testing.T, m *workspace.Mutator) workspace.MutResult {
	t.Helper()
	return <-m.Results()
}

// feedSnapshot feeds one workspaces-topic snapshot into the screens.
func feedSnapshot(sc *screens, payload []byte) {
	sc.update([]hostconn.Update{{Topic: topicWorkspaces, Snapshot: &client.Snapshot{State: payload}}}, nil, nil, nil)
}

// feedEvent feeds one workspaces-topic event into the screens.
func feedEvent(sc *screens, eventType string, payload []byte) {
	sc.update([]hostconn.Update{{Topic: topicWorkspaces, Event: &client.Event{Type: eventType, Payload: payload}}}, nil, nil, nil)
}

// topicSnapshot marshals the summaries the workspaces snapshot carries.
func topicSnapshot(t *testing.T, summaries []workspace.Summary) []byte {
	t.Helper()
	data, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// topicSummary marshals the summary one workspaces event carries.
func topicSummary(t *testing.T, s workspace.Summary) []byte {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// wsTestDetail is the detail a read delivers for one workspace: one root,
// a hash and a pending policy diff.
func wsTestDetail(diff []workspace.DiffEntry) *workspace.Detail {
	return &workspace.Detail{
		ID: "w1", Name: "ws-w1", ConfigHash: "hash-w1",
		Roots:   []workspace.Root{{Name: testMainRoot, Path: "/roots/w1", Access: "rw"}},
		Pending: diff != nil, Diff: diff,
	}
}

// wsSession is a sidebar session bound to a workspace.
func wsSession(id, wsID string) session.Session {
	return session.Session{ID: id, Vendor: client.VendorClaude, State: stateRunning, WorkspaceID: wsID}
}

// TestSidebarGroupsSessionsUnderTheirWorkspaces pins the sidebar's grouping
// rule (desktop-app §5.3): every workspace's sessions ride its section, in
// the workspaces' list order, and a session the list read has not bound to a
// known workspace yet rides the trailing group.
func TestSidebarGroupsSessionsUnderTheirWorkspaces(t *testing.T) {
	t.Parallel()
	groups, other := wsGroups(
		[]session.Session{wsSession("s1", "w2"), wsSession("s2", "w1"), wsSession("s3", "w1"), wsSession("s4", "w9")},
		[]workspace.Summary{{ID: "w1", Name: "A"}, {ID: "w2", Name: "B"}},
	)
	if len(groups) != 2 || groups[0].ws.ID != "w1" || groups[1].ws.ID != "w2" {
		t.Fatalf("groups = %+v, want w1 then w2", groups)
	}
	if len(groups[0].sessions) != 2 || groups[0].sessions[0].ID != "s2" || groups[0].sessions[1].ID != "s3" {
		t.Fatalf("w1 sessions = %+v, want s2 then s3", groups[0].sessions)
	}
	if len(groups[1].sessions) != 1 || groups[1].sessions[0].ID != "s1" {
		t.Fatalf("w2 sessions = %+v, want s1", groups[1].sessions)
	}
	if len(other) != 1 || other[0].ID != "s4" {
		t.Fatalf("other = %+v, want s4", other)
	}
}

// TestWorkspaceDetailReadsArmOncePerMissingDetail pins the read loop: the
// workspaces list read arms on reconnect, one detail read arms per workspace
// the model holds no detail for, nothing re-arms while a read is in flight,
// and a detail-affecting event restarts it.
func TestWorkspaceDetailReadsArmOncePerMissingDetail(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	reads := &recordingReader{}
	sc.wsReads = reads

	// The reconnect arms the list read; the delivered snapshot arms one
	// detail read per workspace.
	sc.reconnected()
	if reads.lists != 1 {
		t.Fatalf("list reads = %d, want 1", reads.lists)
	}
	feedSnapshot(sc, topicSnapshot(t,
		[]workspace.Summary{{ID: "w1", Name: "A"}, {ID: "w2", Name: "B"}}))
	if len(reads.details) != 2 {
		t.Fatalf("detail reads = %v, want one per workspace", reads.details)
	}

	// The next frame arms nothing: every read is in flight.
	sc.update(nil, nil, nil, nil)
	if len(reads.details) != 2 {
		t.Fatalf("detail reads after the second frame = %v, want no re-arm", reads.details)
	}

	// The landing detail frees w1's guard, and nothing re-arms for it.
	sc.workspaces.ApplyDetail(wsTestDetail(nil))
	sc.update(nil, nil, nil, nil)
	if len(reads.details) != 2 {
		t.Fatalf("detail reads after w1's detail = %v, want no re-arm", reads.details)
	}

	// A root change drops w1's detail, and exactly one read re-arms.
	feedEvent(sc, workspace.EventRootAdded, topicSummary(t, workspace.Summary{ID: "w1", Name: "A"}))
	if len(reads.details) != 3 {
		t.Fatalf("detail reads after the root change = %v, want one re-arm", reads.details)
	}
}

// TestDeletedWorkspaceClosesItsDialogs pins that a dialog never stays open
// on a workspace that left the list: a deleted event closes it.
func TestDeletedWorkspaceClosesItsDialogs(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	sc.workspaces.ApplyDetail(wsTestDetail(nil))

	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)
	sc.dlg.del.openFor(det)
	if !sc.dlg.del.dialog.IsOpen() {
		t.Fatal("the delete dialog did not open")
	}
	feedEvent(sc, workspace.EventDeleted, topicSummary(t, workspace.Summary{ID: "w1", Name: "A"}))
	if sc.dlg.del.dialog.IsOpen() {
		t.Fatal("the delete dialog outlived its workspace")
	}
}

// TestDeleteWorkspaceNeedsTheName pins the deletion rule: a mistyped or
// empty name posts nothing and keeps the dialog open; the exact name posts
// the delete with the detail's hash, and its success closes the dialog.
func TestDeleteWorkspaceNeedsTheName(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{}
	startTestMutator(t, sc, rec)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)

	sc.dlg.del.openFor(det)
	sc.dlg.del.submit(sc)
	if len(rec.calls) != 0 {
		t.Fatalf("an empty name posted %+v", rec.calls)
	}
	if !sc.dlg.del.dialog.IsOpen() {
		t.Fatal("the dialog closed without the name")
	}

	sc.dlg.del.name.SetText("not-the-name")
	sc.dlg.del.submit(sc)
	if len(rec.calls) != 0 {
		t.Fatalf("a mistyped name posted %+v", rec.calls)
	}
	if sc.dlg.del.lastErr == "" {
		t.Fatal("a mistyped name shows no failure")
	}

	sc.dlg.del.name.SetText(det.Name)
	sc.dlg.del.submit(sc)
	res := waitMutResult(t, sc.wsMut)
	if len(rec.calls) != 1 {
		t.Fatalf("the exact name posted %+v, want one delete", rec.calls)
	}
	if rec.calls[0].Kind != workspace.MutDelete || rec.calls[0].ExpectedHash != det.ConfigHash || rec.calls[0].ConfirmName != det.Name {
		t.Fatalf("delete posted %+v, want the workspace's hash and name", rec.calls[0])
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.del.dialog.IsOpen() {
		t.Fatal("the delete dialog outlived its success")
	}
}

// TestRootDialogValidatesAndShowsTheHostAnswerInline pins the edit-root
// dialog's evidence: an empty path posts nothing, a filled form posts the
// update with the detail's hash and access, and the host's typed failure
// keeps the dialog open with its message inline.
func TestRootDialogValidatesAndShowsTheHostAnswerInline(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{err: errRootInvalid}
	startTestMutator(t, sc, rec)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)

	sc.dlg.root.openFor(det, &det.Roots[0])
	sc.dlg.root.path.SetText("")
	sc.dlg.root.submit(sc)
	if len(rec.calls) != 0 {
		t.Fatalf("a form without a path posted %+v", rec.calls)
	}
	if !sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the dialog closed without a path")
	}

	sc.dlg.root.path.SetText("/new/root")
	sc.dlg.root.name.SetText(testMainRoot)
	sc.dlg.root.submit(sc)
	res := waitMutResult(t, sc.wsMut)
	call := rec.calls[0]
	if call.Kind != workspace.MutUpdateRoot || call.WorkspaceID != "w1" || call.Path != "/new/root" || call.Access != "rw" || call.ExpectedHash != det.ConfigHash {
		t.Fatalf("update posted %+v, want the detail's hash and rw access", call)
	}

	// The host's typed failure keeps the dialog open with the message, so
	// the user edits the path instead of retyping the form.
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if !sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the host's failure closed the dialog")
	}
	if sc.dlg.root.lastErr == "" {
		t.Fatal("the host's failure is not shown inline")
	}

	// A success closes the dialog.
	rec.err = nil
	sc.dlg.root.submit(sc)
	res = waitMutResult(t, sc.wsMut)
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the root dialog outlived its success")
	}
}

// TestPolicyCardDecisionsCarryTheHash pins the Q-64 card: Confirm posts the
// confirm with the reviewed hash, Reject posts the reject with it, and a
// decision's success closes the card.
func TestPolicyCardDecisionsCarryTheHash(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{}
	startTestMutator(t, sc, rec)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A", PendingPolicyConfirmation: true}}))
	diff := []workspace.DiffEntry{{Key: testPolicyKey, OldValue: testApprovalAsk, NewValue: "auto"}}
	det := wsTestDetail(diff)
	sc.workspaces.ApplyDetail(det)

	sc.dlg.policy.openFor(det)
	sc.dlg.policy.submit(sc)
	res := waitMutResult(t, sc.wsMut)
	if len(rec.calls) != 1 || rec.calls[0].Kind != workspace.MutConfirmPolicy || rec.calls[0].ExpectedHash != det.ConfigHash {
		t.Fatalf("confirm posted %+v, want the reviewed hash", rec.calls)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.policy.dialog.IsOpen() {
		t.Fatal("the policy card outlived its confirm")
	}

	sc.dlg.policy.openFor(det)
	sc.dlg.policy.reject(sc)
	res = waitMutResult(t, sc.wsMut)
	if len(rec.calls) != 2 || rec.calls[1].Kind != workspace.MutRejectPolicy || rec.calls[1].ExpectedHash != det.ConfigHash {
		t.Fatalf("reject posted %+v, want the reviewed hash", rec.calls)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.policy.dialog.IsOpen() {
		t.Fatal("the policy card outlived its reject")
	}
}

// TestDetailDropClosesOpenWorkspaceDialogAndRearmsRead pins that an event
// dropping an open dialog's detail closes that dialog so it never submits a
// stale hash, leaves an unrelated create dialog open, and re-arms the read.
func TestDetailDropClosesOpenWorkspaceDialogAndRearmsRead(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	reads := &recordingReader{}
	sc.wsReads = reads
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A", ConfigRevision: 1}}))
	det := wsTestDetail(nil)
	det.ConfigRevision = 1
	sc.workspaces.ApplyDetail(det)
	sc.update(nil, nil, nil, nil)

	sc.dlg.root.openAdd(sc, det)
	if !sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the root dialog did not open")
	}
	beforeReads := len(reads.details)
	feedEvent(sc, workspace.EventConfigChanged, topicSummary(t, workspace.Summary{ID: "w1", Name: "A", ConfigRevision: 2}))
	if sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the root dialog stayed open after its detail was dropped")
	}
	if len(reads.details) != beforeReads+1 {
		t.Fatalf("detail reads = %d, want re-arm after detail drop", len(reads.details))
	}

	// An open create dialog has no workspace detail and survives a snapshot.
	sc.dlg.create.open(sc)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A", ConfigRevision: 2}}))
	if !sc.dlg.create.dialog.IsOpen() {
		t.Fatal("create dialog was closed by snapshot")
	}
}

// TestDialogsBlockMutationsWithEmptyHash pins that no dialog posts a
// compare-and-swap mutation when its expected hash is empty.
func TestDialogsBlockMutationsWithEmptyHash(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{}
	startTestMutator(t, sc, rec)
	emptyDet := &workspace.Detail{ID: "w1", Name: "ws-w1", ConfigHash: ""}

	sc.dlg.root.openAdd(sc, emptyDet)
	sc.dlg.root.folders.addPath("/repo")
	sc.dlg.root.submit(sc)

	sc.dlg.del.openFor(emptyDet)
	sc.dlg.del.name.SetText("ws-w1")
	sc.dlg.del.submit(sc)

	sc.dlg.policy.openFor(emptyDet)
	sc.dlg.policy.submit(sc)
	sc.dlg.policy.reject(sc)

	sc.dlg.rmRoot.openFor(emptyDet, workspace.Root{Name: testMainRoot})
	sc.dlg.rmRoot.submit(sc)

	if len(rec.calls) != 0 {
		t.Fatalf("empty-hash dialogs posted %+v, want none", rec.calls)
	}
}

// TestResultWorkspacesAndWorkspaceFeedModelAndStartDialog pins the post-#169
// wiring: ResultWorkspaces and ResultWorkspace feed the shared workspace model
// (with ConfigHash and PolicyDiff) and the start dialog reads from it.
func TestResultWorkspacesAndWorkspaceFeedModelAndStartDialog(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.update(nil, []session.Result{{
		Kind: session.ResultWorkspaces,
		Workspaces: []client.WorkspaceSummary{
			{ID: "w1", Name: testBackend, ConfigRevision: 2, PendingPolicyConfirmation: true},
		},
	}, {
		Kind: session.ResultWorkspace,
		Workspace: client.Workspace{
			ID: "w1", Name: testBackend, ConfigHash: "real-hash-2", ConfigRevision: 2,
			PendingPolicyConfirmation: true,
			PolicyDiff:                []client.PolicyDiffEntry{{Key: testPolicyKey, OldValue: testApprovalAsk, NewValue: testApprovalFull}},
			Roots: []client.WorkspaceRoot{
				{Name: testRootAPI, Path: "/repo/api", Access: "rw"},
				{Name: testRootWeb, Path: testRootWebPath, Access: "ro"},
			},
		},
	}}, nil, nil)

	det, ok := sc.workspaces.DetailFor("w1")
	if !ok || det.ConfigHash != "real-hash-2" || len(det.Diff) != 1 || det.Diff[0].NewValue != testApprovalFull {
		t.Fatalf("model detail = %+v, %v; want real hash and diff", det, ok)
	}
	sc.composer.start.chooseWorkspace("w1", sc)
	if got := sc.composer.start.rootName(sc); got != testRootAPI {
		t.Fatalf("start dialog default root = %q, want api from shared model", got)
	}
}

// TestScreensDropsLateDetailArmedBeforePendingConfirmation pins that screens
// tracks armed read generations across topic drops: a read armed before
// EventPendingConfirm returns late and is ignored, a fresh read at the new
// generation re-arms automatically and lands with Pending true.
func TestScreensDropsLateDetailArmedBeforePendingConfirmation(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	reads := &recordingReader{}
	sc.wsReads = reads

	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A", ConfigRevision: 1}}))
	if len(reads.details) != 1 || reads.gens[0] != 0 {
		t.Fatalf("initial armed reads = (%v, %v), want ([w1], [0])", reads.details, reads.gens)
	}

	// An out-of-band edit arrives before the initial read returns, keeping
	// ConfigRevision at 1 while flipping PendingPolicyConfirmation to true.
	feedEvent(sc, workspace.EventPendingConfirm, topicSummary(t, workspace.Summary{
		ID: "w1", Name: "A", ConfigRevision: 1, PendingPolicyConfirmation: true,
	}))
	if len(reads.details) != 2 || reads.gens[1] != 1 {
		t.Fatalf("re-armed reads = (%v, %v), want second w1 read at gen 1", reads.details, reads.gens)
	}

	// Late initial read (gen 0) arrives: ignored.
	sc.update(nil, []session.Result{{
		Kind:       session.ResultWorkspace,
		Generation: reads.gens[0],
		Workspace: client.Workspace{
			ID: "w1", Name: "A", ConfigHash: "hash-stale", ConfigRevision: 1,
		},
	}}, nil, nil)
	if _, ok := sc.workspaces.DetailFor("w1"); ok {
		t.Fatal("stale gen-0 ResultWorkspace populated DetailFor")
	}
	if sum, ok := sc.workspaces.Lookup("w1"); !ok || !sum.PendingPolicyConfirmation {
		t.Fatalf("summary = %+v, %v; want PendingPolicyConfirmation true", sum, ok)
	}

	// Fresh read (gen 1) arrives and lands.
	sc.update(nil, []session.Result{{
		Kind:       session.ResultWorkspace,
		Generation: reads.gens[1],
		Workspace: client.Workspace{
			ID: "w1", Name: "A", ConfigHash: "hash-pending", ConfigRevision: 1,
			PendingPolicyConfirmation: true,
		},
	}}, nil, nil)
	det, ok := sc.workspaces.DetailFor("w1")
	if !ok || det.ConfigHash != "hash-pending" || !det.Pending {
		t.Fatalf("fresh detail = %+v, %v; want hash-pending and Pending true", det, ok)
	}
}

// startTestMutator replaces the screens' mutator with one posting through the
// recording fake, running until the test ends.
func startTestMutator(t *testing.T, sc *screens, rec *recordingMutatorClient) {
	t.Helper()
	m := workspace.NewMutator(func() workspace.MutatorClient { return rec })
	m.Start(t.Context())
	t.Cleanup(m.Stop)
	sc.wsMut = m
}
