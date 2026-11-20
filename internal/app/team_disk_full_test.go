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
	ctx := t.Context()
	f := newDispatchFixture(t, true)
	f.disp.Start(ctx)

	// Phase A: the port reports no space, so the pause holds every child.
	f.disk.setFree(0)
	teamID := f.approve(t, validTeamProposalArgs(), decisionApprove, "")
	if err := f.disp.Dispatch(ctx, teamID); err != nil {
		t.Fatalf("Dispatch under the disk pause: %v", err)
	}
	tasks := f.tasks(t, teamID)
	lead := tasks[teamID+"."+teamTestLeadTask]
	if lead.State != vendorstore.TaskStateRunning {
		t.Fatalf("lead task under the disk pause = %s, want running (it needs no worktree)", lead.State)
	}
	if f.sessions.startCount() != 0 {
		t.Fatalf("sessions started under the disk pause = %d, want 0", f.sessions.startCount())
	}
	queue, err := f.disp.QueueSnapshot(ctx)
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

	// Phase B: space is free again; the re-check loop clears the pause on the
	// fake clock and dispatches.
	f.disk.setFree(2 << 30)
	testutil.Spin(t, "disk pause clears and dispatches the children", func() bool {
		f.clk.Advance(time.Second)
		snap := f.tasks(t, teamID)
		return snap[teamID+"."+teamTestTaskA].State == vendorstore.TaskStateRunning &&
			snap[teamID+"."+teamTestTaskB].State == vendorstore.TaskStateRunning
	})
	queue, err = f.disp.QueueSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queue {
		if q.WaitReason != "" {
			t.Fatalf("queued task after resume = %+v, want no disk wait reason", q)
		}
	}

	// Phase C: a real disk-full worktree failure fails that task failed(disk)
	// with its delegator digest and pauses the rest.
	team2 := f.approve(t, memberTasksArgs(2), decisionApprove, "")
	f.disp.deps.TestWorktreeErr = fmt.Errorf("worktree add: %w", gitops.ErrDiskFull)
	if err := f.disp.Dispatch(ctx, team2); err != nil {
		t.Fatalf("Dispatch with a disk-full worktree failure: %v", err)
	}
	tasks2 := f.tasks(t, team2)
	first, ok := tasks2[team2+".ma"]
	if !ok || first.State != vendorstore.TaskStateFailed || first.FailureCode != failDisk {
		t.Fatalf("first child after disk-full = %+v, want failed(disk)", first)
	}
	second := tasks2[team2+".mb"]
	if second.State != vendorstore.TaskStateQueued {
		t.Fatalf("second child after disk-full = %s, want queued under the pause", second.State)
	}
	queue, err = f.disp.QueueSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawWait bool
	for _, q := range queue {
		if q.TeamID == team2 && q.TaskID == team2+".mb" {
			sawWait = q.Blocked && q.WaitReason == waitDisk
		}
	}
	if !sawWait {
		t.Fatalf("queue snapshot after the failure holds no waiting(%s) child: %+v", waitDisk, queue)
	}
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var leadRecipient bool
	for _, r := range pending {
		if r.TeamID == team2 && r.RecipientTaskID == team2+"."+teamTestLeadTask {
			leadRecipient = true
		}
	}
	if !leadRecipient {
		t.Fatalf("PendingMailbox = %+v, want the failed(disk) digest for the lead", pending)
	}
	mail, err := f.hp.vendorStore.ClaimMailbox(ctx, team2, team2+"."+teamTestLeadTask)
	if err != nil {
		t.Fatal(err)
	}
	digestSeen := false
	for i := range mail {
		var item mailboxDigestItem
		raw, openErr := f.hp.vendorStore.OpenMailboxPayload(ctx, team2, mail[i].MessageID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if json.Unmarshal(raw, &item) == nil && item.State == string(vendorstore.TaskStateFailed) && item.FailureCode == failDisk {
			digestSeen = true
		}
	}
	if !digestSeen {
		t.Fatalf("delegator digest rows = %+v, want a failed(disk) item", mail)
	}

	// Phase D: the failure pause also clears on the re-check once the error
	// is gone, and the remaining child starts.
	f.disp.deps.TestWorktreeErr = nil
	testutil.Spin(t, "failure pause clears and the second child starts", func() bool {
		f.clk.Advance(time.Second)
		return f.tasks(t, team2)[team2+".mb"].State == vendorstore.TaskStateRunning
	})
}
