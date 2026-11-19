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
}
