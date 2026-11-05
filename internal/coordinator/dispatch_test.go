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

func setupAdmittedRun(store *fakeStore) {
	store.admission = Admission{
		SessionID:        testSessionID,
		ActiveRunID:      testRunID1,
		OwnerIncarnation: 1,
		AdmissionToken:   1,
		Revision:         1,
		QueueRevision:    1,
	}

	st := kernel.NewState()
	st.RunID = testRunID1
	st.Admitted = true
	st.AdmissionToken = 1
	st.OwnerIncarnation = 1
	st.LastSequence = 2
	st.StepStarted = true
	st.CurrentStepIndex = 1
	st.CurrentModelInvocationID = "m1"

	inv := &kernel.ModelInvocation{
		InvocationID:      "m1",
		Planned:           true,
		Completed:         false,
		Failed:            false,
		Closed:            false,
		SelectedAttemptID: "",
		Attempts: map[string]*kernel.ModelAttempt{
			"a1": {
				AttemptID: "a1",
				Status:    kernel.ModelAttemptStatusPlanned,
			},
		},
	}
	st.ModelInvocations["m1"] = inv

	store.runs[testRunID1] = st
	store.runRevs[testRunID1] = 1
}

func makeModelDispatchRequest(runID, invID, attID, cmdID string) Request {
	cmd := new(kernel.PrepareDispatchCommand)
	cmd.CommandID = cmdID
	cmd.RunID = runID
	cmd.Origin = testOriginSystem
	cmd.Token = 1
	cmd.InvocationID = invID
	cmd.AttemptID = attID

	md := new(kernel.ModelDispatchStartedEvent)
	md.InvocationID = invID
	md.AttemptID = attID
	md.Token = 1
	md.ProviderSettingsRef = "settings-ref"
	md.AccountID = "acc-1"
	md.CatalogVersion = "cat-v1"

	return Request{
		Command: cmd,
		Facts: kernel.DecisionFacts{
			ModelDispatch:            md,
			DispatchAuthorizationRef: "auth-1",
		},
		Kind:      testKindPrepare,
		Principal: testOriginSystem,
		Origin:    testOriginSystem,
		Target:    runID,
	}
}

func verifyDispatchedWorkItem(t *testing.T, exec *fakeExecutor, wantFence uint64) {
	t.Helper()
	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected exactly 1 work item after commit, got %d", count)
	}

	exec.mu.Lock()
	work := exec.recorded[0]
	exec.mu.Unlock()

	if work.Lease.ExecutorFence != wantFence {
		t.Fatalf("expected ExecutorFence %d, got %d", wantFence, work.Lease.ExecutorFence)
	}
}

func setupHandoffBlockedAppend(t *testing.T, store *fakeStore) (chan struct{}, chan struct{}) {
	t.Helper()
	appendBlocked := make(chan struct{})
	releaseAppend := make(chan struct{})
	var once sync.Once

	store.onAppend = func(_ *Transition) {
		once.Do(func() {
			close(appendBlocked)
			select {
			case <-releaseAppend:
			case <-t.Context().Done():
			}
		})
	}
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-1", Kind: testKindPrepare, Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 1,
	}, nil)

	return appendBlocked, releaseAppend
}

func TestHandoffHappensOnlyAfterCommit(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)

	appendBlocked, releaseAppend := setupHandoffBlockedAppend(t, store)

	exec := newFakeExecutor()
	workReceived := make(chan struct{})
	exec.onExec = func(_ *Work, _ func(*Observation)) {
		close(workReceived)
	}

	ctx, sess := startTestSession(t, store, exec, "handoff-test", 1)
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-1")

	task := supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(ctx, &req)
		return sErr
	})

	waitSignal(t, ctx, appendBlocked, "appendBlocked")

	// Verify executor has received zero work while commit is not confirmed
	if count := exec.workCount(); count != 0 {
		t.Fatalf("expected 0 work items before commit, got %d", count)
	}

	// Release append
	close(releaseAppend)

	if err := task.Wait(); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	waitSignal(t, ctx, workReceived, "workReceived")

	verifyDispatchedWorkItem(t, exec, 3)

	sess.mu.Lock()
	dispAdmRev := sess.admission.Revision
	sess.mu.Unlock()
	if dispAdmRev != 1 {
		t.Fatalf("expected admission revision 1, got %d", dispAdmRev)
	}
}

func releaseSlotAndResumeParked(t *testing.T, ctx context.Context, sess *Session, store *fakeStore, heldRef string, parkDone chan error) {
	t.Helper()
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-disp-parked", Kind: testKindPrepare, Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 1,
	}, nil)

	if err := sess.slots.release(heldRef); err != nil {
		t.Fatalf("release held slot failed: %v", err)
	}
	sess.retryParked(ctx)

	sErr := waitValue(t, ctx, parkDone, "parkDone")
	if sErr != nil {
		t.Fatalf("parked submit failed: %v", sErr)
	}

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 1 {
		t.Fatalf("expected exactly 1 transition after slot release, got %d", txCount)
	}
}

func TestNoSlotParksDispatchWithoutEvents(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "park-test", 1)
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
		_, sErr := sess.Submit(ctx, &req)
		parkDone <- sErr
		return sErr
	})

	waitSignal(t, ctx, parked, "parked")

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected 0 transitions while parked without slot, got %d", txCount)
	}

	releaseSlotAndResumeParked(t, ctx, sess, store, heldRef, parkDone)
}

func submitAcceptR2WhileParked(t *testing.T, ctx context.Context, sess *Session, store *fakeStore) {
	t.Helper()
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: testCmdAcceptR2, Kind: testKindAcceptRun, Target: testRunID2},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)

	cmdAccept := new(kernel.AcceptRunCommand)
	cmdAccept.Budgets = kernel.DefaultRunBudgets()
	cmdAccept.CommandID = testCmdAcceptR2
	cmdAccept.RunID = testRunID2
	cmdAccept.Origin = testOriginUser
	cmdAccept.SessionID = testSessionID

	reqAccept := Request{
		Command:   cmdAccept,
		Facts:     kernel.DecisionFacts{},
		Kind:      testKindAcceptRun,
		Principal: testOriginUser,
		Origin:    testOriginUser,
		Target:    testRunID2,
	}

	reply, err := sess.Submit(ctx, &reqAccept)
	if err != nil {
		t.Fatalf("AcceptRun failed while dispatch was parked: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}
}

func TestActorKeepsServingWhileDispatchWaitsForCapacity(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "serving-test", 1)
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
		_, dErr := sess.Submit(ctx, &reqDispatch)
		return dErr
	})

	waitSignal(t, ctx, parked, "parked")

	// Now submit AcceptRun for r2 - should succeed and commit!
	submitAcceptR2WhileParked(t, ctx, sess, store)

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
	setupAdmittedRun(store)

	// Queue a definite rejection from Append
	fenceErr := errcode.New(errcode.FailedPrecondition, "stale fence")
	store.queueAppend(nil, fenceErr)

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

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-fail")

	_, err = sess.Submit(ctx, &req)
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

func TestIsDispatchHelperEdgeCases(t *testing.T) {
	t.Parallel()

	st := kernel.NewState()
	if isDispatch(nil, new(kernel.PrepareDispatchCommand)) {
		t.Error("expected false for nil state")
	}
	if isDispatch(st, new(kernel.AcceptRunCommand)) {
		t.Error("expected false for non-prepare command")
	}
	// Tool attempt with Planned status (not Ready)
	st.ToolInvocations["t1"] = &kernel.ToolInvocation{
		ToolCallID: "t1",
		Attempts: map[string]*kernel.ToolAttempt{
			testAtt1: {
				AttemptID: testAtt1,
				Status:    kernel.ToolAttemptStatusPlanned,
			},
		},
	}
	prepCmd := new(kernel.PrepareDispatchCommand)
	prepCmd.InvocationID = "t1"
	prepCmd.AttemptID = testAtt1
	if isDispatch(st, prepCmd) {
		t.Error("expected false for tool attempt in Planned status")
	}
	// Tool attempt with Ready status
	st.ToolInvocations["t1"].Attempts[testAtt1].Status = kernel.ToolAttemptStatusReady
	if !isDispatch(st, prepCmd) {
		t.Error("expected true for tool attempt in Ready status")
	}
}

func TestDispatchEdgeCases(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "dispatch-edge-test", 2)
	defer sess.Stop()

	// 2. Dispatch with empty CommandID and Principal (auto-generated ID and default origin)
	req := makeModelDispatchRequest("r1", "m1", "a1", "")
	req.Principal = ""
	_, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch with empty id/principal failed: %v", err)
	}

	// 3. Dispatch when halted returns ErrCommitUnresolved
	sess.mu.Lock()
	sess.runs["r1"].ModelInvocations["m1"].Attempts["a2"] = &kernel.ModelAttempt{
		AttemptID: "a2",
		Status:    kernel.ModelAttemptStatusPlanned,
	}
	sess.halted = true
	sess.mu.Unlock()

	req2 := makeModelDispatchRequest("r1", "m1", "a2", "cmd-halted")
	_, err = sess.Submit(ctx, &req2)
	if !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("expected ErrCommitUnresolved when halted, got %v", err)
	}

	sess.mu.Lock()
	sess.halted = false
	sess.mu.Unlock()

	testDispatchRejectionAndTargetFallback(t, ctx, sess)
}

func testDispatchRejectionAndTargetFallback(t *testing.T, ctx context.Context, sess *Session) {
	t.Helper()

	// 4. Dispatch with Decide rejection releases slot
	sess.mu.Lock()
	sess.runs["r1"].ModelInvocations["m1"].Attempts["a3"] = &kernel.ModelAttempt{
		AttemptID: "a3",
		Status:    kernel.ModelAttemptStatusPlanned,
	}
	sess.mu.Unlock()
	rejReq := makeModelDispatchRequest("r1", "m1", "a3", "cmd-rej")
	rejReq.Facts.ModelDispatch = nil // Causes Decide error
	_, err := sess.Submit(ctx, &rejReq)
	if err == nil {
		t.Fatal("expected Decide error on nil ModelDispatch")
	}

	// 5. Dispatch with empty RunID on command takes req.Target
	sess.mu.Lock()
	sess.runs["r1"].ModelInvocations["m1"].Attempts["a1"].Status = kernel.ModelAttemptStatusCompleted
	sess.runs["r1"].CurrentModelInvocationID = "m2"
	sess.runs["r1"].ModelInvocations["m2"] = &kernel.ModelInvocation{
		InvocationID: "m2",
		Planned:      true,
		Attempts: map[string]*kernel.ModelAttempt{
			"a4": {
				AttemptID: "a4",
				Status:    kernel.ModelAttemptStatusPlanned,
			},
		},
	}
	sess.mu.Unlock()
	reqTarget := makeModelDispatchRequest("", "m2", "a4", "cmd-target")
	reqTarget.Target = "r1"
	_, err = sess.Submit(ctx, &reqTarget)
	if err != nil {
		t.Fatalf("dispatch with target fallback failed: %v", err)
	}
}
