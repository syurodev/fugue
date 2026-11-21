package vendorstore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

func TestTeamTaskEventsMigrationDropsCheckAndPreservesDataAndTriggers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "team-mig.db"))
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, db.Close()) })

	clk := clock.NewFake(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	migrations, err := storage.Migrations()
	requireNoError(t, err)
	if len(migrations) < 16 {
		t.Fatalf("expected >= 16 migrations, got %d", len(migrations))
	}
	requireNoError(t, storage.RunMigrations(ctx, db, clk, migrations[:15]))

	for _, q := range []string{
		`INSERT INTO team_records (team_id, workspace_id, session_id, state, last_seq, task_count, key_id, wrapped_key, failure_code, created_at, updated_at)
		 VALUES ('t1', 'ws1', 's1', 'active', 2, 1, 'k1', x'01', '', 100, 100)`,
		`INSERT INTO team_tasks (task_id, team_id, parent_task_id, hierarchy_role, catalog_role, deliverable, depends_on_json, depth, state, failure_code, spec_envelope, last_event_seq, created_at, updated_at)
		 VALUES ('task1', 't1', '', 'lead', 'researcher', 'report', '[]', 0, 'running', '', x'01', 2, 100, 100)`,
		`INSERT INTO team_task_events (team_id, sequence, event_id, task_id, event_type, outcome, from_state, to_state, failure_code, payload_envelope, created_at)
		 VALUES ('t1', 1, 'e1', '', 'TeamProposed', '', '', 'proposed', '', NULL, 100),
		        ('t1', 2, 'e2', '', 'TeamStarted', '', 'proposed', 'active', '', NULL, 101)`,
	} {
		_, err = db.ExecContext(ctx, q)
		requireNoError(t, err)
	}

	requireNoError(t, storage.Migrate(ctx, db, clk))

	var count int
	requireNoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_task_events WHERE team_id = 't1'`).Scan(&count))
	if count != 2 {
		t.Fatalf("preserved rows = %d, want 2", count)
	}
	var ev1, ev2 string
	requireNoError(t, db.QueryRowContext(ctx, `SELECT event_type FROM team_task_events WHERE team_id = 't1' AND sequence = 1`).Scan(&ev1))
	requireNoError(t, db.QueryRowContext(ctx, `SELECT event_type FROM team_task_events WHERE team_id = 't1' AND sequence = 2`).Scan(&ev2))
	if ev1 != "TeamProposed" || ev2 != "TeamStarted" {
		t.Fatalf("preserved event types = (%q, %q)", ev1, ev2)
	}
	if _, updErr := db.ExecContext(ctx, `UPDATE team_task_events SET outcome = 'mut' WHERE team_id = 't1' AND sequence = 1`); updErr == nil {
		t.Fatal("expected UPDATE trigger to remain active after rebuild")
	}
	if _, delErr := db.ExecContext(ctx, `DELETE FROM team_task_events WHERE team_id = 't1' AND sequence = 1`); delErr == nil {
		t.Fatal("expected DELETE trigger to remain active after rebuild")
	}
	if _, gapErr := db.ExecContext(ctx, `
		INSERT INTO team_task_events (team_id, sequence, event_id, event_type, created_at)
		VALUES ('t1', 5, 'e5', 'TeamNoted', 102)`); gapErr == nil || !strings.Contains(gapErr.Error(), "non-contiguous") {
		t.Fatalf("expected non-contiguous trigger error, got %v", gapErr)
	}
	// New event types (e.g. TaskStalled) insert without SQL CHECK rejection.
	_, err = db.ExecContext(ctx, `
		INSERT INTO team_task_events (team_id, sequence, event_id, task_id, event_type, from_state, to_state, created_at)
		VALUES ('t1', 3, 'e3', 'task1', 'TaskStalled', 'running', 'running', 103)`)
	requireNoError(t, err)
}

func TestTeamEventTypeValidationTable(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)

	// Verify every TeamEventType in AllTeamEventTypes() passes Go validation and can be recorded.
	all := AllTeamEventTypes()
	if len(all) < 23 {
		t.Fatalf("expected at least 23 team event types, got %d", len(all))
	}
	for _, evType := range all {
		t.Run("valid/"+string(evType), func(t *testing.T) {
			t.Parallel()
			if !IsValidTeamEventType(evType) {
				t.Fatalf("IsValidTeamEventType(%q) = false", evType)
			}
			teamID := "team-ev-" + string(evType)
			seq := setupTeamForEventTypeTest(t, s, teamID, evType)
			if seq < 2 {
				t.Fatalf("expected sequence >= 2 for %s, got %d", evType, seq)
			}
		})
	}
	for _, bad := range []TeamEventType{"", "TeamHatched", "TaskTeleported", "unknown"} {
		t.Run("invalid/"+string(bad), func(t *testing.T) {
			t.Parallel()
			if IsValidTeamEventType(bad) {
				t.Fatalf("IsValidTeamEventType(%q) = true, want false", bad)
			}
			_, err := s.AppendTeamEvent(t.Context(), "team-ev-TeamProposed", 2, &AppendTeamEventInput{
				EventID: "bad-" + string(bad), EventType: bad,
			})
			if !errors.Is(err, ErrTeamInvalidInput) {
				t.Fatalf("AppendTeamEvent(%q) err = %v, want ErrTeamInvalidInput", bad, err)
			}
		})
	}
}

const (
	testOutcomeCancel   = "cancel"
	testOutcomeApproved = "approved"
	testTimerTeamID     = "team-tm"
	testTimerLeadID     = "tm-lead"
	testTimerMem1ID     = "tm-m1"
	testTimerMem2ID     = "tm-m2"
	testReloadTeamID    = "tm-reload"
	testReloadLeadID    = "rl-lead"
	testReloadMem1ID    = "rl-m1"
	testReloadMem2ID    = "rl-m2"
)

func setupTeamForEventTypeTest(t *testing.T, s *Store, teamID string, target TeamEventType) int64 {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: teamID, WorkspaceID: testWorkspaceID, ProposedEventID: "p-" + teamID,
		Tasks: []NewTaskInput{
			{TaskID: teamID + "-lead", HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableDiff, CreatedEventID: "c-lead-" + teamID, ProtectedSpec: []byte("{}")},
			{TaskID: teamID + "-mem", ParentTaskID: teamID + "-lead", HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-mem-" + teamID, ProtectedSpec: []byte("{}")},
		},
	})
	requireNoError(t, err)
	seq := rec.LastSeq
	memID := teamID + "-mem"
	if target == EventTeamProposed || target == EventTaskCreated {
		return seq
	}
	if target == EventTeamEnded {
		return appendSeq(t, s, teamID, seq, []AppendTeamEventInput{
			{EventID: "end-m-" + teamID, TaskID: memID, EventType: EventTaskEnded, Outcome: outcomeCancelled},
			{EventID: "end-l-" + teamID, TaskID: teamID + "-lead", EventType: EventTaskEnded, Outcome: outcomeCancelled},
			{EventID: "target-" + teamID, EventType: EventTeamEnded, Outcome: outcomeCancelled},
		})
	}
	seq = appendSeq(t, s, teamID, seq, []AppendTeamEventInput{{EventID: "start-" + teamID, EventType: EventTeamStarted}})
	if target == EventTeamStarted {
		return seq
	}
	return appendSpecificTargetEvent(t, s, teamID, memID, seq, target)
}

func appendSpecificTargetEvent(t *testing.T, s *Store, teamID, memID string, seq int64, target TeamEventType) int64 {
	t.Helper()
	steps := map[TeamEventType][]AppendTeamEventInput{
		EventTeamAmended:         {{EventID: "t-" + teamID, EventType: target}},
		EventTeamNoted:           {{EventID: "t-" + teamID, EventType: target}},
		EventTeamDeadlineReached: {{EventID: "t-" + teamID, EventType: target}},
		EventTeamFinalReview:     {{EventID: "t-" + teamID, EventType: target}},
		EventTeamDeadlineExtended: {
			{EventID: "dr-" + teamID, EventType: EventTeamDeadlineReached},
			{EventID: "t-" + teamID, EventType: EventTeamDeadlineExtended},
		},
		EventTeamDecided: {
			{EventID: "fr-" + teamID, EventType: EventTeamFinalReview},
			{EventID: "t-" + teamID, EventType: EventTeamDecided, Outcome: "revision"},
		},
		EventTaskAssigned: {{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskAssigned}},
		EventTaskEnded:    {{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskEnded, Outcome: outcomeCancelled}},
		EventTaskStarted: {
			{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskStarted},
		},
		EventTaskMarkedUnknown: {
			{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskMarkedUnknown},
		},
		EventTaskStalled: {
			{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskStalled},
		},
		EventTaskSuperseded: {
			{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
			{EventID: "f-" + teamID, TaskID: memID, EventType: EventTaskEnded, Outcome: outcomeFailed, FailureCode: "x"},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskSuperseded},
		},
		EventTaskRecoveryDecided: {
			{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
			{EventID: "u-" + teamID, TaskID: memID, EventType: EventTaskMarkedUnknown},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskRecoveryDecided, Outcome: testOutcomeCancel},
		},
	}
	if evs, ok := steps[target]; ok {
		return appendSeq(t, s, teamID, seq, evs)
	}
	return appendTaskPipelineTargetEvent(t, s, teamID, memID, seq, target)
}

func appendTaskPipelineTargetEvent(t *testing.T, s *Store, teamID, memID string, seq int64, target TeamEventType) int64 {
	t.Helper()
	seq = appendSeq(t, s, teamID, seq, []AppendTeamEventInput{
		{EventID: "a-" + teamID, TaskID: memID, EventType: EventTaskAssigned},
		{EventID: "s-" + teamID, TaskID: memID, EventType: EventTaskStarted},
	})
	pipeline := map[TeamEventType][]AppendTeamEventInput{
		EventTaskQuestionAsked: {{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskQuestionAsked}},
		EventTaskQuestionAnswered: {
			{EventID: "q-" + teamID, TaskID: memID, EventType: EventTaskQuestionAsked},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskQuestionAnswered},
		},
		// F11.10a: the informational sibling send is a no-op transition.
		EventTaskMessaged: {{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskMessaged}},
		EventTaskReported: {{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskReported}},
		EventTaskCheckCompleted: {
			{EventID: "r-" + teamID, TaskID: memID, EventType: EventTaskReported},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
		},
		EventTaskReviewed: {
			{EventID: "r-" + teamID, TaskID: memID, EventType: EventTaskReported},
			{EventID: "c-" + teamID, TaskID: memID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskReviewed, Outcome: testOutcomeApproved},
		},
		EventTaskIntegrated: {
			{EventID: "r-" + teamID, TaskID: memID, EventType: EventTaskReported},
			{EventID: "c-" + teamID, TaskID: memID, EventType: EventTaskCheckCompleted, Outcome: testOutcomePassed},
			{EventID: "v-" + teamID, TaskID: memID, EventType: EventTaskReviewed, Outcome: testOutcomeApproved},
			{EventID: "t-" + teamID, TaskID: memID, EventType: EventTaskIntegrated},
		},
	}
	evs, ok := pipeline[target]
	if !ok {
		t.Fatalf("unhandled event type %q", target)
	}
	return appendSeq(t, s, teamID, seq, evs)
}

func TestTeamTimersIdempotentAndTransactionalLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := protectedStore(t)

	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: testTimerTeamID, WorkspaceID: testWorkspaceID, ProposedEventID: testEvProposed,
		Tasks: []NewTaskInput{
			{TaskID: testTimerLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "tm-c0", ProtectedSpec: []byte("{}")},
			{TaskID: testTimerMem1ID, ParentTaskID: testTimerLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "tm-c1", ProtectedSpec: []byte("{}")},
			{TaskID: testTimerMem2ID, ParentTaskID: testTimerLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "tm-c2", ProtectedSpec: []byte("{}")},
		},
	})
	requireNoError(t, err)

	seq, err := s.DecideTeam(ctx, testTimerTeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "tm-dec", StartedPayload: []byte(`{"decision":"approve"}`),
		ArmTimers: []TeamTimerArmInput{{TeamID: testTimerTeamID, Kind: TimerKindTeamDeadline, DeadlineMs: 50_000}},
	})
	requireNoError(t, err)

	seq = verifyArmAndFireOnce(t, s, seq)
	verifyTerminalCancellation(t, s, seq)
}

func verifyArmAndFireOnce(t *testing.T, s *Store, seq int64) int64 {
	t.Helper()
	ctx := t.Context()
	seq = appendSeq(t, s, testTimerTeamID, seq, []AppendTeamEventInput{
		{EventID: "tm-a1", TaskID: testTimerMem1ID, EventType: EventTaskAssigned},
		{
			EventID: "tm-s1", TaskID: testTimerMem1ID, EventType: EventTaskStarted,
			ArmTimers: []TeamTimerArmInput{{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 10_000}},
		},
	})
	requireNoError(t, s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 10_000}))
	tm1, ok, err := s.GetTeamTimer(ctx, testTimerTeamID, testTimerMem1ID, TimerKindTaskDeadline)
	requireNoError(t, err)
	if !ok || tm1.State != TimerStateArmed || tm1.Generation != 1 {
		t.Fatalf("tm1 = (%+v, %v), want armed gen 1", tm1, ok)
	}
	armed, err := s.ListArmedTeamTimers(ctx)
	requireNoError(t, err)
	if len(armed) != 2 || armed[0].TaskID != testTimerMem1ID || armed[1].Kind != TimerKindTeamDeadline {
		t.Fatalf("armed = %+v", armed)
	}
	seq, err = s.AppendTeamEvent(ctx, testTimerTeamID, seq, &AppendTeamEventInput{
		EventID: "tm-dl1", TaskID: testTimerMem1ID, EventType: EventTaskEnded, Outcome: TaskOutcomeFailed, FailureCode: "deadline_exceeded",
		FireTimer: &TeamTimerRef{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline, Generation: 1},
	})
	requireNoError(t, err)
	if _, dupErr := s.AppendTeamEvent(ctx, testTimerTeamID, seq, &AppendTeamEventInput{
		EventID: "tm-dl-dup", TaskID: testTimerMem2ID, EventType: EventTaskEnded, Outcome: TaskOutcomeFailed, FailureCode: "deadline_exceeded",
		FireTimer: &TeamTimerRef{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline, Generation: 1},
	}); !errors.Is(dupErr, ErrTimerNotArmed) {
		t.Fatalf("duplicate fire err = %v, want ErrTimerNotArmed", dupErr)
	}
	fired, err := s.FireTeamTimer(ctx, TeamTimerRef{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline, Generation: 1})
	requireNoError(t, err)
	if fired {
		t.Fatal("expected already-fired timer FireTeamTimer to return false")
	}
	requireNoError(t, s.CancelTeamTimer(ctx, TeamTimerRef{TeamID: testTimerTeamID, TaskID: testTimerMem1ID, Kind: TimerKindTaskDeadline}))
	return seq
}

func verifyTerminalCancellation(t *testing.T, s *Store, seq int64) {
	t.Helper()
	ctx := t.Context()
	seq = appendSeq(t, s, testTimerTeamID, seq, []AppendTeamEventInput{
		{EventID: "tm-a2", TaskID: testTimerMem2ID, EventType: EventTaskAssigned},
		{
			EventID: "tm-s2", TaskID: testTimerMem2ID, EventType: EventTaskStarted,
			ArmTimers: []TeamTimerArmInput{{TeamID: testTimerTeamID, TaskID: testTimerMem2ID, Kind: TimerKindTaskDeadline, DeadlineMs: 20_000}},
		},
		{EventID: "tm-e2", TaskID: testTimerMem2ID, EventType: EventTaskEnded, Outcome: outcomeCancelled},
	})
	tm2, ok, err := s.GetTeamTimer(ctx, testTimerTeamID, testTimerMem2ID, TimerKindTaskDeadline)
	requireNoError(t, err)
	if !ok || tm2.State != TimerStateCancelled {
		t.Fatalf("tm2 after task end = (%+v, %v), want cancelled", tm2, ok)
	}
	_, err = s.SettleTeam(ctx, testTimerTeamID, &TeamSettleInput{ExpectedSeq: seq, EndEventID: "tm-end", EndPayload: []byte(`{"decision":"cancel"}`)})
	requireNoError(t, err)
	teamTm, ok, err := s.GetTeamTimer(ctx, testTimerTeamID, "", TimerKindTeamDeadline)
	requireNoError(t, err)
	if !ok || teamTm.State != TimerStateCancelled {
		t.Fatalf("team deadline after SettleTeam = (%+v, %v), want cancelled", teamTm, ok)
	}
}

func TestTeamTimersValidationReloadAndDBErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, path := protectedStore(t)

	verifyTimerInputValidationAndMissingTargets(t, s)

	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: testReloadTeamID, WorkspaceID: testWorkspaceID, ProposedEventID: "p-rl",
		Tasks: []NewTaskInput{
			{TaskID: testReloadLeadID, HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "c-rl-0", ProtectedSpec: []byte("{}")},
			{TaskID: testReloadMem1ID, ParentTaskID: testReloadLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-rl-1", ProtectedSpec: []byte("{}")},
			{TaskID: testReloadMem2ID, ParentTaskID: testReloadLeadID, HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-rl-2", ProtectedSpec: []byte("{}")},
		},
	})
	requireNoError(t, err)
	seq, err := s.DecideTeam(ctx, testReloadTeamID, &TeamDecisionInput{
		ExpectedSeq: rec.LastSeq, DecisionEventID: "d-rl", StartedPayload: []byte(`{"decision":"approve"}`),
	})
	requireNoError(t, err)

	verifyTimerRearmAndFireWithoutGeneration(t, s)
	seq = verifyTimerReloadAcrossHostRestart(t, s, v, path, seq)
	verifyTimerTerminalRejectionsAndClosedDB(t, s, seq)
}

func verifyTimerInputValidationAndMissingTargets(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	for _, in := range []*TeamTimerArmInput{
		nil,
		{TeamID: "", Kind: TimerKindTeamDeadline},
		{TeamID: "t", Kind: "bad_kind"},
	} {
		if err := s.ArmTeamTimer(ctx, in); !errors.Is(err, ErrTeamInvalidInput) {
			t.Fatalf("ArmTeamTimer(%+v) = %v, want ErrTeamInvalidInput", in, err)
		}
	}
	if err := s.CancelTeamTimer(ctx, TeamTimerRef{TeamID: "", Kind: TimerKindTaskDeadline}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("CancelTeamTimer invalid = %v, want ErrTeamInvalidInput", err)
	}
	if _, err := s.FireTeamTimer(ctx, TeamTimerRef{TeamID: "t", Kind: "bad"}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("FireTeamTimer invalid = %v, want ErrTeamInvalidInput", err)
	}
	if err := s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: "no-team", Kind: TimerKindTeamDeadline, DeadlineMs: 100}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("ArmTeamTimer missing team = %v, want ErrTeamNotFound", err)
	}
	_, found, err := s.GetTeamTimer(ctx, "no-team", "", TimerKindTeamDeadline)
	requireNoError(t, err)
	if found {
		t.Fatal("expected missing timer to return found=false")
	}
}

func verifyTimerRearmAndFireWithoutGeneration(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if err := s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, TaskID: "no-task", Kind: TimerKindTaskDeadline, DeadlineMs: 100}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("ArmTeamTimer missing task = %v, want ErrTaskNotFound", err)
	}
	requireNoError(t, s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 1_000}))
	requireNoError(t, s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 2_000}))
	tm, ok, err := s.GetTeamTimer(ctx, testReloadTeamID, testReloadMem1ID, TimerKindTaskDeadline)
	requireNoError(t, err)
	if !ok || tm.Generation != 2 || tm.DeadlineMs != 2_000 {
		t.Fatalf("re-armed timer = %+v, want gen 2 deadline 2000", tm)
	}
	fired, err := s.FireTeamTimer(ctx, TeamTimerRef{TeamID: testReloadTeamID, TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline})
	requireNoError(t, err)
	if !fired {
		t.Fatal("expected FireTeamTimer without generation to fire armed timer")
	}
	firedAgain, err := s.FireTeamTimer(ctx, TeamTimerRef{TeamID: testReloadTeamID, TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline})
	requireNoError(t, err)
	if firedAgain {
		t.Fatal("expected second FireTeamTimer without generation to be a no-op")
	}
}

func verifyTimerReloadAcrossHostRestart(t *testing.T, s *Store, v *vault.Service, path string, seq int64) int64 {
	t.Helper()
	ctx := t.Context()
	nowMs := s.clock.Now().UnixMilli()
	requireNoError(t, s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, TaskID: testReloadMem2ID, Kind: TimerKindTaskDeadline, DeadlineMs: nowMs - 1_000}))
	requireNoError(t, s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, Kind: TimerKindTeamDeadline, DeadlineMs: nowMs + 60_000}))

	restarted := reopenStoreLocked(t, s, v, path)
	armed, err := restarted.ListArmedTeamTimers(ctx)
	requireNoError(t, err)
	if len(armed) != 2 || armed[0].TaskID != testReloadMem2ID || armed[0].DeadlineMs > s.clock.Now().UnixMilli() {
		t.Fatalf("reloaded armed timers on host start = %+v", armed)
	}

	if _, badArmErr := s.AppendTeamEvent(ctx, testReloadTeamID, seq, &AppendTeamEventInput{
		EventID: "bad-arm", TaskID: testReloadMem1ID, EventType: EventTaskAssigned,
		ArmTimers: []TeamTimerArmInput{{TeamID: "other-team", TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 9_000}},
	}); !errors.Is(badArmErr, ErrTeamInvalidInput) {
		t.Fatalf("mismatched ArmTimers team_id = %v, want ErrTeamInvalidInput", badArmErr)
	}
	if _, badFireErr := s.AppendTeamEvent(ctx, testReloadTeamID, seq, &AppendTeamEventInput{
		EventID: "bad-fire", TaskID: testReloadMem1ID, EventType: EventTaskAssigned,
		FireTimer: &TeamTimerRef{TeamID: "", Kind: TimerKindTaskDeadline},
	}); !errors.Is(badFireErr, ErrTeamInvalidInput) {
		t.Fatalf("invalid FireTimer ref = %v, want ErrTeamInvalidInput", badFireErr)
	}
	return seq
}

func verifyTimerTerminalRejectionsAndClosedDB(t *testing.T, s *Store, seq int64) {
	t.Helper()
	ctx := t.Context()
	seq = appendSeq(t, s, testReloadTeamID, seq, []AppendTeamEventInput{
		{EventID: "end-m1", TaskID: testReloadMem1ID, EventType: EventTaskEnded, Outcome: outcomeCancelled},
	})
	if err := s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, TaskID: testReloadMem1ID, Kind: TimerKindTaskDeadline, DeadlineMs: 9_000}); !errors.Is(err, ErrTeamTransition) {
		t.Fatalf("ArmTeamTimer on terminal task = %v, want ErrTeamTransition", err)
	}
	_, err := s.SettleTeam(ctx, testReloadTeamID, &TeamSettleInput{ExpectedSeq: seq, EndEventID: "end-rl", EndPayload: []byte(`{"decision":"cancel"}`)})
	requireNoError(t, err)
	if err := s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, Kind: TimerKindTeamDeadline, DeadlineMs: 9_000}); !errors.Is(err, ErrTeamTerminal) {
		t.Fatalf("ArmTeamTimer on terminal team = %v, want ErrTeamTerminal", err)
	}

	verifyCountSessionTurnsAndClosedDBErrors(t)
}

func verifyCountSessionTurnsAndClosedDBErrors(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "closed-timer.db"))
	requireNoError(t, err)
	requireNoError(t, storage.Migrate(ctx, db, clk))
	s, err := New(ctx, db, nil, clk, nil)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.Close()) })

	n0, err := s.CountSessionTurns(ctx, "")
	requireNoError(t, err)
	if n0 != 0 {
		t.Fatalf("CountSessionTurns empty = %d, want 0", n0)
	}
	requireNoError(t, s.CreateSession(ctx, &Session{ID: "sess-turns", WorkspaceID: testWorkspaceID, Vendor: "claude", AccountID: "a", Role: "r", State: StateStarting}))
	_, err = s.Append(ctx, "sess-turns", 1, &Event{Type: VendorTurnStarted, TurnID: "t1", TurnOrigin: TurnOriginFugue})
	requireNoError(t, err)
	n1, err := s.CountSessionTurns(ctx, "sess-turns")
	requireNoError(t, err)
	if n1 != 1 {
		t.Fatalf("CountSessionTurns = %d, want 1", n1)
	}

	requireNoError(t, db.Close())
	if err := s.ArmTeamTimer(ctx, &TeamTimerArmInput{TeamID: testReloadTeamID, Kind: TimerKindTeamDeadline, DeadlineMs: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ArmTeamTimer on closed DB = %v, want ErrUnavailable", err)
	}
	if err := s.CancelTeamTimer(ctx, TeamTimerRef{TeamID: testReloadTeamID, Kind: TimerKindTeamDeadline}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CancelTeamTimer on closed DB = %v, want ErrUnavailable", err)
	}
	if _, err := s.FireTeamTimer(ctx, TeamTimerRef{TeamID: testReloadTeamID, Kind: TimerKindTeamDeadline}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("FireTeamTimer on closed DB = %v, want ErrUnavailable", err)
	}
	if _, err := s.ListArmedTeamTimers(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListArmedTeamTimers on closed DB = %v, want ErrUnavailable", err)
	}
	if _, _, err := s.GetTeamTimer(ctx, testReloadTeamID, "", TimerKindTeamDeadline); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetTeamTimer on closed DB = %v, want ErrUnavailable", err)
	}
	if _, err := s.CountSessionTurns(ctx, "sess-turns"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CountSessionTurns on closed DB = %v, want ErrUnavailable", err)
	}
}
