package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func makeModelResponseObservation(
	sessionID, runID, invID, attID, cmdID string,
	token, incarnation, fence uint64,
) Observation {
	return Observation{
		Lease: Lease{
			SessionID:        sessionID,
			RunID:            runID,
			InvocationID:     invID,
			AttemptID:        attID,
			AdmissionToken:   token,
			OwnerIncarnation: incarnation,
			ExecutorFence:    fence,
		},
		Command: &kernel.ObserveModelResponseCommand{
			CommandID:    cmdID,
			RunID:        runID,
			Origin:       testOriginWorker,
			Token:        token,
			InvocationID: invID,
			AttemptID:    attID,
			ResponseRef:  "resp-" + attID,
			UsageStatus:  "known",
			FinishReason: kernel.FinishReasonStop,
		},
		Facts: kernel.DecisionFacts{
			OwnerIncarnation: incarnation,
			ExecutorFence:    fence,
			Usage: func() *kernel.BudgetObservedEvent {
				b := new(kernel.BudgetObservedEvent)
				b.AttemptID = attID
				b.TokensUsed = 10
				b.Basis = "exact"
				return b
			}(),
		},
	}
}

func setupDispatchedRun(store *fakeStore, incarnation uint64) {
	store.admission = Admission{
		SessionID:        testSessionID,
		ActiveRunID:      testRunID1,
		OwnerIncarnation: incarnation,
		AdmissionToken:   1,
		Revision:         1,
		QueueRevision:    1,
	}

	st := kernel.NewState()
	st.RunID = testRunID1
	st.Admitted = true
	st.AdmissionToken = 1
	st.OwnerIncarnation = incarnation
	st.LastSequence = 4
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

func TestStaleWorkerObservationIsFenced(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("stale-observation-test"))

	store := newFakeStore()
	// Sequence 4 -> next dispatch will have sequence 5 (fence 5)
	setupDispatchedRun(store, 1)

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
	defer sess.Stop()

	// Dispatch attempt a1 -> sequence is 5, fence is 5
	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-fence-5")
	req.Facts.ModelDispatch.ExecutorFence = 5
	_, err = sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsAfterDispatch := store.appendCount()

	// Stale observation with fence 4 (earlier fence)
	obsFence4 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-f4", 1, 1, 4)
	_, err = sess.SubmitObservation(ctx, &obsFence4)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for stale fence 4, got %v", err)
	}

	if count := store.appendCount(); count != appendsAfterDispatch {
		t.Fatalf("expected no append on stale observation, append count went from %d to %d",
			appendsAfterDispatch, count)
	}

	// Valid observation with fence 5
	obsFence5 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-f5", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, &obsFence5)
	if err != nil {
		t.Fatalf("expected valid observation with fence 5 to commit, got %v", err)
	}

	if count := store.appendCount(); count != appendsAfterDispatch+1 {
		t.Fatalf("expected exactly one append for valid observation, got %d", count)
	}
}

func TestObservationFromEarlierIncarnationIsFenced(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("earlier-incarnation-test"))

	store := newFakeStore()
	setupDispatchedRun(store, 2)

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 2,
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
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-inc-2")
	_, err = sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsAfterDispatch := store.appendCount()

	// Observation with earlier incarnation 1
	obsInc1 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-inc-1", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, &obsInc1)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for earlier incarnation 1, got %v", err)
	}

	if count := store.appendCount(); count != appendsAfterDispatch {
		t.Fatalf("expected no append on earlier incarnation observation, count is %d", count)
	}
}

func TestTerminalObservationReleasesSlot(t *testing.T) {
	t.Parallel()

	// The executor holds the work until the test releases it, so the slot is
	// genuinely held while the attempt is running. An executor that returned
	// without reporting would have abandoned the attempt instead.
	workReceived := make(chan struct{}, 1)
	release := make(chan struct{})
	exec := newFakeExecutor()
	exec.onExec = func(w *Work, report func(*Observation)) {
		workReceived <- struct{}{}
		<-release
		obs := makeModelResponseObservation(
			w.Lease.SessionID, w.Lease.RunID, w.Lease.InvocationID, w.Lease.AttemptID,
			"cmd-obs-term", w.Lease.AdmissionToken, w.Lease.OwnerIncarnation, w.Lease.ExecutorFence,
		)
		report(&obs)
	}

	committed := make(chan struct{}, 2)
	store := newFakeStore()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSessionOnCommit(t, store, exec, "terminal-release-slot-test", func(string, *kernel.State) {
		committed <- struct{}{}
	})

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-term")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	waitSignal(t, ctx, workReceived, "work received")

	// Slot should be held at capacity 1 while the executor is running.
	if _, canReserve := reserveSlot(sess.slots); canReserve {
		t.Fatal("expected slot to be held after dispatch")
	}

	// The executor finishes with the terminal observation; its commit completes
	// the slot and finishes the run. Two deliveries arrive: the dispatch's own
	// notification and the observation's.
	release <- struct{}{}
	waitSignal(t, ctx, committed, "dispatch commit notification")
	waitSignal(t, ctx, committed, "terminal observation commit")

	// Slot should now be released
	ref, canReserveAfter := reserveSlot(sess.slots)
	if !canReserveAfter {
		t.Fatal("expected slot to be released after terminal observation commit")
	}
	if err := sess.slots.release(ref); err != nil {
		t.Fatalf("release: %v", err)
	}

	if activeRun := sess.admission.ActiveRunID; activeRun != "" {
		t.Fatalf("expected active run cleared after finish, got %s", activeRun)
	}
}

// startTestSessionOnCommit starts a single-capacity session whose hook signals
// every applied commit.
func startTestSessionOnCommit(t *testing.T, store *fakeStore, exec *fakeExecutor, seed string, onCommitted func(string, *kernel.State)) (context.Context, *Session) {
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
		OnCommitted:      onCommitted,
	}
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	t.Cleanup(sess.Stop)
	return ctx, sess
}

func TestDuplicateObservationAppendsOnce(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "duplicate-observation-test", 1)
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-dup")
	_, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsBeforeObs := store.appendCount()

	obs := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-dup", 1, 1, 5)

	// First submission
	reply1, err1 := sess.SubmitObservation(ctx, &obs)
	if err1 != nil {
		t.Fatalf("first observation failed: %v", err1)
	}
	if reply1.Duplicate {
		t.Fatal("expected first observation reply to not be duplicate")
	}

	appendsAfterFirst := store.appendCount()
	if appendsAfterFirst != appendsBeforeObs+1 {
		t.Fatalf("expected 1 append for first observation, got %d", appendsAfterFirst-appendsBeforeObs)
	}

	// Second submission of identical observation
	reply2, err2 := sess.SubmitObservation(ctx, &obs)
	if err2 != nil {
		t.Fatalf("second observation failed: %v", err2)
	}
	if !reply2.Duplicate {
		t.Fatal("expected second observation reply to have Duplicate == true")
	}

	appendsAfterSecond := store.appendCount()
	if appendsAfterSecond != appendsAfterFirst {
		t.Fatalf("expected store append count to not increase on duplicate, went from %d to %d",
			appendsAfterFirst, appendsAfterSecond)
	}
}

func verifyAttemptStillRequesting(t *testing.T, st *kernel.State) {
	t.Helper()
	if st == nil {
		t.Fatal("expected run r1 in session cache")
	}
	inv := st.ModelInvocations["m1"]
	if inv == nil {
		t.Fatal("expected invocation m1 in run state")
	}
	att := inv.Attempts["a1"]
	if att == nil {
		t.Fatal("expected attempt a1 in invocation state")
	}
	if att.Status != kernel.ModelAttemptStatusRequesting {
		t.Fatalf("expected attempt status Requesting, got %v", att.Status)
	}
}

func TestWorkerPanicKeepsAttemptDispatchingAndSlotHeld(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)

	panicked := make(chan struct{}, 1)
	exec := newFakeExecutor()
	exec.onExec = func(_ *Work, _ func(*Observation)) {
		panicked <- struct{}{}
		panic("simulated worker explosion")
	}

	ctx, sess := startTestSession(t, store, exec, "worker-panic-test", 1)
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-panic")
	_, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	// Wait for worker to panic
	waitSignal(t, ctx, panicked, "panicked")

	// Wait for worker task to be done
	if len(sess.workerTasks) > 0 {
		waitSignal(t, ctx, sess.workerTasks[0].Done(), "worker task done")
	}

	// Verify run cache still shows attempt Requesting
	st := sess.runs["r1"]

	verifyAttemptStillRequesting(t, st)

	// Verify slot is still held
	_, canReserve := reserveSlot(sess.slots)
	if canReserve {
		t.Fatal("expected slot to still be held after worker panic")
	}

	// Verify zero further appends after dispatch
	if count := store.appendCount(); count != 1 {
		t.Fatalf("expected exactly 1 append (the dispatch), got %d", count)
	}
}

func testInvalidLeaseRejections(t *testing.T, ctx context.Context, sess *Session) {
	t.Helper()
	// 1. Empty RunID
	obsEmptyRun := makeModelResponseObservation("s1", "", "m1", "a1", "cmd-empty-run", 1, 1, 5)
	_, err := sess.SubmitObservation(ctx, &obsEmptyRun)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for empty RunID, got %v", err)
	}

	// 2. Admission token mismatch
	obsWrongToken := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-wrong-tok", 999, 1, 5)
	_, err = sess.SubmitObservation(ctx, &obsWrongToken)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for token mismatch, got %v", err)
	}

	// 3. Active run ID mismatch
	obsWrongRun := makeModelResponseObservation("s1", "other-run", "m1", "a1", "cmd-wrong-run", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, &obsWrongRun)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for active run mismatch, got %v", err)
	}

	// 4. Zero executor fence
	obsZeroFence := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-zero-fence", 1, 1, 0)
	_, err = sess.SubmitObservation(ctx, &obsZeroFence)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for zero fence, got %v", err)
	}

	// 5. Unknown attempt
	obsUnknownAtt := makeModelResponseObservation("s1", "r1", "m1", "unknown-att", "cmd-unkn-att", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, &obsUnknownAtt)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for unknown attempt, got %v", err)
	}
}

func TestObservationRejectionsAndEdgeCases(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "obs-edge-cases-test", 1)

	testInvalidLeaseRejections(t, ctx, sess)

	// 6. Canceled context
	obsEmptyRun := makeModelResponseObservation("s1", "", "m1", "a1", "cmd-empty-run", 1, 1, 5)
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := sess.SubmitObservation(cancCtx, &obsEmptyRun)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// 7. Stopped session
	sess.Stop()
	_, err = sess.SubmitObservation(ctx, &obsEmptyRun)
	if !errors.Is(err, ErrSessionStopped) {
		t.Errorf("expected ErrSessionStopped on stopped session, got %v", err)
	}
}

func TestWorkerReportsViaCallback(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("worker-callback-test"))

	store := newFakeStore()
	setupDispatchedRun(store, 1)

	done := make(chan struct{}, 1)
	exec := newFakeExecutor()
	exec.onExec = func(w *Work, report func(*Observation)) {
		obs := makeModelResponseObservation(
			w.Lease.SessionID, w.Lease.RunID, w.Lease.InvocationID, w.Lease.AttemptID,
			"cmd-obs-cb", w.Lease.AdmissionToken, w.Lease.OwnerIncarnation, w.Lease.ExecutorFence,
		)
		report(&obs)
		done <- struct{}{}
	}

	committed := make(chan struct{}, 2)
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   1,
		Store:            store,
		Executor:         exec,
		Clock:            fakeClk,
		IDs:              idGen,
		OnCommitted: func(string, *kernel.State) {
			committed <- struct{}{}
		},
	}

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-cb")
	_, err = sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	waitSignal(t, ctx, committed, "committed 1")

	waitSignal(t, ctx, done, "done")
	waitSignal(t, ctx, committed, "committed 2")

	// Verify terminal observation committed through report callback
	assertWorkerReportCompleted(t, sess)
}

func assertWorkerReportCompleted(t *testing.T, sess *Session) {
	t.Helper()
	ref, canReserve := reserveSlot(sess.slots)
	if !canReserve {
		t.Fatal("expected slot to be released after worker report")
	}
	if err := sess.slots.release(ref); err != nil {
		t.Fatalf("release: %v", err)
	}

	activeRun := sess.admission.ActiveRunID

	if activeRun != "" {
		t.Fatalf("expected active run cleared after finish, got %s", activeRun)
	}
}

func setupToolFencingState(store *fakeStore) {
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "r1",
		OwnerIncarnation: 1,
		AdmissionToken:   1,
		Revision:         1,
		QueueRevision:    1,
	}

	st := kernel.NewState()
	st.RunID = "r1"
	st.Admitted = true
	st.AdmissionToken = 1
	st.OwnerIncarnation = 1
	st.LastSequence = 6
	st.StepStarted = true
	st.CurrentStepIndex = 1

	respEvt := new(kernel.ModelResponseRecordedEvent)
	respEvt.ResponseRef = "model-resp-ref"

	toolDispEvt := new(kernel.ToolDispatchStartedEvent)
	toolDispEvt.ExecutorFence = 7

	st.CurrentModelInvocationID = "m1"
	st.ModelInvocations["m1"] = &kernel.ModelInvocation{
		InvocationID:      "m1",
		Completed:         true,
		SelectedAttemptID: "a1",
		Attempts: map[string]*kernel.ModelAttempt{
			"a1": {
				AttemptID: "a1",
				Status:    kernel.ModelAttemptStatusCompleted,
				Response:  *respEvt,
			},
		},
	}
	st.ToolOrder = []string{"t1"}
	st.ToolInvocations["t1"] = &kernel.ToolInvocation{
		ToolCallID: "t1",
		Attempts: map[string]*kernel.ToolAttempt{
			testAttTool1: {
				AttemptID: testAttTool1,
				Status:    kernel.ToolAttemptStatusDispatching,
				Dispatch:  *toolDispEvt,
			},
		},
	}
	store.runs["r1"] = st
	store.runRevs["r1"] = 1
}

func TestToolObservationFencing(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupToolFencingState(store)
	exec := newFakeExecutor()

	ctx, sess := startTestSession(t, store, exec, "tool-observation-test", 1)
	defer sess.Stop()

	// Tool observation with stale fence 6
	staleToolObs := Observation{
		Lease: Lease{
			SessionID:        "s1",
			RunID:            "r1",
			InvocationID:     "t1",
			AttemptID:        testAttTool1,
			AdmissionToken:   1,
			OwnerIncarnation: 1,
			ExecutorFence:    6,
		},
		Command: &kernel.ObserveToolResultCommand{
			CommandID:      "cmd-tool-res-stale",
			RunID:          "r1",
			Origin:         testOriginWorker,
			Token:          1,
			ToolCallID:     "t1",
			AttemptID:      testAttTool1,
			Outcome:        "succeeded",
			ResultRef:      "res-ref",
			Code:           "200",
			EffectEvidence: "ev",
			SourcePosition: 0,
		},
	}
	_, err := sess.SubmitObservation(ctx, &staleToolObs)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for tool fence mismatch, got %v", err)
	}

	submitMatchingToolObservation(t, ctx, sess, &staleToolObs)
}

func submitMatchingToolObservation(t *testing.T, ctx context.Context, sess *Session, baseObs *Observation) {
	t.Helper()
	matchingToolObs := *baseObs
	matchingToolObs.Lease.ExecutorFence = 7
	matchingToolObs.Command = &kernel.ObserveToolResultCommand{
		CommandID:      "cmd-tool-res-match",
		RunID:          "r1",
		Origin:         testOriginWorker,
		Token:          1,
		ToolCallID:     "t1",
		AttemptID:      testAttTool1,
		Outcome:        "succeeded",
		ResultRef:      "res-ref",
		Code:           "200",
		EffectEvidence: "ev",
		SourcePosition: 0,
	}
	matchingToolObs.Facts.ExecutorFence = 7
	matchingToolObs.Facts.Usage = func() *kernel.BudgetObservedEvent {
		b := new(kernel.BudgetObservedEvent)
		b.AttemptID = testAttTool1
		b.TokensUsed = 5
		b.Basis = "exact"
		return b
	}()
	matchingToolObs.Facts.ModelPlan = func() *kernel.ModelRequestPlannedEvent {
		m := new(kernel.ModelRequestPlannedEvent)
		m.InvocationID = "m2"
		m.FirstAttemptID = "a2"
		m.Purpose = "turn"
		m.ModelRef = "model-ref"
		m.ContextRef = "ctx-ref"
		m.EstimatedBudgetTokens = 10
		return m
	}()

	ref, ok := reserveSlot(sess.slots)
	if !ok {
		t.Fatal("reserve failed")
	}
	if handoffErr := sess.slots.handoff(ref, &Work{}); handoffErr != nil {
		t.Fatalf("handoff: %v", handoffErr)
	}
	sess.attemptSlots[attemptKey{runID: "r1", invocationID: "t1", attemptID: testAttTool1}] = ref

	if _, err := sess.SubmitObservation(ctx, &matchingToolObs); err != nil {
		t.Fatalf("matching tool observation failed: %v", err)
	}

	if _, canReserve := reserveSlot(sess.slots); !canReserve {
		t.Fatal("expected slot to be released after tool result commit")
	}
}
