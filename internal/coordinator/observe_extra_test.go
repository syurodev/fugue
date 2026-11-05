package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func testTerminalHelpers(t *testing.T) {
	t.Helper()
	if isTerminalObservation(new(kernel.ObserveModelStreamCommand)) {
		t.Error("expected false for stream command")
	}
	if !isTerminalObservation(new(kernel.ObserveModelFailureCommand)) {
		t.Error("expected true for model failure command")
	}
	if isTerminalObservation(new(kernel.ObserveToolCheckCommand)) {
		t.Error("expected false for tool check command")
	}
	if findDispatchEvent(nil) != nil {
		t.Error("expected nil dispatch event for nil")
	}
	toolEvt := new(kernel.ToolDispatchStartedEvent)
	if findDispatchEvent([]kernel.Event{toolEvt}) != toolEvt {
		t.Error("expected tool dispatch event")
	}
}

func testObsSubmitFailures(t *testing.T) {
	t.Helper()
	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("obs-fail-test"))
	store := newFakeStore()
	setupDispatchedRun(store, 1)
	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        testSessionID,
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
		t.Fatalf("start failed: %v", err)
	}
	defer sess.Stop()

	setupManualSlotAttempt(sess, store)

	obsFail := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-fail", 1, 1, 5)

	// 1. Append fails
	store.queueAppend(nil, errAppendObsBoom)
	_, err = sess.SubmitObservation(ctx, &obsFail)
	if !errors.Is(err, errAppendObsBoom) {
		t.Errorf("expected append error, got %v", err)
	}

	// 2. Kernel rejection on observation
	rejObs := obsFail
	if c, ok := rejObs.Command.(*kernel.ObserveModelResponseCommand); ok {
		c.ResponseRef = "" // causes rejection
	}
	_, err = sess.SubmitObservation(ctx, &rejObs)
	if err == nil {
		t.Error("expected kernel rejection on empty response ref")
	}

	// 3. LoadRun error on observation
	store.loadRunFn = func(_ context.Context, _ string) (*kernel.State, uint64, error) {
		return nil, 0, errLoadRunBoom
	}
	obsUncached := makeModelResponseObservation(testSessionID, "r-uncached", "m1", "a1", "cmd-uncached", 1, 1, 5)
	sess.mu.Lock()
	sess.admission.ActiveRunID = "r-uncached"
	sess.mu.Unlock()
	_, err = sess.SubmitObservation(ctx, &obsUncached)
	if !errors.Is(err, errLoadRunBoom) {
		t.Errorf("expected load error, got %v", err)
	}
}

func TestObservationAdditionalCoverage(t *testing.T) {
	t.Parallel()

	testTerminalHelpers(t)

	store := newFakeStore()
	setupDispatchedRun(store, 1)

	exec := newFakeExecutor()
	ctx, sess := startTestSession(t, store, exec, "obs-more-test", 1)
	defer sess.Stop()

	testNilSubmissions(t, ctx, sess)

	// 1. Observation with empty CommandID, Origin, Token (auto-generates and defaults)
	obsDefaults := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "", 0, 1, 5)
	if c, ok := obsDefaults.Command.(*kernel.ObserveModelResponseCommand); ok {
		c.Origin = ""
		c.Token = 0
		c.RunID = ""
	}
	obsDefaults.Lease.AdmissionToken = 1
	setupManualSlotAttempt(sess, store)

	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-obs-def", Kind: testKindObserve, Target: testRunID1},
		RunRevision:       3,
		AdmissionRevision: 1,
	}, nil)
	reply, err := sess.SubmitObservation(ctx, &obsDefaults)
	if err != nil {
		t.Fatalf("submit obs defaults: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected not duplicate")
	}

	sess.mu.Lock()
	obsAdmRev := sess.admission.Revision
	sess.mu.Unlock()
	if obsAdmRev != 1 {
		t.Fatalf("expected admission revision 1, got %d", obsAdmRev)
	}

	testObsSubmitFailures(t)
}

func testNilSubmissions(t *testing.T, ctx context.Context, sess *Session) {
	t.Helper()
	if _, err := sess.SubmitObservation(ctx, nil); err == nil {
		t.Error("expected error for nil observation")
	}
	if _, err := sess.Submit(ctx, nil); err == nil {
		t.Error("expected error for nil request")
	}
	sess.report(nil)
}

func setupManualSlotAttempt(sess *Session, store *fakeStore) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.runs[testRunID1] = store.runs[testRunID1]
	sess.attemptSlots[attemptKey{runID: testRunID1, invocationID: "m1", attemptID: "a1"}] = "slot-manual"
	sess.slots.active["slot-manual"] = slotHandedOff
	sess.runs[testRunID1].ModelInvocations["m1"].Attempts["a1"].Status = kernel.ModelAttemptStatusRequesting
	sess.runs[testRunID1].ModelInvocations["m1"].Attempts["a1"].Dispatch.ExecutorFence = 5
}
