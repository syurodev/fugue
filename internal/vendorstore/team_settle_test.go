package vendorstore

import (
	"errors"
	"testing"
)

// settleTestTasks lists a lead and two members; the member IDs sort before
// the lead, so a settlement visits them first.
const settleLeadTask = "s-lead"

func settleTestTasks() []NewTaskInput {
	return []NewTaskInput{
		{TaskID: settleLeadTask, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-sl", ProtectedSpec: []byte("{}")},
		{TaskID: "a-mem", ParentTaskID: settleLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-sa", ProtectedSpec: []byte("{}")},
		{TaskID: "b-mem", ParentTaskID: settleLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-sb", ProtectedSpec: []byte("{}")},
	}
}

func createSettleTeam(t *testing.T, s *Store, teamID string) TeamRecord {
	t.Helper()
	rec, _, err := s.CreateTeam(t.Context(), &NewTeamInput{
		TeamID: teamID, WorkspaceID: testWorkspaceID, SessionID: decideTestSession, ProposedEventID: "ev-sp", Tasks: settleTestTasks(),
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	return rec
}

// TestSettleTeamCancelsTasksThenEndsTheTeam pins Q-84 in one transaction:
// every task cancels first and TeamEnded is the last event.
func TestSettleTeamCancelsTasksThenEndsTheTeam(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createSettleTeam(t, s, "team-settle-ok")
	last, err := s.SettleTeam(t.Context(), rec.TeamID, &TeamSettleInput{ExpectedSeq: rec.LastSeq, EndEventID: "ev-end", EndPayload: []byte(`{"decision":"rejected"}`)})
	if err != nil {
		t.Fatalf("SettleTeam: %v", err)
	}
	got, tasks, err := s.TeamSnapshot(t.Context(), rec.TeamID)
	if err != nil || got.State != TeamStateCancelled || got.LastSeq != last {
		t.Fatalf("team = (%+v, %v), want cancelled at %d", got, err, last)
	}
	for i := range tasks {
		if tasks[i].State != TaskStateCancelled {
			t.Fatalf("task %s = %s, want cancelled", tasks[i].TaskID, tasks[i].State)
		}
	}
	events, err := s.TeamEventsSince(t.Context(), rec.TeamID, rec.LastSeq, 50)
	if err != nil || len(events) != 4 || events[3].EventType != EventTeamEnded {
		t.Fatalf("settle events = (%+v, %v), want 3 task cancels then TeamEnded", events, err)
	}
	for _, ev := range events[:3] {
		if ev.EventType != EventTaskEnded {
			t.Fatalf("event %d = %s, want TaskEnded before TeamEnded", ev.Sequence, ev.EventType)
		}
	}
}

// TestSettleTeamFailureLeavesNoPartialEvents pins the atomicity: a task that
// cannot be cancelled (unknown awaits its recovery decision) fails the whole
// settlement after earlier tasks were already cancelled in the transaction,
// and nothing of it persists.
func TestSettleTeamFailureLeavesNoPartialEvents(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createSettleTeam(t, s, "team-settle-fail")
	seq := appendSeq(t, s, rec.TeamID, rec.LastSeq, []AppendTeamEventInput{
		{EventID: "sf-start", EventType: EventTeamStarted},
		{EventID: "sf-1", TaskID: settleLeadTask, EventType: EventTaskAssigned},
		{EventID: "sf-2", TaskID: settleLeadTask, EventType: EventTaskStarted},
		{EventID: "sf-3", TaskID: settleLeadTask, EventType: EventTaskMarkedUnknown},
	})
	_, err := s.SettleTeam(t.Context(), rec.TeamID, &TeamSettleInput{ExpectedSeq: seq, EndEventID: "ev-end-fail", EndPayload: []byte(`{"decision":"rejected"}`)})
	if err == nil {
		t.Fatal("SettleTeam with an unknown task succeeded, want a typed failure")
	}
	if errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("SettleTeam error = %v, want a transition failure, not an input refusal", err)
	}
	got, tasks, snapErr := s.TeamSnapshot(t.Context(), rec.TeamID)
	if snapErr != nil || got.State != TeamStateActive || got.LastSeq != seq {
		t.Fatalf("team after the failed settle = (%+v, %v), want active at %d", got, snapErr, seq)
	}
	for i := range tasks {
		if tasks[i].TaskID != settleLeadTask && tasks[i].State != TaskStateQueued {
			t.Fatalf("task %s = %s after the failed settle, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
	events, evErr := s.TeamEventsSince(t.Context(), rec.TeamID, seq, 50)
	if evErr != nil || len(events) != 0 {
		t.Fatalf("events after the failed settle = (%+v, %v), want none", events, evErr)
	}
}

// TestDecideTeamRemovesSeveralTasksAndInserts pins the sequence bookkeeping
// of a decision that cancels two tasks and inserts one: every cancel gets its
// own event ID and the sequence stays contiguous through the insertion.
func TestDecideTeamRemovesSeveralTasksAndInserts(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := createSettleTeam(t, s, "team-decide-many")
	last, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-dm", StartedPayload: []byte(`{"decision":"approve_edited"}`),
		RemovedTaskIDs: []string{"a-mem", "b-mem"},
		AddedTasks: []NewTaskInput{{
			TaskID: "c-mem", ParentTaskID: settleLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
			Deliverable: DeliverableReport, CreatedEventID: "ev-sc", ProtectedSpec: []byte("{}"),
		}},
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	got, tasks, err := s.TeamSnapshot(t.Context(), rec.TeamID)
	if err != nil || got.LastSeq != last || got.State != TeamStateActive || len(tasks) != 4 {
		t.Fatalf("team = (%+v, %v) with %d tasks, want active at %d with 4 tasks", got, err, len(tasks), last)
	}
}
