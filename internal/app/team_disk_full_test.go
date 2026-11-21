package app

// The F11.9 §12 disk-full evidence: a low injected free space pauses new
// worktree-bound dispatches behind a visible waiting(disk) queue reason until
// the injected clock's re-check sees space again, and a real classified
// disk-full failure ends that task failed(disk) with the usual delegator
// digest before the pause holds the rest.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TestDiskFullFailsTaskAndPausesWorktreeCreation pins the scheduler half of
// §12 (F11.9): the injected disk port below the 1 GiB threshold holds every
// task that needs a worktree as waiting(disk) while the lead still binds; the
// 30 s re-check on the fake clock resumes dispatching; and a worktree
// creation that fails with the classified disk-full error ends the task
// failed(disk), notifies its delegator through the usual digest, and pauses
// the remaining children until space is free again.
func TestDiskFullFailsTaskAndPausesWorktreeCreation(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	f.disp.Start(t.Context())

	teamID := assertDiskPauseHoldsChildren(t, f)
	assertDiskRecheckResumes(t, f, teamID)
	team2 := assertDiskFullFailureFailsTaskAndPauses(t, f)
	assertFailurePauseClearsAndStarts(t, f, team2)
}

// assertDiskPauseHoldsChildren dispatches a team with the injected port below
// the threshold and asserts the lead binds while the children wait.
func assertDiskPauseHoldsChildren(t *testing.T, f *dispatchFixture) string {
	t.Helper()
	f.disk.setFree(0)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if dispErr := f.disp.Dispatch(t.Context(), teamID); dispErr != nil {
		t.Fatalf("Dispatch under the disk pause: %v", dispErr)
	}
	tasks := f.tasks(t, teamID)
	lead := tasks[teamID+"."+teamTestLeadTask]
	if lead.State != vendorstore.TaskStateRunning {
		t.Fatalf("lead task under the disk pause = %s, want running (it needs no worktree)", lead.State)
	}
	if f.sessions.startCount() != 0 {
		t.Fatalf("sessions started under the disk pause = %d, want 0", f.sessions.startCount())
	}
	assertChildrenWaitOnDisk(t, f, teamID)
	return teamID
}

// assertChildrenWaitOnDisk reads the queue snapshot and asserts the two
// members are blocked with the visible waiting(disk) reason.
func assertChildrenWaitOnDisk(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	queue, err := f.disp.QueueSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waiting := 0
	for _, q := range queue {
		if q.TaskID == teamID+"."+teamTestLeadTask {
			continue
		}
		waiting++
		if !q.Blocked || q.WaitReason != waitDisk {
			t.Fatalf("queued child under the pause = %+v, want blocked with %s", q, waitDisk)
		}
	}
	if waiting != 2 {
		t.Fatalf("waiting children = %d, want the two members", waiting)
	}
}

// assertDiskRecheckResumes restores free space and spins until the re-check
// loop cleared the pause and dispatched the children.
func assertDiskRecheckResumes(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	f.disk.setFree(2 << 30)
	testutil.Spin(t, "disk pause clears and dispatches the children", func() bool {
		f.clk.Advance(time.Second)
		snap := f.tasks(t, teamID)
		return snap[teamID+"."+teamTestTaskA].State == vendorstore.TaskStateRunning &&
			snap[teamID+"."+teamTestTaskB].State == vendorstore.TaskStateRunning
	})
	queue, err := f.disp.QueueSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queue {
		if q.WaitReason != "" {
			t.Fatalf("queued task after resume = %+v, want no disk wait reason", q)
		}
	}
}

// assertDiskFullFailureFailsTaskAndPauses injects a classified disk-full
// worktree error into the second team's start: the first child ends
// failed(disk) with its delegator digest and the second stays queued behind
// the pause. It returns the second team's ID.
func assertDiskFullFailureFailsTaskAndPauses(t *testing.T, f *dispatchFixture) string {
	t.Helper()
	team2 := f.approve(t, memberTasksArgs(2), decisionApprove, "")
	f.disp.deps.TestWorktreeErr = fmt.Errorf("worktree add: %w", gitops.ErrDiskFull)
	if dispErr := f.disp.Dispatch(t.Context(), team2); dispErr != nil {
		t.Fatalf("Dispatch with a disk-full worktree failure: %v", dispErr)
	}
	assertDiskFailedTask(t, f, team2)
	assertSecondChildWaits(t, f, team2)
	assertLeadDigestRecipient(t, f, team2)
	assertClaimedDigestFailsDisk(t, f, team2)
	return team2
}

// assertDiskFailedTask asserts the first child of team2 ended failed(disk).
func assertDiskFailedTask(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	first, ok := f.tasks(t, teamID)[teamID+".ma"]
	if !ok || first.State != vendorstore.TaskStateFailed || first.FailureCode != failDisk {
		t.Fatalf("first child after disk-full = %+v, want failed(disk)", first)
	}
}

// assertSecondChildWaits asserts the second child is queued behind the pause
// with the visible waiting(disk) reason.
func assertSecondChildWaits(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	if second := f.tasks(t, teamID)[teamID+".mb"]; second.State != vendorstore.TaskStateQueued {
		t.Fatalf("second child after disk-full = %s, want queued under the pause", second.State)
	}
	queue, err := f.disp.QueueSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queue {
		if q.TeamID == teamID && q.TaskID == teamID+".mb" && (!q.Blocked || q.WaitReason != waitDisk) {
			t.Fatalf("queue snapshot after the failure = %+v, want a waiting(%s) child", q, waitDisk)
		}
	}
}

// assertLeadDigestRecipient asserts the lead holds pending mail after the
// disk-full failure.
func assertLeadDigestRecipient(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	pending, err := f.hp.vendorStore.PendingMailbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.TeamID == teamID && r.RecipientTaskID == teamID+"."+teamTestLeadTask {
			return
		}
	}
	t.Fatalf("PendingMailbox = %+v, want the failed(disk) digest for the lead", pending)
}

// assertClaimedDigestFailsDisk opens the lead's claimed digest and asserts
// one failed(disk) item reached it.
func assertClaimedDigestFailsDisk(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	mail, err := f.hp.vendorStore.ClaimMailbox(t.Context(), teamID, teamID+"."+teamTestLeadTask)
	if err != nil {
		t.Fatal(err)
	}
	if digestItemWithCode(t, f, mail, failDisk) {
		return
	}
	t.Fatalf("delegator digest rows = %+v, want a failed(disk) item", mail)
}

// digestItemWithCode reports whether one claimed row is a failed item with
// the given failure code.
func digestItemWithCode(t *testing.T, f *dispatchFixture, mail []vendorstore.MailboxMessage, code string) bool {
	t.Helper()
	for i := range mail {
		var item mailboxDigestItem
		raw, openErr := f.hp.vendorStore.OpenMailboxPayload(t.Context(), mail[i].TeamID, mail[i].MessageID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if json.Unmarshal(raw, &item) == nil && item.State == string(vendorstore.TaskStateFailed) && item.FailureCode == code {
			return true
		}
	}
	return false
}

// assertFailurePauseClearsAndStarts removes the injected error and spins until
// the re-check loop cleared the failure pause and the remaining child starts.
func assertFailurePauseClearsAndStarts(t *testing.T, f *dispatchFixture, teamID string) {
	t.Helper()
	f.disp.deps.TestWorktreeErr = nil
	testutil.Spin(t, "failure pause clears and the second child starts", func() bool {
		f.clk.Advance(time.Second)
		return f.tasks(t, teamID)[teamID+".mb"].State == vendorstore.TaskStateRunning
	})
}
