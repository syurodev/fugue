package coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func acceptRunRequest(runID, cmdID, invID, attID string) Request {
	plan := new(kernel.ModelRequestPlannedEvent)
	plan.InvocationID = invID
	plan.FirstAttemptID = attID
	plan.Purpose = testPurposeTurn
	plan.ModelRef = "gpt-4"
	plan.ContextRef = "ctx-" + runID

	cmd := new(kernel.AcceptRunCommand)
	cmd.CommandID = cmdID
	cmd.RunID = runID
	cmd.SessionID = testSessionID
	cmd.InputRef = "input-" + runID
	cmd.QueueItemRef = "queue-" + runID
	cmd.ProtocolBinding = "binding-v1"
	cmd.Budgets = kernel.DefaultRunBudgets()

	return Request{
		Command:          cmd,
		Facts:            kernel.DecisionFacts{ModelPlan: plan},
		Origin:           testOriginUser,
		QueueDecisionRef: "decision-" + runID,
		Kind:             testKindAcceptRun,
		Principal:        testOriginUser,
		Target:           runID,
	}
}

func submitAsync(ctx context.Context, sess *Session, req *Request) <-chan error {
	done := make(chan error, 1)
	supervise.Go(ctx, "test.submit", func(_ context.Context) error {
		_, err := sess.Submit(ctx, req)
		done <- err
		return nil
	})
	return done
}

func (s *fakeStore) transitionList() []*Transition {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Transition(nil), s.transitions...)
}

func (s *fakeStore) maxAppendOverlap() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

// blockAppendsOf makes the first append of runID wait until release is closed.
func blockAppendsOf(ctx context.Context, store *fakeStore, runID string) (blocked <-chan struct{}, release chan<- struct{}) {
	blockedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	store.onAppend = func(tr *Transition) {
		if tr.RunID != runID {
			return
		}
		once.Do(func() {
			close(blockedCh)
			select {
			case <-releaseCh:
			case <-ctx.Done():
			}
		})
	}
	return blockedCh, releaseCh
}

func TestResolverNeverAppendsAndActorAppliesTheResolution(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	store.queueLostAck(false)

	exec := newFakeExecutor()
	workReceived := make(chan struct{}, 1)
	exec.onExec = func(*Work, func(*Observation)) { workReceived <- struct{}{} }
	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, exec, "single-writer")
	defer sess.Stop()

	dispatch := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-single-writer")
	dispatchDone := submitAsync(ctx, sess, &dispatch)
	waitValue(t, ctx, resolveReport, "inline read-back")
	waitSignal(t, ctx, armed, "resolver timer")

	// The actor keeps serving other commands while the commit is unresolved. Its
	// append blocks until the resolver has read the commit back.
	acceptBlocked, releaseAccept := blockAppendsOf(ctx, store, testRunID2)
	accept := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	acceptDone := submitAsync(ctx, sess, &accept)
	waitSignal(t, ctx, acceptBlocked, "accept r2 append")

	store.queueResolve(CommitResolutionNotCommitted, nil)
	fakeClk.Advance(time.Second)
	waitValue(t, ctx, resolveReport, "resolver read-back")
	close(releaseAccept)

	if err := waitValue(t, ctx, acceptDone, "accept r2 reply"); err != nil {
		t.Fatalf("accept r2: %v", err)
	}
	if err := waitValue(t, ctx, dispatchDone, "dispatch reply"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if overlap := store.maxAppendOverlap(); overlap != 1 {
		t.Fatalf("the store saw %d concurrent appends; only the actor may append", overlap)
	}
	transitions := store.transitionList()
	if len(transitions) != 3 {
		t.Fatalf("expected dispatch, accept and one retry, got %d appends", len(transitions))
	}
	if transitions[0].CommandID != transitions[2].CommandID {
		t.Fatalf("the retry must reuse command ID %q, got %q", transitions[0].CommandID, transitions[2].CommandID)
	}
	waitSignal(t, ctx, workReceived, "work handed off")
	if count := exec.workCount(); count != 1 {
		t.Fatalf("expected exactly one work item, got %d", count)
	}
}

func setupReadyToolRun(store *fakeStore) {
	setupAdmittedRun(store)
	st := store.runs[testRunID1]
	st.Budgets = kernel.RunBudgets{Steps: 10, Tokens: 1000, ToolCalls: 1, ActiveTime: time.Hour}
	st.ToolCallsDispatched = 1
	st.ModelInvocations["m1"].Completed = true
	st.ModelInvocations["m1"].Attempts["a1"].Status = kernel.ModelAttemptStatusCompleted
	st.ToolOrder = []string{"t1"}
	st.ToolInvocations["t1"] = &kernel.ToolInvocation{
		ToolCallID: "t1", EffectClass: kernel.EffectClassPureRead, CheckGeneration: 1,
		Attempts: map[string]*kernel.ToolAttempt{testToolAttempt: {AttemptID: testToolAttempt, Status: kernel.ToolAttemptStatusReady}},
	}
}

func toolDispatchRequest(cmdID string) Request {
	return Request{
		Command: &kernel.PrepareDispatchCommand{
			CommandID: cmdID, RunID: testRunID1, Origin: "", Token: 1, InvocationID: "t1", AttemptID: testToolAttempt,
		},
		Facts:     kernel.DecisionFacts{DispatchAuthorizationRef: "auth-tool"},
		Origin:    testOriginSystem,
		Kind:      testKindPrepare,
		Principal: testOriginSystem,
		Target:    testRunID1,
	}
}

func TestBudgetBlockOfDispatchBatchFreesTheSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupReadyToolRun(store)
	exec := newFakeExecutor()
	workReceived := make(chan struct{}, 1)
	exec.onExec = func(*Work, func(*Observation)) { workReceived <- struct{}{} }
	ctx, sess := startTestSession(t, store, exec, "budget-block-slot", 1)
	defer sess.Stop()

	blocked := toolDispatchRequest("cmd-tool-blocked")
	reply, err := sess.Submit(ctx, &blocked)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(reply.Events) != 1 || reply.Events[0].EventName() != new(kernel.RunBlockAddedEvent).EventName() {
		t.Fatalf("expected a single budget block, got %v", reply.Events)
	}
	if count := exec.workCount(); count != 0 {
		t.Fatalf("a budget block must not hand off work, got %d", count)
	}

	extend := extendBudgetRequest(testOriginUser, testOriginUser)
	extendCmd, ok := extend.Command.(*kernel.ExtendBudgetCommand)
	if !ok {
		t.Fatal("extend request has no ExtendBudget command")
	}
	extendCmd.Kind = kernel.BudgetKindToolCalls
	if _, err = sess.Submit(ctx, &extend); err != nil {
		t.Fatalf("extend: %v", err)
	}

	retry := toolDispatchRequest("cmd-tool-resumed")
	if _, err = sess.Submit(ctx, &retry); err != nil {
		t.Fatalf("the slot stayed reserved after the budget block: %v", err)
	}
	waitSignal(t, ctx, workReceived, "tool work after the extension")
}

func setupDispatchedObservationSession(t *testing.T, store *fakeStore, seed string) (context.Context, *Session) {
	t.Helper()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), seed, 1)
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-"+seed)
	req.Facts.ModelDispatch.ExecutorFence = 5
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	return ctx, sess
}

func TestLostAckOfObservationIsReadBackAndApplied(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := setupDispatchedObservationSession(t, store, "obs-lost-ack")
	defer sess.Stop()

	receipt := codec.Receipt{CommandID: "cmd-obs-lost", Kind: testKindObserve, Target: testRunID1}
	store.queueLostAck(true)
	store.queueResolve(CommitResolutionCommitted, &receipt)

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-lost", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); err != nil {
		t.Fatalf("a committed observation must succeed after read-back, got %v", err)
	}

	store.mu.Lock()
	resolves := append([]string(nil), store.resolves...)
	storeState := store.runs[testRunID1]
	storeRev := store.runRevs[testRunID1]
	store.mu.Unlock()
	if len(resolves) != 1 || resolves[0] != "cmd-obs-lost" {
		t.Fatalf("expected one read-back of cmd-obs-lost, got %v", resolves)
	}
	if got := sess.runs[testRunID1]; got == nil || got.LastSequence != storeState.LastSequence {
		t.Fatalf("the cached run is not the committed state: %+v", got)
	}
	if sess.runRevs[testRunID1] != storeRev {
		t.Fatalf("cached run revision %d, store %d", sess.runRevs[testRunID1], storeRev)
	}
}

func TestLostAckOfObservationNotCommittedRetriesWithSameCommandID(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := setupDispatchedObservationSession(t, store, "obs-not-committed")
	defer sess.Stop()
	before := store.appendCount()

	store.queueLostAck(false)
	store.queueResolve(CommitResolutionNotCommitted, nil)

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-retry", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); err != nil {
		t.Fatalf("observation: %v", err)
	}
	transitions := store.transitionList()[before:]
	if len(transitions) != 2 || transitions[0].CommandID != transitions[1].CommandID {
		t.Fatalf("expected a retry with the same command ID, got %d transitions", len(transitions))
	}
}

func TestLostAckOfPlainSubmitIsReadBack(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "submit-lost-ack", 1)
	defer sess.Stop()

	store.queueLostAck(true)
	receipt := codec.Receipt{CommandID: testCmdAccept1, Kind: testKindAcceptRun, Target: testRunID1}
	store.queueResolve(CommitResolutionCommitted, &receipt)

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept after lost ack: %v", err)
	}
	if got := sess.runs[testRunID1]; got == nil || !got.Accepted {
		t.Fatal("the committed acceptance is not in the cache")
	}
}

func TestLostAckOfAdmissionIsReadBack(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1, ContextEpoch: 1, ContextRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admit-lost-ack", 1)
	defer sess.Stop()

	store.queueAppend(&AppendResult{RunRevision: 1, AdmissionRevision: 2}, nil)
	store.queueLostAck(true)
	store.queueResolve(CommitResolutionCommitted, &codec.Receipt{CommandID: "ignored", Kind: testKindAdmitRun, Target: testRunID1})

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}

	transitions := store.transitionList()
	if len(transitions) != 2 || transitions[1].Kind != testKindAdmitRun {
		t.Fatalf("expected accept then admit, got %d transitions", len(transitions))
	}
	store.mu.Lock()
	resolves := append([]string(nil), store.resolves...)
	store.mu.Unlock()
	if len(resolves) != 1 || resolves[0] != transitions[1].CommandID {
		t.Fatalf("expected a read-back of the admission command %q, got %v", transitions[1].CommandID, resolves)
	}
	if sess.admission.ActiveRunID != testRunID1 || !sess.queue.empty() {
		t.Fatalf("a committed admission must be applied: active=%q queueEmpty=%v", sess.admission.ActiveRunID, sess.queue.empty())
	}
}

func TestLostAckOfAdmissionNotCommittedRetriesWithSameCommandID(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1, ContextEpoch: 1, ContextRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admit-not-committed", 1)
	defer sess.Stop()

	store.queueAppend(&AppendResult{RunRevision: 1, AdmissionRevision: 2}, nil)
	store.queueLostAck(false)
	store.queueResolve(CommitResolutionNotCommitted, nil)

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	transitions := store.transitionList()
	if len(transitions) != 3 || transitions[1].CommandID != transitions[2].CommandID {
		t.Fatalf("expected admit and a retry with the same command ID, got %d transitions", len(transitions))
	}
	if sess.admission.ActiveRunID != testRunID1 {
		t.Fatalf("the retried admission did not take effect: %q", sess.admission.ActiveRunID)
	}
}

func TestRejectedAdmissionKeepsTheRunAtTheHeadOfTheQueue(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1, ContextEpoch: 1, ContextRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admit-rejected", 1)
	defer sess.Stop()

	store.queueAppend(&AppendResult{RunRevision: 1, AdmissionRevision: 2}, nil)
	store.queueAppend(nil, errAppendObsBoom)

	first := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &first); err != nil {
		t.Fatalf("accept r1: %v", err)
	}
	second := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	if _, err := sess.Submit(ctx, &second); err != nil {
		t.Fatalf("accept r2: %v", err)
	}

	var admitted []string
	for _, tr := range store.transitionList() {
		if tr.Kind == testKindAdmitRun {
			admitted = append(admitted, tr.RunID)
		}
	}
	if len(admitted) != 2 || admitted[0] != testRunID1 || admitted[1] != testRunID1 {
		t.Fatalf("a rejected admission must be retried for r1 before r2, got %v", admitted)
	}
	if sess.admission.ActiveRunID != testRunID1 {
		t.Fatalf("r1 must be active after the retry, got %q", sess.admission.ActiveRunID)
	}
}

func TestDefiniteRejectionReloadsRunAndAdmissionBeforeTheNextAppend(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := setupDispatchedObservationSession(t, store, "reject-reload")
	defer sess.Stop()
	before := store.appendCount()

	store.onAppend = func(*Transition) {
		store.runRevs[testRunID1] = 40
		store.admission.Revision = 50
	}
	store.queueAppend(nil, errAppendObsBoom)
	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-rejected", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); !errors.Is(err, errAppendObsBoom) {
		t.Fatalf("expected the store rejection, got %v", err)
	}
	store.onAppend = nil

	again := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-rejected", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &again); err != nil {
		t.Fatalf("retry after rejection: %v", err)
	}
	next := store.transitionList()[before+1]
	if next.ExpectedRunRevision != 40 || next.ExpectedAdmissionRevision != 50 {
		t.Fatalf("append used revisions %d/%d, want the store's 40/50", next.ExpectedRunRevision, next.ExpectedAdmissionRevision)
	}
}

func TestAppendAfterResolvedUnknownCommitUsesTheStoresRevisions(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "resolved-revisions", 1)
	defer sess.Stop()

	store.onAppend = func(*Transition) {
		store.runRevs[testRunID1] = 6
		store.admission.Revision = 8
	}
	store.queueLostAck(true)
	store.queueResolve(CommitResolutionCommitted, &codec.Receipt{CommandID: "cmd-disp-revs", Kind: testKindPrepare, Target: testRunID1})
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-revs")
	req.Facts.ModelDispatch.ExecutorFence = 5
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	store.onAppend = nil

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-revs", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); err != nil {
		t.Fatalf("observation: %v", err)
	}
	transitions := store.transitionList()
	last := transitions[len(transitions)-1]
	if last.ExpectedRunRevision != 7 || last.ExpectedAdmissionRevision != 9 {
		t.Fatalf("append used revisions %d/%d, want the store's 7/9", last.ExpectedRunRevision, last.ExpectedAdmissionRevision)
	}
}
