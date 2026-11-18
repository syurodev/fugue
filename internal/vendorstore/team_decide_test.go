package vendorstore

import (
	"errors"
	"testing"
)

// Shared literals of the decide and amend store tests.
const (
	decideTestLeadTask  = "d-lead"
	decideTestSession   = "sess-1"
	decideTestSpecAmend = `{"goal":"amended"}`
	decideTestSpecNew   = `{"goal":"new"}`
)

func decideTestTasks() []NewTaskInput {
	return []NewTaskInput{
		{TaskID: decideTestLeadTask, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-dl", ProtectedSpec: []byte(`{"goal":"lead"}`)},
		{TaskID: "d-mem", ParentTaskID: decideTestLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-dm", ProtectedSpec: []byte(`{"goal":"read"}`)},
	}
}

func createDecideTeam(t *testing.T, s *Store, teamID string) (TeamRecord, []TaskRecord) {
	t.Helper()
	rec, tasks, err := s.CreateTeam(t.Context(), &NewTeamInput{
		TeamID: teamID, WorkspaceID: testWorkspaceID, SessionID: decideTestSession, ProposedEventID: "ev-dp",
		Tasks: decideTestTasks(),
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	return rec, tasks
}

// TestDecideTeamActivatesAndSealsThePayload pins the approve append: a
// proposed team moves to active in the same transaction as its TeamStarted
// event with the sealed payload, and the tasks stay queued.
func TestDecideTeamActivatesAndSealsThePayload(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-decide")
	next, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-decide", StartedPayload: []byte(`{"decision":"approve"}`),
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	got, tasks, err := s.TeamSnapshot(t.Context(), rec.TeamID)
	if err != nil {
		t.Fatalf("TeamSnapshot: %v", err)
	}
	if got.State != TeamStateActive || got.LastSeq != next {
		t.Fatalf("team = %s last_seq %d, want active at %d", got.State, got.LastSeq, next)
	}
	for i := range tasks {
		if tasks[i].State != TaskStateQueued {
			t.Fatalf("task %s = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
	payload, err := s.OpenTeamEventPayload(t.Context(), rec.TeamID, got.LastSeq)
	if err != nil {
		t.Fatalf("OpenTeamEventPayload: %v", err)
	}
	if string(payload) != `{"decision":"approve"}` {
		t.Fatalf("sealed payload = %s, want the decision document", payload)
	}
}

// TestDecideTeamRefusals pins the typed refusals: a decision on an
// already-active team, on a missing team and a sequence conflict all fail
// without appending.
func TestDecideTeamRefusals(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-decide-ref")
	if _, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-ok", StartedPayload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("the first decision failed: %v", err)
	}
	cur, _, snapErr := s.TeamSnapshot(t.Context(), rec.TeamID)
	if snapErr != nil {
		t.Fatalf("TeamSnapshot: %v", snapErr)
	}
	if _, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: cur.LastSeq, DecisionEventID: "ev-twice", StartedPayload: []byte(`{}`),
	}); !errors.Is(err, ErrTeamTransition) {
		t.Fatalf("second decision error = %v, want ErrTeamTransition", err)
	}
	if _, err := s.DecideTeam(t.Context(), "gone", &TeamDecisionInput{
		ExpectedSeq: 1, DecisionEventID: "ev-x", StartedPayload: []byte(`{}`),
	}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team error = %v, want ErrTeamNotFound", err)
	}
	if _, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: 1, DecisionEventID: "ev-stale", StartedPayload: []byte(`{}`),
	}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("stale sequence error = %v, want ErrSequenceConflict", err)
	}
	if _, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{ExpectedSeq: rec.LastSeq}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("missing payload error = %v, want ErrTeamInvalidInput", err)
	}
}

// TestAmendTeamResealsAndInserts pins the amend append: an active team stays
// active, the re-sealed spec opens with the amended content, inserted tasks
// arrive queued with their events, and the record's task count moves.
func TestAmendTeamResealsAndInserts(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-amend")
	started, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-amend-start", StartedPayload: []byte(`{"decision":"approve"}`),
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	next, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: started, AmendEventID: "ev-amend", AmendPayload: []byte(`{"role_edits":[{"name":"researcher"}]}`),
		ResealedSpecs: []ResealedTaskSpec{{TaskID: decideTestLeadTask, ProtectedSpec: []byte(decideTestSpecAmend)}},
		AddedTasks: []NewTaskInput{{
			TaskID: "d-new", ParentTaskID: decideTestLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
			Deliverable: DeliverableReport, CreatedEventID: "ev-dn", ProtectedSpec: []byte(decideTestSpecNew),
		}},
	})
	if err != nil {
		t.Fatalf("AmendTeam: %v", err)
	}
	assertAmendedTeam(t, s, rec.TeamID, next)
}

// assertAmendedTeam asserts the amended team's state, task count, re-sealed
// spec and inserted task.
func assertAmendedTeam(t *testing.T, s *Store, teamID string, wantLastSeq int64) {
	t.Helper()
	got, tasks, err := s.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatalf("TeamSnapshot: %v", err)
	}
	if got.State != TeamStateActive || got.TaskCount != 3 {
		t.Fatalf("team = %s with %d tasks, want active with 3", got.State, got.TaskCount)
	}
	if got.LastSeq != wantLastSeq {
		t.Fatalf("last_seq = %d, want %d", got.LastSeq, wantLastSeq)
	}
	spec, err := s.OpenTaskSpec(t.Context(), teamID, decideTestLeadTask)
	if err != nil {
		t.Fatalf("OpenTaskSpec: %v", err)
	}
	if string(spec) != decideTestSpecAmend {
		t.Fatalf("re-sealed spec = %s, want the amended goal", spec)
	}
	added, err := s.OpenTaskSpec(t.Context(), teamID, "d-new")
	if err != nil {
		t.Fatalf("OpenTaskSpec(inserted): %v", err)
	}
	if string(added) != decideTestSpecNew {
		t.Fatalf("inserted spec = %s, want the new goal", added)
	}
	for i := range tasks {
		if tasks[i].State != TaskStateQueued {
			t.Fatalf("task %s = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
}

// TestAmendTeamRefusals pins the amend guard rails on a still-proposed team
// and on stale input: the transition refusal and the sequence conflict.
func TestAmendTeamRefusals(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-amend-ref")
	if _, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: rec.LastSeq, AmendEventID: "ev-early", AmendPayload: []byte(`{}`),
	}); !errors.Is(err, ErrTeamTransition) {
		t.Fatalf("amend on proposed error = %v, want ErrTeamTransition", err)
	}
	started, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-amend-ref-start", StartedPayload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	if _, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: 1, AmendEventID: "ev-stale-amend", AmendPayload: []byte(`{}`),
	}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("stale amend sequence error = %v, want ErrSequenceConflict", err)
	}
	if _, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{ExpectedSeq: started}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("missing amend payload error = %v, want ErrTeamInvalidInput", err)
	}
	if _, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: started, AmendEventID: "ev-amend-r2",
		AmendPayload: []byte(`{}`), ResealedSpecs: []ResealedTaskSpec{{TaskID: decideTestLeadTask}},
	}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("empty reseal payload error = %v, want ErrTeamInvalidInput", err)
	}
	if _, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
		ExpectedSeq: started, AmendEventID: "ev-amend-r1",
		AmendPayload: []byte(`{}`), ResealedSpecs: []ResealedTaskSpec{{TaskID: "gone", ProtectedSpec: []byte(`{}`)}},
	}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("unknown reseal error = %v, want ErrTaskNotFound", err)
	}
}

// TestAmendTeamInsertionRefusals pins the insertion guard rails: a duplicate
// task ID, a solo task joining a team, a missing parent and a missing
// depends_on target all fail typed and append nothing.
func TestAmendTeamInsertionRefusals(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-amend-ins")
	started, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-amend-ins-start", StartedPayload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	insert := func(eventID string, task NewTaskInput) error {
		_, err := s.AmendTeam(t.Context(), rec.TeamID, &TeamAmendInput{
			ExpectedSeq: started, AmendEventID: eventID, AmendPayload: []byte(`{}`),
			AddedTasks: []NewTaskInput{task},
		})
		return err
	}
	dup := insert("ev-amend-r3", NewTaskInput{
		TaskID: decideTestLeadTask, HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
		Deliverable: DeliverableReport, CreatedEventID: "ev-x", ProtectedSpec: []byte(`{}`),
	})
	if !errors.Is(dup, ErrTaskExists) {
		t.Fatalf("duplicate ID error = %v, want ErrTaskExists", dup)
	}
	solo := insert("ev-amend-r4", NewTaskInput{
		TaskID: "d-solo", HierarchyRole: RoleSolo, CatalogRole: testRoleResearch,
		Deliverable: DeliverableReport, CreatedEventID: "ev-y", ProtectedSpec: []byte(`{}`),
	})
	if !errors.Is(solo, ErrTeamInvalidInput) {
		t.Fatalf("solo insertion error = %v, want ErrTeamInvalidInput", solo)
	}
	orphan := insert("ev-amend-r5", NewTaskInput{
		TaskID: "d-orphan", ParentTaskID: "no-such", HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
		Deliverable: DeliverableReport, CreatedEventID: "ev-z", ProtectedSpec: []byte(`{}`),
	})
	if !errors.Is(orphan, ErrTeamInvalidInput) {
		t.Fatalf("missing parent error = %v, want ErrTeamInvalidInput", orphan)
	}
	missing := insert("ev-amend-r6", NewTaskInput{
		TaskID: "d-dep", HierarchyRole: RoleMember, CatalogRole: testRoleResearch,
		Deliverable: DeliverableReport, DependsOn: []string{"no-such"}, CreatedEventID: "ev-w", ProtectedSpec: []byte(`{}`),
	})
	if !errors.Is(missing, ErrTeamInvalidInput) {
		t.Fatalf("missing dependency error = %v, want ErrTeamInvalidInput", missing)
	}
}

// TestActiveTeamForSession pins the tool-offering query: it returns the
// session's one active team and no row before the decision or after the end.
func TestActiveTeamForSession(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec, _ := createDecideTeam(t, s, "team-active")
	if _, ok, err := s.ActiveTeamForSession(t.Context(), decideTestSession); err != nil || ok {
		t.Fatalf("before the decision ok = %v err = %v, want no active team", ok, err)
	}
	if _, err := s.DecideTeam(t.Context(), rec.TeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "ev-active-start", StartedPayload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("DecideTeam: %v", err)
	}
	got, ok, err := s.ActiveTeamForSession(t.Context(), decideTestSession)
	if err != nil || !ok || got.TeamID != rec.TeamID {
		t.Fatalf("active team = %v ok = %v err = %v, want %s", got, ok, err, rec.TeamID)
	}
	if _, ok, err := s.ActiveTeamForSession(t.Context(), "other"); err != nil || ok {
		t.Fatalf("other session ok = %v err = %v, want no active team", ok, err)
	}
}
