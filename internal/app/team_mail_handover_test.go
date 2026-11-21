package app

// The F11.9 mailbox hand-over evidence: a member digest queued while its
// manager was unknown must not strand in the cancelled manager's mailbox —
// the takeover cancel re-addresses it to the adopting lead in the same
// transaction, and the lead's next wake delivers it exactly once.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TestManagerCancelHandsQueuedMailToTheLead pins §12 (F11.9): the member
// reports while its manager is unknown, so its done digest waits in the
// manager's mailbox; the lead cancels the manager and the digest moves to the
// lead with its original message ID; the lead's next wake delivers it exactly
// once, and nothing is duplicated on later passes.
func TestManagerCancelHandsQueuedMailToTheLead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newActiveTeamFixture(t, nil)

	assertMemberDigestQueuedForManager(t, f, ctx)
	digestID := assertCancelHandsDigestToLead(t, f, ctx)
	assertLeadDeliversHandoverOnce(t, f, ctx, digestID)
}

// assertMemberDigestQueuedForManager marks the manager unknown (§12 live
// path) and has the member report done, so its digest waits in the mailbox of
// a manager that can never claim it.
func assertMemberDigestQueuedForManager(t *testing.T, f *activeTeamFixture, ctx context.Context) {
	t.Helper()
	markManagerUnknown(t, f, ctx, "sess-mgr")

	doneRes, err := f.tools.Call(ctx, f.memberBinding, taskDoneToolName, []byte(`{"summary":"queued while the manager was unknown","status":"succeeded"}`))
	if err != nil || doneRes.IsError {
		t.Fatalf("fugue_task_done = (%+v, %v), want success", doneRes, err)
	}
	if mem := takeoverTaskState(t, f, ctx, f.memberTaskID); mem.State != vendorstore.TaskStateChecking {
		t.Fatalf("member after the report = %s, want checking", mem.State)
	}
	testutil.Spin(t, "member digest queued for the manager", func() bool {
		rows, rowErr := f.hp.vendorStore.PendingMailboxRows(ctx, f.teamID, f.managerTaskID)
		return rowErr == nil && len(rows) == 1 && rows[0].Kind == vendorstore.MailboxKindDigest
	})
}

// assertCancelHandsDigestToLead cancels the unknown manager: the running
// child is re-parented and the queued digest is re-addressed to the lead in
// the same transaction. It returns the digest's message ID.
func assertCancelHandsDigestToLead(t *testing.T, f *activeTeamFixture, ctx context.Context) string {
	t.Helper()
	if _, err := f.rt.teamTasks.Recover(ctx, f.leadBinding.SessionID, f.managerTaskID, recoveryDecisionCancel, "lead takes over"); err != nil {
		t.Fatalf("manager recovery cancel: %v", err)
	}
	if mgr := takeoverTaskState(t, f, ctx, f.managerTaskID); mgr.State != vendorstore.TaskStateCancelled {
		t.Fatalf("manager after cancel = %s, want cancelled", mgr.State)
	}
	rows, err := f.hp.vendorStore.PendingMailboxRows(ctx, f.teamID, f.leadTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var digestID string
	for i := range rows {
		if rows[i].Kind == vendorstore.MailboxKindDigest {
			digestID = rows[i].MessageID
		}
	}
	if digestID == "" {
		t.Fatalf("lead pending rows = %+v, want the re-addressed member digest", rows)
	}
	if orphaned, orphanErr := f.hp.vendorStore.PendingMailboxRows(ctx, f.teamID, f.managerTaskID); orphanErr != nil || len(orphaned) != 0 {
		t.Fatalf("manager pending rows after the hand-over = (%+v, %v), want none", orphaned, orphanErr)
	}
	return digestID
}

// assertLeadDeliversHandoverOnce interrupts the lead's held turn, waits for
// the wake pump to deliver the re-addressed batch, and asserts the digest
// appears in the lead session's transcript exactly once with no rows left
// pending or claimable.
func assertLeadDeliversHandoverOnce(t *testing.T, f *activeTeamFixture, ctx context.Context, digestID string) {
	t.Helper()
	if _, intErr := f.svc.Interrupt(ctx, testAppPrincipal, VendorStop{CommandID: "handover-int", SessionID: f.session.ID}); intErr != nil {
		t.Fatalf("interrupt the lead's held turn: %v", intErr)
	}
	testutil.Spin(t, "lead session ready for its batch", func() bool {
		snap, snapErr := f.hp.vendorStore.Snapshot(ctx, f.session.ID)
		return snapErr == nil && snap.State == vendorstore.StateReady
	})

	var delivered []string
	testutil.Spin(t, "wake delivers the hand-over batch to the lead", func() bool {
		f.clk.Advance(5 * time.Second)
		if pumpErr := f.rt.teamWake.Pump(ctx); pumpErr != nil {
			return false
		}
		delivered = leadBatchMessages(ctx, t, f, f.session.ID)
		return len(delivered) > 0
	})
	assertDigestDeliveredOnce(t, delivered)
	assertHandoverMailboxDrained(t, f, ctx, digestID)
}

// assertDigestDeliveredOnce counts the member digest in the delivered batch
// texts: exactly one occurrence.
func assertDigestDeliveredOnce(t *testing.T, delivered []string) {
	t.Helper()
	hits := 0
	for i := range delivered {
		if strings.Contains(delivered[i], "queued while the manager was unknown") {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("hand-over digest occurrences in the lead transcript = %d (%+v), want exactly once", hits, delivered)
	}
}

// assertHandoverMailboxDrained asserts no mail is left pending or claimable
// after the delivery and that the digest keeps its original message ID.
func assertHandoverMailboxDrained(t *testing.T, f *activeTeamFixture, ctx context.Context, digestID string) {
	t.Helper()
	if pending, pendErr := f.hp.vendorStore.PendingMailbox(ctx); pendErr != nil || len(pending) != 0 {
		t.Fatalf("PendingMailbox after delivery = (%+v, %v), want empty", pending, pendErr)
	}
	if rows, rowErr := f.hp.vendorStore.PendingMailboxRows(ctx, f.teamID, f.leadTaskID); rowErr != nil || len(rows) != 0 {
		t.Fatalf("lead pending rows after delivery = (%+v, %v), want none", rows, rowErr)
	}
	raw, openErr := f.hp.vendorStore.OpenMailboxPayload(ctx, f.teamID, digestID)
	if openErr != nil {
		t.Fatalf("the delivered digest lost its idempotency key %s: %v", digestID, openErr)
	}
	if !strings.Contains(string(raw), f.memberTaskID) {
		t.Fatalf("delivered digest payload = %q, want the member as its subject", raw)
	}
}

// leadBatchMessages returns the text of the session's delivered fugue-origin
// batches (user messages carrying the batch marker).
func leadBatchMessages(ctx context.Context, t *testing.T, f *activeTeamFixture, sessionID string) []string {
	t.Helper()
	var out []string
	for before := int64(0); ; {
		page, err := f.hp.vendorStore.Transcript(ctx, sessionID, before, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return out
		}
		out = appendBatchTexts(ctx, t, f, sessionID, page, out)
		before = page[0].Sequence
	}
}

// appendBatchTexts appends the batch texts of one transcript page.
func appendBatchTexts(ctx context.Context, t *testing.T, f *activeTeamFixture, sessionID string, page []vendorstore.StoredItem, out []string) []string {
	t.Helper()
	for i := range page {
		if page[i].Kind != vendorstore.KindUserMessage || page[i].TurnOrigin != vendorstore.TurnOriginFugue {
			continue
		}
		text := leadUserMessageText(ctx, t, f, sessionID, page[i].ID)
		if strings.Contains(text, "[fugue-batch ") {
			out = append(out, text)
		}
	}
	return out
}

// leadUserMessageText opens one transcript item's user text.
func leadUserMessageText(ctx context.Context, t *testing.T, f *activeTeamFixture, sessionID, id string) string {
	t.Helper()
	_, content, err := f.hp.vendorStore.ReadItem(ctx, sessionID, id)
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Text string `json:"Text"`
	}
	if json.Unmarshal(content, &msg) != nil {
		return string(content)
	}
	return msg.Text
}

func takeoverTaskState(t *testing.T, f *activeTeamFixture, ctx context.Context, taskID string) vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i]
		}
	}
	t.Fatalf("task %s not found", taskID)
	return vendorstore.TaskRecord{}
}
