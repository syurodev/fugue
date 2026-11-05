package coordinator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

var errIDsBoom = errors.New("ids boom")

// flakyIDs hands out real IDs until okCount runs out, then fails.
type flakyIDs struct {
	inner   ids.Generator
	okCount int
}

func (f *flakyIDs) NewID() (string, error) {
	if f.okCount <= 0 {
		return "", errIDsBoom
	}
	f.okCount--
	id, err := f.inner.NewID()
	if err != nil {
		return "", fmt.Errorf("inner ids: %w", err)
	}
	return id, nil
}

func startSessionWithIDs(t *testing.T, store *fakeStore, gen ids.Generator) (context.Context, *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	sess, err := Start(ctx, &Config{
		SessionID: testSessionID, OwnerIncarnation: 1, WorkerCapacity: 1,
		Store: store, Executor: newFakeExecutor(), Clock: clock.NewFake(testNow()), IDs: gen,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return ctx, sess
}

func newTestIDs(seed string, okCount int) *flakyIDs {
	return &flakyIDs{inner: ids.NewUUIDv7(clock.NewFake(testNow()), ids.NewDeterministic(seed)), okCount: okCount}
}

func TestDispatchFailsCleanlyWhenNoReservationRefCanBeMade(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess := startSessionWithIDs(t, store, newTestIDs("no-ref", 0))
	defer sess.Stop()

	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-noref")
	if _, err := sess.Submit(ctx, &req); !errors.Is(err, errIDsBoom) {
		t.Fatalf("expected the ID failure, got %v", err)
	}
	if count := store.appendCount(); count != 0 {
		t.Fatalf("appended %d transitions without a reservation", count)
	}
}

func TestDispatchWithoutCommandIDAndNoIDsReleasesTheSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	gen := newTestIDs("one-id", 1)
	ctx, sess := startSessionWithIDs(t, store, gen)
	defer sess.Stop()

	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "")
	if _, err := sess.Submit(ctx, &req); !errors.Is(err, errIDsBoom) {
		t.Fatalf("expected the ID failure, got %v", err)
	}
	gen.okCount = 1
	if _, ok := reserveSlot(sess.slots); !ok {
		t.Fatal("the reserved slot was not released")
	}
}

func TestRequestWithoutCommandIDFailsWhenNoIDCanBeMade(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	ctx, sess := startSessionWithIDs(t, store, newTestIDs("no-ids", 0))
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, "", "m1", "a1")
	if _, err := sess.Submit(ctx, &req); !errors.Is(err, errIDsBoom) {
		t.Fatalf("expected the ID failure for the request, got %v", err)
	}
}

func TestObservationWithoutCommandIDFailsWhenNoIDCanBeMade(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)
	store.runs[testRunID1].ModelInvocations["m1"].Attempts["a1"].Dispatch.ExecutorFence = 5
	ctx, sess := startSessionWithIDs(t, store, newTestIDs("no-obs-ids", 0))
	defer sess.Stop()

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); !errors.Is(err, errIDsBoom) {
		t.Fatalf("expected the ID failure for the observation, got %v", err)
	}
}

func TestAdmissionStaysQueuedWhenNoCommandIDCanBeMade(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	// One ID for the acceptance, none for the admission.
	ctx, sess := startSessionWithIDs(t, store, newTestIDs("admit-no-id", 0))
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if sess.queue.empty() || sess.admission.ActiveRunID != "" {
		t.Fatal("the run must stay queued when no admission command ID can be made")
	}
}

func TestUnresolvedCommitBlocksOnlyItsOwnRun(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "busy-run", 1)
	defer sess.Stop()

	sess.unresolved["cmd-pending"] = &step{t: &Transition{RunID: testRunID1, CommandID: "cmd-pending"}}

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-busy", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("an observation of a busy run must wait for the read-back, got %v", err)
	}
	req := extendBudgetRequest(testOriginUser, testOriginUser)
	if _, err := sess.Submit(ctx, &req); !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("a request for a busy run must wait for the read-back, got %v", err)
	}

	other := acceptRunRequest(testRunID2, testCmdAcceptR2, "m2", "a2")
	if _, err := sess.Submit(ctx, &other); err != nil {
		t.Fatalf("another run must stay available: %v", err)
	}
}

func TestExhaustedReadBacksFailTheStepAndFreeTheSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "exhausted", 1)
	defer sess.Stop()

	for range maxCommitAttempts {
		store.queueLostAck(false)
		store.queueResolve(CommitResolutionNotCommitted, nil)
	}
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-exhausted")
	if _, err := sess.Submit(ctx, &req); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("expected ErrCommitUnknown once the attempts are used up, got %v", err)
	}
	if count := store.appendCount(); count != maxCommitAttempts {
		t.Fatalf("expected %d appends, got %d", maxCommitAttempts, count)
	}
	if _, ok := reserveSlot(sess.slots); !ok {
		t.Fatal("the slot of a failed dispatch must be free")
	}
}

func TestDuplicateAppendReloadsInsteadOfFoldingEventsTwice(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := setupDispatchedObservationSession(t, store, "duplicate-append")
	defer sess.Stop()

	store.queueAppend(&AppendResult{Duplicate: true, RunRevision: 9, AdmissionRevision: 9}, nil)
	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-dup", 1, 1, 5)
	reply, err := sess.SubmitObservation(ctx, &obs)
	if err != nil {
		t.Fatalf("observation: %v", err)
	}
	if !reply.Duplicate {
		t.Fatal("the reply must report the duplicate")
	}
	store.mu.Lock()
	want := store.runRevs[testRunID1]
	store.mu.Unlock()
	if sess.runRevs[testRunID1] != want {
		t.Fatalf("cached revision %d, store %d: a duplicate must reload", sess.runRevs[testRunID1], want)
	}
}

func TestFailedReloadIsRepairedByTheNextCommand(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := setupDispatchedObservationSession(t, store, "failed-reload")
	defer sess.Stop()

	store.queueAppend(nil, errAppendObsBoom)
	store.loadRunFn = func(context.Context, string) (*kernel.State, uint64, error) {
		return nil, 0, errLoadRunBoom
	}
	store.admissionErr = errAdmLoadBoom
	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-reload", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &obs); !errors.Is(err, errAppendObsBoom) {
		t.Fatalf("expected the store rejection, got %v", err)
	}
	if _, cached := sess.runs[testRunID1]; cached || !sess.admissionStale {
		t.Fatal("what could not be reloaded must be dropped or marked stale")
	}

	again := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-reload", 1, 1, 5)
	if _, err := sess.SubmitObservation(ctx, &again); !errors.Is(err, errAdmLoadBoom) {
		t.Fatalf("expected the admission load error while the store is down, got %v", err)
	}

	store.admissionErr = nil
	if _, err := sess.SubmitObservation(ctx, &again); !errors.Is(err, errLoadRunBoom) {
		t.Fatalf("expected the run load error while the store is down, got %v", err)
	}

	store.mu.Lock()
	store.loadRunFn = nil
	store.mu.Unlock()
	if _, err := sess.SubmitObservation(ctx, &again); err != nil {
		t.Fatalf("the next command must reload and succeed: %v", err)
	}
}

func TestResolutionOfUnknownCommandIsIgnoredAndUnavailableKeepsWaiting(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess, _, resolveReport, armed := setupBackgroundResolveSession(t, store, newFakeExecutor(), "stray-resolution")
	defer sess.Stop()

	store.queueLostAck(false)
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-stray")
	dispatchDone := submitAsync(ctx, sess, &req)
	waitValue(t, ctx, resolveReport, "inline read-back")
	waitSignal(t, ctx, armed, "resolver timer")

	sess.mailbox <- submission{resolved: &resolution{commandID: "never-asked", status: CommitResolutionCommitted, receipt: codec.Receipt{}}}
	sess.mailbox <- submission{resolved: &resolution{commandID: "cmd-disp-stray", status: CommitResolutionUnavailable, receipt: codec.Receipt{}}}

	other := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-other")
	if _, err := sess.Submit(ctx, &other); !errors.Is(err, ErrCommitUnresolved) {
		t.Fatalf("the unresolved commit must still halt dispatching, got %v", err)
	}
	select {
	case err := <-dispatchDone:
		t.Fatalf("the first dispatch must stay pending, got %v", err)
	default:
	}
}

func TestResolverKeepsAskingAfterStoreErrors(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	exec := newFakeExecutor()
	workReceived := make(chan struct{}, 1)
	exec.onExec = func(*Work, func(*Observation)) { workReceived <- struct{}{} }
	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, exec, "resolver-errors")
	defer sess.Stop()

	store.queueLostAck(true)
	store.mu.Lock()
	store.resolveQueue = append(store.resolveQueue,
		resolveResultOrError{err: errAppendObsBoom},
		resolveResultOrError{err: errAppendObsBoom},
		resolveResultOrError{status: CommitResolutionCommitted},
	)
	store.mu.Unlock()

	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-errors")
	done := submitAsync(ctx, sess, &req)
	waitValue(t, ctx, resolveReport, "inline read-back")
	waitSignal(t, ctx, armed, "first resolver timer")
	fakeClk.Advance(time.Second)
	waitValue(t, ctx, resolveReport, "first resolver read-back")
	waitSignal(t, ctx, armed, "second resolver timer")
	fakeClk.Advance(2 * time.Second)
	if err := waitValue(t, ctx, done, "dispatch reply"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitSignal(t, ctx, workReceived, "work")
}

func TestTransitionWithoutEventsHasNoSequenceRange(t *testing.T) {
	t.Parallel()

	_, sess := startTestSession(t, newFakeStore(), newFakeExecutor(), "no-events", 1)
	defer sess.Stop()

	tr := sess.buildTransition(testRunID1, "cmd", testOriginUser, kernel.NewState(), nil, new(Request))
	if tr.Result.FirstSequence != 0 || tr.Result.LastSequence != 0 {
		t.Fatalf("an empty batch has no sequence range, got %+v", tr.Result)
	}
}

func TestResolverBackoffDoublesUpToTheCap(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	store.queueLostAck(false)
	ctx, sess, fakeClk, resolveReport, armed := setupBackgroundResolveSession(t, store, newFakeExecutor(), "backoff")
	defer sess.Stop()

	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-backoff")
	_ = submitAsync(ctx, sess, &req)
	waitValue(t, ctx, resolveReport, "inline read-back")

	for _, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, resolveBackoffMax, resolveBackoffMax} {
		waitSignal(t, ctx, armed, "resolver timer")
		fakeClk.Advance(delay - time.Millisecond)
		if fakeClk.Pending() != 1 {
			t.Fatalf("the resolver woke before its %v delay elapsed", delay)
		}
		fakeClk.Advance(time.Millisecond)
		waitValue(t, ctx, resolveReport, "resolver read-back")
	}
}
