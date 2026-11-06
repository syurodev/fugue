package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestArmDoesNotUnparkOtherSessionsTimers(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)
	store.admission.OwnerIncarnation = 1
	store.admission.ActiveRunID = testRun1

	deadline1 := startTime.Add(5 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline1},
	})

	ctx := t.Context()
	sess1, sess2, cfg := setupTwoSessionsForUnparkTest(t, ctx, store, fakeClk)

	var mu sync.Mutex
	activeSess1 := sess1
	fire1Count := 0
	parkedCh1 := make(chan struct{}, 1)
	armCh2 := make(chan struct{}, 1)
	fireCh1 := make(chan struct{}, 1)

	sched := startTestSchedulerForUnpark(
		t, ctx, store, fakeClk, &mu, &activeSess1, sess2,
		&fire1Count, parkedCh1, armCh2, fireCh1,
	)
	defer sched.Stop()

	parkInitialTimer(t, ctx, sched, fakeClk, deadline1, parkedCh1)
	armSession2TimerAndAssertNoFire(t, ctx, sched, fakeClk, startTime, armCh2, fireCh1)

	queueTimerAppend(store, "cmd-recovered-fire-scoped", testRun1, 2)
	store.admission.OwnerIncarnation = 2
	recSess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start recovered session: %v", err)
	}
	defer recSess.Stop()

	mu.Lock()
	activeSess1 = recSess
	mu.Unlock()

	wakeAndAssertSingleFire(t, ctx, sched, fakeClk, fireCh1, &fire1Count, &mu)
}

func setupTwoSessionsForUnparkTest(
	t *testing.T,
	ctx context.Context,
	store *fakeStore,
	fakeClk clock.Clock,
) (*Session, *Session, Config) {
	t.Helper()
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("arm-unpark-sess"))
	cfg := Config{
		SessionID:        testSessionID1,
		OwnerIncarnation: 2,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         newFakeExecutor(),
		Clock:            fakeClk,
		IDs:              idGen,
	}
	sess1, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("Start session 1: %v", err)
	}

	cfg2 := cfg
	cfg2.SessionID = "s2"
	cfg2.IDs = ids.NewUUIDv7(fakeClk, ids.NewDeterministic("arm-unpark-sess2"))
	sess2, err := Start(ctx, &cfg2)
	if err != nil {
		sess1.Stop()
		t.Fatalf("Start session 2: %v", err)
	}

	return sess1, sess2, cfg
}

func startTestSchedulerForUnpark(
	t *testing.T,
	ctx context.Context,
	store Store,
	fakeClk clock.Clock,
	mu *sync.Mutex,
	activeSess1 **Session,
	sess2 *Session,
	fire1Count *int,
	parkedCh1, armCh2, fireCh1 chan struct{},
) *Scheduler {
	t.Helper()
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			mu.Lock()
			defer mu.Unlock()
			if sessionID == testSessionID1 {
				return *activeSess1, true
			}
			if sessionID == "s2" {
				return sess2, true
			}
			return nil, false
		},
		WithOnParked(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				parkedCh1 <- struct{}{}
			}
		}),
		WithOnArm(func(at *ArmedTimer) {
			if at.TimerID == "tm-2" {
				armCh2 <- struct{}{}
			}
		}),
		WithOnFire(func(at *ArmedTimer) {
			if at.TimerID == testTimer1 {
				mu.Lock()
				*fire1Count++
				mu.Unlock()
				select {
				case fireCh1 <- struct{}{}:
				default:
				}
			}
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	return sched
}

func parkInitialTimer(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	parkedCh1 <-chan struct{},
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
	case <-parkedCh1:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer 1 to be parked")
	}
}

func armSession2TimerAndAssertNoFire(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	startTime time.Time,
	armCh2, fireCh1 <-chan struct{},
) {
	t.Helper()
	sched.Arm(&ArmedTimer{
		TimerID:    "tm-2",
		SessionID:  "s2",
		RunID:      testRun2,
		Generation: 1,
		Purpose:    kernel.TimerPurposeModelRetry,
		Deadline:   startTime.Add(30 * time.Second),
	})
	select {
	case <-armCh2:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer 2 to arm")
	}
	fakeClk.Advance(5 * time.Second)
	select {
	case <-fireCh1:
		t.Fatal("timer 1 should not unpark or fire when arming session 2 timer")
	default:
	}
}

func wakeAndAssertSingleFire(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	fireCh1 <-chan struct{},
	fireCount *int,
	mu *sync.Mutex,
) {
	t.Helper()
	sched.Wake(testSessionID1)
	select {
	case <-fireCh1:
	case <-ctx.Done():
		t.Fatal("timeout waiting for timer 1 to fire after Wake")
	}
	fakeClk.Advance(10 * time.Minute)
	mu.Lock()
	count := *fireCount
	mu.Unlock()
	if count != 1 {
		t.Fatalf("expected exactly 1 fire for timer 1, got %d", count)
	}
}
