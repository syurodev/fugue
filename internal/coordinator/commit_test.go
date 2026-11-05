package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func startCommitSession(t *testing.T, store *fakeStore, exec *fakeExecutor, seed string) (context.Context, *Session) {
	t.Helper()
	return startTestSession(t, store, exec, seed, 1)
}

func verifyTxAndResolveCounts(t *testing.T, store *fakeStore, wantTx, wantRes int) {
	t.Helper()
	store.mu.Lock()
	txCount := len(store.transitions)
	resolves := len(store.resolves)
	store.mu.Unlock()

	if txCount != wantTx {
		t.Fatalf("expected exactly %d append call(s), got %d", wantTx, txCount)
	}
	if resolves != wantRes {
		t.Fatalf("expected exactly %d resolve call(s), got %d", wantRes, resolves)
	}
}

func TestUnknownCommitReadBackCommittedHandsOffOnce(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)

	// Append fails with ErrCommitUnknown
	store.queueAppend(nil, ErrCommitUnknown)
	// ResolveCommit confirms it was committed
	receipt := codec.Receipt{CommandID: "cmd-disp-unknown", Kind: testKindPrepare, Target: "r1"}
	store.queueResolve(CommitResolutionCommitted, &receipt)

	exec := newFakeExecutor()
	workReceived := make(chan struct{})
	exec.onExec = func(_ *Work, _ func(*Observation)) {
		close(workReceived)
	}

	ctx, sess := startCommitSession(t, store, exec, "commit-committed-test")
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-unknown")

	reply, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	waitSignal(t, ctx, workReceived, "workReceived")

	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected exactly 1 work item handed off, got %d", count)
	}

	verifyTxAndResolveCounts(t, store, 1, 1)

	sess.mu.Lock()
	rRev := sess.runRevs["r1"]
	sess.mu.Unlock()
	if rRev != 2 {
		t.Fatalf("expected run revision 2, got %d", rRev)
	}
}

func TestUnknownCommitReadBackNotCommittedReusesCommandID(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)

	// First append returns ErrCommitUnknown
	store.queueAppend(nil, ErrCommitUnknown)
	// ResolveCommit returns NotCommitted
	store.queueResolve(CommitResolutionNotCommitted, nil)
	// Retried append succeeds
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-retry", Kind: "PrepareDispatch", Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 1,
	}, nil)

	exec := newFakeExecutor()
	workReceived := make(chan struct{})
	exec.onExec = func(_ *Work, _ func(*Observation)) {
		close(workReceived)
	}

	ctx, sess := startCommitSession(t, store, exec, "commit-not-committed-test")
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-retry")

	reply, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	waitSignal(t, ctx, workReceived, "workReceived")

	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected at most 1 work overall, got %d", count)
	}

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) != 2 {
		t.Fatalf("expected 2 append calls (first and retry), got %d", len(transitions))
	}

	if transitions[0].CommandID != transitions[1].CommandID {
		t.Fatalf("expected retried transition to reuse command ID %q, got %q",
			transitions[0].CommandID, transitions[1].CommandID)
	}
}

func setupBackgroundResolveSession(
	t *testing.T,
	store *fakeStore,
	exec *fakeExecutor,
	seed string,
) (context.Context, *Session, *clock.Fake, chan time.Time, chan struct{}) {
	t.Helper()
	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic(seed))
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	resolveReport := make(chan time.Time, 10)
	sess.onResolve = func(tm time.Time) {
		resolveReport <- tm
	}
	armed := make(chan struct{}, 1)
	sess.onTimerArmed = func() {
		select {
		case armed <- struct{}{}:
		default:
		}
	}
	return ctx, sess, fakeClk, resolveReport, armed
}

func advanceRetriesAndCollect(t *testing.T, ctx context.Context, fakeClk *clock.Fake, resolveReport chan time.Time, armed chan struct{}) []time.Time {
	t.Helper()
	resolveTimes := make([]time.Time, 0, 4)
	t1 := waitValue(t, ctx, resolveReport, "retry 0")
	resolveTimes = append(resolveTimes, t1)

	waitSignal(t, ctx, armed, "arm 1")
	fakeClk.Advance(1 * time.Second)
	t2 := waitValue(t, ctx, resolveReport, "retry 1")
	resolveTimes = append(resolveTimes, t2)

	waitSignal(t, ctx, armed, "arm 2")
	fakeClk.Advance(2 * time.Second)
	t3 := waitValue(t, ctx, resolveReport, "retry 2")
	resolveTimes = append(resolveTimes, t3)

	waitSignal(t, ctx, armed, "arm 3")
	fakeClk.Advance(4 * time.Second)
	t4 := waitValue(t, ctx, resolveReport, "retry 3")
	resolveTimes = append(resolveTimes, t4)

	return resolveTimes
}

func verifyResolveOffsets(t *testing.T, resolveTimes []time.Time, startTime time.Time) {
	t.Helper()
	if len(resolveTimes) < 4 {
		t.Fatalf("expected at least 4 resolve calls, got %d", len(resolveTimes))
	}
	if offset := resolveTimes[1].Sub(startTime); offset != 1*time.Second {
		t.Errorf("expected retry 1 at offset 1s, got %v", offset)
	}
	if offset := resolveTimes[2].Sub(startTime); offset != 3*time.Second {
		t.Errorf("expected retry 2 at offset 3s, got %v", offset)
	}
	if offset := resolveTimes[3].Sub(startTime); offset != 7*time.Second {
		t.Errorf("expected retry 3 at offset 7s, got %v", offset)
	}
}

func TestUnknownCommitUnresolvedHaltsDispatchAndKeepsSlot(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	setupAdmittedRun(store)

	// First append returns ErrCommitUnknown
	store.queueAppend(nil, ErrCommitUnknown)
	// ResolveCommit returns unavailable 3 times
	store.queueResolve(CommitResolutionUnavailable, nil)
	store.queueResolve(CommitResolutionUnavailable, nil)
	store.queueResolve(CommitResolutionUnavailable, nil)

	exec := newFakeExecutor()
	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, exec, "commit-unresolved-test")
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-unresolved")

	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, &req)
		return sErr
	})

	resolveTimes := advanceRetriesAndCollect(t, ctx, fakeClk, resolveReport, armed)
	verifyResolveOffsets(t, resolveTimes, startTime)

	// Verify zero work handed off
	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected zero work items, got %d", count)
	}

	// Slot is still held
	_, canReserve := sess.slots.reserve()
	if canReserve {
		t.Fatal("expected slot to still be held during unresolved commit")
	}

	// New dispatch request returns ErrCommitUnresolved
	reqNew := makeModelDispatchRequest("r1", "m1", "a1", "cmd-new-disp")
	_, err := sess.Submit(ctx, &reqNew)
	if !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("expected ErrCommitUnresolved for new dispatch, got %v", err)
	}
}

func TestShutdownDuringUnknownCommitNeverHandsOff(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("shutdown-unknown-test"))

	store := newFakeStore()
	setupAdmittedRun(store)

	store.queueAppend(nil, ErrCommitUnknown)
	store.queueResolve(CommitResolutionUnavailable, nil)

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}

	resolveDone := make(chan struct{})
	sess.onResolve = func(_ time.Time) {
		close(resolveDone)
	}

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-shutdown")

	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, &req)
		return sErr
	})

	waitSignal(t, ctx, resolveDone, "resolveDone")

	// Shutdown the session
	sess.Stop()

	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected 0 work items after shutdown during unknown commit, got %d", count)
	}
}

func TestBackgroundResolverCommitted(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)

	// Append fails with unknown
	store.queueAppend(nil, ErrCommitUnknown)
	// First resolve: unavailable (spawns background loop)
	store.queueResolve(CommitResolutionUnavailable, nil)
	// Second resolve (inside background loop): committed
	receipt := codec.Receipt{
		CommandID: "cmd-disp-bg-commit",
		Kind:      testKindPrepare,
		Principal: testOriginSystem,
		Target:    "r1",
	}
	store.queueResolve(CommitResolutionCommitted, &receipt)

	workCh := make(chan *Work, 1)
	exec := newFakeExecutor()
	exec.onExec = func(w *Work, _ func(*Observation)) {
		select {
		case workCh <- w:
		default:
		}
	}

	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, exec, "bg-committed-test")
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-bg-commit")
	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, &req)
		return sErr
	})

	// Initial resolve
	waitValue(t, ctx, resolveReport, "resolveReport initial")

	// Wait for background timer to arm, then advance clock
	waitSignal(t, ctx, armed, "armed")
	fakeClk.Advance(1 * time.Second)
	waitValue(t, ctx, resolveReport, "resolveReport retry")

	// Work should now be handed off
	waitValue(t, ctx, workCh, "workCh")

	sess.mu.Lock()
	rRev := sess.runRevs["r1"]
	sess.mu.Unlock()
	if rRev != 2 {
		t.Fatalf("expected run revision 2, got %d", rRev)
	}
}

func TestBackgroundResolverNotCommitted(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)

	// Append fails with unknown
	store.queueAppend(nil, ErrCommitUnknown)
	// First resolve: unavailable (spawns background loop)
	store.queueResolve(CommitResolutionUnavailable, nil)
	// Second resolve (inside background loop): not committed
	store.queueResolve(CommitResolutionNotCommitted, nil)

	workCh := make(chan *Work, 1)
	exec := newFakeExecutor()
	exec.onExec = func(w *Work, _ func(*Observation)) {
		select {
		case workCh <- w:
		default:
		}
	}

	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, exec, "bg-not-committed-test")
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-bg-not-commit")
	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, &req)
		return sErr
	})

	// Initial resolve
	waitValue(t, ctx, resolveReport, "resolveReport initial")

	// Wait for timer to arm, advance clock to trigger retry
	waitSignal(t, ctx, armed, "armed")
	fakeClk.Advance(1 * time.Second)
	waitValue(t, ctx, resolveReport, "resolveReport retry")

	// Work should be handed off after retry succeeds
	waitValue(t, ctx, workCh, "workCh")
}
