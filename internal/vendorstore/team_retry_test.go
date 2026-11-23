package vendorstore

import (
	"errors"
	"testing"
)

func TestRetryTaskSupersedesAndTracksChain(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	snap, tasks, err := s.TeamSnapshot(ctx, rec.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	_, childID := roleTasksInTeam(t, tasks)

	// Non-failed task cannot retry.
	if _, retryErr := s.RetryTask(ctx, rec.TeamID, childID, &TaskRetryInput{
		ExpectedSeq: snap.LastSeq, SupersededEventID: "s0", CreatedEventID: "c0",
	}); !errors.Is(retryErr, ErrTeamTransition) {
		t.Fatalf("RetryTask(queued) = %v, want ErrTeamTransition", retryErr)
	}

	seq, res1 := failAndRetryTask(t, s, rec.TeamID, childID, snap.LastSeq, "1", childID+"~r1", 2, childID)
	seq, res2 := failAndRetryTask(t, s, rec.TeamID, res1.NewTask.TaskID, seq, "2", childID+"~r2", 3, res1.NewTask.TaskID)

	spec, err := s.OpenTaskSpec(ctx, rec.TeamID, res2.NewTask.TaskID)
	if err != nil || len(spec) == 0 {
		t.Fatalf("OpenTaskSpec(%s) = (%q, %v)", res2.NewTask.TaskID, spec, err)
	}

	// Fail ~r2; 3rd retry is refused with ErrRetryLimitReached.
	seq = failTaskEvent(t, s, rec.TeamID, res2.NewTask.TaskID, seq, "f3")
	if _, limitErr := s.RetryTask(ctx, rec.TeamID, res2.NewTask.TaskID, &TaskRetryInput{
		ExpectedSeq: seq, SupersededEventID: "sup3", CreatedEventID: "cre3",
	}); !errors.Is(limitErr, ErrRetryLimitReached) {
		t.Fatalf("3rd RetryTask = %v, want ErrRetryLimitReached", limitErr)
	}
	assertRetryChainStates(t, s, rec.TeamID, childID, res1.NewTask.TaskID, res2.NewTask.TaskID)
}

func failTaskEvent(t *testing.T, s *Store, teamID, taskID string, seq int64, evID string) int64 {
	t.Helper()
	nextSeq, err := s.AppendTeamEvent(t.Context(), teamID, seq, &AppendTeamEventInput{
		EventID: evID, TaskID: taskID, EventType: EventTaskEnded, Outcome: TaskOutcomeFailed, FailureCode: "boom",
	})
	if err != nil {
		t.Fatal(err)
	}
	return nextSeq
}

func failAndRetryTask(t *testing.T, s *Store, teamID, taskID string, seq int64, tag, wantID string, wantAttempt int, wantRetryOf string) (int64, TaskRetryResult) {
	t.Helper()
	nextSeq := failTaskEvent(t, s, teamID, taskID, seq, "f"+tag)
	res, err := s.RetryTask(t.Context(), teamID, taskID, &TaskRetryInput{
		ExpectedSeq: nextSeq, SupersededEventID: "sup" + tag, CreatedEventID: "cre" + tag,
	})
	if err != nil {
		t.Fatalf("RetryTask(%s): %v", tag, err)
	}
	if res.NewTask.TaskID != wantID || res.NewTask.Attempt != wantAttempt || res.NewTask.RetryOf != wantRetryOf {
		t.Fatalf("retry %s = %+v, want (%s, attempt=%d, retry_of=%s)", tag, res.NewTask, wantID, wantAttempt, wantRetryOf)
	}
	return res.LastSeq, res
}

func assertRetryChainStates(t *testing.T, s *Store, teamID, rootID, r1ID, r2ID string) {
	t.Helper()
	_, allTasks, err := s.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]TaskRecord, len(allTasks))
	for i := range allTasks {
		byID[allTasks[i].TaskID] = allTasks[i]
	}
	if byID[rootID].State != TaskStateStopped || byID[r1ID].State != TaskStateStopped || byID[r2ID].State != TaskStateFailed {
		t.Fatalf("states = (%s, %s, %s), want (stopped, stopped, failed)",
			byID[rootID].State, byID[r1ID].State, byID[r2ID].State)
	}
}

func roleTasksInTeam(t *testing.T, tasks []TaskRecord) (leadID, memberID string) {
	t.Helper()
	for i := range tasks {
		switch tasks[i].HierarchyRole {
		case RoleLead:
			leadID = tasks[i].TaskID
		case RoleMember:
			if memberID == "" {
				memberID = tasks[i].TaskID
			}
		case RoleTeamManager, RoleSolo:
		}
	}
	if memberID == "" {
		t.Fatal("missing member task in seeded team")
	}
	return leadID, memberID
}

func TestAppendTeamEventsAtomicBatch(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	snap, tasks, err := s.TeamSnapshot(ctx, rec.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	leadID, childID := roleTasksInTeam(t, tasks)
	seq, err := s.AppendTeamEvents(ctx, rec.TeamID, snap.LastSeq, []*AppendTeamEventInput{
		{EventID: "a1", TaskID: leadID, EventType: EventTaskStarted},
		{EventID: "a2", TaskID: childID, EventType: EventTaskEnded, Outcome: TaskOutcomeFailed, FailureCode: "x"},
	})
	if err != nil || seq != snap.LastSeq+2 {
		t.Fatalf("AppendTeamEvents = (%d, %v), want %d", seq, err, snap.LastSeq+2)
	}
	// Second event invalid -> whole batch rolls back and leadID keeps its running state.
	if _, badErr := s.AppendTeamEvents(ctx, rec.TeamID, seq, []*AppendTeamEventInput{
		{EventID: "b1", TaskID: leadID, EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"q","to":"parent"}`)},
		{EventID: "b2", TaskID: "missing-task", EventType: EventTaskStarted},
	}); !errors.Is(badErr, ErrTaskNotFound) {
		t.Fatalf("AppendTeamEvents(bad 2nd) = %v, want ErrTaskNotFound", badErr)
	}
	after, afterTasks, err := s.TeamSnapshot(ctx, rec.TeamID)
	if err != nil || after.LastSeq != seq {
		t.Fatalf("after rollback = (%d, %v), want seq %d", after.LastSeq, err, seq)
	}
	for i := range afterTasks {
		if afterTasks[i].TaskID == leadID && afterTasks[i].State != TaskStateRunning {
			t.Fatalf("lead state = %s, want running (rolled back)", afterTasks[i].State)
		}
	}
	// Task event after TeamEnded in the same batch must see updated meta.State and fail.
	assertTaskAfterTeamEndedRollsBack(t, s, rec.TeamID, seq, leadID, afterTasks)
}

func assertTaskAfterTeamEndedRollsBack(t *testing.T, s *Store, teamID string, seq int64, leadID string, tasks []TaskRecord) {
	t.Helper()
	ctx := t.Context()
	// First settle all non-terminal tasks except lead so TeamEnded is valid in isolation,
	// or test TeamEnded -> task event after settling tasks in batch.
	var batch []*AppendTeamEventInput
	for i := range tasks {
		if tasks[i].TaskID == leadID || isTaskTerminal(tasks[i].State) {
			continue
		}
		batch = append(batch, &AppendTeamEventInput{
			EventID: "settle-" + tasks[i].TaskID, TaskID: tasks[i].TaskID,
			EventType: EventTaskEnded, Outcome: outcomeCancelled, FailureCode: "x",
		})
	}
	batch = append(batch,
		&AppendTeamEventInput{EventID: "settle-lead", TaskID: leadID, EventType: EventTaskEnded, Outcome: outcomeCancelled, FailureCode: "x"},
		&AppendTeamEventInput{EventID: "end-team", EventType: EventTeamEnded, Outcome: outcomeCancelled},
		&AppendTeamEventInput{EventID: "post-end-task", TaskID: leadID, EventType: EventTaskStarted},
	)
	if _, err := s.AppendTeamEvents(ctx, teamID, seq, batch); !errors.Is(err, ErrTeamTerminal) {
		t.Fatalf("task event after TeamEnded in batch = %v, want ErrTeamTerminal", err)
	}
	after, _, err := s.TeamSnapshot(ctx, teamID)
	if err != nil || after.State != TeamStateActive || after.LastSeq != seq {
		t.Fatalf("after TeamEnded rollback = (%s, %d, %v), want (active, %d)", after.State, after.LastSeq, err, seq)
	}
}

func TestRetryTaskRejectsManagerWithNonTerminalChildren(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx := t.Context()
	rec, seq := seedManagerTeamForRetry(t, s)

	// Child is still queued (non-terminal): retrying the failed manager is refused.
	if _, err := s.RetryTask(ctx, rec.TeamID, "mgr-team.mgr", &TaskRetryInput{
		ExpectedSeq: seq, SupersededEventID: "sm0", CreatedEventID: "cm0",
	}); !errors.Is(err, ErrTeamChildrenNotTerminal) {
		t.Fatalf("RetryTask(manager with queued child) = %v, want ErrTeamChildrenNotTerminal", err)
	}

	// Once child ends, manager retry succeeds cleanly.
	seq = failTaskEvent(t, s, rec.TeamID, "mgr-team.child", seq, "fc1")
	res, err := s.RetryTask(ctx, rec.TeamID, "mgr-team.mgr", &TaskRetryInput{
		ExpectedSeq: seq, SupersededEventID: "sm1", CreatedEventID: "cm1",
	})
	if err != nil || res.NewTask.TaskID != "mgr-team.mgr~r1" {
		t.Fatalf("RetryTask(manager after child terminal) = (%+v, %v)", res, err)
	}
}

func seedManagerTeamForRetry(t *testing.T, s *Store) (TeamRecord, int64) {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "mgr-team", WorkspaceID: "ws-m", SessionID: "sess-lead-m",
		ProposedEventID: "pm", ProposedPayload: []byte(`{"brief":"b"}`),
		Tasks: []NewTaskInput{
			{TaskID: "mgr-team.lead", HierarchyRole: RoleLead, CatalogRole: "r", Deliverable: DeliverableReport, CreatedEventID: "cl", ProtectedSpec: []byte(`{"goal":"g"}`)},
			{TaskID: "mgr-team.mgr", ParentTaskID: "mgr-team.lead", HierarchyRole: RoleTeamManager, CatalogRole: "r", Deliverable: DeliverableReport, CreatedEventID: "cm", ProtectedSpec: []byte(`{"goal":"g"}`)},
			{TaskID: "mgr-team.child", ParentTaskID: "mgr-team.mgr", HierarchyRole: RoleMember, CatalogRole: "r", Deliverable: DeliverableReport, CreatedEventID: "cc", ProtectedSpec: []byte(`{"goal":"g"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.AppendTeamEvent(ctx, rec.TeamID, rec.LastSeq, &AppendTeamEventInput{EventID: "stm", EventType: EventTeamStarted})
	if err != nil {
		t.Fatal(err)
	}
	seq = failTaskEvent(t, s, rec.TeamID, "mgr-team.mgr", seq, "fm0")
	return rec, seq
}

// TestRetryTaskRefusesUnknownTask pins OR-15: a task whose outcome is unknown
// may leave unknown only through an explicit recovery decision, never through
// the failed-task retry path, which would start a second attempt of work that
// may already have taken effect.
func TestRetryTaskRefusesUnknownTask(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := protectedStore(t)
	setupMixedStateTeamForRecovery(t, s)
	if _, err := s.RecoverTeams(ctx); err != nil {
		t.Fatalf("RecoverTeams: %v", err)
	}
	rec, before, err := s.TeamSnapshot(ctx, "team-recover")
	if err != nil {
		t.Fatal(err)
	}

	_, retryErr := s.RetryTask(ctx, "team-recover", testTaskRunID, &TaskRetryInput{
		ExpectedSeq: rec.LastSeq, SupersededEventID: "sup-unknown", CreatedEventID: "cre-unknown",
	})
	if !errors.Is(retryErr, ErrTeamTransition) {
		t.Fatalf("RetryTask(unknown) = %v, want ErrTeamTransition", retryErr)
	}

	after, afterTasks, err := s.TeamSnapshot(ctx, "team-recover")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSeq != rec.LastSeq || len(afterTasks) != len(before) {
		t.Fatalf("refused retry changed the team: seq %d -> %d, tasks %d -> %d",
			rec.LastSeq, after.LastSeq, len(before), len(afterTasks))
	}
	if st := findTaskState(afterTasks, testTaskRunID); st != TaskStateUnknown {
		t.Fatalf("task %s state = %s, want unknown", testTaskRunID, st)
	}
}

func findTaskState(tasks []TaskRecord, taskID string) TaskState {
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i].State
		}
	}
	return ""
}
