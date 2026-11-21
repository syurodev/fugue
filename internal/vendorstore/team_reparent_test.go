package vendorstore

import "testing"

// Shared literals of the F11.9 re-parent store tests (§12 takeover, Q-159 A).
const (
	reparentTeamID    = "team-reparent"
	reparentLeadTask  = "r-lead"
	reparentMgrTask   = "r-mgr"
	reparentMemTask   = "r-mem"
	reparentDoneTask  = "r-done"
	reparentLeadSess  = "sess-r-lead"
	reparentMgrSess   = "sess-r-mgr"
	reparentMemSess   = "sess-r-mem"
	reparentDoneSess  = "sess-r-done"
	reparentTeamSeqEv = "ev-r-start"
)

func reparentTestTasks() []NewTaskInput {
	spec := []byte(`{"goal":"reparent"}`)
	return []NewTaskInput{
		{TaskID: reparentLeadTask, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-rl", ProtectedSpec: spec},
		{TaskID: reparentMgrTask, ParentTaskID: reparentLeadTask, HierarchyRole: RoleTeamManager, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-rm", ProtectedSpec: spec},
		{TaskID: reparentMemTask, ParentTaskID: reparentMgrTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-rmem", ProtectedSpec: spec},
		{TaskID: reparentDoneTask, ParentTaskID: reparentMgrTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-rdone", ProtectedSpec: spec},
	}
}

func createReparentTeam(t *testing.T, s *Store) TeamRecord {
	t.Helper()
	rec, _, err := s.CreateTeam(t.Context(), &NewTeamInput{
		TeamID: reparentTeamID, WorkspaceID: testWorkspaceID, SessionID: reparentLeadSess, ProposedEventID: "ev-rp",
		Tasks: reparentTestTasks(),
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	evs := []AppendTeamEventInput{
		{EventID: reparentTeamSeqEv, EventType: EventTeamStarted},
		{EventID: "ev-r-a0", TaskID: reparentLeadTask, EventType: EventTaskAssigned, SessionID: reparentLeadSess},
		{EventID: "ev-r-s0", TaskID: reparentLeadTask, EventType: EventTaskStarted},
		{EventID: "ev-r-a1", TaskID: reparentMgrTask, EventType: EventTaskAssigned, SessionID: reparentMgrSess},
		{EventID: "ev-r-s1", TaskID: reparentMgrTask, EventType: EventTaskStarted},
		{EventID: "ev-r-a2", TaskID: reparentMemTask, EventType: EventTaskAssigned, SessionID: reparentMemSess},
		{EventID: "ev-r-s2", TaskID: reparentMemTask, EventType: EventTaskStarted},
		{EventID: "ev-r-a3", TaskID: reparentDoneTask, EventType: EventTaskAssigned, SessionID: reparentDoneSess},
		{EventID: "ev-r-s3", TaskID: reparentDoneTask, EventType: EventTaskStarted},
		{EventID: "ev-r-rep", TaskID: reparentDoneTask, EventType: EventTaskReported},
		{EventID: "ev-r-chk", TaskID: reparentDoneTask, EventType: EventTaskCheckCompleted, Outcome: "passed"},
		{EventID: "ev-r-end", TaskID: reparentDoneTask, EventType: EventTaskEnded, Outcome: outcomeDone},
		{EventID: "ev-r-unk", TaskID: reparentMgrTask, EventType: EventTaskMarkedUnknown},
	}
	seq := rec.LastSeq
	for i := range evs {
		if seq, err = s.AppendTeamEvent(t.Context(), reparentTeamID, seq, &evs[i]); err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", evs[i].EventID, err)
		}
	}
	rec, _, err = s.TeamSnapshot(t.Context(), reparentTeamID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func reparentTaskState(t *testing.T, s *Store, taskID string) TaskRecord {
	t.Helper()
	_, tasks, err := s.TeamSnapshot(t.Context(), reparentTeamID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i]
		}
	}
	t.Fatalf("task %s not found", taskID)
	return TaskRecord{}
}

// TestManagerCancelRecoversByReparentingLiveChildren pins the §12 takeover
// projection (Q-159 A): the cancel recovery decision on an unknown team
// manager cancels only the manager; its non-terminal children are re-parented
// to the lead in the same transaction, becoming depth-1 members, while its
// terminal children keep their history.
func TestManagerCancelRecoversByReparentingLiveChildren(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createReparentTeam(t, s)

	seq, err := s.AppendTeamEvent(t.Context(), reparentTeamID, rec.LastSeq, &AppendTeamEventInput{
		EventID: "ev-r-cancel", TaskID: reparentMgrTask, EventType: EventTaskRecoveryDecided,
		Outcome: RecoveryOutcomeCancel, ReparentTo: reparentLeadTask, SessionID: reparentMgrSess,
	})
	if err != nil {
		t.Fatalf("manager recovery cancel with re-parent: %v", err)
	}

	if mgr := reparentTaskState(t, s, reparentMgrTask); mgr.State != TaskStateCancelled {
		t.Fatalf("manager state = %s, want cancelled", mgr.State)
	}
	mem := reparentTaskState(t, s, reparentMemTask)
	if mem.State != TaskStateRunning || mem.ParentTaskID != reparentLeadTask || mem.Depth != 1 {
		t.Fatalf("live child = %+v, want running under the lead at depth 1", mem)
	}
	if done := reparentTaskState(t, s, reparentDoneTask); done.ParentTaskID != reparentMgrTask || done.State != TaskStateDone {
		t.Fatalf("terminal child = %+v, want done under the manager", done)
	}

	// The moved child is now the lead's direct child: its session is bound
	// and a lead-delegator check resolves through the normal chain.
	if task, found, lookupErr := s.TaskForSession(t.Context(), reparentMemSess); lookupErr != nil || !found || task.ParentTaskID != reparentLeadTask {
		t.Fatalf("TaskForSession after re-parent = (%+v, %v, %v)", task, found, lookupErr)
	}
	if _, err = s.AppendTeamEvent(t.Context(), reparentTeamID, seq, &AppendTeamEventInput{
		EventID: "ev-r-cancel-2", TaskID: reparentMgrTask, EventType: EventTaskRecoveryDecided, Outcome: outcomeCancelled, ReparentTo: reparentLeadTask,
	}); err == nil {
		t.Fatal("a cancelled manager accepted a second recovery decision")
	}
}

// TestReparentToIsRejectedOutsideManagerCancel pins the input validation: a
// ReparentTo on any event but a team manager's recovery cancel, or with a
// non-lead adopter, is refused before any row changes.
func TestReparentToIsRejectedOutsideManagerCancel(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createReparentTeam(t, s)

	cases := []struct {
		name string
		ev   AppendTeamEventInput
	}{
		{
			name: "task ended event",
			ev:   AppendTeamEventInput{EventID: "ev-bad-1", TaskID: reparentMgrTask, EventType: EventTaskEnded, Outcome: outcomeCancelled, ReparentTo: reparentLeadTask},
		},
		{
			name: "member recovery cancel",
			ev:   AppendTeamEventInput{EventID: "ev-bad-2", TaskID: reparentMemTask, EventType: EventTaskRecoveryDecided, Outcome: RecoveryOutcomeCancel, ReparentTo: reparentLeadTask},
		},
		{
			name: "non-lead adopter",
			ev:   AppendTeamEventInput{EventID: "ev-bad-3", TaskID: reparentMgrTask, EventType: EventTaskRecoveryDecided, Outcome: RecoveryOutcomeCancel, ReparentTo: reparentMgrTask},
		},
	}
	for _, tc := range cases {
		if _, err := s.AppendTeamEvent(t.Context(), reparentTeamID, rec.LastSeq, &tc.ev); err == nil {
			t.Fatalf("%s: append accepted ReparentTo", tc.name)
		}
		if mem := reparentTaskState(t, s, reparentMemTask); mem.ParentTaskID != reparentMgrTask {
			t.Fatalf("%s: member parent changed to %s", tc.name, mem.ParentTaskID)
		}
	}
}

// TestManagerCancelReaddressesQueuedMailToTheLead pins the mailbox half of
// the takeover cancel (F11.9): the manager's pending, unclaimed rows are
// re-addressed to the adopting lead in the same transaction, keeping their
// message IDs and creation order; rows the lead itself sent (named in the
// keep list) stay with the manager, and rows a batch already claimed are
// left alone so a delivery never duplicates.
func TestManagerCancelReaddressesQueuedMailToTheLead(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createReparentTeam(t, s)

	// A row the wake already claimed and delivered (never re-addressed nor
	// duplicated), the child's done digest queued while the manager was
	// unknown, and the lead's own message to the manager.
	claimedDigestID := reparentTeamID + ":claimed-digest:" + reparentMgrTask
	if err := s.EnqueueDirectMailbox(t.Context(), reparentTeamID, reparentMgrTask, MailboxKindDigest, claimedDigestID,
		[]byte(`{"task_id":"`+reparentMemTask+`","state":"checking"}`)); err != nil {
		t.Fatalf("enqueue %s: %v", claimedDigestID, err)
	}
	claimed, err := s.ClaimMailbox(t.Context(), reparentTeamID, reparentMgrTask)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimMailbox = (%d rows, %v), want the one queued row", len(claimed), err)
	}
	if err = s.MarkMailboxBatchDelivered(t.Context(), claimed[0].BatchID); err != nil {
		t.Fatal(err)
	}
	for _, mail := range []struct{ id, kind string }{
		{"child-done-digest", MailboxKindDigest},
		{"lead-own-send", MailboxKindSend},
	} {
		if err := s.EnqueueDirectMailbox(t.Context(), reparentTeamID, reparentMgrTask, mail.kind, mail.id,
			[]byte(`{"task_id":"`+reparentMemTask+`","state":"checking"}`)); err != nil {
			t.Fatalf("enqueue %s: %v", mail.id, err)
		}
	}
	queued, err := s.PendingMailboxRows(t.Context(), reparentTeamID, reparentMgrTask)
	if err != nil || len(queued) != 2 {
		t.Fatalf("PendingMailboxRows before the cancel = (%d rows, %v), want the two queued rows", len(queued), err)
	}
	var digestID, leadSendID string
	for i := range queued {
		switch queued[i].Kind {
		case MailboxKindDigest:
			digestID = queued[i].MessageID
		case MailboxKindSend:
			leadSendID = queued[i].MessageID
		}
	}
	if digestID == "" || leadSendID == "" {
		t.Fatalf("queued rows = %+v, want one digest and one send", queued)
	}

	seq, err := s.AppendTeamEvent(t.Context(), reparentTeamID, rec.LastSeq, &AppendTeamEventInput{
		EventID: "ev-r-cancel-mail", TaskID: reparentMgrTask, EventType: EventTaskRecoveryDecided,
		Outcome: RecoveryOutcomeCancel, ReparentTo: reparentLeadTask, SessionID: reparentMgrSess,
		ReaddressMailboxFrom: reparentMgrTask, ReaddressMailboxKeep: []string{leadSendID},
	})
	if err != nil {
		t.Fatalf("manager cancel with re-address: %v", err)
	}

	rows, err := s.PendingMailboxRows(t.Context(), reparentTeamID, reparentLeadTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].MessageID != digestID {
		t.Fatalf("lead pending rows = %+v, want exactly the moved digest with its original ID", rows)
	}
	kept, err := s.PendingMailboxRows(t.Context(), reparentTeamID, reparentMgrTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].MessageID != leadSendID {
		t.Fatalf("manager pending rows = %+v, want only the lead's own send kept", kept)
	}
	if rows[0].CreatedAt != kept[0].CreatedAt {
		t.Fatal("the re-addressed digest lost its creation order")
	}
	if _, err = s.AppendTeamEvent(t.Context(), reparentTeamID, seq, &AppendTeamEventInput{
		EventID: "ev-r-mail-bad", TaskID: reparentLeadTask, EventType: EventTaskEnded, Outcome: outcomeCancelled,
		ReaddressMailboxFrom: reparentMgrTask,
	}); err == nil {
		t.Fatal("a non-cancel event accepted ReaddressMailboxFrom")
	}
}
