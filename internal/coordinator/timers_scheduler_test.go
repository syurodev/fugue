package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	testSessionFast = "s-fast"
	testTimerSlow   = "tm-slow"
	testTimerFast   = "tm-fast"
	testRunSlow     = "r-slow"
	testRunFast     = "r-fast"
)

func setupSlowFastSessions(
	t *testing.T,
	ctx context.Context,
	store *fakeStore,
	fakeClk *clock.Fake,
	startTime time.Time,
) (*Session, *Session) {
	t.Helper()
	_, sessSlow := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "slow-sess")

	idGenFast := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("fast-sess"))
	cfgFast := Config{
		SessionID:        testSessionFast,
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         newFakeExecutor(),
		Clock:            fakeClk,
		IDs:              idGenFast,
	}
	sessFast, err := Start(ctx, &cfgFast)
	if err != nil {
		t.Fatalf("start fast session: %v", err)
	}

	setupTestRunState(store, testRunSlow, true, map[string]*kernel.ActiveTimer{
		testTimerSlow: {TimerID: testTimerSlow, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(5 * time.Second)},
	})
	setupTestRunState(store, testRunFast, true, map[string]*kernel.ActiveTimer{
		testTimerFast: {TimerID: testTimerFast, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(10 * time.Second)},
	})

	queueTimerAppend(store, "cmd-fast", testRunFast, 2)
	queueTimerAppend(store, "cmd-slow", testRunSlow, 2)

	return sessSlow, sessFast
}

func TestSlowSessionDoesNotDelayOtherSessionsTimers(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)

	sessSlow, sessFast := setupSlowFastSessions(t, ctx, store, fakeClk, startTime)
	defer sessSlow.Stop()
	defer sessFast.Stop()

	slowBlockCh := make(chan struct{})
	fastFiredCh := make(chan struct{}, 1)
	slowFiredCh := make(chan struct{}, 1)

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			if sessionID == testSessionFast {
				return sessFast, true
			}
			if sessionID == testSessionID1 {
				<-slowBlockCh
				return sessSlow, true
			}
			return nil, false
		},
		WithOnFire(func(at *ArmedTimer) {
			switch at.TimerID {
			case testTimerFast:
				fastFiredCh <- struct{}{}
			case testTimerSlow:
				slowFiredCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	var slowOnce sync.Once
	defer func() {
		slowOnce.Do(func() { close(slowBlockCh) })
		sched.Stop()
	}()

	sessFast.SetScheduler(sched)
	sessSlow.SetScheduler(sched)

	armAndVerifyFastSlow(t, ctx, sched, fakeClk, &slowOnce, slowBlockCh, fastFiredCh, slowFiredCh, startTime)
}

func armAndVerifyFastSlow(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	slowOnce *sync.Once,
	slowBlockCh chan struct{},
	fastFiredCh, slowFiredCh <-chan struct{},
	startTime time.Time,
) {
	t.Helper()
	sched.Arm(&ArmedTimer{
		TimerID:    testTimerSlow,
		SessionID:  testSessionID1,
		RunID:      testRunSlow,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   startTime.Add(5 * time.Second),
	})
	sched.Arm(&ArmedTimer{
		TimerID:    testTimerFast,
		SessionID:  testSessionFast,
		RunID:      testRunFast,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   startTime.Add(10 * time.Second),
	})

	fakeClk.Advance(15 * time.Second)

	select {
	case <-fastFiredCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for fast session timer to fire")
	}

	slowOnce.Do(func() { close(slowBlockCh) })

	select {
	case <-slowFiredCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for slow session timer to fire")
	}
}

func assertNonBlockingArm(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	startTime time.Time,
) {
	t.Helper()
	armDone := make(chan struct{})
	_ = supervise.Go(ctx, "test.arm", func(_ context.Context) error {
		sched.Arm(&ArmedTimer{
			TimerID:    testTimer2,
			SessionID:  testSessionID1,
			RunID:      testRun1,
			Generation: 1,
			Purpose:    kernel.TimerPurposeModelRetry,
			Deadline:   startTime.Add(20 * time.Second),
		})
		close(armDone)
		return nil
	})

	select {
	case <-armDone:
	case <-ctx.Done():
		t.Fatal("sched.Arm blocked while scheduler was submitting!")
	}
}

func TestArmFromActorNeverBlocksWhileSchedulerSubmits(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	blockSubmit := make(chan struct{})
	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "arm-nonblock")
	defer sess.Stop()

	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(5 * time.Second)},
		testTimer2: {TimerID: testTimer2, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(20 * time.Second)},
	})

	inSubmit := make(chan struct{}, 1)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			select {
			case inSubmit <- struct{}{}:
			default:
			}
			<-blockSubmit
			return sess, true
		},
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	var unblockOnce sync.Once
	defer func() {
		unblockOnce.Do(func() { close(blockSubmit) })
		sched.Stop()
	}()

	sched.Arm(&ArmedTimer{
		TimerID:    testTimer1,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   startTime.Add(5 * time.Second),
	})

	fakeClk.Advance(6 * time.Second)

	select {
	case <-inSubmit:
	case <-ctx.Done():
		t.Fatal("timeout waiting for submit to begin")
	}

	assertNonBlockingArm(t, ctx, sched, startTime)
	unblockOnce.Do(func() { close(blockSubmit) })
}

func TestUnroutableTimerStaysPendingUntilSessionRegisters(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	timer := setupOverdueTimer(store, startTime.Add(-10*time.Second))

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "unroutable")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-unroutable", testRun1, 2)

	var mu sync.Mutex
	registered := false
	firedCh, parkedCh := make(chan struct{}, 1), make(chan struct{}, 1)

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			mu.Lock()
			defer mu.Unlock()
			if registered && sessionID == testSessionID1 {
				return sess, true
			}
			return nil, false
		},
		WithOnParked(func(at *ArmedTimer) {
			if at.TimerID == timer.TimerID {
				parkedCh <- struct{}{}
			}
		}),
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == timer.TimerID {
				firedCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()

	verifyUnroutableParksThenFires(t, ctx, sched, sess, fakeClk, parkedCh, firedCh, &mu, &registered)
}

func verifyUnroutableParksThenFires(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	sess *Session,
	fakeClk *clock.Fake,
	parkedCh, firedCh <-chan struct{},
	mu *sync.Mutex,
	registered *bool,
) {
	t.Helper()
	waitSignal(t, ctx, parkedCh, "unroutable timer to park")
	fakeClk.Advance(1 * time.Second)
	select {
	case <-firedCh:
		t.Fatal("timer fired while session was unroutable!")
	default:
	}

	mu.Lock()
	*registered = true
	mu.Unlock()
	sess.SetScheduler(sched)
	waitSignal(t, ctx, firedCh, "timer to fire after session registration")
}

func armAndAwaitRetry(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	armCh, firedCh <-chan struct{},
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

	select {
	case <-armCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for initial arm")
	}

	fakeClk.Advance(6 * time.Second)

	// Wait for retry to be scheduled after attempt 1
	select {
	case <-armCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for retry arm")
	}

	fakeClk.Advance(timerBackoffStart + time.Millisecond)

	select {
	case <-firedCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer to fire on retry after transient error")
	}
}

func TestTransientSubmitErrorDoesNotDropTimer(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(5 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "transient-err")
	defer sess.Stop()

	store.queueAppend(nil, errcode.New(errcode.Unavailable, "store unavailable"))
	queueTimerAppend(store, "cmd-retry", testRun1, 2)

	armCh := make(chan struct{}, 10)
	firedCh := make(chan struct{}, 1)

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			if sessionID == testSessionID1 {
				return sess, true
			}
			return nil, false
		},
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				firedCh <- struct{}{}
			}
		}),
		WithOnArm(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				armCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()
	sess.SetScheduler(sched)

	armAndAwaitRetry(t, ctx, sched, fakeClk, deadline, armCh, firedCh)
}

func TestPermanentRejectionDropsTimer(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(5 * time.Second)
	setupTestRunState(store, testRun1, false, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})
	store.mu.Lock()
	store.runs[testRun1].RunID = "mismatched-run"
	store.mu.Unlock()

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "perm-rej")
	defer sess.Stop()

	fireCh := make(chan struct{}, 1)
	droppedCh := make(chan struct{}, 1)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			return sess, sessionID == testSessionID1
		},
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				fireCh <- struct{}{}
			}
		}),
		WithOnDropped(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				droppedCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()

	sched.Arm(&ArmedTimer{
		TimerID:    testTimer1,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   deadline,
	})

	assertDroppedTimerNeverRetries(t, ctx, sched, fakeClk, fireCh, droppedCh)
}

func assertDroppedTimerNeverRetries(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	fireCh, droppedCh <-chan struct{},
) {
	t.Helper()
	fakeClk.Advance(6 * time.Second)
	select {
	case <-droppedCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer to be dropped")
	}
	select {
	case <-fireCh:
		t.Fatal("timer should be dropped after definite rejection")
	default:
	}

	fakeClk.Advance(10 * time.Minute)
	sched.Wake(testSessionID1)
	select {
	case <-fireCh:
		t.Fatal("timer should not retry after being dropped")
	default:
	}
}

func TestTransientErrorBacksOff(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "transient-backoff")
	defer sess.Stop()

	var mu sync.Mutex
	attempts := 0

	store.queueAppend(nil, errcode.New(errcode.Unavailable, "store unavailable 1"))
	store.queueAppend(nil, errcode.New(errcode.Unavailable, "store unavailable 2"))
	store.queueAppend(nil, errcode.New(errcode.Unavailable, "store unavailable 3"))

	armCh := make(chan struct{}, 10)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			if sessionID == testSessionID1 {
				mu.Lock()
				attempts++
				mu.Unlock()
				return sess, true
			}
			return nil, false
		},
		WithOnArm(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				armCh <- struct{}{}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()

	verifyTransientBackoffIntervals(t, ctx, sched, fakeClk, deadline, armCh, &attempts, &mu)
}

func verifyTransientBackoffIntervals(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	armCh <-chan struct{},
	attempts *int,
	mu *sync.Mutex,
) {
	t.Helper()
	sched.Arm(&ArmedTimer{
		TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1,
		Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline,
	})
	waitSignal(t, ctx, armCh, "initial arm")

	fakeClk.Advance(time.Second)
	waitSignal(t, ctx, armCh, "retry 1 arm")
	requireAttemptCount(t, mu, attempts, 1, "at t=1s")

	fakeClk.Advance(500 * time.Millisecond)
	requireAttemptCount(t, mu, attempts, 1, "during 1s backoff")

	fakeClk.Advance(500 * time.Millisecond)
	waitSignal(t, ctx, armCh, "retry 2 arm")
	requireAttemptCount(t, mu, attempts, 2, "at t=2s")

	fakeClk.Advance(1500 * time.Millisecond)
	requireAttemptCount(t, mu, attempts, 2, "during 2s backoff")

	fakeClk.Advance(500 * time.Millisecond)
	waitSignal(t, ctx, armCh, "retry 3 arm")
	requireAttemptCount(t, mu, attempts, 3, "at t=4s")
}

func requireAttemptCount(t *testing.T, mu *sync.Mutex, attempts *int, want int, when string) {
	t.Helper()
	mu.Lock()
	got := *attempts
	mu.Unlock()
	if got != want {
		t.Fatalf("expected %d attempts %s, got %d", want, when, got)
	}
}

func TestStopCancelsInFlightFire(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	deadline := startTime.Add(time.Second)

	// Create a session and stop its loop so its mailbox has no reader,
	// causing sess.Submit to block on s.mailbox <- sub until fireCtx is cancelled.
	_, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "stop-cancel")
	sess.task.Stop()
	<-sess.task.Done()

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			return sess, sessionID == testSessionID1
		},
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}

	sched.Arm(&ArmedTimer{
		TimerID:    testTimer1,
		SessionID:  testSessionID1,
		RunID:      testRun1,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   deadline,
	})

	fakeClk.Advance(time.Second)

	stopped := make(chan struct{})
	_ = supervise.Go(ctx, "test.stop", func(_ context.Context) error {
		sched.Stop()
		close(stopped)
		return nil
	})

	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("sched.Stop hung while a fire was in-flight")
	}
}
