package workspace

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// summaryJSON marshals one summary the way the topic payloads do.
func summaryJSON(t *testing.T, s Summary) []byte {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wsSummary(id, name string, pending bool) Summary {
	return Summary{ID: id, Name: name, PendingPolicyConfirmation: pending}
}

func wsDetail(id string, roots ...Root) *Detail {
	return &Detail{ID: id, Name: "name-" + id, ConfigHash: "hash-" + id, Roots: roots, Pending: false}
}

// TestApplySnapshotReplacesListAndDetails pins the snapshot rule: the list
// becomes the snapshot's summaries in order, and every detail the previous
// connection held is dropped, so no stale roots or hash survive a reconnect.
func TestApplySnapshotReplacesListAndDetails(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("a", "A", false), wsSummary("b", "B", false)})); err != nil {
		t.Fatal(err)
	}
	m.ApplyDetail(wsDetail("a"))

	got := m.List()
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("list = %+v, want a then b", got)
	}
	if _, ok := m.DetailFor("a"); !ok {
		t.Fatal("the detail read before the second snapshot is missing")
	}

	// A fresh snapshot reorders and shrinks the list, and every detail the
	// previous connection held is gone with it.
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("b", "B", false), wsSummary("a", "A", false)})); err != nil {
		t.Fatal(err)
	}
	if got := m.List(); len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("list after the second snapshot = %+v, want b first", got)
	}
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("a detail outlived the snapshot that replaced the list")
	}
}

// TestApplyEventLifecycle pins the event rules: created appends, the
// detail-affecting events refresh the summary and drop the detail, and
// deleted removes the workspace whole.
func TestApplyEventLifecycle(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("a", "A", false)})); err != nil {
		t.Fatal(err)
	}
	m.ApplyDetail(wsDetail("a"))

	// A created workspace appends and is not answered by a detail yet.
	if err := m.ApplyEvent(EventCreated, summaryJSON(t, wsSummary("b", "B", false))); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.DetailFor("b"); ok {
		t.Fatal("a created workspace carries a detail no read delivered")
	}
	m.ApplyDetail(wsDetail("b"))

	// A root change drops the workspace's own detail and keeps its list
	// place; another workspace's detail is untouched.
	if err := m.ApplyEvent(EventRootAdded, summaryJSON(t, wsSummary("a", "A", false))); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("a detail outlived the root change that invalidated it")
	}
	if _, ok := m.DetailFor("b"); !ok {
		t.Fatal("workspace b's detail was dropped with a's")
	}

	// Deleted removes the workspace and its detail.
	if err := m.ApplyEvent(EventDeleted, summaryJSON(t, wsSummary("b", "B", false))); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup("b"); ok {
		t.Fatal("deleted workspace b is still listed")
	}
	if _, ok := m.DetailFor("b"); ok {
		t.Fatal("deleted workspace b's detail survived")
	}
	if slices.ContainsFunc(m.List(), func(s Summary) bool { return s.ID == "b" }) {
		t.Fatal("b still sits in the list order")
	}
}

// TestApplyEventPendingDropsDetail pins that the pending-confirmation event
// both flags the summary and drops the detail, so the next read carries the
// reviewed diff the card shows.
func TestApplyEventPendingDropsDetail(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("a", "A", false)})); err != nil {
		t.Fatal(err)
	}
	m.ApplyDetail(wsDetail("a"))

	if err := m.ApplyEvent(EventPendingConfirm, summaryJSON(t, wsSummary("a", "A", true))); err != nil {
		t.Fatal(err)
	}
	got, ok := m.Lookup("a")
	if !ok || !got.PendingPolicyConfirmation {
		t.Fatalf("lookup a = %+v, %v; want the pending flag", got, ok)
	}
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("the pending event kept a detail without the diff")
	}
}

// TestApplyDetailFillsNeedsDetailOnce pins the detail loop's fill half:
// NeedsDetail names every summary without a detail in list order, and
// ApplyDetail ends the need with the read's content.
func TestApplyDetailFillsNeedsDetailOnce(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("a", "A", false), wsSummary("b", "B", false)})); err != nil {
		t.Fatal(err)
	}
	if got := m.NeedsDetail(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("needs detail = %v, want a then b", got)
	}

	m.ApplyDetail(wsDetail("a", Root{Name: "main", Path: "/r", Access: "rw"}))
	if got := m.NeedsDetail(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("needs detail after a's read = %v, want b", got)
	}
	d, ok := m.DetailFor("a")
	if !ok || d.ConfigHash != "hash-a" || len(d.Roots) != 1 || d.Roots[0].Name != "main" {
		t.Fatalf("detail a = %+v, %v", d, ok)
	}

	m.ApplyDetail(&Detail{ID: "b", Name: "B2", Pending: true})
	got, ok := m.Lookup("b")
	if !ok || got.Name != "B2" || !got.PendingPolicyConfirmation {
		t.Fatalf("b after its detail read = %+v, %v; want the read's name and pending flag", got, ok)
	}
	if got := m.NeedsDetail(); len(got) != 0 {
		t.Fatalf("needs detail after both reads = %v, want none", got)
	}
}

// TestEventRestartsTheDetailNeed pins the detail loop's restart half: a
// detail-affecting event drops the detail, so the workspace needs a read
// again while the rest of the list stays filled.
func TestEventRestartsTheDetailNeed(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{wsSummary("a", "A", false), wsSummary("b", "B", false)})); err != nil {
		t.Fatal(err)
	}
	m.ApplyDetail(wsDetail("a"))
	m.ApplyDetail(wsDetail("b"))
	if got := m.NeedsDetail(); len(got) != 0 {
		t.Fatalf("needs detail after both reads = %v, want none", got)
	}

	if err := m.ApplyEvent(EventRootAdded, summaryJSON(t, wsSummary("b", "B", false))); err != nil {
		t.Fatal(err)
	}
	if got := m.NeedsDetail(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("needs detail after b's root change = %v, want b only", got)
	}
	if _, ok := m.DetailFor("a"); !ok {
		t.Fatal("a's detail was dropped with b's")
	}
}

// TestApplyEventUnknownType pins that an event the topic does not define is
// an error the caller surfaces, never a silent skip.
func TestApplyEventUnknownType(t *testing.T) {
	t.Parallel()
	m := NewModel()
	err := m.ApplyEvent("resurrected", summaryJSON(t, wsSummary("a", "A", false)))
	if err == nil || !errors.Is(err, errUnknownWorkspaceEvent) {
		t.Fatalf("unknown event error = %v, want errUnknownWorkspaceEvent", err)
	}
}

// TestApplySnapshotGarbageIsAnError pins that a payload the host did not
// write is surfaced, not swallowed.
func TestApplySnapshotGarbageIsAnError(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot([]byte("nonsense")); err == nil {
		t.Fatal("a garbage snapshot decoded without error")
	}
	if err := m.ApplyEvent(EventCreated, []byte("nonsense")); err == nil {
		t.Fatal("a garbage event decoded without error")
	}
}

// TestApplyDetailAfterDeletedDoesNotResurrectWorkspace pins that a detail read
// returning after a deleted event is ignored and never brings the workspace
// back into byID, the list order, or NeedsDetail.
func TestApplyDetailAfterDeletedDoesNotResurrectWorkspace(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{{ID: "a", Name: "A", ConfigRevision: 1}})); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyEvent(EventDeleted, summaryJSON(t, Summary{ID: "a", Name: "A", ConfigRevision: 2})); err != nil {
		t.Fatal(err)
	}

	m.ApplyDetail(&Detail{ID: "a", Name: "A", ConfigHash: "hash-a", ConfigRevision: 1})
	if _, ok := m.Lookup("a"); ok {
		t.Fatal("late detail resurrected deleted workspace a")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("list after late detail = %+v, want empty", got)
	}
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("late detail populated DetailFor after delete")
	}
	if got := m.NeedsDetail(); len(got) != 0 {
		t.Fatalf("NeedsDetail after late detail = %v, want empty", got)
	}
}

// TestApplyDetailOlderRevisionDoesNotOverwriteNewerHash pins that a late
// detail read from an older ConfigRevision never populates a stale hash or
// overwrites a newer revision's detail.
func TestApplyDetailOlderRevisionDoesNotOverwriteNewerHash(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{{ID: "a", Name: "A", ConfigRevision: 1}})); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyEvent(EventRootAdded, summaryJSON(t, Summary{ID: "a", Name: "A", ConfigRevision: 2})); err != nil {
		t.Fatal(err)
	}

	// Stale read from revision 1 arrives before revision 2's read: ignored.
	m.ApplyDetail(&Detail{ID: "a", Name: "A", ConfigHash: "hash-rev1", ConfigRevision: 1})
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("stale rev 1 detail populated DetailFor when model is at rev 2")
	}
	if got := m.NeedsDetail(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("NeedsDetail = %v, want [a] still pending rev 2 read", got)
	}

	// Revision 2's read lands with the new hash, then another rev 1 read arrives:
	// the newer hash stays intact.
	m.ApplyDetail(&Detail{ID: "a", Name: "A", ConfigHash: "hash-rev2", ConfigRevision: 2, Generation: m.Generation("a")})
	m.ApplyDetail(&Detail{ID: "a", Name: "A", ConfigHash: "hash-rev1", ConfigRevision: 1})
	det, ok := m.DetailFor("a")
	if !ok || det.ConfigHash != "hash-rev2" || det.ConfigRevision != 2 {
		t.Fatalf("detail after stale read = %+v, %v; want hash-rev2 at rev 2", det, ok)
	}
}

// TestApplyDetailDropsReadArmedBeforePendingConfirmation pins the per-workspace
// generation guard: an out-of-band edit publishes EventPendingConfirm without
// bumping ConfigRevision, so a read armed before the event lands late, is
// dropped, keeps PendingPolicyConfirmation true and leaves the workspace in
// NeedsDetail.
func TestApplyDetailDropsReadArmedBeforePendingConfirmation(t *testing.T) {
	t.Parallel()
	m := NewModel()
	if err := m.ApplySnapshot(mustJSON(t, []Summary{{ID: "a", Name: "A", ConfigRevision: 1}})); err != nil {
		t.Fatal(err)
	}
	armedGen := m.Generation("a")

	if err := m.ApplyEvent(EventPendingConfirm, summaryJSON(t, Summary{
		ID: "a", Name: "A", ConfigRevision: 1, PendingPolicyConfirmation: true,
	})); err != nil {
		t.Fatal(err)
	}

	// Late read armed before pending_confirmation arrives with the same
	// ConfigRevision and Pending: false, but an outdated generation.
	m.ApplyDetail(&Detail{
		ID: "a", Name: "A", ConfigHash: "hash-old", ConfigRevision: 1, Pending: false, Generation: armedGen,
	})
	if _, ok := m.DetailFor("a"); ok {
		t.Fatal("stale pre-pending read populated DetailFor")
	}
	s, ok := m.Lookup("a")
	if !ok || !s.PendingPolicyConfirmation {
		t.Fatalf("summary after stale read = %+v, %v; want PendingPolicyConfirmation true", s, ok)
	}
	if got := m.NeedsDetail(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("NeedsDetail after dropped read = %v, want [a]", got)
	}

	// A fresh read armed at the bumped generation lands cleanly.
	freshGen := m.Generation("a")
	m.ApplyDetail(&Detail{
		ID: "a", Name: "A", ConfigHash: "hash-pending", ConfigRevision: 1, Pending: true, Generation: freshGen,
	})
	det, ok := m.DetailFor("a")
	if !ok || det.ConfigHash != "hash-pending" || !det.Pending {
		t.Fatalf("fresh detail = %+v, %v; want hash-pending and Pending true", det, ok)
	}
	if got := m.NeedsDetail(); len(got) != 0 {
		t.Fatalf("NeedsDetail after fresh read = %v, want none", got)
	}
}

// mustJSON marshals v for a test fixture.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
