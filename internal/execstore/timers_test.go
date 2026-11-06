package execstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

const (
	testTimer1ID    = "timer-1"
	testTimer2ID    = "timer-2"
	testTimerGenID  = "timer-gen"
	testTimerDisc   = "t-disc"
	testClockDomain = "utc"
	testStateArmed  = "armed"
)

func assertTimerDBState(ctx context.Context, t *testing.T, db *sql.DB, timerID, wantState string) {
	t.Helper()
	var stateStr string
	err := db.QueryRowContext(ctx, "SELECT state FROM exec_timers WHERE timer_id = ?", timerID).Scan(&stateStr)
	if err != nil {
		t.Fatalf("query exec_timers for %s: %v", timerID, err)
	}
	if stateStr != wantState {
		t.Fatalf("state in DB for %s = %s, want %s", timerID, stateStr, wantState)
	}
}

func assertGen2TimerState(ctx context.Context, t *testing.T, db *sql.DB, wantState string) {
	t.Helper()
	var (
		gen   uint64
		state string
	)
	err := db.QueryRowContext(ctx, "SELECT generation, state FROM exec_timers WHERE timer_id = ?", testTimerGenID).Scan(&gen, &state)
	if err != nil {
		t.Fatalf("query exec_timers for %s: %v", testTimerGenID, err)
	}
	if gen != 2 || state != wantState {
		t.Fatalf("timer %s: expected gen 2 state %s, got gen=%d state=%s", testTimerGenID, wantState, gen, state)
	}
}

func makeOrderSchedEvent(timerID string, seq uint64, ts, deadline time.Time, purpose kernel.TimerPurpose) *kernel.TimerScheduledEvent {
	return &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope("run-timers-order", seq, "cmd-order-1", 1, ts),
		TimerID:            timerID,
		Generation:         1,
		Purpose:            purpose,
		TargetID:           "target",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: deadline,
		Duration:           time.Second,
		PolicyVersion:      "v1",
	}
}

func setupTestRun(ctx context.Context, t *testing.T, s *Store, sessionID, runID string) {
	t.Helper()
	cmdAccept := "cmd-accept-" + runID
	acceptEvt := makeRunAccepted(makeEnvelope(runID, 1, cmdAccept, 0, s.clk.Now()), sessionID, "")
	reqAccept := &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 cmdAccept,
		Kind:                      cmdAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{acceptEvt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1},
	}
	appendPersisted(ctx, t, s, reqAccept)

	cmdAdmit := "cmd-admit-" + runID
	admitEvt := makeRunAdmitted(makeEnvelope(runID, 2, cmdAdmit, 1, s.clk.Now()), 1)
	reqAdmit := &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 cmdAdmit,
		Kind:                      testKindAdmitRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{admitEvt},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 2},
	}
	appendPersisted(ctx, t, s, reqAdmit)
}

func buildAppendRequest(sessionID, runID, cmdID string, expectedRunRev, expectedAdmRev, seq uint64, events ...kernel.Event) *AppendRequest {
	return &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 cmdID,
		Kind:                      testKindAdvanceRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		ExpectedRunRevision:       expectedRunRev,
		ExpectedAdmissionRevision: expectedAdmRev,
		Events:                    events,
		Result:                    codec.ReceiptResult{FirstSequence: seq, LastSequence: seq + uint64(len(events)) - 1},
	}
}

func appendEvents(ctx context.Context, t *testing.T, s *Store, sessionID, runID, cmdID string, expectedRunRev, expectedAdmRev, seq uint64, events ...kernel.Event) {
	t.Helper()
	req := buildAppendRequest(sessionID, runID, cmdID, expectedRunRev, expectedAdmRev, seq, events...)
	appendPersisted(ctx, t, s, req)
}

func TestTimerProjectionTracksScheduleElapseCancel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-1"
	runID := "run-timers-1"
	setupTestRun(ctx, t, s, sessionID, runID)

	t0 := time.Date(2026, 11, 6, 12, 0, 5, 0, time.UTC)
	schedEvt1 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 3, "cmd-sched-1", 1, s.clk.Now()),
		TimerID:            testTimer1ID,
		Generation:         1,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "attempt-1",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(10 * time.Second),
		Duration:           10 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-sched-1", 2, 2, 3, schedEvt1)

	armed, err := s.ListArmedTimers(ctx)
	if err != nil {
		t.Fatalf("ListArmedTimers failed: %v", err)
	}
	if len(armed) != 1 || armed[0].TimerID != testTimer1ID || armed[0].Generation != 1 {
		t.Fatalf("unexpected armed timer: %+v", armed)
	}

	assertTimerDBState(ctx, t, db, testTimer1ID, testStateArmed)

	elapseEvt1 := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 4, "cmd-elapse-1", 1, s.clk.Now()),
		TimerID:      testTimer1ID,
		Generation:   1,
		ObservedTime: t0.Add(10 * time.Second),
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-elapse-1", 3, 3, 4, elapseEvt1)

	armed, err = s.ListArmedTimers(ctx)
	if err != nil || len(armed) != 0 {
		t.Fatalf("expected 0 armed timers after elapse, got %d (err: %v)", len(armed), err)
	}

	assertTimerDBState(ctx, t, db, testTimer1ID, "elapsed")

	verifyScheduleAndCancelTimer2(ctx, t, s, db, sessionID, runID, t0)
}

func verifyScheduleAndCancelTimer2(ctx context.Context, t *testing.T, s *Store, db *sql.DB, sessionID, runID string, t0 time.Time) {
	t.Helper()
	schedEvt2 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 5, "cmd-sched-2", 1, s.clk.Now()),
		TimerID:            testTimer2ID,
		Generation:         1,
		Purpose:            kernel.TimerPurposeToolRetry,
		TargetID:           "attempt-2",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(20 * time.Second),
		Duration:           20 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-sched-2", 4, 4, 5, schedEvt2)

	armed, err := s.ListArmedTimers(ctx)
	if err != nil || len(armed) != 1 || armed[0].TimerID != testTimer2ID {
		t.Fatalf("expected timer-2 armed, got %+v (err: %v)", armed, err)
	}

	cancelEvt2 := &kernel.TimerCancelledEvent{
		Envelope:   *makeEnvelope(runID, 6, "cmd-cancel-2", 1, s.clk.Now()),
		TimerID:    testTimer2ID,
		Generation: 1,
		CauseRef:   "user_cancel",
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-cancel-2", 5, 5, 6, cancelEvt2)

	armed, err = s.ListArmedTimers(ctx)
	if err != nil || len(armed) != 0 {
		t.Fatalf("expected 0 armed timers after cancel, got %d (err: %v)", len(armed), err)
	}

	assertTimerDBState(ctx, t, db, testTimer2ID, "cancelled")
}

func TestOlderTimerGenerationNeverOverwrites(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-gen"
	runID := "run-timers-gen"
	setupTestRun(ctx, t, s, sessionID, runID)

	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	schedGen2 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 3, "cmd-sched-g2", 1, s.clk.Now()),
		TimerID:            testTimerGenID,
		Generation:         2,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "attempt-g2",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(30 * time.Second),
		Duration:           30 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-sched-g2", 2, 2, 3, schedGen2)

	schedGen1 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 4, "cmd-sched-g1", 1, s.clk.Now()),
		TimerID:            testTimerGenID,
		Generation:         1,
		Purpose:            kernel.TimerPurposeToolRetry,
		TargetID:           "attempt-g1",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(5 * time.Second),
		Duration:           5 * time.Second,
		PolicyVersion:      "v1",
	}
	reqSched1 := buildAppendRequest(sessionID, runID, "cmd-sched-g1", 3, 3, 4, schedGen1)
	if _, err := s.Append(ctx, reqSched1); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when scheduling older generation, got %v", err)
	}

	assertGen2TimerState(ctx, t, db, testStateArmed)

	verifyStaleElapseAndCancel(ctx, t, s, db, sessionID, runID, t0)

	elapseGen2 := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 4, "cmd-elapse-g2", 1, s.clk.Now()),
		TimerID:      testTimerGenID,
		Generation:   2,
		ObservedTime: t0.Add(30 * time.Second),
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-elapse-g2", 3, 3, 4, elapseGen2)

	assertGen2TimerState(ctx, t, db, "elapsed")
}

func verifyStaleElapseAndCancel(ctx context.Context, t *testing.T, s *Store, db *sql.DB, sessionID, runID string, t0 time.Time) {
	t.Helper()
	elapseGen1 := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 4, "cmd-elapse-g1", 1, s.clk.Now()),
		TimerID:      testTimerGenID,
		Generation:   1,
		ObservedTime: t0.Add(10 * time.Second),
	}
	reqElapse1 := buildAppendRequest(sessionID, runID, "cmd-elapse-g1", 3, 3, 4, elapseGen1)
	if _, err := s.Append(ctx, reqElapse1); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when elapsing older generation, got %v", err)
	}

	assertGen2TimerState(ctx, t, db, testStateArmed)

	cancelGen1 := &kernel.TimerCancelledEvent{
		Envelope:   *makeEnvelope(runID, 4, "cmd-cancel-g1", 1, s.clk.Now()),
		TimerID:    testTimerGenID,
		Generation: 1,
		CauseRef:   "old",
	}
	reqCancel1 := buildAppendRequest(sessionID, runID, "cmd-cancel-g1", 3, 3, 4, cancelGen1)
	if _, err := s.Append(ctx, reqCancel1); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when cancelling older generation, got %v", err)
	}

	assertGen2TimerState(ctx, t, db, testStateArmed)
}

func TestTimerProjectionRollsBackWithBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-rollback"
	runID := "run-timers-rollback"
	setupTestRun(ctx, t, s, sessionID, runID)

	s.SetFault(FaultBeforeTimers, errTestInjected)

	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	schedEvt := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 3, "cmd-sched-fault", 1, s.clk.Now()),
		TimerID:            "timer-fault",
		Generation:         1,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "attempt-fault",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(10 * time.Second),
		Duration:           10 * time.Second,
		PolicyVersion:      "v1",
	}

	req := &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 "cmd-sched-fault",
		Kind:                      testKindAdvanceRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		ExpectedRunRevision:       2,
		ExpectedAdmissionRevision: 2,
		Events:                    []kernel.Event{schedEvt},
		Result:                    codec.ReceiptResult{FirstSequence: 3, LastSequence: 3},
	}

	_, err := s.Append(ctx, req)
	if err == nil {
		t.Fatal("expected append error from FaultBeforeTimers")
	}

	var timerCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_timers WHERE timer_id = ?", "timer-fault").Scan(&timerCount); err != nil {
		t.Fatalf("query exec_timers count: %v", err)
	}
	if timerCount != 0 {
		t.Fatalf("atomicity violation: timer row persisted on rollback, count=%d", timerCount)
	}

	var eventCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events WHERE run_id = ? AND sequence = 3", runID).Scan(&eventCount); err != nil {
		t.Fatalf("query exec_events count: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("atomicity violation: event row persisted on rollback, count=%d", eventCount)
	}
}

func TestListArmedTimersOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-order"
	runID := "run-timers-order"
	setupTestRun(ctx, t, s, sessionID, runID)

	// Pitfall test: deadlines that differ only in sub-second digits, including a whole-second value.
	dWhole := time.Date(2026, 11, 6, 12, 0, 5, 0, time.UTC)
	dNano := time.Date(2026, 11, 6, 12, 0, 5, 1, time.UTC)
	dMid := time.Date(2026, 11, 6, 12, 0, 5, 50000000, time.UTC)
	dHigh := time.Date(2026, 11, 6, 12, 0, 5, 500000000, time.UTC)

	now := s.clk.Now()
	events := []kernel.Event{
		makeOrderSchedEvent("timer-high", 3, now, dHigh, kernel.TimerPurposeRunDeadline),
		makeOrderSchedEvent("timer-nano", 4, now, dNano, kernel.TimerPurposeModelRetry),
		makeOrderSchedEvent("timer-whole", 5, now, dWhole, kernel.TimerPurposeToolRetry),
		makeOrderSchedEvent("timer-mid", 6, now, dMid, kernel.TimerPurposeModelRetry),
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-order-1", 2, 2, 3, events...)

	armed, err := s.ListArmedTimers(ctx)
	if err != nil {
		t.Fatalf("ListArmedTimers: %v", err)
	}
	if len(armed) != 4 {
		t.Fatalf("expected 4 armed timers, got %d", len(armed))
	}

	expectedIDs := []string{"timer-whole", "timer-nano", "timer-mid", "timer-high"}
	for i, wantID := range expectedIDs {
		if armed[i].TimerID != wantID {
			t.Errorf("at index %d: got timer %s, want %s (deadline %v)", i, armed[i].TimerID, wantID, armed[i].Deadline)
		}
	}
}

func TestReplayMatchesTimerProjection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-replay"
	runID := "run-timers-replay"
	setupTestRun(ctx, t, s, sessionID, runID)

	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	sched1 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 3, "cmd-replay-1", 1, s.clk.Now()),
		TimerID:            "timer-rep-1",
		Generation:         1,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "att-1",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(10 * time.Second),
		Duration:           10 * time.Second,
		PolicyVersion:      "v1",
	}
	elapse1 := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 4, "cmd-replay-1", 1, s.clk.Now()),
		TimerID:      "timer-rep-1",
		Generation:   1,
		ObservedTime: t0.Add(10 * time.Second),
	}
	sched2 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 5, "cmd-replay-1", 1, s.clk.Now()),
		TimerID:            "timer-rep-2",
		Generation:         1,
		Purpose:            kernel.TimerPurposeToolRetry,
		TargetID:           "att-2",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(20 * time.Second),
		Duration:           20 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sessionID, runID, "cmd-replay-1", 2, 2, 3, sched1, elapse1, sched2)

	state, err := s.RebuildRun(ctx, runID)
	if err != nil {
		t.Fatalf("RebuildRun failed: %v", err)
	}
	if len(state.ActiveTimers) != 1 || state.ActiveTimers["timer-rep-2"] == nil {
		t.Fatalf("unexpected active timers in rebuilt state: %+v", state.ActiveTimers)
	}

	// Tamper with exec_timers: delete the active timer row
	if _, execErr := db.ExecContext(ctx, "UPDATE exec_timers SET state = 'cancelled' WHERE timer_id = 'timer-rep-2'"); execErr != nil {
		t.Fatalf("tamper exec_timers: %v", execErr)
	}

	_, err = s.RebuildRun(ctx, runID)
	if err == nil {
		t.Fatal("expected RebuildRun to fail with ErrCorruptHistory after tampering with timer projection")
	}
}

func TestListArmedTimersErrorBranches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	var nilStore *Store
	if _, err := nilStore.ListArmedTimers(ctx); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable for nil store, got %v", err)
	}

	s, db, _ := newTestStore(ctx, t, nil)
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ListArmedTimers(cancCtx); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on cancelled context, got %v", err)
	}

	setupTestRun(ctx, t, s, "sess-corrupt", "run-corrupt")
	_, execErr := db.ExecContext(ctx, `INSERT INTO exec_timers (timer_id, session_id, run_id, generation, purpose, target_id, deadline, state)
		VALUES ('corrupt-timer', 'sess-corrupt', 'run-corrupt', 1, 'model_retry', 'target-1', '2026-11-06T12:00:00.notadateeZ', 'armed')`)
	if execErr != nil {
		t.Fatalf("insert corrupt timer: %v", execErr)
	}
	if _, err := s.ListArmedTimers(ctx); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on unparseable deadline, got %v", err)
	}
}

func TestVerifyTimerProjectionArmedDiscrepancies(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	runID := "run-verify-disc"
	sessID := "sess-verify-disc"
	setupTestRun(ctx, t, s, sessID, runID)
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	deadlineStr := formatTimerDeadline(t0)

	insertTimer := func(gen uint64, purp, dl, st string) {
		_, execErr := db.ExecContext(ctx, `INSERT INTO exec_timers (timer_id, session_id, run_id, generation, purpose, target_id, deadline, state)
			VALUES (?, ?, ?, ?, ?, 'target', ?, ?)
			ON CONFLICT(timer_id) DO UPDATE SET generation=excluded.generation, purpose=excluded.purpose, deadline=excluded.deadline, state=excluded.state`,
			testTimerDisc, sessID, runID, gen, purp, dl, st)
		if execErr != nil {
			t.Fatalf("insert timer: %v", execErr)
		}
	}

	insertTimer(1, string(kernel.TimerPurposeModelRetry), deadlineStr, testStateArmed)
	st1 := kernel.NewState()
	if err := s.verifyTimerProjection(ctx, runID, st1); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory when armed in DB but missing in state, got %v", err)
	}

	st2 := kernel.NewState()
	st2.ActiveTimers[testTimerDisc] = &kernel.ActiveTimer{TimerID: testTimerDisc, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: t0}
	if err := s.verifyTimerProjection(ctx, runID, st2); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on generation mismatch, got %v", err)
	}

	st3 := kernel.NewState()
	st3.ActiveTimers[testTimerDisc] = &kernel.ActiveTimer{TimerID: testTimerDisc, Generation: 1, Purpose: kernel.TimerPurposeToolRetry, Deadline: t0}
	if err := s.verifyTimerProjection(ctx, runID, st3); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on purpose mismatch, got %v", err)
	}

	st4 := kernel.NewState()
	st4.ActiveTimers[testTimerDisc] = &kernel.ActiveTimer{TimerID: testTimerDisc, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t0.Add(time.Second)}
	if err := s.verifyTimerProjection(ctx, runID, st4); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on deadline mismatch, got %v", err)
	}

	insertTimer(1, string(kernel.TimerPurposeModelRetry), "2026-11-06T12:00:00.notadateeZ", testStateArmed)
	if err := s.verifyTimerProjection(ctx, runID, st3); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on unparseable stored deadline, got %v", err)
	}
}

func TestVerifyTimerProjectionInactiveDiscrepancies(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	runID := "run-verify-disc2"
	sessID := "sess-verify-disc2"
	setupTestRun(ctx, t, s, sessID, runID)
	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	deadlineStr := formatTimerDeadline(t0)

	_, execErr := db.ExecContext(ctx, `INSERT INTO exec_timers (timer_id, session_id, run_id, generation, purpose, target_id, deadline, state)
		VALUES ('t-disc2', ?, ?, 1, 'model_retry', 'target', ?, 'elapsed')`,
		sessID, runID, deadlineStr)
	if execErr != nil {
		t.Fatalf("insert timer: %v", execErr)
	}

	st := kernel.NewState()
	st.ActiveTimers["t-disc2"] = &kernel.ActiveTimer{TimerID: "t-disc2", Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t0}
	if err := s.verifyTimerProjection(ctx, runID, st); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory when elapsed in DB but active in state, got %v", err)
	}

	stMissing := kernel.NewState()
	stMissing.ActiveTimers["t-missing"] = &kernel.ActiveTimer{TimerID: "t-missing", Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t0}
	if err := s.verifyTimerProjection(ctx, runID, stMissing); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory when active in state but missing in DB, got %v", err)
	}
}

func TestTimerEventWithoutMatchingRowRollsBack(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sessionID := "sess-timers-norow"
	runID := "run-timers-norow"
	setupTestRun(ctx, t, s, sessionID, runID)

	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	elapseEvt := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 3, "cmd-orphan-elapse", 1, s.clk.Now()),
		TimerID:      "orphan-timer",
		Generation:   1,
		ObservedTime: t0,
	}

	req := buildAppendRequest(sessionID, runID, "cmd-orphan-elapse", 2, 2, 3, elapseEvt)
	if _, err := s.Append(ctx, req); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for orphan timer elapse, got %v", err)
	}

	var count int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM exec_timers WHERE timer_id = 'orphan-timer'").Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("expected 0 timer rows, got count=%d, err=%v", count, err)
	}
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM exec_events WHERE run_id = ? AND sequence = 3", runID).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("expected 0 events, got count=%d, err=%v", count, err)
	}
}

func TestTimerIDCannotMoveAcrossRuns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	sess1 := "sess-timers-cross-1"
	sess2 := "sess-timers-cross-2"
	run1 := "run-timers-cross-1"
	run2 := "run-timers-cross-2"
	setupTestRun(ctx, t, s, sess1, run1)
	setupTestRun(ctx, t, s, sess2, run2)

	t0 := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	timerID := "timer-pinned"

	sched1 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(run1, 3, "cmd-pinned-1", 1, s.clk.Now()),
		TimerID:            timerID,
		Generation:         1,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "target-1",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(10 * time.Second),
		Duration:           10 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sess1, run1, "cmd-pinned-1", 2, 2, 3, sched1)

	sched2 := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(run2, 3, "cmd-pinned-2", 1, s.clk.Now()),
		TimerID:            timerID,
		Generation:         2,
		Purpose:            kernel.TimerPurposeToolRetry,
		TargetID:           "target-2",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(20 * time.Second),
		Duration:           20 * time.Second,
		PolicyVersion:      "v1",
	}
	req2 := buildAppendRequest(sess2, run2, "cmd-pinned-2", 2, 2, 3, sched2)
	if _, err := s.Append(ctx, req2); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when timer moves across runs, got %v", err)
	}

	var storedRunID string
	err := db.QueryRowContext(ctx, "SELECT run_id FROM exec_timers WHERE timer_id = ?", timerID).Scan(&storedRunID)
	if err != nil || storedRunID != run1 {
		t.Fatalf("expected timer to stay in %s, got %s, err: %v", run1, storedRunID, err)
	}

	_, execErr := db.ExecContext(ctx, "UPDATE exec_timers SET run_id = ? WHERE timer_id = ?", run2, timerID)
	if execErr == nil {
		t.Fatal("expected trigger exec_timers_immutable_run to prevent run_id change")
	}

	_, execErr = db.ExecContext(ctx, "UPDATE exec_timers SET session_id = ? WHERE timer_id = ?", sess2, timerID)
	if execErr == nil {
		t.Fatal("expected trigger exec_timers_immutable_run to prevent session_id change")
	}
}

func TestElapsedAfterCancelledRollsBack(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)
	sessID, runID := "sess-cancel", "run-cancel"
	setupTestRun(ctx, t, s, sessID, runID)

	t0 := s.clk.Now()
	timerID := "timer-cancel-test"

	sched := &kernel.TimerScheduledEvent{
		Envelope:           *makeEnvelope(runID, 3, "cmd-sched", 1, t0),
		TimerID:            timerID,
		Generation:         1,
		Purpose:            kernel.TimerPurposeModelRetry,
		TargetID:           "target-1",
		ClockDomain:        testClockDomain,
		SchedulingDeadline: t0.Add(10 * time.Second),
		Duration:           10 * time.Second,
		PolicyVersion:      "v1",
	}
	appendEvents(ctx, t, s, sessID, runID, "cmd-sched", 2, 2, 3, sched)

	cancel := &kernel.TimerCancelledEvent{
		Envelope:   *makeEnvelope(runID, 4, "cmd-cancel", 1, t0.Add(time.Second)),
		TimerID:    timerID,
		Generation: 1,
		CauseRef:   "manual cancellation",
	}
	appendEvents(ctx, t, s, sessID, runID, "cmd-cancel", 3, 3, 4, cancel)

	elapsed := &kernel.TimerElapsedEvent{
		Envelope:     *makeEnvelope(runID, 5, "cmd-elapsed", 1, t0.Add(10*time.Second)),
		TimerID:      timerID,
		Generation:   1,
		ObservedTime: t0.Add(10 * time.Second),
	}
	req := buildAppendRequest(sessID, runID, "cmd-elapsed", 4, 4, 5, elapsed)
	if _, err := s.Append(ctx, req); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when elapsing cancelled timer, got: %v", err)
	}

	var state string
	err := db.QueryRowContext(ctx, "SELECT state FROM exec_timers WHERE timer_id = ?", timerID).Scan(&state)
	if err != nil || state != "cancelled" {
		t.Fatalf("expected timer state to remain 'cancelled', got %q, err: %v", state, err)
	}
}
