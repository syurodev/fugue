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
			Origin:       "worker",
			Token:        token,
			InvocationID: invID,
			AttemptID:    attID,
			ResponseRef:  "resp-" + attID,
			UsageStatus:  "known",
		},
		Facts: kernel.DecisionFacts{
			OwnerIncarnation: incarnation,
			ExecutorFence:    fence,
			Usage: &kernel.BudgetObservedEvent{
				AttemptID:  attID,
				TokensUsed: 10,
				Basis:      "exact",
			},
		},
	}
}

func setupDispatchedRun(
	store *fakeStore,
	runID, invID, attID string,
	token, incarnation, lastSeq uint64,
) {
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      runID,
		OwnerIncarnation: incarnation,
		AdmissionToken:   token,
		Revision:         1,
		QueueRevision:    1,
	}

	st := kernel.NewState()
	st.RunID = runID
	st.Admitted = true
	st.AdmissionToken = token
	st.OwnerIncarnation = incarnation
	st.LastSequence = lastSeq
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

func TestStaleWorkerObservationIsFenced(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("stale-observation-test"))

	store := newFakeStore()
	// Sequence 4 -> next dispatch will have sequence 5 (fence 5)
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

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

	// Dispatch attempt a1 -> sequence is 5, fence is 5
	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-fence-5")
	req.Facts.ModelDispatch.ExecutorFence = 5
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsAfterDispatch := store.appendCount()

	// Stale observation with fence 4 (earlier fence)
	obsFence4 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-f4", 1, 1, 4)
	_, err = sess.SubmitObservation(ctx, obsFence4)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for stale fence 4, got %v", err)
	}

	if count := store.appendCount(); count != appendsAfterDispatch {
		t.Fatalf("expected no append on stale observation, append count went from %d to %d",
			appendsAfterDispatch, count)
	}

	// Valid observation with fence 5
	obsFence5 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-f5", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsFence5)
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
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 2, 4)

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

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-inc-2")
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsAfterDispatch := store.appendCount()

	// Observation with earlier incarnation 1
	obsInc1 := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-inc-1", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsInc1)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for earlier incarnation 1, got %v", err)
	}

	if count := store.appendCount(); count != appendsAfterDispatch {
		t.Fatalf("expected no append on earlier incarnation observation, count is %d", count)
	}
}

func TestTerminalObservationReleasesSlot(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("terminal-release-slot-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

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

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-term")
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	// Slot should be held at capacity 1
	_, canReserve := sess.slots.reserve()
	if canReserve {
		t.Fatal("expected slot to be held after dispatch")
	}

	// Submit terminal observation
	obs := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-term", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obs)
	if err != nil {
		t.Fatalf("terminal observation failed: %v", err)
	}

	// Slot should now be released
	ref, canReserveAfter := sess.slots.reserve()
	if !canReserveAfter {
		t.Fatal("expected slot to be released after terminal observation commit")
	}
	_ = sess.slots.release(ref)
}

func TestDuplicateObservationAppendsOnce(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("duplicate-observation-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

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

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-dup")
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	appendsBeforeObs := store.appendCount()

	obs := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-obs-dup", 1, 1, 5)

	// First submission
	reply1, err1 := sess.SubmitObservation(ctx, obs)
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
	reply2, err2 := sess.SubmitObservation(ctx, obs)
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

func TestWorkerPanicKeepsAttemptDispatchingAndSlotHeld(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("worker-panic-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

	panicked := make(chan struct{}, 1)
	exec := newFakeExecutor()
	exec.onExec = func(_ Work, _ func(Observation)) {
		panicked <- struct{}{}
		panic("simulated worker explosion")
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

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-panic")
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	// Wait for worker to panic
	<-panicked

	// Wait for worker task to be done
	if len(sess.workerTasks) > 0 {
		<-sess.workerTasks[0].Done()
	}

	// Verify run cache still shows attempt Requesting
	sess.mu.Lock()
	st := sess.runs["r1"]
	sess.mu.Unlock()

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

	// Verify slot is still held
	_, canReserve := sess.slots.reserve()
	if canReserve {
		t.Fatal("expected slot to still be held after worker panic")
	}

	// Verify zero further appends after dispatch
	if count := store.appendCount(); count != 1 {
		t.Fatalf("expected exactly 1 append (the dispatch), got %d", count)
	}
}

func TestObservationRejectionsAndEdgeCases(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("obs-edge-cases-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

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

	// 1. Empty RunID
	obsEmptyRun := makeModelResponseObservation("s1", "", "m1", "a1", "cmd-empty-run", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsEmptyRun)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for empty RunID, got %v", err)
	}

	// 2. Admission token mismatch
	obsWrongToken := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-wrong-tok", 999, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsWrongToken)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for token mismatch, got %v", err)
	}

	// 3. Active run ID mismatch
	obsWrongRun := makeModelResponseObservation("s1", "other-run", "m1", "a1", "cmd-wrong-run", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsWrongRun)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for active run mismatch, got %v", err)
	}

	// 4. Zero executor fence
	obsZeroFence := makeModelResponseObservation("s1", "r1", "m1", "a1", "cmd-zero-fence", 1, 1, 0)
	_, err = sess.SubmitObservation(ctx, obsZeroFence)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for zero fence, got %v", err)
	}

	// 5. Unknown attempt
	obsUnknownAtt := makeModelResponseObservation("s1", "r1", "m1", "unknown-att", "cmd-unkn-att", 1, 1, 5)
	_, err = sess.SubmitObservation(ctx, obsUnknownAtt)
	if !errors.Is(err, ErrStaleLease) {
		t.Errorf("expected ErrStaleLease for unknown attempt, got %v", err)
	}

	// 6. Canceled context
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = sess.SubmitObservation(cancCtx, obsEmptyRun)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// 7. Stopped session
	sess.Stop()
	_, err = sess.SubmitObservation(ctx, obsEmptyRun)
	if !errors.Is(err, ErrSessionStopped) {
		t.Errorf("expected ErrSessionStopped on stopped session, got %v", err)
	}
}

func TestWorkerReportsViaCallback(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("worker-callback-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

	done := make(chan struct{}, 1)
	exec := newFakeExecutor()
	exec.onExec = func(w Work, report func(Observation)) {
		obs := makeModelResponseObservation(
			w.Lease.SessionID, w.Lease.RunID, w.Lease.InvocationID, w.Lease.AttemptID,
			"cmd-obs-cb", w.Lease.AdmissionToken, w.Lease.OwnerIncarnation, w.Lease.ExecutorFence,
		)
		report(obs)
		done <- struct{}{}
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

	committed := make(chan struct{}, 2)
	sess.setOnCommitted(func() {
		committed <- struct{}{}
	})

	req := makeModelDispatchRequest("r1", "m1", "a1", "cmd-disp-cb")
	_, err = sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	<-committed

	<-done
	<-committed

	// Verify terminal observation committed through report callback
	ref, canReserve := sess.slots.reserve()
	if !canReserve {
		t.Fatal("expected slot to be released after worker report")
	}
	_ = sess.slots.release(ref)
}

func TestToolObservationFencing(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("tool-observation-test"))

	store := newFakeStore()
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

	st.CurrentModelInvocationID = "m1"
	st.ModelInvocations["m1"] = &kernel.ModelInvocation{
		InvocationID:      "m1",
		Completed:         true,
		SelectedAttemptID: "a1",
		Attempts: map[string]*kernel.ModelAttempt{
			"a1": {
				AttemptID: "a1",
				Status:    kernel.ModelAttemptStatusCompleted,
				Response: kernel.ModelResponseRecordedEvent{
					ResponseRef: "model-resp-ref",
				},
			},
		},
	}
	st.ToolOrder = []string{"t1"}
	st.ToolInvocations["t1"] = &kernel.ToolInvocation{
		ToolCallID: "t1",
		Attempts: map[string]*kernel.ToolAttempt{
			"att-tool-1": {
				AttemptID: "att-tool-1",
				Status:    kernel.ToolAttemptStatusDispatching,
				Dispatch: kernel.ToolDispatchStartedEvent{
					ExecutorFence: 7,
				},
			},
		},
	}
	store.runs["r1"] = st
	store.runRevs["r1"] = 1

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

	// Tool observation with stale fence 6
	staleToolObs := Observation{
		Lease: Lease{
			SessionID:        "s1",
			RunID:            "r1",
			InvocationID:     "t1",
			AttemptID:        "att-tool-1",
			AdmissionToken:   1,
			OwnerIncarnation: 1,
			ExecutorFence:    6,
		},
		Command: &kernel.ObserveToolResultCommand{
			CommandID:      "cmd-tool-res-stale",
			RunID:          "r1",
			Origin:         "worker",
			Token:          1,
			ToolCallID:     "t1",
			AttemptID:      "att-tool-1",
			Outcome:        "succeeded",
			ResultRef:      "res-ref",
			Code:           "200",
			EffectEvidence: "ev",
			SourcePosition: 0,
		},
	}
	_, err = sess.SubmitObservation(ctx, staleToolObs)
	if !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for tool fence mismatch, got %v", err)
	}

	// Tool observation with matching fence 7
	matchingToolObs := staleToolObs
	matchingToolObs.Lease.ExecutorFence = 7
	matchingToolObs.Command = &kernel.ObserveToolResultCommand{
		CommandID:      "cmd-tool-res-match",
		RunID:          "r1",
		Origin:         "worker",
		Token:          1,
		ToolCallID:     "t1",
		AttemptID:      "att-tool-1",
		Outcome:        "succeeded",
		ResultRef:      "res-ref",
		Code:           "200",
		EffectEvidence: "ev",
		SourcePosition: 0,
	}
	matchingToolObs.Facts.ExecutorFence = 7
	matchingToolObs.Facts.Usage = &kernel.BudgetObservedEvent{
		AttemptID:  "att-tool-1",
		TokensUsed: 5,
		Basis:      "exact",
	}
	matchingToolObs.Facts.ModelPlan = &kernel.ModelRequestPlannedEvent{
		InvocationID:          "m2",
		FirstAttemptID:        "a2",
		Purpose:               "turn",
		ModelRef:              "model-ref",
		ContextRef:            "ctx-ref",
		EstimatedBudgetTokens: 10,
	}

	// Manually assign slot to attempt for test completion check
	sess.mu.Lock()
	ref, _ := sess.slots.reserve()
	_ = sess.slots.handoff(ref, Work{})
	sess.attemptSlots[attemptKey{runID: "r1", invocationID: "t1", attemptID: "att-tool-1"}] = ref
	sess.mu.Unlock()

	_, err = sess.SubmitObservation(ctx, matchingToolObs)
	if err != nil {
		t.Fatalf("matching tool observation failed: %v", err)
	}

	// Verify slot completed
	_, canReserve := sess.slots.reserve()
	if !canReserve {
		t.Fatal("expected slot to be released after tool result commit")
	}
}

func TestObservationAdditionalCoverage(t *testing.T) {
	t.Parallel()

	// Terminal helper tests
	if isTerminalObservation(&kernel.ObserveModelStreamCommand{}) {
		t.Error("expected false for stream command")
	}
	if !isTerminalObservation(&kernel.ObserveModelFailureCommand{}) {
		t.Error("expected true for model failure command")
	}
	if isTerminalObservation(&kernel.ObserveToolCheckCommand{}) {
		t.Error("expected false for tool check command")
	}

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("obs-more-test"))

	store := newFakeStore()
	setupDispatchedRun(store, "r1", "m1", "a1", 1, 1, 4)

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
		t.Fatalf("start failed: %v", err)
	}
	defer sess.Stop()

	// 1. Observation with empty CommandID, Origin, Token (auto-generates and defaults)
	obsDefaults := makeModelResponseObservation("s1", "r1", "m1", "a1", "", 0, 1, 5)
	obsDefaults.Command.(*kernel.ObserveModelResponseCommand).Origin = ""
	obsDefaults.Command.(*kernel.ObserveModelResponseCommand).Token = 0
	obsDefaults.Lease.AdmissionToken = 1
	// Set slot ref so it can complete
	sess.mu.Lock()
	sess.runs["r1"] = store.runs["r1"]
	sess.attemptSlots[attemptKey{runID: "r1", invocationID: "m1", attemptID: "a1"}] = "slot-manual"
	sess.slots.active["slot-manual"] = slotHandedOff
	sess.runs["r1"].ModelInvocations["m1"].Attempts["a1"].Status = kernel.ModelAttemptStatusRequesting
	sess.runs["r1"].ModelInvocations["m1"].Attempts["a1"].Dispatch.ExecutorFence = 5
	sess.mu.Unlock()

	// Append fails
	appendErr := errors.New("append obs failure")
	store.queueAppend(AppendResult{}, appendErr)
	_, err = sess.SubmitObservation(ctx, obsDefaults)
	if !errors.Is(err, appendErr) {
		t.Errorf("expected append error, got %v", err)
	}

	// 2. Kernel rejection on observation
	rejObs := obsDefaults
	rejObs.Command.(*kernel.ObserveModelResponseCommand).ResponseRef = "" // causes rejection
	store.queueAppend(AppendResult{}, nil)
	_, err = sess.SubmitObservation(ctx, rejObs)
	if err == nil {
		t.Error("expected kernel rejection on empty response ref")
	}

	// 3. LoadRun error on observation
	loadErr := errors.New("load run boom")
	store.loadRunFn = func(_ context.Context, _ string) (*kernel.State, uint64, error) {
		return nil, 0, loadErr
	}
	obsUncached := makeModelResponseObservation("s1", "r-uncached", "m1", "a1", "cmd-uncached", 1, 1, 5)
	sess.mu.Lock()
	sess.admission.ActiveRunID = "r-uncached"
	sess.mu.Unlock()
	_, err = sess.SubmitObservation(ctx, obsUncached)
	if !errors.Is(err, loadErr) {
		t.Errorf("expected load error, got %v", err)
	}
}
