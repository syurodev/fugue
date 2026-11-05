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
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func verifyFirstTxEnvelope(t *testing.T, firstTx *Transition, fakeClk clock.Clock) {
	t.Helper()
	if firstTx.RunID != "r1" {
		t.Fatalf("expected RunID 'r1', got %q", firstTx.RunID)
	}
	if firstTx.Fence.QueueRevision != 5 {
		t.Fatalf("expected Fence.QueueRevision 5, got %d", firstTx.Fence.QueueRevision)
	}
	if len(firstTx.Events) != 1 {
		t.Fatalf("expected 1 event in first transition, got %d", len(firstTx.Events))
	}

	evt := firstTx.Events[0]
	env := evt.EventEnvelope()
	if env.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", env.Sequence)
	}
	if !env.Timestamp.Equal(fakeClk.Now()) {
		t.Fatalf("expected timestamp %v, got %v", fakeClk.Now(), env.Timestamp)
	}
}

func makeEnvelopeTestRequest() Request {
	cmdAccept := new(kernel.AcceptRunCommand)
	cmdAccept.Budgets = kernel.DefaultRunBudgets()
	cmdAccept.CommandID = testCmdAccept1
	cmdAccept.RunID = testRunID1
	cmdAccept.Origin = testOriginUser
	cmdAccept.SessionID = testSessionID

	plan := new(kernel.ModelRequestPlannedEvent)
	plan.InvocationID = "m1"
	plan.FirstAttemptID = "a1"
	plan.Purpose = testPurposeTurn
	plan.ModelRef = "gpt-4"
	plan.ContextRef = "c1"

	return Request{
		Command: cmdAccept,
		Facts: kernel.DecisionFacts{
			ModelPlan: plan,
		},
		Kind:      testKindAcceptRun,
		Principal: testOriginUser,
		Origin:    testOriginUser,
		Target:    testRunID1,
	}
}

func TestSubmitAppendsDecidedBatchWithCoordinatorEnvelope(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    5,
	}

	exec := newFakeExecutor()
	ctx, sess := startTestSession(t, store, exec, "session-test", 2)
	defer sess.Stop()

	// Queue responses for AcceptRun and subsequent automatic AdmitRun
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: testCmdAccept1, Kind: testKindAcceptRun, Target: testRunID1},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-1", Kind: testKindAdmitRun, Target: testRunID1},
		RunRevision:       2,
		AdmissionRevision: 2,
	}, nil)

	req := makeEnvelopeTestRequest()

	reply, err := sess.Submit(ctx, &req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	sess.mu.Lock()
	sessAdmRev := sess.admission.Revision
	sess.mu.Unlock()
	if sessAdmRev != 2 {
		t.Fatalf("expected admission revision 2, got %d", sessAdmRev)
	}

	verifySubmitEnvelopeTransitions(t, store, fakeClk)
}

func verifySubmitEnvelopeTransitions(t *testing.T, store *fakeStore, fakeClk clock.Clock) {
	t.Helper()
	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) == 0 {
		t.Fatal("expected at least one transition recorded")
	}

	tx := transitions[0]
	if tx.Kind != testKindAcceptRun {
		t.Fatalf("expected Kind %s, got %s", testKindAcceptRun, tx.Kind)
	}
	if tx.Principal != testOriginUser {
		t.Fatalf("expected Principal %s, got %s", testOriginUser, tx.Principal)
	}
	if tx.Target != testRunID1 {
		t.Fatalf("expected Target %s, got %s", testRunID1, tx.Target)
	}
	if tx.Result.FirstSequence != 1 || tx.Result.LastSequence != 1 {
		t.Fatalf("expected sequence 1, got %d..%d", tx.Result.FirstSequence, tx.Result.LastSequence)
	}

	verifyFirstTxEnvelope(t, tx, fakeClk)
}

func startSingleCapacitySession(t *testing.T, store *fakeStore, seed string) (context.Context, *Session) {
	t.Helper()
	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic(seed))
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
	return ctx, sess
}

func TestKernelRejectionAppendsNothing(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}

	ctx, sess := startSingleCapacitySession(t, store, "reject-test")
	defer sess.Stop()

	// PrepareDispatch on an unadmitted run should return kernel.Rejection
	cmdPrep := new(kernel.PrepareDispatchCommand)
	cmdPrep.CommandID = "cmd-prep-1"
	cmdPrep.RunID = "unadmitted-run"
	cmdPrep.Origin = testOriginSystem
	cmdPrep.Token = 1
	cmdPrep.InvocationID = "m1"
	cmdPrep.AttemptID = "a1"

	req := Request{
		Command:   cmdPrep,
		Facts:     kernel.DecisionFacts{},
		Kind:      testKindPrepare,
		Principal: testOriginSystem,
		Origin:    testOriginSystem,
		Target:    "unadmitted-run",
	}

	_, err := sess.Submit(ctx, &req)
	if err == nil {
		t.Fatal("expected rejection error, got nil")
	}

	var rej kernel.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("expected kernel.Rejection, got %T: %v", err, err)
	}
	if rej.Code != kernel.RejectionCodeRunNotAdmitted {
		t.Fatalf("expected RejectionCodeRunNotAdmitted, got %v", rej.Code)
	}

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected zero append calls on rejection, got %d", txCount)
	}
}

func TestInheritedActiveRunReportsRecoveryRequired(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("inherited-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "r0",
		OwnerIncarnation: 1, // earlier incarnation
		AdmissionToken:   3,
		Revision:         4,
		QueueRevision:    2,
	}

	exec := newFakeExecutor()

	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 2, // new incarnation
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

	req := makeInheritedPrepareRequest()

	_, err = sess.Submit(ctx, &req)
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got %v", err)
	}

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected zero appends for inherited active run, got %d", txCount)
	}

	testSameIncarnationNoInherit(t, ctx, &cfg)
}

func makeInheritedPrepareRequest() Request {
	return Request{
		Command: &kernel.PrepareDispatchCommand{
			CommandID:    "cmd-1",
			RunID:        "r0",
			Origin:       "system",
			Token:        3,
			InvocationID: "m1",
			AttemptID:    "a1",
		},
		Facts:     kernel.DecisionFacts{},
		Kind:      testKindPrepare,
		Principal: "system",
		Origin:    "system",
		Target:    "r0",
	}
}

func testSameIncarnationNoInherit(t *testing.T, ctx context.Context, cfg *Config) {
	t.Helper()
	cfgSame := *cfg
	cfgSame.OwnerIncarnation = 1
	sessSame, err := Start(ctx, &cfgSame)
	if err != nil {
		t.Fatalf("start same failed: %v", err)
	}
	defer sessSame.Stop()
	if sessSame.inheritedRunID != "" {
		t.Fatalf("expected empty inheritedRunID for same incarnation, got %s", sessSame.inheritedRunID)
	}
}

func verifyCommittedAdvance(t *testing.T, store *fakeStore, sess *Session) {
	t.Helper()
	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount < 1 {
		t.Fatalf("expected at least one transition recorded after release, got %d", txCount)
	}

	sess.mu.Lock()
	r1State := sess.runs["r1"]
	sess.mu.Unlock()

	if r1State == nil || !r1State.Accepted {
		t.Fatalf("expected run r1 cache advanced with Accepted=true, got %+v", r1State)
	}
}

func setupBlockedAppend(t *testing.T, store *fakeStore) (chan struct{}, chan struct{}, chan struct{}) {
	t.Helper()
	appendBlocked := make(chan struct{})
	releaseAppend := make(chan struct{})
	appendDone := make(chan struct{})
	var once sync.Once

	store.onAppend = func(_ *Transition) {
		once.Do(func() {
			close(appendBlocked)
			select {
			case <-releaseAppend:
			case <-t.Context().Done():
			}
			close(appendDone)
		})
	}
	store.queueAppend(&AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-1", Kind: testKindAcceptRun, Target: "r1"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)

	return appendBlocked, releaseAppend, appendDone
}

func TestCallerCancellationDoesNotAbortAcceptedCommand(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}

	appendBlocked, releaseAppend, appendDone := setupBlockedAppend(t, store)

	sessCtx, sess := startSingleCapacitySession(t, store, "cancel-test")
	defer sess.Stop()

	committed := make(chan struct{}, 1)
	sess.setOnCommitted(func() {
		select {
		case committed <- struct{}{}:
		default:
		}
	})

	callerCtx, cancelCaller := context.WithCancel(sessCtx)

	cmdAccept := new(kernel.AcceptRunCommand)
	cmdAccept.Budgets = kernel.DefaultRunBudgets()
	cmdAccept.CommandID = "cmd-accept-1"
	cmdAccept.RunID = "r1"
	cmdAccept.Origin = testOriginUser
	cmdAccept.SessionID = "s1"
	req := Request{
		Command:   cmdAccept,
		Facts:     kernel.DecisionFacts{},
		Kind:      testKindAcceptRun,
		Principal: testOriginUser,
		Origin:    testOriginUser,
		Target:    "r1",
	}

	task := supervise.Go(sessCtx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(callerCtx, &req)
		return sErr
	})

	waitSignal(t, sessCtx, appendBlocked, "appendBlocked")
	cancelCaller()

	submitErr := task.Wait()
	if !errors.Is(submitErr, context.Canceled) {
		t.Fatalf("expected context.Canceled for caller Submit, got %v", submitErr)
	}

	close(releaseAppend)
	waitSignal(t, sessCtx, appendDone, "appendDone")
	waitSignal(t, sessCtx, committed, "committed")

	verifyCommittedAdvance(t, store, sess)
}

func TestSessionEdgeCases(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("session-edge-test"))

	t.Run("start failure on admission load error", func(t *testing.T) {
		t.Parallel()
		failStore := newFakeStore()
		failStore.admissionErr = errAdmLoadBoom
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		t.Cleanup(cancel)
		_, err := Start(ctx, &Config{
			SessionID: "s-fail",
			Store:     failStore,
			Clock:     fakeClk,
			IDs:       idGen,
		})
		if !errors.Is(err, errAdmLoadBoom) {
			t.Errorf("expected admission load error, got %v", err)
		}
	})

	t.Run("zero worker capacity defaults to 1 and stopped submit fails", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		exec := newFakeExecutor()
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		t.Cleanup(cancel)
		sess, err := Start(ctx, &Config{
			SessionID:      "s-cap0",
			WorkerCapacity: 0,
			Store:          store,
			Executor:       exec,
			Clock:          fakeClk,
			IDs:            idGen,
		})
		if err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if sess.slots.capacity != 1 {
			t.Errorf("expected capacity 1, got %d", sess.slots.capacity)
		}
		sess.Stop()
		_, err = sess.Submit(ctx, &Request{})
		if !errors.Is(err, ErrSessionStopped) {
			t.Errorf("expected ErrSessionStopped, got %v", err)
		}
	})

	t.Run("submit errors and default fields", func(t *testing.T) {
		t.Parallel()
		testSessionSubmitCases(t, fakeClk, idGen)
	})
}

func testSessionSubmitCases(t *testing.T, fakeClk clock.Clock, idGen ids.Generator) {
	t.Helper()
	store := newFakeStore()
	exec := newFakeExecutor()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &Config{
		SessionID: "s2",
		Store:     store,
		Executor:  exec,
		Clock:     fakeClk,
		IDs:       idGen,
	})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer sess.Stop()

	cancCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = sess.Submit(cancCtx, &Request{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	store.loadRunFn = func(_ context.Context, _ string) (*kernel.State, uint64, error) {
		return nil, 0, errLoadRunBoom
	}
	cmdFail := new(kernel.AcceptRunCommand)
	cmdFail.Budgets = kernel.DefaultRunBudgets()
	cmdFail.RunID = "r-fail-load"
	reqFail := Request{
		Target:  "r-fail-load",
		Origin:  testOriginUser,
		Command: cmdFail,
	}
	_, err = sess.Submit(ctx, &reqFail)
	if !errors.Is(err, errLoadRunBoom) {
		t.Errorf("expected loadErr, got %v", err)
	}

	store.loadRunFn = nil
	acceptCmd := new(kernel.AcceptRunCommand)
	acceptCmd.Budgets = kernel.DefaultRunBudgets()
	acceptCmd.RunID = "r-defaults"
	reqDefaults := Request{
		Command: acceptCmd,
		Origin:  defaultOriginSession,
	}
	_, err = sess.Submit(ctx, &reqDefaults)
	if err != nil {
		t.Fatalf("submit with default fields failed: %v", err)
	}
	verifyDefaultFieldTransitions(t, store)
}

func verifyDefaultFieldTransitions(t *testing.T, store *fakeStore) {
	t.Helper()
	store.mu.Lock()
	txDef := store.transitions[len(store.transitions)-1]
	store.mu.Unlock()

	if txDef.Kind != testKindAcceptRun {
		t.Errorf("expected Kind %s from command name, got %s", testKindAcceptRun, txDef.Kind)
	}
	if txDef.Principal != defaultOriginSession {
		t.Errorf("expected Principal %s from origin, got %s", defaultOriginSession, txDef.Principal)
	}
	if txDef.Target != "r-defaults" {
		t.Errorf("expected Target %s from runID, got %s", "r-defaults", txDef.Target)
	}
}
