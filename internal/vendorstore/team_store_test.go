package vendorstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	testWorkspaceID   = "ws-1"
	testRoleResearch  = "researcher"
	testRoleCoder     = "coder"
	testEvProposed    = "ev-p"
	testEvLead        = "ev-l"
	testOutcomePassed = "passed"
	testTaskLeadID    = "t-lead"
	testTaskMgrID     = "t-mgr"
	testTaskChildID   = "t-child"
	testTaskStartID   = "t-starting"
	testTaskRunID     = "t-running"
	testTaskAskID     = "t-asking"
	testTaskCheckID   = "t-checking"
	testTaskReviewID  = "t-review"
)

var errSimulatedIO = errors.New("simulated io failure")

func TestTeamStoreKeepsContentOutOfMetadataColumns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, path := protectedStore(t)

	markers := []string{
		"SECRET_GOAL_PLANT_alpha91",
		"SECRET_CONTEXT_PLANT_bravo82",
		"SECRET_SCOPE_GLOB_charlie73/*.go",
		"SECRET_CHECK_CMD_delta64",
		"SECRET_SCHEMA_PROP_echo55",
		"SECRET_QUESTION_TEXT_foxtrot46",
		"SECRET_REPORT_TEXT_golf37",
	}
	specPayload := mustMarshalProtectedSpec(t, markers)
	rec, tasks, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-inv09", WorkspaceID: testWorkspaceID, SessionID: "sess-1", ProposedEventID: "ev-propose",
		Tasks: []NewTaskInput{{
			TaskID: "task-lead-inv", HierarchyRole: RoleLead, CatalogRole: testRoleResearch,
			Deliverable: DeliverableReport, CreatedEventID: "ev-task-lead", ProtectedSpec: specPayload,
		}},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	seq := appendPlantedTeamEvents(t, s, rec.LastSeq, "team-inv09", tasks[0].TaskID, markers[5], markers[6])
	verifyTeamLockedBehavior(t, s, v, "team-inv09", tasks[0].TaskID, seq)
	verifyNoPlantedMarkersInDB(t, s, path, markers)
	verifyTeamReopenRoundTrip(t, s, v, path, "team-inv09", tasks[0].TaskID, seq, specPayload, markers[6])
}

func mustMarshalProtectedSpec(t *testing.T, markers []string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"goal":          markers[0],
		"context":       markers[1],
		"write_scope":   []string{markers[2]},
		"checks":        []string{markers[3]},
		"output_schema": json.RawMessage(`{"properties":{"` + markers[4] + `":{"type":"string"}}}`),
	})
	if err != nil {
		t.Fatalf("marshal protected spec: %v", err)
	}
	return b
}

func appendPlantedTeamEvents(t *testing.T, s *Store, seq int64, teamID, taskID, question, report string) int64 {
	t.Helper()
	ctx := t.Context()
	for _, ev := range []AppendTeamEventInput{
		{EventID: "ev-start-team", EventType: EventTeamStarted},
		{EventID: "ev-assign", TaskID: taskID, EventType: EventTaskAssigned},
		{EventID: "ev-start", TaskID: taskID, EventType: EventTaskStarted},
		{EventID: "ev-ask", TaskID: taskID, EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(question)},
		{EventID: "ev-ans", TaskID: taskID, EventType: EventTaskQuestionAnswered, ProtectedPayload: []byte("yes")},
		{EventID: "ev-rep", TaskID: taskID, EventType: EventTaskReported, ProtectedPayload: []byte(report)},
	} {
		var err error
		evCopy := ev
		seq, err = s.AppendTeamEvent(ctx, teamID, seq, &evCopy)
		if err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventType, err)
		}
	}
	return seq
}

func verifyTeamLockedBehavior(t *testing.T, s *Store, v *vault.Service, teamID, taskID string, seq int64) {
	t.Helper()
	ctx := t.Context()
	v.Lock()
	if _, err := s.OpenTaskSpec(ctx, teamID, taskID); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("OpenTaskSpec while locked err = %v, want vault.ErrLocked", err)
	}
	if _, err := s.OpenTeamEventPayload(ctx, teamID, seq); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("OpenTeamEventPayload while locked err = %v, want vault.ErrLocked", err)
	}
	if _, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-locked", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{{
			TaskID: "t-locked", HierarchyRole: RoleSolo, CatalogRole: testRoleCoder,
			Deliverable: DeliverableDiff, CreatedEventID: "ev-c", ProtectedSpec: []byte("secret"),
		}},
	}); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("CreateTeam while locked err = %v, want vault.ErrLocked", err)
	}
	if _, _, err := s.TeamSnapshot(ctx, teamID); err != nil {
		t.Fatalf("metadata TeamSnapshot should succeed while locked: %v", err)
	}
}

func verifyNoPlantedMarkersInDB(t *testing.T, s *Store, path string, markers []string) {
	t.Helper()
	for _, m := range markers {
		scanTeamNonEnvelopeColumns(t, s, "team_records", "wrapped_key", []byte(m))
		scanTeamNonEnvelopeColumns(t, s, "team_tasks", "spec_envelope", []byte(m))
		scanTeamNonEnvelopeColumns(t, s, "team_task_events", "payload_envelope", []byte(m))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { requireNoError(t, root.Close()) }()
	rawDB, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	for _, m := range markers {
		if bytes.Contains(rawDB, []byte(m)) {
			t.Fatalf("planted marker %q leaked into raw SQLite bytes", m)
		}
	}
}

func verifyTeamReopenRoundTrip(
	t *testing.T,
	s *Store,
	v *vault.Service,
	path, teamID, taskID string,
	seq int64,
	wantSpec []byte,
	wantReport string,
) {
	t.Helper()
	ctx := t.Context()
	db2, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { requireNoError(t, db2.Close()) })
	if unlockErr := v.Unlock(ctx, redact.NewSecret("master-pw")); unlockErr != nil {
		t.Fatalf("Unlock vault: %v", unlockErr)
	}
	s2, err := New(ctx, db2, v, s.clock, nil)
	if err != nil {
		t.Fatalf("New reopened store: %v", err)
	}
	t.Cleanup(func() { requireNoError(t, s2.Close()) })

	gotSpec, err := s2.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil {
		t.Fatalf("OpenTaskSpec: %v", err)
	}
	if diff := cmp.Diff(wantSpec, gotSpec); diff != "" {
		t.Fatalf("OpenTaskSpec mismatch (-want +got):\n%s", diff)
	}
	gotReport, err := s2.OpenTeamEventPayload(ctx, teamID, seq)
	if err != nil {
		t.Fatalf("OpenTeamEventPayload: %v", err)
	}
	if string(gotReport) != wantReport {
		t.Fatalf("OpenTeamEventPayload = %q, want %q", gotReport, wantReport)
	}
}

func TestTeamStoreAppendIsContiguousAndProjectionIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := protectedStore(t)

	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-atomic", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{
			{TaskID: testTaskLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: testEvLead, ProtectedSpec: []byte("{}")},
			{TaskID: testTaskMgrID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleTeamManager, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-m", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskChildID, ParentTaskID: testTaskMgrID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-c", ProtectedSpec: []byte("{}")},
		},
	})
	if err != nil || rec.LastSeq != 4 || rec.TaskCount != 3 {
		t.Fatalf("CreateTeam = (%+v, %v)", rec, err)
	}

	assertOptimisticAndAtomicGuards(t, s)
	appendManagerTerminalWorkflow(t, s, rec.LastSeq)
	assertContiguousAndAppendOnly(t, s)
}

func assertOptimisticAndAtomicGuards(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.AppendTeamEvent(ctx, "team-atomic", 99, &AppendTeamEventInput{EventID: "ev-bad-seq", EventType: EventTeamStarted}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("stale expectedSeq err = %v, want ErrSequenceConflict", err)
	}
	s.SetCommitFault(errSimulatedIO)
	if _, err := s.AppendTeamEvent(ctx, "team-atomic", 4, &AppendTeamEventInput{EventID: "ev-fault", EventType: EventTeamStarted}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("faulted append err = %v, want ErrUnavailable", err)
	}
	s.SetCommitFault(nil)
	snapAfterFault, _, err := s.TeamSnapshot(ctx, "team-atomic")
	if err != nil || snapAfterFault.LastSeq != 4 || snapAfterFault.State != TeamStateProposed {
		t.Fatalf("faulted commit leaked state: %+v, err=%v", snapAfterFault, err)
	}
	if _, sqlErr := s.db.ExecContext(ctx, `
		INSERT INTO team_task_events (team_id, sequence, event_id, event_type, created_at)
		VALUES ('team-atomic', 7, 'ev-gap', 'TeamStarted', 100)`); sqlErr == nil || !strings.Contains(sqlErr.Error(), "non-contiguous") {
		t.Fatalf("expected non-contiguous SQL trigger error, got: %v", sqlErr)
	}
}

func appendManagerTerminalWorkflow(t *testing.T, s *Store, seq int64) {
	t.Helper()
	ctx := t.Context()
	for _, ev := range []AppendTeamEventInput{
		{EventID: "ev-1", EventType: EventTeamStarted},
		{EventID: "ev-2", TaskID: testTaskMgrID, EventType: EventTaskAssigned},
		{EventID: "ev-3", TaskID: testTaskMgrID, EventType: EventTaskStarted},
	} {
		var err error
		evCopy := ev
		seq, err = s.AppendTeamEvent(ctx, "team-atomic", seq, &evCopy)
		if err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventType, err)
		}
	}
	if _, repErr := s.AppendTeamEvent(ctx, "team-atomic", seq, &AppendTeamEventInput{
		EventID: "ev-mgr-early", TaskID: testTaskMgrID, EventType: EventTaskReported,
	}); !errors.Is(repErr, ErrTeamChildrenNotTerminal) {
		t.Fatalf("manager report with non-terminal child err = %v, want ErrTeamChildrenNotTerminal", repErr)
	}
	for _, ev := range []AppendTeamEventInput{
		{EventID: "ev-4", TaskID: testTaskChildID, EventType: EventTaskAssigned},
		{EventID: "ev-5", TaskID: testTaskChildID, EventType: EventTaskStarted},
		{EventID: "ev-6", TaskID: testTaskChildID, EventType: EventTaskReported},
		{EventID: "ev-7", TaskID: testTaskChildID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
		{EventID: "ev-8", TaskID: testTaskChildID, EventType: EventTaskReviewed, Outcome: "complete_no_diff"},
		{EventID: "ev-9", TaskID: testTaskMgrID, EventType: EventTaskReported},
	} {
		var err error
		evCopy := ev
		seq, err = s.AppendTeamEvent(ctx, "team-atomic", seq, &evCopy)
		if err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventType, err)
		}
	}
}

func assertContiguousAndAppendOnly(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	events, err := s.TeamEventsSince(ctx, "team-atomic", 0, 100)
	if err != nil {
		t.Fatalf("TeamEventsSince: %v", err)
	}
	for i := range events {
		if wantSeq := int64(i + 1); events[i].Sequence != wantSeq {
			t.Fatalf("event %d sequence = %d, want contiguous %d", i, events[i].Sequence, wantSeq)
		}
	}
	if _, updErr := s.db.ExecContext(ctx, `UPDATE team_task_events SET outcome = 'x' WHERE team_id = 'team-atomic' AND sequence = 1`); updErr == nil {
		t.Fatal("expected append-only trigger to reject UPDATE on team_task_events")
	}
}

func TestRestartMarksRunningTasksUnknownAndNeverReruns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, path := protectedStore(t)

	setupMixedStateTeamForRecovery(t, s)
	v.Lock()
	if closeErr := s.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	s2 := reopenStoreLocked(t, s, v, path)
	recovered, err := s2.RecoverTeams(ctx)
	if err != nil || len(recovered) != 4 {
		t.Fatalf("RecoverTeams = (%d, %v), want (4, nil)", len(recovered), err)
	}
	if second, recErr := s2.RecoverTeams(ctx); recErr != nil || len(second) != 0 {
		t.Fatalf("second RecoverTeams = (%d, %v), want (0, nil)", len(second), recErr)
	}
	teamAfter := assertRecoveredTaskStates(t, s2)
	assertUnknownDecisionsOnlyLeaveUnknown(t, s2, teamAfter.LastSeq)
}

func setupMixedStateTeamForRecovery(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-recover", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{
			{TaskID: testTaskLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "ev-0", ProtectedSpec: []byte("{}")},
			{TaskID: "t-queued", ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-1", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskStartID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-2", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskRunID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-3", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskAskID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-4", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskCheckID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-5", ProtectedSpec: []byte("{}")},
			{TaskID: testTaskReviewID, ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "ev-6", ProtectedSpec: []byte("{}")},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	seq := rec.LastSeq
	for _, ev := range []AppendTeamEventInput{
		{EventID: "s1", EventType: EventTeamStarted},
		{EventID: "s2", TaskID: testTaskStartID, EventType: EventTaskAssigned},
		{EventID: "s3", TaskID: testTaskRunID, EventType: EventTaskAssigned},
		{EventID: "s4", TaskID: testTaskRunID, EventType: EventTaskStarted},
		{EventID: "s5", TaskID: testTaskAskID, EventType: EventTaskAssigned},
		{EventID: "s6", TaskID: testTaskAskID, EventType: EventTaskStarted},
		{EventID: "s7", TaskID: testTaskAskID, EventType: EventTaskQuestionAsked},
		{EventID: "s8", TaskID: testTaskCheckID, EventType: EventTaskAssigned},
		{EventID: "s9", TaskID: testTaskCheckID, EventType: EventTaskStarted},
		{EventID: "s10", TaskID: testTaskCheckID, EventType: EventTaskReported},
		{EventID: "s11", TaskID: testTaskReviewID, EventType: EventTaskAssigned},
		{EventID: "s12", TaskID: testTaskReviewID, EventType: EventTaskStarted},
		{EventID: "s13", TaskID: testTaskReviewID, EventType: EventTaskReported},
		{EventID: "s14", TaskID: testTaskReviewID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
	} {
		evCopy := ev
		seq, err = s.AppendTeamEvent(ctx, "team-recover", seq, &evCopy)
		if err != nil {
			t.Fatalf("setup %s: %v", ev.EventID, err)
		}
	}
}

func reopenStoreLocked(t *testing.T, s *Store, v *vault.Service, path string) *Store {
	t.Helper()
	ctx := t.Context()
	db2, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	t.Cleanup(func() { requireNoError(t, db2.Close()) })
	s2, err := New(ctx, db2, v, s.clock, nil)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { requireNoError(t, s2.Close()) })
	return s2
}

func assertRecoveredTaskStates(t *testing.T, s2 *Store) TeamRecord {
	t.Helper()
	teamAfter, tasksAfter, err := s2.TeamSnapshot(t.Context(), "team-recover")
	if err != nil {
		t.Fatalf("TeamSnapshot: %v", err)
	}
	byID := make(map[string]TaskState, len(tasksAfter))
	for i := range tasksAfter {
		byID[tasksAfter[i].TaskID] = tasksAfter[i].State
	}
	wantStates := map[string]TaskState{
		testTaskLeadID:   TaskStateQueued,
		"t-queued":       TaskStateQueued,
		testTaskStartID:  TaskStateUnknown,
		testTaskRunID:    TaskStateUnknown,
		testTaskAskID:    TaskStateUnknown,
		testTaskCheckID:  TaskStateUnknown,
		testTaskReviewID: TaskStateReview,
	}
	if diff := cmp.Diff(wantStates, byID); diff != "" {
		t.Fatalf("recovered task states mismatch (-want +got):\n%s", diff)
	}
	return teamAfter
}

func assertUnknownDecisionsOnlyLeaveUnknown(t *testing.T, s2 *Store, seq int64) {
	t.Helper()
	ctx := t.Context()
	if _, badErr := s2.AppendTeamEvent(ctx, "team-recover", seq, &AppendTeamEventInput{
		EventID: "bad-leave", TaskID: testTaskRunID, EventType: EventTaskStarted,
	}); !errors.Is(badErr, ErrTeamTransition) {
		t.Fatalf("non-decision transition from unknown err = %v, want ErrTeamTransition", badErr)
	}
	for _, ev := range []AppendTeamEventInput{
		{EventID: "dec-keep", TaskID: testTaskRunID, EventType: EventTaskRecoveryDecided, Outcome: "keep_diff"},
		{EventID: "dec-resume", TaskID: testTaskStartID, EventType: EventTaskRecoveryDecided, Outcome: "resume"},
		{EventID: "dec-cancel", TaskID: testTaskAskID, EventType: EventTaskRecoveryDecided, Outcome: "cancel"},
	} {
		var err error
		evCopy := ev
		seq, err = s2.AppendTeamEvent(ctx, "team-recover", seq, &evCopy)
		if err != nil {
			t.Fatalf("decision %s: %v", ev.EventID, err)
		}
	}
}

func TestTeamStoreValidationAndTeamLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := protectedStore(t)

	assertInvalidTeamCreations(t, s)
	assertTeamLifecycleAndErrors(t, s)
	if _, err := s.OpenTaskSpec(ctx, "no-such-team", "t1"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing OpenTaskSpec err = %v, want ErrTaskNotFound", err)
	}
	if _, err := s.OpenTeamEventPayload(ctx, "no-such-team", 1); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing OpenTeamEventPayload err = %v, want ErrTeamNotFound", err)
	}
}

func assertInvalidTeamCreations(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	validTask := NewTaskInput{
		TaskID: testTaskLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch,
		Deliverable: DeliverableReport, CreatedEventID: testEvLead, ProtectedSpec: []byte("{}"),
	}
	badInputs := []*NewTeamInput{
		nil,
		{WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed, Tasks: []NewTaskInput{validTask}},
		{TeamID: "t-bad-empty", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed},
		{TeamID: "t-bad-role", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed, Tasks: []NewTaskInput{{
			TaskID: "a", HierarchyRole: TeamRole("bad"), CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "e1", ProtectedSpec: []byte("{}"),
		}}},
		{TeamID: "t-bad-deliv", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed, Tasks: []NewTaskInput{{
			TaskID: "a", HierarchyRole: RoleSolo, CatalogRole: testRoleCoder, Deliverable: TeamDeliverable("bad"), CreatedEventID: "e1", ProtectedSpec: []byte("{}"),
		}}},
		{TeamID: "t-bad-member-parent", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed, Tasks: []NewTaskInput{
			validTask,
			{TaskID: "m1", ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "e2", ProtectedSpec: []byte("{}")},
			{TaskID: "m2", ParentTaskID: "m1", HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, CreatedEventID: "e3", ProtectedSpec: []byte("{}")},
		}},
		{TeamID: "t-bad-cycle", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed, Tasks: []NewTaskInput{
			validTask,
			{TaskID: "m1", ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, DependsOn: []string{"m2"}, CreatedEventID: "e2", ProtectedSpec: []byte("{}")},
			{TaskID: "m2", ParentTaskID: testTaskLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableDiff, DependsOn: []string{"m1"}, CreatedEventID: "e3", ProtectedSpec: []byte("{}")},
		}},
	}
	for i, in := range badInputs {
		if _, _, err := s.CreateTeam(ctx, in); !errors.Is(err, ErrTeamInvalidInput) {
			t.Fatalf("badInput[%d] err = %v, want ErrTeamInvalidInput", i, err)
		}
	}
}

func assertTeamLifecycleAndErrors(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-life", WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{{
			TaskID: testTaskLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch,
			Deliverable: DeliverableDiff, CreatedEventID: testEvLead, ProtectedSpec: []byte("{}"),
		}},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, _, dupErr := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-life", WorkspaceID: testWorkspaceID, ProposedEventID: "ev-p2",
		Tasks: []NewTaskInput{{
			TaskID: "x1", HierarchyRole: RoleLead, CatalogRole: testRoleResearch,
			Deliverable: DeliverableDiff, CreatedEventID: "ev-x1", ProtectedSpec: []byte("{}"),
		}},
	}); !errors.Is(dupErr, ErrTeamExists) {
		t.Fatalf("duplicate team err = %v, want ErrTeamExists", dupErr)
	}
	seq := rec.LastSeq
	for _, ev := range []AppendTeamEventInput{
		{EventID: "tl-1", EventType: EventTeamStarted},
		{EventID: "tl-2", EventType: EventTeamFinalReview},
		{EventID: "tl-3", EventType: EventTeamDecided, Outcome: "revision"},
		{EventID: "tl-4", EventType: EventTeamFinalReview},
		{EventID: "tl-5", TaskID: testTaskLeadID, EventType: EventTaskAssigned},
		{EventID: "tl-6", TaskID: testTaskLeadID, EventType: EventTaskStarted},
		{EventID: "tl-7", TaskID: testTaskLeadID, EventType: EventTaskReported},
		{EventID: "tl-8", TaskID: testTaskLeadID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
		{EventID: "tl-9", TaskID: testTaskLeadID, EventType: EventTaskReviewed, Outcome: "approved"},
		{EventID: "tl-10", TaskID: testTaskLeadID, EventType: EventTaskIntegrated},
		{EventID: "tl-11", EventType: EventTeamEnded, Outcome: "done"},
	} {
		evCopy := ev
		seq, err = s.AppendTeamEvent(ctx, "team-life", seq, &evCopy)
		if err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventType, err)
		}
	}
	if payload, payErr := s.OpenTeamEventPayload(ctx, "team-life", seq); payErr != nil || payload != nil {
		t.Fatalf("metadata-only event payload = (%v, %v), want (nil, nil)", payload, payErr)
	}
	if _, misErr := s.AppendTeamEvent(ctx, "team-life", seq, &AppendTeamEventInput{
		EventID: "tl-bad", TaskID: testTaskLeadID, EventType: EventTeamStarted,
	}); !errors.Is(misErr, ErrTeamInvalidInput) {
		t.Fatalf("team event with task_id err = %v, want ErrTeamInvalidInput", misErr)
	}
}

func scanTeamNonEnvelopeColumns(t *testing.T, s *Store, table, skipCol string, marker []byte) {
	t.Helper()
	var query string
	switch table {
	case "team_records":
		query = "SELECT * FROM team_records"
	case "team_tasks":
		query = "SELECT * FROM team_tasks"
	case "team_task_events":
		query = "SELECT * FROM team_task_events"
	default:
		t.Fatalf("unsupported table %q", table)
	}
	ctx := t.Context()
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer closeRows(ctx, rows)

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %s: %v", table, err)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		requireNoError(t, rows.Scan(ptrs...))
		for i, col := range cols {
			if col == skipCol {
				continue
			}
			assertNoMarker(t, vals[i], marker)
		}
	}
	requireNoError(t, rows.Err())
}
