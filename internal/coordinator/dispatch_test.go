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
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func setupAdmittedRun(store *fakeStore, runID, invID, attID string) {
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      runID,
		OwnerIncarnation: 1,
		AdmissionToken:   1,
		Revision:         1,
		QueueRevision:    1,
	}

	st := kernel.NewState()
	st.RunID = runID
	st.Admitted = true
	st.AdmissionToken = 1
	st.OwnerIncarnation = 1
	st.LastSequence = 2
	st.StepStarted = true
	st.CurrentStepIndex = 1
	st.CurrentModelInvocationID = invID

	inv := &kernel.ModelInvocation{
		InvocationID:      invID,
		Planned:           true,
		Completed:         false,
		Failed:            false,
		Closed:            false,
		SelectedAttemptID: "",
		Attempts: map[string]*kernel.ModelAttempt{
			attID: {
				AttemptID: attID,
				Status:    kernel.ModelAttemptStatusPlanned,
			},
		},
	}
	st.ModelInvocations[invID] = inv

	store.runs[runID] = st
	store.runRevs[runID] = 1
}

func makeModelDispatchRequest(runID, invID, attID, cmdID string) Request {
	return Request{
		Command: &kernel.PrepareDispatchCommand{
			CommandID:    cmdID,
			RunID:        runID,
			Origin:       "system",
			Token:        1,
			InvocationID: invID,
			AttemptID:    attID,
		},
		Facts: kernel.DecisionFacts{
			ModelDispatch: &kernel.ModelDispatchStartedEvent{
				InvocationID:        invID,
				AttemptID:           attID,
				Token:               1,
				ProviderSettingsRef: "settings-ref",
				AccountID:           "acc-1",
				CatalogVersion:      "cat-v1",
			},
			DispatchAuthorizationRef: "auth-1",
		},
		Kind:      "PrepareDispatch",
		Principal: "system",
		Target:    runID,
	}
}

func TestHandoffHappensOnlyAfterCommit(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("handoff-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	appendBlocked := make(chan struct{})
	releaseAppend := make(chan struct{})
	var once sync.Once

	store.onAppend = func(_ *Transition) {
		once.Do(func() {
			close(appendBlocked)
			<-releaseAppend
		})
	}
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-1", Kind: "PrepareDispatch", Target: "r1"},
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

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-1")

	task := supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, req)
		return sErr
	})

	<-appendBlocked

	// Verify executor has received zero work while commit is not confirmed
	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected 0 work items before commit, got %d", count)
	}

	// Release append
	close(releaseAppend)

	if err := task.Wait(); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	<-workReceived

	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected exactly 1 work item after commit, got %d", count)
	}

	exec.mu.Lock()
	work := exec.recorded[0]
	exec.mu.Unlock()

	// Verify ExecutorFence matches dispatch event sequence (sequence 3)
	if work.Lease.ExecutorFence != 3 {
		t.Fatalf("expected ExecutorFence 3, got %d", work.Lease.ExecutorFence)
	}
}

func TestNoSlotParksDispatchWithoutEvents(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("park-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1, // capacity 1
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

	// Manually hold the 1 available slot
	heldRef, ok := sess.slots.reserve()
	if !ok {
		t.Fatal("expected reserve to succeed")
	}

	parked := make(chan struct{})
	sess.onPark = func() {
		close(parked)
	}

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-parked")

	// Submit dispatch request while no slot is available
	parkDone := make(chan error, 1)
	_ = supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, req)
		parkDone <- sErr
		return sErr
	})

	<-parked

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected 0 transitions while parked without slot, got %d", txCount)
	}

	// Prepare append response for when slot is freed
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-parked", Kind: "PrepareDispatch", Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 1,
	}, nil)

	// Release the held slot
	_ = sess.slots.release(heldRef)
	sess.retryParked(ctx)

	sErr := <-parkDone
	if sErr != nil {
		t.Fatalf("parked submit failed: %v", sErr)
	}

	store.mu.Lock()
	txCount = len(store.transitions)
	store.mu.Unlock()

	if txCount != 1 {
		t.Fatalf("expected exactly 1 transition after slot release, got %d", txCount)
	}
}

func TestActorKeepsServingWhileDispatchWaitsForCapacity(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("serving-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1, // capacity 1 held
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

	// Hold the capacity
	_, ok := sess.slots.reserve()
	if !ok {
		t.Fatal("reserve failed")
	}

	parked := make(chan struct{})
	sess.onPark = func() {
		close(parked)
	}

	// Dispatch r1 is parked
	reqDispatch := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-parked")
	_ = supervise.Go(ctx, "test.dispatch", func(_ context.Context) error {
		_, _ = sess.Submit(ctx, reqDispatch)
		return nil
	})

	<-parked

	// Now submit AcceptRun for r2 - should succeed and commit!
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-r2", Kind: "AcceptRun", Target: "r2"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)

	reqAccept := Request{
		Command: &kernel.AcceptRunCommand{
			CommandID: "cmd-accept-r2",
			RunID:     "r2",
			Origin:    "user",
			SessionID: "s1",
		},
		Facts:     kernel.DecisionFacts{},
		Kind:      "AcceptRun",
		Principal: "user",
		Target:    "r2",
	}

	reply, err := sess.Submit(ctx, reqAccept)
	if err != nil {
		t.Fatalf("AcceptRun failed while dispatch was parked: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	foundAccept := false
	for _, tx := range transitions {
		if tx.RunID == "r2" {
			foundAccept = true
			break
		}
	}
	if !foundAccept {
		t.Fatal("expected AcceptRun for r2 to commit while dispatch is parked")
	}
}

func TestFailedCommitReleasesSlot(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("failed-commit-test"))

	store := newFakeStore()
	setupAdmittedRun(store, "r1", "m1", "a1")

	// Queue a definite rejection from Append
	fenceErr := errcode.New(errcode.FailedPrecondition, "stale fence")
	store.queueAppend(AppendResult{}, fenceErr)

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1, // capacity 1
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

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-fail")

	_, err = sess.Submit(ctx, req)
	if !errors.Is(err, fenceErr) {
		t.Fatalf("expected fenceErr, got %v", err)
	}

	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected 0 work items on failed commit, got %d", count)
	}

	// Slot must be returned: reserve() should succeed!
	ref, canReserve := sess.slots.reserve()
	if !canReserve || ref == "" {
		t.Fatal("expected slot to be released after failed commit, reserve failed")
	}
}
