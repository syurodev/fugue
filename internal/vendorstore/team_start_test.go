package vendorstore

import (
	"slices"
	"testing"
)

// TestAssignedTaskRecordsItsSessionAndAmendedTasksStartExplicitly pins the
// F11.3a store half: TaskAssigned projects the session onto the task row and
// TaskForSession reads it back; a task the decision adds is auto-started and
// one an amend adds is marked for its delegator's explicit start (Q-116).
func TestAssignedTaskRecordsItsSessionAndAmendedTasksStartExplicitly(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-start")
	added := func(id, ev string) NewTaskInput {
		return NewTaskInput{
			TaskID: id, ParentTaskID: decideTestLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
			Deliverable: DeliverableReport, CreatedEventID: ev, ProtectedSpec: []byte(decideTestSpecNew),
		}
	}
	seq, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-start-d", StartedPayload: []byte(`{"decision":"approve"}`),
		AddedTasks: []NewTaskInput{added("d-decided", "ev-dd")},
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	if seq, err = s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: seq, AmendEventID: "ev-start-a", AmendPayload: []byte(`{"amend":true}`),
		AddedTasks: []NewTaskInput{added("d-amended", "ev-da")},
	}); err != nil {
		t.Fatalf("AmendTeam: %v", err)
	}
	if _, err = s.AppendTeamEvent(t.Context(), rec.TeamID, seq, &AppendTeamEventInput{
		EventID: "ev-assign", TaskID: decideTestLeadTask, EventType: EventTaskAssigned, SessionID: "sess-lead",
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	assertStartState(t, s, rec.TeamID)
}

// assertStartState asserts the explicit-start marks, the session lookup and
// the active-team listing.
func assertStartState(t *testing.T, s *Store, teamID string) {
	t.Helper()
	_, tasks, err := s.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	explicit := map[string]bool{}
	for i := range tasks {
		explicit[tasks[i].TaskID] = tasks[i].ExplicitStart
	}
	if explicit["d-decided"] || !explicit["d-amended"] || explicit[decideTestLeadTask] {
		t.Fatalf("explicit starts = %v, want only the amended task", explicit)
	}
	assertSessionLookup(t, s)
	if ids, err := s.ActiveTeamIDs(t.Context()); err != nil || !slices.Contains(ids, teamID) {
		t.Fatalf("ActiveTeamIDs = %v, %v", ids, err)
	}
}

func assertSessionLookup(t *testing.T, s *Store) {
	t.Helper()
	task, found, err := s.TaskForSession(t.Context(), "sess-lead")
	if err != nil || !found || task.TaskID != decideTestLeadTask || task.SessionID != "sess-lead" {
		t.Fatalf("TaskForSession = %+v, %v, %v", task, found, err)
	}
	if _, found, err = s.TaskForSession(t.Context(), "sess-none"); err != nil || found {
		t.Fatalf("TaskForSession of a plain session = %v, %v; want none", found, err)
	}
}
