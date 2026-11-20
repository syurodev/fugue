package vendorstore

import "testing"

const (
	recoveryTestTeamID = "team-unk-q"
	recoveryTestChild  = "task-unk-child"
	recoveryTestLead   = "sess-unk-lead"
)

func TestUnknownTaskQuestionAnsweredTeamIDsByStateAndIdempotentMailbox(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	spec := mustMarshalProtectedSpec(t, []string{"g1", "c1", "s1", "k1", "p1", "q1", "r1", "u1"})
	assertTeamIDsByStateProposed(t, s, spec)
	assertUnknownTaskAnsweredTransition(t, s, spec)
	assertIdempotentDirectMailbox(t, s)
}

func assertTeamIDsByStateProposed(t *testing.T, s *Store, spec []byte) {
	t.Helper()
	ctx := t.Context()
	if _, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-prop", WorkspaceID: testWorkspaceID, SessionID: "sess-p", ProposedEventID: "ev-p",
		Tasks: []NewTaskInput{{
			TaskID: "p-lead", HierarchyRole: RoleLead, CatalogRole: testRoleResearch,
			Deliverable: DeliverableReport, CreatedEventID: "ev-p1", ProtectedSpec: spec,
		}},
	}); err != nil {
		t.Fatalf("CreateTeam(proposed): %v", err)
	}
	propIDs, err := s.TeamIDsByState(ctx, TeamStateProposed)
	if err != nil || len(propIDs) != 1 || propIDs[0] != "team-prop" {
		t.Fatalf("TeamIDsByState(proposed) = (%v, %v), want [team-prop]", propIDs, err)
	}
}

func assertUnknownTaskAnsweredTransition(t *testing.T, s *Store, spec []byte) {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: recoveryTestTeamID, WorkspaceID: testWorkspaceID, SessionID: recoveryTestLead, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{
			{TaskID: testTaskLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: testEvLead, ProtectedSpec: spec},
			{TaskID: recoveryTestChild, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-c1", ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam(recoveryTestTeamID): %v", err)
	}
	appendSeq(t, s, recoveryTestTeamID, rec.LastSeq, []AppendTeamEventInput{
		{EventID: "uq-s", EventType: EventTeamStarted},
		{EventID: "uq-a", TaskID: recoveryTestChild, EventType: EventTaskAssigned, SessionID: "sess-old"},
		{EventID: "uq-r", TaskID: recoveryTestChild, EventType: EventTaskStarted},
		{EventID: "uq-q", TaskID: recoveryTestChild, EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"q"}`)},
	})
	if _, recErr := s.RecoverTeams(ctx); recErr != nil {
		t.Fatalf("RecoverTeams: %v", recErr)
	}
	rec, tasks, err := s.TeamSnapshot(ctx, recoveryTestTeamID)
	if err != nil || tasks[1].State != TaskStateUnknown {
		t.Fatalf("child after recover = (%+v, %v), want unknown", tasks[1], err)
	}
	appendSeq(t, s, recoveryTestTeamID, rec.LastSeq, []AppendTeamEventInput{
		{EventID: "uq-ans", TaskID: recoveryTestChild, EventType: EventTaskQuestionAnswered, SessionID: "sess-respawned", ProtectedPayload: []byte(`{"answer":"ok"}`)},
	})
	if _, tasks, err = s.TeamSnapshot(ctx, recoveryTestTeamID); err != nil || tasks[1].State != TaskStateRunning || tasks[1].SessionID != "sess-respawned" {
		t.Fatalf("child after EventTaskQuestionAnswered = (%+v, %v), want running with sess-respawned", tasks[1], err)
	}
}

func assertIdempotentDirectMailbox(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if firstErr := s.EnqueueDirectMailboxIdempotent(ctx, recoveryTestTeamID, testTaskLeadID, MailboxKindSend, "notice-1", []byte(`{"msg":"hi"}`)); firstErr != nil {
		t.Fatalf("first EnqueueDirectMailboxIdempotent: %v", firstErr)
	}
	if dupErr := s.EnqueueDirectMailboxIdempotent(ctx, recoveryTestTeamID, testTaskLeadID, MailboxKindSend, "notice-1", []byte(`{"msg":"hi"}`)); dupErr != nil {
		t.Fatalf("duplicate EnqueueDirectMailboxIdempotent: %v", dupErr)
	}
	pending, err := s.PendingMailbox(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingMailbox after duplicate idempotent enqueue = (%+v, %v), want 1", pending, err)
	}
}
