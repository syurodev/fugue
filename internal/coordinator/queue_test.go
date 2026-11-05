package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestAdmissionFollowsFIFOForRunsAcceptedThisIncarnation(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("fifo-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
		ContextEpoch:     1,
		ContextRevision:  1,
	}

	exec := newFakeExecutor()
	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   2,
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

	// 1. Accept r1 (results queued: AcceptRun, then auto AdmitRun)
	queueAcceptR1(t, ctx, sess, store)

	// 2. Accept r2 while r1 is active (results queued: AcceptRun only, no auto AdmitRun yet)
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: testCmdAcceptR2, Kind: testKindAcceptRun, Target: testRunID2},
		RunRevision:       1,
		AdmissionRevision: 2,
	}, nil)

	queueTestAccept(t, ctx, sess, testRunID2, testCmdAcceptR2, "m2", "a2", "c2")

	// 3. Finish r1 -> should trigger auto-admit of r2
	queueFinishR1(t, ctx, sess, store)
	verifyFIFOTransitions(t, store)

	activeRun := sess.admission.ActiveRunID
	admRev := sess.admission.Revision

	if activeRun != testRunID2 {
		t.Fatalf("expected active run %s, got %s", testRunID2, activeRun)
	}
	if admRev != 4 {
		t.Fatalf("expected admission revision 4, got %d", admRev)
	}
}

func queueFinishR1(t *testing.T, ctx context.Context, sess *Session, store *fakeStore) {
	t.Helper()

	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-finish-r1", Kind: testKindObserve, Target: "r1"},
		RunRevision:       3,
		AdmissionRevision: 3,
	}, nil)
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-r2", Kind: testKindAdmitRun, Target: testRunID2},
		RunRevision:       2,
		AdmissionRevision: 4,
	}, nil)

	r1State := sess.runs["r1"]
	if r1State != nil {
		if inv := r1State.ModelInvocations["m1"]; inv != nil {
			if att := inv.Attempts["a1"]; att != nil {
				att.Status = kernel.ModelAttemptStatusRequesting
				att.Dispatch.ExecutorFence = 10
			}
		}
	}

	cmdFinish := new(kernel.ObserveModelResponseCommand)
	cmdFinish.CommandID = "cmd-finish-r1"
	cmdFinish.RunID = "r1"
	cmdFinish.Origin = testOriginSystem
	cmdFinish.Token = 1
	cmdFinish.AttemptID = "a1"
	cmdFinish.InvocationID = "m1"
	cmdFinish.ResponseRef = "resp-r1"
	cmdFinish.UsageStatus = "known"
	cmdFinish.FinishReason = kernel.FinishReasonStop

	usageEvt := new(kernel.BudgetObservedEvent)
	usageEvt.AttemptID = "a1"
	usageEvt.TokensUsed = 10
	usageEvt.Basis = "provider"

	reqFinish := Request{
		Command: cmdFinish,
		Facts: kernel.DecisionFacts{
			ExecutorFence: 10,
			Usage:         usageEvt,
		},
		Kind:      testKindObserve,
		Principal: testOriginSystem,
		Origin:    testOriginSystem,
		Target:    "r1",
	}
	_, err := sess.Submit(ctx, &reqFinish)
	if err != nil {
		t.Fatalf("finish r1 failed: %v", err)
	}
}

func verifyFIFOTransitions(t *testing.T, store *fakeStore) {
	t.Helper()

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) < 5 {
		t.Fatalf("expected at least 5 transitions, got %d", len(transitions))
	}

	admitR1Tx := transitions[1]
	if admitR1Tx.RunID != "r1" {
		t.Fatalf("expected r1 for first admit, got %s", admitR1Tx.RunID)
	}
	if admitR1Tx.Fence.AdmissionToken != 1 {
		t.Fatalf("expected token 1 for r1, got %d", admitR1Tx.Fence.AdmissionToken)
	}

	admitR2Tx := transitions[4]
	if admitR2Tx.RunID != "r2" {
		t.Fatalf("expected r2 for second admit, got %s", admitR2Tx.RunID)
	}
	if admitR2Tx.Fence.AdmissionToken != 2 {
		t.Fatalf("expected token 2 for r2, got %d", admitR2Tx.Fence.AdmissionToken)
	}
}

func TestQueuedRunsFromEarlierIncarnationAreNotAutoAdmitted(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("earlier-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 2,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}
	// Store holds a queued run from an earlier incarnation
	store.runs["r-old"] = &kernel.State{
		RunID:    "r-old",
		Accepted: true,
		Admitted: false,
	}

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

	// Verify no auto-admit was recorded
	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected zero transitions for queued run from earlier incarnation, got %d", txCount)
	}
}

func queueTestAccept(t *testing.T, ctx context.Context, sess *Session, runID, cmdID, invID, attID, ctxRef string) {
	t.Helper()

	req := acceptRunRequest(runID, cmdID, invID, attID)
	req.Facts.ModelPlan.ContextRef = ctxRef
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept %s failed: %v", runID, err)
	}
}

func TestSessionQueueEmptyPop(t *testing.T) {
	t.Parallel()
	q := newSessionQueue()
	if !q.empty() {
		t.Error("expected empty queue")
	}
	if item, ok := q.pop(); ok || item != nil {
		t.Errorf("expected nothing to pop, got %v, %v", ok, item)
	}
}

func queueAcceptR1(t *testing.T, ctx context.Context, sess *Session, store *fakeStore) {
	t.Helper()
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-r1", Kind: testKindAcceptRun, Target: "r1"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-r1", Kind: testKindAdmitRun, Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 2,
	}, nil)

	queueTestAccept(t, ctx, sess, "r1", "cmd-accept-r1", "m1", "a1", "c1")
}
