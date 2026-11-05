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

func TestUnknownCommitReadBackCommittedHandsOffOnce(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("commit-committed-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	// Append fails with ErrCommitUnknown
	store.queueAppend(AppendResult{}, ErrCommitUnknown)
	// ResolveCommit confirms it was committed
	receipt := codec.Receipt{CommandID: "cmd-disp-unknown", Kind: "PrepareDispatch", Target: "r1"}
	store.queueResolve(CommitResolutionCommitted, receipt, nil)

	exec := newFakeExecutor()
	workReceived := make(chan struct{})
	exec.onExec = func(_ Work, _ func(Observation)) {
		close(workReceived)
	}

	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-unknown")

	reply, err := sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	<-workReceived

	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected exactly 1 work item handed off, got %d", count)
	}

	store.mu.Lock()
	txCount := len(store.transitions)
	resolves := len(store.resolves)
	store.mu.Unlock()

	if txCount != 1 {
		t.Fatalf("expected exactly 1 append call, got %d", txCount)
	}
	if resolves != 1 {
		t.Fatalf("expected exactly 1 resolve call, got %d", resolves)
	}
}

func TestUnknownCommitReadBackNotCommittedReusesCommandID(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("commit-not-committed-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	// First append returns ErrCommitUnknown
	store.queueAppend(AppendResult{}, ErrCommitUnknown)
	// ResolveCommit returns NotCommitted
	store.queueResolve(CommitResolutionNotCommitted, codec.Receipt{}, nil)
	// Retried append succeeds
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-retry", Kind: "PrepareDispatch", Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 1,
	}, nil)

	exec := newFakeExecutor()
	workReceived := make(chan struct{})
	exec.onExec = func(_ Work, _ func(Observation)) {
		close(workReceived)
	}

	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
	}

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-retry")

	reply, err := sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	<-workReceived

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

func TestUnknownCommitUnresolvedHaltsDispatchAndKeepsSlot(t *testing.T) {
	t.Parallel()

	startTime := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(startTime)
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("commit-unresolved-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	// First append returns ErrCommitUnknown
	store.queueAppend(AppendResult{}, ErrCommitUnknown)
	// ResolveCommit returns unavailable 3 times
	store.queueResolve(CommitResolutionUnavailable, codec.Receipt{}, nil)
	store.queueResolve(CommitResolutionUnavailable, codec.Receipt{}, nil)
	store.queueResolve(CommitResolutionUnavailable, codec.Receipt{}, nil)

	resolveTimes := make([]time.Time, 0)
	resolveReport := make(chan time.Time, 10)
	originalResolve := store.ResolveCommit
	_ = originalResolve

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

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	sess.onResolve = func(t time.Time) {
		resolveReport <- t
	}

	armed := make(chan struct{}, 1)
	sess.onTimerArmed = func() {
		select {
		case armed <- struct{}{}:
		default:
		}
	}

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-unresolved")

	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, _ = sess.Submit(ctx, req)
		return nil
	})

	// Initial ResolveCommit called immediately upon unknown commit
	t1 := <-resolveReport
	resolveTimes = append(resolveTimes, t1)

	// Wait for timer to be armed before advancing fake clock
	<-armed
	// Advance clock by 1s: first retry
	fakeClk.Advance(1 * time.Second)
	t2 := <-resolveReport
	resolveTimes = append(resolveTimes, t2)

	<-armed
	// Advance clock by 2s: second retry (offset 3s)
	fakeClk.Advance(2 * time.Second)
	t3 := <-resolveReport
	resolveTimes = append(resolveTimes, t3)

	<-armed
	// Advance clock by 4s: third retry (offset 7s)
	fakeClk.Advance(4 * time.Second)
	t4 := <-resolveReport
	resolveTimes = append(resolveTimes, t4)

	// Verify offsets
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
	_, err = sess.Submit(ctx, reqNew)
	if !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("expected ErrCommitUnresolved for new dispatch, got %v", err)
	}
}

func TestShutdownDuringUnknownCommitNeverHandsOff(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("shutdown-unknown-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	store.queueAppend(AppendResult{}, ErrCommitUnknown)
	store.queueResolve(CommitResolutionUnavailable, codec.Receipt{}, nil)

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

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}

	resolveDone := make(chan struct{})
	sess.onResolve = func(_ time.Time) {
		close(resolveDone)
	}

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-shutdown")

	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, _ = sess.Submit(ctx, req)
		return nil
	})

	<-resolveDone

	// Shutdown the session
	sess.Stop()

	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected 0 work items after shutdown during unknown commit, got %d", count)
	}
}
