package coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	testTimer1         = "tm-1"
	testTimer2         = "tm-2"
	testTimerBarrier   = "tm-barrier"
	testTimerEarly     = "tm-early"
	testTimerLate      = "tm-late"
	testTimerOverdue   = "tm-overdue"
	testTimerQueued    = "tm-queued"
	testKindTimerFired = "TimerFired"
	testOriginTimerCmd = "timer"
	testRun1           = "r1"
	testRun2           = "r2"
	testSessionID1     = "s1"
	timerTestTimeout   = 5 * time.Second
)

func newTestStoreWithAdmission(queueRev uint64) *fakeStore {
	store := newFakeStore()
	store.admission = Admission{
		SessionID:         testSessionID1,
		ActiveRunID:       testRun1,
		OwnerIncarnation:  1,
		AdmissionToken:    1,
		Revision:          1,
		QueueRevision:     queueRev,
		ContextEpoch:      0,
		ContextRevision:   0,
		ContextStartRunID: "",
	}
	return store
}

func startTestScheduler(
	t *testing.T,
	ctx context.Context,
	store Store,
	clk clock.Clock,
	sess *Session,
	opts ...SchedulerOption,
) (*Scheduler, chan *ArmedTimer) {
	t.Helper()
	firedCh := make(chan *ArmedTimer, 10)
	allOpts := append([]SchedulerOption{WithOnFire(func(t *ArmedTimer) {
		firedCh <- t
	})}, opts...)
	sched, err := StartScheduler(ctx, store, clk, func(sessionID string) (*Session, bool) {
		return sess, sessionID == testSessionID1
	}, allOpts...)
	if err != nil {
		t.Fatalf("start scheduler failed: %v", err)
	}
	sess.SetScheduler(sched)
	return sched, firedCh
}

func newTestSessionWithClock(
	t *testing.T,
	store *fakeStore,
	exec *fakeExecutor,
	fakeClk clock.Clock,
	seed string,
) (context.Context, *Session) {
	t.Helper()
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic(seed))
	cfg := Config{
		SessionID:        testSessionID1,
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}
	ctx, cancel := context.WithTimeout(t.Context(), timerTestTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	return ctx, sess
}

func setupTestRunState(
	store *fakeStore,
	runID string,
	admitted bool,
	activeTimers map[string]*kernel.ActiveTimer,
) {
	store.mu.Lock()
	defer store.mu.Unlock()
	st := kernel.NewState()
	st.Accepted = true
	st.Admitted = admitted
	st.LastSequence = 1
	for k, v := range activeTimers {
		st.ActiveTimers[k] = v
	}
	store.runs[runID] = st
	store.runRevs[runID] = 1
}

func waitTimerFired(t *testing.T, ctx context.Context, ch <-chan *ArmedTimer, expectedID string) {
	t.Helper()
	select {
	case fired := <-ch:
		if fired.TimerID != expectedID {
			t.Fatalf("expected timer %s to fire, got %s", expectedID, fired.TimerID)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for timer %s to fire: %v", expectedID, ctx.Err())
	}
}

func waitTimerArm(t *testing.T, ctx context.Context, ch <-chan string, expectedID string) {
	t.Helper()
	select {
	case id := <-ch:
		if expectedID != "" && id != expectedID {
			t.Fatalf("expected timer %s to arm, got %s", expectedID, id)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for timer %s to arm: %v", expectedID, ctx.Err())
	}
}

func queueTimerAppend(store *fakeStore, cmdID, runID string, runRev uint64) {
	store.queueAppend(&AppendResult{
		Duplicate: false,
		Receipt: codec.Receipt{
			CommandID: cmdID,
			Kind:      testKindTimerFired,
			Principal: testOriginTimerCmd,
			Target:    runID,
			Result:    codec.ReceiptResult{},
		},
		RunRevision:          runRev,
		AdmissionRevision:    1,
		RevisionsUnavailable: false,
	}, nil)
}

func TestSchedulerRearmsDurableTimersAfterRestart(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	t1Deadline := startTime.Add(10 * time.Second)
	t2Deadline := startTime.Add(20 * time.Second)

	timer1 := ArmedTimer{
		TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t1Deadline,
	}
	timer2 := ArmedTimer{
		TimerID: testTimer2, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t2Deadline,
	}
	store.setArmedTimers(timer1, timer2)

	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t1Deadline},
		testTimer2: {TimerID: testTimer2, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: t2Deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-rearms")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-t1", testRun1, 2)
	queueTimerAppend(store, "cmd-t2", testRun1, 3)

	sched, firedCh := startTestScheduler(t, ctx, store, fakeClk, sess)
	defer sched.Stop()

	fakeClk.Advance(10 * time.Second)
	waitTimerFired(t, ctx, firedCh, testTimer1)

	fakeClk.Advance(10 * time.Second)
	waitTimerFired(t, ctx, firedCh, testTimer2)

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) != 2 {
		t.Fatalf("expected 2 transitions appended, got %d", len(transitions))
	}
}

func TestSchedulerFiresInDeadlineOrder(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	tEarlyDeadline := startTime.Add(10 * time.Second)
	tLateDeadline := startTime.Add(20 * time.Second)

	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimerEarly: {TimerID: testTimerEarly, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: tEarlyDeadline},
		testTimerLate:  {TimerID: testTimerLate, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: tLateDeadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-order")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-early", testRun1, 2)
	queueTimerAppend(store, "cmd-late", testRun1, 3)

	armedCh := make(chan string, 2)
	sched, firedCh := startTestScheduler(t, ctx, store, fakeClk, sess, WithOnArm(func(t *ArmedTimer) {
		armedCh <- t.TimerID
	}))
	defer sched.Stop()

	sched.Arm(&ArmedTimer{
		TimerID: testTimerLate, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: tLateDeadline,
	})
	sched.Arm(&ArmedTimer{
		TimerID: testTimerEarly, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: tEarlyDeadline,
	})

	waitTimerArm(t, ctx, armedCh, "")
	waitTimerArm(t, ctx, armedCh, "")

	fakeClk.Advance(10 * time.Second)
	waitTimerFired(t, ctx, firedCh, testTimerEarly)

	fakeClk.Advance(10 * time.Second)
	waitTimerFired(t, ctx, firedCh, testTimerLate)
}

func TestEarlyTimerFiredIsRejectedAndRearmed(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(10 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-early")
	defer sess.Stop()

	armedCh := make(chan string, 1)
	sched, firedCh := startTestScheduler(t, ctx, store, fakeClk, sess, WithOnArm(func(t *ArmedTimer) {
		armedCh <- t.TimerID
	}))
	defer sched.Stop()

	earlyCmd := &kernel.TimerFiredCommand{
		CommandID:    "cmd-early-observation",
		RunID:        testRun1,
		Origin:       testOriginTimerCmd,
		TimerID:      testTimer1,
		Generation:   1,
		ObservedTime: startTime.Add(5 * time.Second),
	}

	_, submitErr := sess.Submit(ctx, &Request{
		Command:          earlyCmd,
		Facts:            kernel.DecisionFacts{},
		Origin:           testOriginTimerCmd,
		QueueDecisionRef: "",
		Kind:             "",
		Principal:        testOriginTimerCmd,
		Target:           testRun1,
		MetadataInput:    nil,
		ProtectedInput:   nil,
		Payloads:         nil,
	})

	if submitErr == nil {
		t.Fatal("expected early timer submission to be rejected, got nil error")
	}
	var rej kernel.Rejection
	if !errors.As(submitErr, &rej) || rej.Code != kernel.RejectionCodeEarlyTimer {
		t.Fatalf("expected early_timer rejection error, got: %v", submitErr)
	}

	waitTimerArm(t, ctx, armedCh, testTimer1)

	queueTimerAppend(store, "cmd-t1", testRun1, 2)

	fakeClk.Advance(10 * time.Second)
	waitTimerFired(t, ctx, firedCh, testTimer1)
}

func TestStaleTimerGenerationIsNoOp(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(10 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-stale")
	defer sess.Stop()

	staleCmd := &kernel.TimerFiredCommand{
		CommandID:    "cmd-stale",
		RunID:        testRun1,
		Origin:       testOriginTimerCmd,
		TimerID:      testTimer1,
		Generation:   1,
		ObservedTime: startTime.Add(15 * time.Second),
	}

	queueTimerAppend(store, "cmd-stale", testRun1, 2)

	reply, err := sess.Submit(ctx, &Request{
		Command:          staleCmd,
		Facts:            kernel.DecisionFacts{},
		Origin:           testOriginTimerCmd,
		QueueDecisionRef: "",
		Kind:             "",
		Principal:        testOriginTimerCmd,
		Target:           testRun1,
		MetadataInput:    nil,
		ProtectedInput:   nil,
		Payloads:         nil,
	})
	if err != nil {
		t.Fatalf("unexpected error on stale timer fire: %v", err)
	}
	if reply.Duplicate || len(reply.Events) != 0 || reply.Receipt.CommandID != "cmd-stale" {
		t.Fatalf("expected non-duplicate reply with 0 events and persisted receipt, got %+v", reply)
	}

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition committed for stale generation, got %d", len(transitions))
	}
	if transitions[0].Result.FirstSequence != 0 || transitions[0].Result.LastSequence != 0 {
		t.Fatalf("expected sequence range 0..0 for no-op transition, got %+v", transitions[0].Result)
	}
}

func setupOverdueTimer(store *fakeStore, overdueDeadline time.Time) ArmedTimer {
	timer := ArmedTimer{
		TimerID:    testTimerOverdue,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   overdueDeadline,
	}
	store.setArmedTimers(timer)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimerOverdue: {TimerID: testTimerOverdue, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: overdueDeadline},
	})
	return timer
}

func TestOverdueTimerFiresOnceAfterRestart(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	timer := setupOverdueTimer(store, startTime.Add(-1*time.Hour))

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-overdue")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-overdue", testRun1, 2)

	var mu sync.Mutex
	fireCount := 0
	fireCh := make(chan string, 10)
	armCh := make(chan string, 10)

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			return sess, sessionID == testSessionID1
		},
		WithOnFire(func(t *ArmedTimer) {
			mu.Lock()
			if t.TimerID == testTimerOverdue {
				fireCount++
			}
			mu.Unlock()
			fireCh <- t.TimerID
		}),
		WithOnArm(func(t *ArmedTimer) {
			armCh <- t.TimerID
		}),
	)
	if err != nil {
		t.Fatalf("start scheduler failed: %v", err)
	}
	defer sched.Stop()
	sess.SetScheduler(sched)

	verifyOverdueFireAndRearm(t, ctx, sched, &timer, fireCh, armCh)

	fakeClk.Advance(10 * time.Minute)
	fireOverdueBarrier(t, ctx, sched, store, fakeClk, startTime, armCh, fireCh)

	mu.Lock()
	count := fireCount
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected exactly 1 fire for overdue timer, got %d", count)
	}
}

func verifyOverdueFireAndRearm(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	timer *ArmedTimer,
	fireCh, armCh <-chan string,
) {
	t.Helper()
	select {
	case id := <-fireCh:
		if id != testTimerOverdue {
			t.Fatalf("unexpected timer fired: %s", id)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for overdue timer to fire: %v", ctx.Err())
	}

	sched.Arm(timer)
	select {
	case id := <-armCh:
		if id != testTimerOverdue {
			t.Fatalf("unexpected timer armed: %s", id)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for re-arm to process: %v", ctx.Err())
	}
}

func fireOverdueBarrier(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	store *fakeStore,
	fakeClk *clock.Fake,
	startTime time.Time,
	armCh, fireCh <-chan string,
) {
	t.Helper()
	timerBarrier := ArmedTimer{
		TimerID:    testTimerBarrier,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeToolRetry,
		Deadline:   startTime.Add(15 * time.Minute),
	}
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimerOverdue: {TimerID: testTimerOverdue, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(-1 * time.Hour)},
		testTimerBarrier: {TimerID: testTimerBarrier, Generation: 1, Purpose: kernel.TimerPurposeToolRetry, Deadline: startTime.Add(15 * time.Minute)},
	})
	queueTimerAppend(store, "cmd-barrier", testRun1, 3)

	sched.Arm(&timerBarrier)
	select {
	case <-armCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for barrier timer arm")
	}

	fakeClk.Advance(10 * time.Minute)
	select {
	case id := <-fireCh:
		if id != testTimerBarrier {
			t.Fatalf("expected %s to fire, got %s", testTimerBarrier, id)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for barrier fire")
	}
}

func TestQueuedRunTimerNeverTouchesAdmission(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(7)

	deadline := startTime.Add(10 * time.Second)
	setupTestRunState(store, testRun2, false, map[string]*kernel.ActiveTimer{
		testTimerQueued: {TimerID: testTimerQueued, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "test-queued-timer")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-q", testRun2, 2)

	cmd := &kernel.TimerFiredCommand{
		CommandID:    "cmd-queued-fire",
		RunID:        testRun2,
		Origin:       testOriginTimerCmd,
		TimerID:      testTimerQueued,
		Generation:   1,
		ObservedTime: startTime.Add(12 * time.Second),
	}

	reply, err := sess.Submit(ctx, &Request{
		Command:          cmd,
		Facts:            kernel.DecisionFacts{},
		Origin:           testOriginTimerCmd,
		QueueDecisionRef: "",
		Kind:             "",
		Principal:        testOriginTimerCmd,
		Target:           testRun2,
		MetadataInput:    nil,
		ProtectedInput:   nil,
		Payloads:         nil,
	})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected successful elapsed event, not duplicate")
	}

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(transitions))
	}

	tx := transitions[0]
	if tx.RunID != testRun2 || tx.Fence.AdmissionToken != 0 || tx.Fence.QueueRevision != 7 || tx.Kind == testKindAdmitRun {
		t.Fatalf("unexpected queued run transition: %+v", tx)
	}

	if sess.admission.ActiveRunID != testRun1 {
		t.Fatalf("expected active run to remain r1, got %s", sess.admission.ActiveRunID)
	}
}

func TestRecoveryRequiredTimerParksUntilWake(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)
	store.admission.OwnerIncarnation = 1
	store.admission.ActiveRunID = testRun1

	deadline := startTime.Add(5 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("rec-sess"))
	cfg := Config{
		SessionID:        testSessionID1,
		OwnerIncarnation: 2,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         newFakeExecutor(),
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx := t.Context()
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("Start session: %v", err)
	}
	defer sess.Stop()

	var mu sync.Mutex
	activeSess := sess
	fireCh := make(chan struct{}, 1)
	parkedCh := make(chan struct{}, 1)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			mu.Lock()
			s := activeSess
			mu.Unlock()
			return s, sessionID == testSessionID1
		},
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				fireCh <- struct{}{}
			}
		}),
		WithOnParked(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				parkedCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()

	verifyParkedTimer(t, ctx, sched, fakeClk, store, deadline, fireCh, parkedCh, &activeSess, &mu, &cfg)
}

func verifyParkedTimer(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	store *fakeStore,
	deadline time.Time,
	fireCh, parkedCh <-chan struct{},
	activeSess **Session,
	mu *sync.Mutex,
	cfg *Config,
) {
	t.Helper()
	assertParkedDoesNotFire(t, ctx, sched, fakeClk, deadline, fireCh, parkedCh)

	queueTimerAppend(store, "cmd-recovered-fire", testRun1, 2)
	store.admission.OwnerIncarnation = 2
	recSess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start recovered session: %v", err)
	}
	defer recSess.Stop()

	mu.Lock()
	*activeSess = recSess
	mu.Unlock()

	sched.Wake(testSessionID1)

	select {
	case <-fireCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer to fire after Wake")
	}
}

func assertParkedDoesNotFire(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	fireCh, parkedCh <-chan struct{},
) {
	t.Helper()
	sched.Arm(&ArmedTimer{
		TimerID:    testTimer1,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   deadline,
	})

	fakeClk.Advance(6 * time.Second)

	select {
	case <-parkedCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer to be parked")
	}

	select {
	case <-fireCh:
		t.Fatal("timer should not succeed while recovery required")
	default:
	}

	fakeClk.Advance(10 * time.Minute)
	select {
	case <-fireCh:
		t.Fatal("parked timer should not fire without Wake")
	default:
	}
}

func TestMetadataMarshalFailureDropsTimer(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)
	deadline := startTime.Add(5 * time.Second)

	invalidTimerID := "\xff\xfe"
	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "marshal-fail")
	defer sess.Stop()

	droppedCh := make(chan struct{}, 1)
	fireCh := make(chan struct{}, 1)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			return sess, sessionID == testSessionID1
		},
		WithOnDropped(func(at *ArmedTimer) {
			if at.TimerID == invalidTimerID {
				droppedCh <- struct{}{}
			}
		}),
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == invalidTimerID {
				fireCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()

	sched.Arm(&ArmedTimer{
		TimerID:    invalidTimerID,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   deadline,
	})

	assertDroppedTimerNeverRetries(t, ctx, sched, fakeClk, fireCh, droppedCh)
}
