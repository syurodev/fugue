package app

// The F11.9 §12 takeover evidence: while a team_manager task is unknown, the
// lead may act as delegator for that manager's direct children and gets one
// idempotent notice that the manager is unknown with its children still
// active; the grant ends as soon as the manager leaves unknown; and
// fugue_task_recover(cancel) on the manager re-parents its live children to
// the lead instead of cancelling them (Q-159 A).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// markManagerUnknown drives the live §12 path: the manager's session died,
// so OnSessionDied marks the manager unknown and notifies the lead.
func markManagerUnknown(t *testing.T, f *activeTeamFixture, sess string) {
	t.Helper()
	f.rt.teamTasks.OnSessionDied(t.Context(), sess)
	testutil.Spin(t, "manager task unknown", func() bool {
		_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), f.teamID)
		if err != nil {
			return false
		}
		mgr, ok := findTask(tasks, f.managerTaskID)
		return ok && mgr.State == vendorstore.TaskStateUnknown
	})
}

// TestLeadRecoversChildrenOfUnknownTeamManager pins the notice, the
// delegation grant bounded to the manager's unknown state, its revocation,
// and the re-parenting recovery cancel.
func TestLeadRecoversChildrenOfUnknownTeamManager(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newActiveTeamFixture(t, nil)

	assertNoTakeoverWhileManagerRuns(t, f, ctx)
	markManagerUnknown(t, f, "sess-mgr")
	assertUnknownManagerNoticeIsIdempotent(t, f, ctx)
	assertLeadTakeoverGrant(t, f, ctx)
	assertTakeoverRevokedWhenManagerRecovers(t, f, ctx)
	markManagerUnknown(t, f, "sess-mgr")
	assertManagerCancelReparentsLiveChildren(t, f, ctx)
}

// assertNoTakeoverWhileManagerRuns pins the baseline: without an unknown
// manager neither the lead nor a member may act across their subtree.
func assertNoTakeoverWhileManagerRuns(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	if _, err := f.rt.teamTasks.Status(ctx, f.leadBinding.SessionID, f.memberTaskID); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("lead status of the manager's member before unknown = %v, want PermissionDenied", err)
	}
	if _, err := f.rt.teamTasks.Status(ctx, f.memberBinding.SessionID, f.siblingTaskID); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("member status of a lead child = %v, want PermissionDenied", err)
	}
}

// assertUnknownManagerNoticeIsIdempotent opens the lead's takeover notice and
// asserts a second exit handling enqueues nothing.
func assertUnknownManagerNoticeIsIdempotent(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	markedSeq := int64(0)
	evs, err := f.hp.vendorStore.TeamEventsSince(ctx, f.teamID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if evs[i].TaskID != f.managerTaskID || evs[i].EventType != vendorstore.EventTaskMarkedUnknown {
			continue
		}
		if evs[i].FromState != string(vendorstore.TaskStateRunning) {
			t.Fatalf("manager TaskMarkedUnknown from_state = %q, want running", evs[i].FromState)
		}
		markedSeq = evs[i].Sequence
	}
	if markedSeq <= 0 {
		t.Fatal("missing TaskMarkedUnknown for the manager")
	}
	raw, openErr := f.hp.vendorStore.OpenMailboxPayload(ctx, f.teamID, fmt.Sprintf("%s:unknown-notice:%s:%d:%s", f.teamID, f.managerTaskID, markedSeq, f.leadTaskID))
	if openErr != nil {
		t.Fatalf("lead takeover notice not enqueued: %v", openErr)
	}
	if !strings.Contains(string(raw), "team manager is unknown") || !strings.Contains(string(raw), "still active") {
		t.Fatalf("lead notice = %q, want the unknown manager and active-children summary", raw)
	}
	f.rt.teamTasks.OnSessionDied(ctx, "sess-mgr")
	pending, pendErr := f.hp.vendorStore.PendingMailbox(ctx)
	if pendErr != nil || len(pending) != 1 {
		t.Fatalf("PendingMailbox after repeated handling = (%+v, %v), want the one lead notice", pending, pendErr)
	}
}

// assertLeadTakeoverGrant exercises the §12 grant while the manager is
// unknown: status, send and recover resolve for the lead, and a member caller
// gains nothing.
func assertLeadTakeoverGrant(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	if _, err := f.rt.teamTasks.Status(ctx, f.leadBinding.SessionID, f.memberTaskID); err != nil {
		t.Fatalf("lead status of the manager's member while unknown: %v", err)
	}
	if _, err := f.rt.teamTasks.Send(ctx, f.leadBinding.SessionID, f.memberTaskID, "lead follow-up"); err != nil {
		t.Fatalf("lead send to the manager's member while unknown: %v", err)
	}
	if _, err := f.rt.teamTasks.Recover(ctx, f.leadBinding.SessionID, f.memberTaskID, recoveryDecisionResume, ""); errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("recover of a running member = %v, want FailedPrecondition", err)
	}
	if _, err := f.rt.teamTasks.Status(ctx, f.memberBinding.SessionID, f.memberTaskID); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("member status of its own task through takeover = %v, want PermissionDenied", err)
	}
}

// assertTakeoverRevokedWhenManagerRecovers moves the manager back to running
// and asserts the lead's grant ended with the unknown state.
func assertTakeoverRevokedWhenManagerRecovers(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	if err := appendTeamEventNamed(t, f, ctx, f.managerTaskID, vendorstore.EventTaskRecoveryDecided, vendorstore.RecoveryOutcomeResume, ""); err != nil {
		t.Fatal(err)
	}
	if err := appendTeamEventNamed(t, f, ctx, f.managerTaskID, vendorstore.EventTaskStarted, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.teamTasks.Status(ctx, f.leadBinding.SessionID, f.memberTaskID); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("lead status of the manager's member after recovery = %v, want PermissionDenied", err)
	}
}

// assertManagerCancelReparentsLiveChildren recovers the unknown manager with
// cancel: the manager is cancelled, its live child moves under the lead at
// depth 1 and keeps running, its session stays open, and the lead's own child
// is untouched.
func assertManagerCancelReparentsLiveChildren(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	res, err := f.rt.teamTasks.Recover(ctx, f.leadBinding.SessionID, f.managerTaskID, recoveryDecisionCancel, "lead takes over")
	if err != nil {
		t.Fatalf("manager recovery cancel: %v", err)
	}
	if !strings.Contains(res, "re-parented") {
		t.Fatalf("manager cancel result = %q, want the re-parent note", res)
	}
	_, tasks, snapErr := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	assertCancelledManagerTree(t, f, tasks)
	if snap, sessErr := f.hp.vendorStore.Snapshot(ctx, "sess-mem"); sessErr != nil || snap.State == vendorstore.StateClosed {
		t.Fatalf("member session after manager cancel = (%+v, %v), want untouched", snap, sessErr)
	}
	// After the re-parent the delegation is the normal chain: the manager is
	// no longer unknown, yet the lead still acts on its former child.
	if _, err := f.rt.teamTasks.Status(ctx, f.leadBinding.SessionID, f.memberTaskID); err != nil {
		t.Fatalf("lead status of the re-parented member: %v", err)
	}
}

// assertCancelledManagerTree asserts the tree after the takeover cancel: the
// manager terminal, its live child under the lead at depth 1, the lead's own
// child untouched.
func assertCancelledManagerTree(t *testing.T, f *activeTeamFixture, tasks []vendorstore.TaskRecord) {
	t.Helper()
	mgr, _ := findTask(tasks, f.managerTaskID)
	if mgr.State != vendorstore.TaskStateCancelled {
		t.Fatalf("manager state after cancel = %s, want cancelled", mgr.State)
	}
	mem, _ := findTask(tasks, f.memberTaskID)
	if mem.State != vendorstore.TaskStateRunning || mem.ParentTaskID != f.leadTaskID || mem.Depth != 1 {
		t.Fatalf("manager's live child after cancel = %+v, want running under the lead at depth 1", mem)
	}
	sib, _ := findTask(tasks, f.siblingTaskID)
	if sib.ParentTaskID != f.leadTaskID || sib.State != vendorstore.TaskStateQueued {
		t.Fatalf("lead's own child after cancel = %+v, want untouched", sib)
	}
}

// appendTeamEventNamed appends one team event after the team's last sequence.
func appendTeamEventNamed(t *testing.T, f *activeTeamFixture, ctx context.Context, taskID string, evType vendorstore.TeamEventType, outcome, sessionID string) error {
	t.Helper()
	rec, _, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		return fmt.Errorf("read the takeover team: %w", err)
	}
	if _, err = f.hp.vendorStore.AppendTeamEvent(ctx, f.teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "tk-" + taskID + "-" + string(evType) + "-" + outcome, TaskID: taskID, EventType: evType, Outcome: outcome, SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("append %s for %s: %w", evType, taskID, err)
	}
	return nil
}
