package coordinator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
)

func TestFiredSetIsBounded(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	deadline := startTime.Add(5 * time.Second)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1:       {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline},
		testTimerBarrier: {TimerID: testTimerBarrier, Generation: 1, Purpose: kernel.TimerPurposeToolRetry, Deadline: deadline.Add(firedTTL + time.Second)},
	})

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "fired-bounded")
	defer sess.Stop()

	queueTimerAppend(store, "cmd-fired-1", testRun1, 2)
	queueTimerAppend(store, "cmd-fired-barrier", testRun1, 3)
	queueTimerAppend(store, "cmd-fired-2", testRun1, 4)

	fireCh := make(chan *ArmedTimer, 5)
	armCh := make(chan string, 5)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sessionID string) (*Session, bool) {
			return sess, sessionID == testSessionID1
		},
		WithOnFire(func(at *ArmedTimer) { fireCh <- at }),
		WithOnArm(func(at *ArmedTimer) { armCh <- at.TimerID }),
		WithOnDropped(func(at *ArmedTimer) {
			t.Errorf("unexpected drop for %s gen %d", at.TimerID, at.Generation)
		}),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer sched.Stop()
	sess.SetScheduler(sched)

	verifyLateRearmNoOp(t, ctx, sched, fakeClk, deadline, armCh, fireCh, store)
}

func armAndFireInitial(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	armCh <-chan string,
	fireCh <-chan *ArmedTimer,
) {
	t.Helper()
	sched.Arm(&ArmedTimer{
		TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1,
		Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline,
	})
	waitTimerArm(t, ctx, armCh, testTimer1)
	fakeClk.Advance(6 * time.Second)
	waitTimerFired(t, ctx, fireCh, testTimer1)
}

func verifyLateRearmNoOp(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	deadline time.Time,
	armCh <-chan string,
	fireCh <-chan *ArmedTimer,
	store *fakeStore,
) {
	t.Helper()
	armAndFireInitial(t, ctx, sched, fakeClk, deadline, armCh, fireCh)

	// At exact firedTTL (10 min), fired entry is not evicted yet (now.Sub > firedTTL is false).
	fakeClk.Advance(firedTTL)
	sched.Arm(&ArmedTimer{
		TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1,
		Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline,
	})
	waitTimerArm(t, ctx, armCh, testTimer1)

	// Fire a barrier timer at now+500ms; testTimer1 must stay deduplicated while barrier fires.
	barrierDeadline := fakeClk.Now().Add(500 * time.Millisecond)
	sched.Arm(&ArmedTimer{
		TimerID: testTimerBarrier, SessionID: testSessionID1, RunID: testRun1,
		Generation: 1, Purpose: kernel.TimerPurposeToolRetry, Deadline: barrierDeadline,
	})
	waitTimerArm(t, ctx, armCh, testTimerBarrier)
	fakeClk.Advance(500 * time.Millisecond)
	waitTimerFired(t, ctx, fireCh, testTimerBarrier)

	// Advance past firedTTL to evict entry from fired set, then re-arm old gen as no-op.
	fakeClk.Advance(time.Second)
	sched.Arm(&ArmedTimer{
		TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1,
		Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: deadline,
	})
	waitTimerArm(t, ctx, armCh, testTimer1)
	waitTimerFired(t, ctx, fireCh, testTimer1)

	if txCount := store.appendCount(); txCount != 3 {
		t.Fatalf("expected 3 transitions (initial, barrier, late no-op), got %d", txCount)
	}
}

const (
	testTimerA = "tm-a"
	testTimerB = "tm-b"
)

func TestWakeIsScopedToSessionUnlessEmpty(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)
	store.admission.OwnerIncarnation = 2
	store.admission.ActiveRunID = testRun1

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess1, sess2, _ := setupTwoSessionsForUnparkTest(t, ctx, store, fakeClk)
	defer sess1.Stop()
	defer sess2.Stop()

	setupScopedWakeStore(store, startTime)
	sched, armCh, parkedCh, firedCh, setRoutable := startScopedWakeScheduler(t, ctx, store, fakeClk, sess1, sess2)
	defer sched.Stop()

	sched.Arm(&ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(time.Second)})
	sched.Arm(&ArmedTimer{TimerID: testTimer2, SessionID: "s2", RunID: testRun2, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(time.Second)})
	waitTimerArm(t, ctx, armCh, "")
	waitTimerArm(t, ctx, armCh, "")
	fakeClk.Advance(2 * time.Second)
	waitValue(t, ctx, parkedCh, "park 1")
	waitValue(t, ctx, parkedCh, "park 2")

	setRoutable()
	sched.Wake(testSessionID1)
	waitTimerFired(t, ctx, firedCh, testTimer1)
	fakeClk.Advance(time.Minute)
	select {
	case id := <-firedCh:
		t.Fatalf("Wake(s1) unexpectedly woke timer %s for s2", id.TimerID)
	default:
	}
	sched.Wake("")
	waitTimerFired(t, ctx, firedCh, testTimer2)
}

func setupScopedWakeStore(store *fakeStore, startTime time.Time) {
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(time.Second)},
	})
	setupTestRunState(store, testRun2, false, map[string]*kernel.ActiveTimer{
		testTimer2: {TimerID: testTimer2, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(time.Second)},
	})
	queueTimerAppend(store, "cmd-wake-1", testRun1, 2)
	queueTimerAppend(store, "cmd-wake-2", testRun2, 2)
}

func startScopedWakeScheduler(
	t *testing.T,
	ctx context.Context,
	store Store,
	fakeClk clock.Clock,
	sess1, sess2 *Session,
) (*Scheduler, <-chan string, <-chan string, <-chan *ArmedTimer, func()) {
	t.Helper()
	var mu sync.Mutex
	routable := false
	armCh, parkedCh := make(chan string, 2), make(chan string, 2)
	firedCh := make(chan *ArmedTimer, 2)
	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sid string) (*Session, bool) {
			mu.Lock()
			defer mu.Unlock()
			if !routable {
				return nil, false
			}
			if sid == testSessionID1 {
				return sess1, true
			}
			return sess2, sid == "s2"
		},
		WithOnArm(func(at *ArmedTimer) { armCh <- at.TimerID }),
		WithOnParked(func(at *ArmedTimer) { parkedCh <- at.TimerID }),
		WithOnFire(func(at *ArmedTimer) { firedCh <- at }),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	return sched, armCh, parkedCh, firedCh, func() {
		mu.Lock()
		routable = true
		mu.Unlock()
	}
}

func TestSetSchedulerNilAndZeroSchedulerStopAreSafe(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "set-sched-nil", 1)
	defer sess.Stop()

	sess.SetScheduler(nil)
	zeroSched := &Scheduler{stopCh: make(chan struct{})}
	zeroSched.Stop()
	_ = ctx
}

func TestSchedulerStartupDeduplicatesInitialTimersByGeneration(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	store.setArmedTimers(
		ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(20 * time.Second)},
		ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(5 * time.Second)},
		ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(10 * time.Second)},
	)
	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(10 * time.Second)},
	})
	queueTimerAppend(store, "cmd-dedup-init", testRun1, 2)

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "init-dedup")
	defer sess.Stop()
	sched, firedCh := startTestScheduler(t, ctx, store, fakeClk, sess)
	defer sched.Stop()

	// Gen 1 at +5s must NOT overwrite Gen 2; Gen 2 at +10s overwrote Gen 2 at +20s.
	fakeClk.Advance(9 * time.Second)
	select {
	case fired := <-firedCh:
		t.Fatalf("timer fired too early at +9s: %+v", fired)
	default:
	}

	fakeClk.Advance(time.Second)
	fired := waitValue(t, ctx, firedCh, "timer fired")
	if fired.TimerID != testTimer1 || fired.Generation != 2 || !fired.Deadline.Equal(startTime.Add(10*time.Second)) {
		t.Fatalf("expected gen 2 (+10s) timer to fire, got %+v", fired)
	}
}

func TestDueTimersAndFireDueTimersEdgeCases(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	sched := &Scheduler{clk: fakeClk}

	active := map[string]*scheduledTimer{
		testTimerB: {ArmedTimer: ArmedTimer{TimerID: testTimerB, Deadline: startTime, Generation: 1}},
		testTimerA: {ArmedTimer: ArmedTimer{TimerID: testTimerA, Deadline: startTime, Generation: 1}},
	}
	due := sched.dueTimers(active, startTime)
	if len(due) != 2 || due[0].TimerID != testTimerA || due[1].TimerID != testTimerB {
		t.Fatalf("expected deterministic ID tie-break [tm-a, tm-b], got %+v", due)
	}

	fired := map[string]firedEntry{testTimerA: {generation: 1, firedAt: startTime}}
	inFlight := make(map[string]uint64)
	activeOnlyFired := map[string]*scheduledTimer{
		testTimerA: {ArmedTimer: ArmedTimer{TimerID: testTimerA, Deadline: startTime, Generation: 1}},
	}
	sched.fireDueTimers(t.Context(), activeOnlyFired, fired, inFlight)
	if _, exists := activeOnlyFired[testTimerA]; exists {
		t.Fatal("expected already-fired timer to be removed from active without launching")
	}
	if _, launching := inFlight[testTimerA]; launching {
		t.Fatal("expected already-fired timer not to enter inFlight")
	}
}

func TestInFlightTimerDoesNotDoubleFireAndDrainArmsUpdatesEqualGen(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	store := newTestStoreWithAdmission(1)

	setupTestRunState(store, testRun1, true, map[string]*kernel.ActiveTimer{
		testTimer1: {TimerID: testTimer1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(5 * time.Second)},
		testTimer2: {TimerID: testTimer2, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(15 * time.Second)},
	})
	queueTimerAppend(store, "cmd-inflight-1", testRun1, 2)
	queueTimerAppend(store, "cmd-inflight-2", testRun1, 3)

	ctx, sess := newTestSessionWithClock(t, store, newFakeExecutor(), fakeClk, "inflight-dedup")
	defer sess.Stop()

	routeEntered := make(chan struct{}, 4)
	releaseRoute := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	routeCalls := 0
	armCh := make(chan string, 10)
	fireCh := make(chan *ArmedTimer, 10)

	sched, err := StartScheduler(
		ctx, store, fakeClk,
		func(sid string) (*Session, bool) {
			mu.Lock()
			routeCalls++
			mu.Unlock()
			routeEntered <- struct{}{}
			<-releaseRoute
			return sess, sid == testSessionID1
		},
		WithOnArm(func(at *ArmedTimer) { armCh <- at.TimerID }),
		WithOnFire(func(at *ArmedTimer) { fireCh <- at }),
	)
	if err != nil {
		t.Fatalf("StartScheduler: %v", err)
	}
	defer func() {
		releaseOnce.Do(func() { close(releaseRoute) })
		sched.Stop()
	}()

	armGenerationsAndWait(t, ctx, sched, startTime, armCh)
	assertInFlightWakeAndDrain(t, ctx, sched, fakeClk, routeEntered, &releaseOnce, releaseRoute, &mu, &routeCalls, fireCh)
}

func armGenerationsAndWait(t *testing.T, ctx context.Context, sched *Scheduler, startTime time.Time, armCh <-chan string) {
	t.Helper()
	sched.Arm(&ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(10 * time.Second)})
	waitTimerArm(t, ctx, armCh, testTimer1)
	sched.Arm(&ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(2 * time.Second)})
	waitTimerArm(t, ctx, armCh, testTimer1)
	sched.Arm(&ArmedTimer{TimerID: testTimer1, SessionID: testSessionID1, RunID: testRun1, Generation: 2, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(5 * time.Second)})
	waitTimerArm(t, ctx, armCh, testTimer1)
	sched.Arm(&ArmedTimer{TimerID: testTimer2, SessionID: testSessionID1, RunID: testRun1, Generation: 1, Purpose: kernel.TimerPurposeModelRetry, Deadline: startTime.Add(15 * time.Second)})
	waitTimerArm(t, ctx, armCh, testTimer2)
}

func assertInFlightWakeAndDrain(
	t *testing.T,
	ctx context.Context,
	sched *Scheduler,
	fakeClk *clock.Fake,
	routeEntered <-chan struct{},
	releaseOnce *sync.Once,
	releaseRoute chan struct{},
	mu *sync.Mutex,
	routeCalls *int,
	fireCh <-chan *ArmedTimer,
) {
	t.Helper()
	fakeClk.Advance(5 * time.Second)
	waitSignal(t, ctx, routeEntered, "tm-1 route entered")

	sched.Wake(testSessionID1)
	fakeClk.Advance(5 * time.Second)
	mu.Lock()
	callsWhileInFlight := *routeCalls
	mu.Unlock()
	if callsWhileInFlight != 1 {
		t.Fatalf("expected 1 route call while tm-1 in-flight, got %d", callsWhileInFlight)
	}

	releaseOnce.Do(func() { close(releaseRoute) })
	waitTimerFired(t, ctx, fireCh, testTimer1)
	fakeClk.Advance(5 * time.Second)
	waitTimerFired(t, ctx, fireCh, testTimer2)
}

func TestSchedulerInternalsBoundaries(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	sched := &Scheduler{clk: clock.NewFake(startTime)}
	assertCleanFiredAndDrainArms(t, sched, startTime)
	assertEarliestTargetAndBackoff(t, sched, startTime)
}

func assertCleanFiredAndDrainArms(t *testing.T, sched *Scheduler, startTime time.Time) {
	t.Helper()
	fired := map[string]firedEntry{testTimer1: {generation: 1, firedAt: startTime}}
	sched.cleanFired(fired, startTime.Add(firedTTL))
	if _, ok := fired[testTimer1]; !ok {
		t.Fatal("cleanFired must retain entry at exact firedTTL")
	}
	sched.cleanFired(fired, startTime.Add(firedTTL+time.Nanosecond))
	if _, ok := fired[testTimer1]; ok {
		t.Fatal("cleanFired must evict entry past firedTTL")
	}

	fired = map[string]firedEntry{testTimer1: {generation: 2, firedAt: startTime}}
	active := make(map[string]*scheduledTimer)
	sched.drainArms([]*ArmedTimer{
		{TimerID: testTimer1, Generation: 1},
		{TimerID: testTimer1, Generation: 2},
	}, active, fired)
	if len(active) != 0 || fired[testTimer1].generation != 2 {
		t.Fatalf("drainArms must ignore already-fired gen <=2, active=%+v fired=%+v", active, fired)
	}
	sched.drainArms([]*ArmedTimer{{TimerID: testTimer1, Generation: 3}}, active, fired)
	if _, ok := fired[testTimer1]; ok || active[testTimer1].Generation != 3 {
		t.Fatalf("drainArms must replace fired entry with newer gen 3, active=%+v fired=%+v", active, fired)
	}
}

func assertEarliestTargetAndBackoff(t *testing.T, sched *Scheduler, startTime time.Time) {
	t.Helper()
	active := map[string]*scheduledTimer{
		testTimer1: {ArmedTimer: ArmedTimer{TimerID: testTimer1, Generation: 1, Deadline: startTime}},
		testTimer2: {ArmedTimer: ArmedTimer{TimerID: testTimer2, Generation: 1, Deadline: startTime.Add(10 * time.Second)}},
	}
	target, ok := earliestTarget(active, map[string]uint64{testTimer1: 1})
	if !ok || !target.Equal(startTime.Add(10*time.Second)) {
		t.Fatalf("expected earliestTarget to skip in-flight tm-1 and pick +10s, got %v ok=%v", target, ok)
	}

	timer := &scheduledTimer{ArmedTimer: ArmedTimer{TimerID: testTimer1, Generation: 1}}
	active = make(map[string]*scheduledTimer)
	fired := make(map[string]firedEntry)
	_, retried, _, _ := sched.handleFireResult(t.Context(), fireResult{timer: timer, err: errAppendObsBoom}, active, fired)
	if retried == nil || timer.backoff != timerBackoffStart || !timer.retryAfter.Equal(startTime.Add(timerBackoffStart)) {
		t.Fatalf("expected initial backoff %v, got %v / %v", timerBackoffStart, timer.backoff, timer.retryAfter)
	}
	_, _, _, _ = sched.handleFireResult(t.Context(), fireResult{timer: timer, err: errAppendObsBoom}, active, fired)
	if timer.backoff != 2*timerBackoffStart || !timer.retryAfter.Equal(startTime.Add(2*timerBackoffStart)) {
		t.Fatalf("expected doubled backoff %v, got %v / %v", 2*timerBackoffStart, timer.backoff, timer.retryAfter)
	}
}
