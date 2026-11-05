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

func TestSubmitAppendsDecidedBatchWithCoordinatorEnvelope(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("session-test"))

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

	cfg := Config{
		SessionID:        "s1",
		OwnerIncarnation: 1,
		WorkerCapacity:   2,
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

	// Queue responses for AcceptRun and subsequent automatic AdmitRun
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-1", Kind: "AcceptRun", Target: "r1"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-admit-1", Kind: "AdmitRun", Target: "r1"},
		RunRevision:       2,
		AdmissionRevision: 2,
	}, nil)

	req := Request{
		Command: &kernel.AcceptRunCommand{
			CommandID: "cmd-accept-1",
			RunID:     "r1",
			Origin:    "user",
			SessionID: "s1",
		},
		Facts: kernel.DecisionFacts{
			ModelPlan: &kernel.ModelRequestPlannedEvent{
				InvocationID:   "m1",
				FirstAttemptID: "a1",
				Purpose:        "turn",
				ModelRef:       "gpt-4",
				ContextRef:     "c1",
			},
		},
		Kind:      "AcceptRun",
		Principal: "user",
		Target:    "r1",
	}

	reply, err := sess.Submit(ctx, req)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if reply.Duplicate {
		t.Fatal("expected non-duplicate reply")
	}

	store.mu.Lock()
	transitions := store.transitions
	store.mu.Unlock()

	if len(transitions) == 0 {
		t.Fatal("expected at least one transition recorded")
	}

	firstTx := transitions[0]
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

func TestKernelRejectionAppendsNothing(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("reject-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}

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

	// PrepareDispatch on an unadmitted run should return kernel.Rejection
	req := Request{
		Command: &kernel.PrepareDispatchCommand{
			CommandID:    "cmd-prep-1",
			RunID:        "unadmitted-run",
			Origin:       "system",
			Token:        1,
			InvocationID: "m1",
			AttemptID:    "a1",
		},
		Facts:     kernel.DecisionFacts{},
		Kind:      "PrepareDispatch",
		Principal: "system",
		Target:    "unadmitted-run",
	}

	_, err = sess.Submit(ctx, req)
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

	ctx := t.Context()
	sess, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	req := Request{
		Command: &kernel.PrepareDispatchCommand{
			CommandID:    "cmd-1",
			RunID:        "r0",
			Origin:       "system",
			Token:        3,
			InvocationID: "m1",
			AttemptID:    "a1",
		},
		Facts:     kernel.DecisionFacts{},
		Kind:      "PrepareDispatch",
		Principal: "system",
		Target:    "r0",
	}

	_, err = sess.Submit(ctx, req)
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired, got %v", err)
	}

	store.mu.Lock()
	txCount := len(store.transitions)
	store.mu.Unlock()

	if txCount != 0 {
		t.Fatalf("expected zero appends for inherited active run, got %d", txCount)
	}
}

func TestCallerCancellationDoesNotAbortAcceptedCommand(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("cancel-test"))

	store := newFakeStore()
	store.admission = Admission{
		SessionID:        "s1",
		ActiveRunID:      "",
		OwnerIncarnation: 1,
		AdmissionToken:   0,
		Revision:         1,
		QueueRevision:    1,
	}

	appendBlocked := make(chan struct{})
	releaseAppend := make(chan struct{})
	appendDone := make(chan struct{})
	var once sync.Once

	store.onAppend = func(_ *Transition) {
		once.Do(func() {
			close(appendBlocked)
			<-releaseAppend
			close(appendDone)
		})
	}
	store.queueAppend(AppendResult{
		Duplicate:         false,
		Receipt:           codec.Receipt{CommandID: "cmd-accept-1", Kind: "AcceptRun", Target: "r1"},
		RunRevision:       1,
		AdmissionRevision: 1,
	}, nil)

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

	sessCtx := t.Context()
	sess, err := Start(sessCtx, cfg)
	if err != nil {
		t.Fatalf("start session failed: %v", err)
	}
	defer sess.Stop()

	committed := make(chan struct{}, 1)
	sess.setOnCommitted(func() {
		select {
		case committed <- struct{}{}:
		default:
		}
	})

	callerCtx, cancelCaller := context.WithCancel(sessCtx)

	req := Request{
		Command: &kernel.AcceptRunCommand{
			CommandID: "cmd-accept-1",
			RunID:     "r1",
			Origin:    "user",
			SessionID: "s1",
		},
		Facts:     kernel.DecisionFacts{},
		Kind:      "AcceptRun",
		Principal: "user",
		Target:    "r1",
	}

	task := supervise.Go(sessCtx, "test.submit", func(_ context.Context) error {
		_, sErr := sess.Submit(callerCtx, req)
		return sErr
	})

	<-appendBlocked
	cancelCaller()

	submitErr := task.Wait()
	if !errors.Is(submitErr, context.Canceled) {
		t.Fatalf("expected context.Canceled for caller Submit, got %v", submitErr)
	}

	close(releaseAppend)
	<-appendDone
	<-committed

	// Verify transition recorded and run cache advanced
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

func TestSessionEdgeCases(t *testing.T) {
	t.Parallel()

	fakeClk := clock.NewFake(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	idGen := ids.NewUUIDv7(fakeClk, ids.NewDeterministic("session-edge-test"))

	// 1. Start failure when LoadAdmission fails
	failStore := newFakeStore()
	admErr := errors.New("admission load boom")
	failStore.admissionErr = admErr
	_, err := Start(t.Context(), Config{
		SessionID: "s-fail",
		Store:     failStore,
		Clock:     fakeClk,
		IDs:       idGen,
	})
	if !errors.Is(err, admErr) {
		t.Errorf("expected admission load error, got %v", err)
	}

	// 2. Start with 0 worker capacity defaults to 1
	store := newFakeStore()
	exec := newFakeExecutor()
	sess, err := Start(t.Context(), Config{
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

	// 3. Submit on stopped session
	sess.Stop()
	_, err = sess.Submit(t.Context(), Request{})
	if !errors.Is(err, ErrSessionStopped) {
		t.Errorf("expected ErrSessionStopped, got %v", err)
	}

	// 4. Submit with canceled context
	sess2, err := Start(t.Context(), Config{
		SessionID: "s2",
		Store:     store,
		Executor:  exec,
		Clock:     fakeClk,
		IDs:       idGen,
	})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer sess2.Stop()

	cancCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = sess2.Submit(cancCtx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// 5. Submit with LoadRun error
	loadErr := errors.New("load run boom")
	store.loadRunFn = func(_ context.Context, _ string) (*kernel.State, uint64, error) {
		return nil, 0, loadErr
	}
	_, err = sess2.Submit(t.Context(), Request{
		Target:  "r-fail-load",
		Command: &kernel.AcceptRunCommand{RunID: "r-fail-load"},
	})
	if !errors.Is(err, loadErr) {
		t.Errorf("expected loadErr, got %v", err)
	}

	// 6. Submit with default fields (auto-populates kind, principal, target)
	store.loadRunFn = nil
	acceptCmd := &kernel.AcceptRunCommand{
		RunID: "r-defaults",
	}
	_, err = sess2.Submit(t.Context(), Request{
		Command: acceptCmd,
	})
	if err != nil {
		t.Fatalf("submit with default fields failed: %v", err)
	}
}
