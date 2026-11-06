package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

func TestLostAcknowledgementResolvedThroughRealStore(t *testing.T) {
	t.Parallel()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	store := &realLostAckStore{Store: NewCoordinatorStore(s), store: s, resolved: make(chan string, 8)}
	executor := newRealExecutor(true)
	sess := startRealSessionWith(t, store, clk, "lost-ack", 1, executor)
	submitRealAccept(t, sess, realFirstRun)
	ctx := t.Context()
	reply, err := sess.Submit(ctx, realDispatch(realFirstRun))
	if err != nil || reply.Receipt.CommandID != "dispatch-first" {
		t.Fatalf("dispatch must resolve its real committed receipt: err=%v command=%q", err, reply.Receipt.CommandID)
	}
	assertDispatchReadBack(t, store)
	work := waitRealWork(t, ctx, executor)
	if work.Lease.RunID != realFirstRun || work.Lease.ExecutorFence != 5 {
		t.Fatal("work must carry the committed dispatch identity and sequence fence")
	}
	obs := waitRealObservation(t, ctx, executor)
	duplicate, err := sess.SubmitObservation(ctx, obs)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("scripted observation must commit once and deduplicate: err=%v duplicate=%t", err, duplicate.Duplicate)
	}
	sess.Stop()
	if executor.calls.Load() != 1 {
		t.Fatal("lost acknowledgement must hand off exactly one Work")
	}
	state, err := s.RebuildRun(ctx, realFirstRun)
	if err != nil || !state.Finished {
		t.Fatal("scripted completion must finish the durable run")
	}
}

func TestStaleIncarnationCannotAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	firstStore, clk, closeFirst := openCoordinatorHost(t, dir)
	t.Cleanup(closeFirst)
	first := startRealSession(t, NewCoordinatorStore(firstStore), clk, "original")
	submitRealAccept(t, first, realFirstRun)
	first.Stop()
	closeFirst()
	secondStore, secondClock, closeSecond := openCoordinatorHost(t, dir)
	t.Cleanup(closeSecond)
	host, err := storage.CurrentHostIncarnation(t.Context(), secondStore.DB())
	if err != nil || host.LockGeneration != 2 {
		t.Fatal("reopened host must durably own incarnation 2")
	}
	executor := newRealExecutor(false)
	stale := startRealSessionWith(t, NewCoordinatorStore(secondStore), secondClock, "stale", 1, executor)
	_, err = stale.Submit(t.Context(), realDispatch(realFirstRun))
	if !errors.Is(err, execstore.ErrStaleFence) || errors.Is(err, coordinator.ErrCommitUnknown) {
		t.Fatal("stale incarnation must receive a definite ErrStaleFence")
	}
	stale.Stop()
	if executor.calls.Load() != 0 {
		t.Fatal("stale incarnation must hand off no work")
	}
	assertRunStillPlanned(t, secondStore)
}

func TestRestartNeverRedispatchesDispatchingIntent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, clk, closeFirst := openCoordinatorHost(t, dir)
	t.Cleanup(closeFirst)
	store := &realLostAckStore{Store: NewCoordinatorStore(s), store: s, committed: make(chan struct{}), stopBeforeAck: true}
	executor := newRealExecutor(false)
	sess := startRealSessionWith(t, store, clk, "before-restart", 1, executor)
	submitRealAccept(t, sess, realFirstRun)
	ctx := t.Context()
	submitter := supervise.Go(ctx, "test.dispatch_before_stop", func(taskCtx context.Context) error {
		_, err := sess.Submit(taskCtx, realDispatch(realFirstRun))
		if errors.Is(err, coordinator.ErrSessionStopped) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("dispatch during shutdown: %w", err)
		}
		return nil
	})
	defer stopSubmission(submitter)
	select {
	case <-store.committed:
	case <-ctx.Done():
		t.Fatalf("dispatch did not commit before shutdown: %v", ctx.Err())
	}
	sess.Stop()
	if submitter.Wait() != nil || executor.calls.Load() != 0 {
		t.Fatal("stopped incarnation must hand off no work after commit")
	}
	assertRunDispatching(t, s)
	closeFirst()
	reopened, reopenedClock, closeReopened := openCoordinatorHost(t, dir)
	t.Cleanup(closeReopened)
	restartedExecutor := newRealExecutor(false)
	restarted := startRealSessionWith(t, NewCoordinatorStore(reopened), reopenedClock, "after-restart", reopened.Host().LockGeneration, restartedExecutor)
	if _, err := restarted.Submit(ctx, realDispatch(realFirstRun)); !errors.Is(err, coordinator.ErrRecoveryRequired) {
		t.Fatal("inherited dispatching run must require recovery")
	}
	restarted.Stop()
	if restartedExecutor.calls.Load() != 0 {
		t.Fatal("restart must never redispatch the durable intent")
	}
	assertRunDispatching(t, reopened)
}

func TestRepeatedAcceptsUseDurableQueueFenceAndContinueContext(t *testing.T) {
	t.Parallel()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	executor := newRealExecutor(true)
	sess := startRealSessionWith(t, NewCoordinatorStore(s), clk, "context", 1, executor)
	for _, runID := range []string{realFirstRun, realSecondRun, realThirdRun} {
		submitRealAccept(t, sess, runID)
	}
	ctx := t.Context()
	var last *coordinator.Observation
	for _, runID := range []string{realFirstRun, realSecondRun, realThirdRun} {
		if _, err := sess.Submit(ctx, realDispatch(runID)); err != nil {
			t.Fatalf("queued run must dispatch with its actual admitted token: %v", err)
		}
		assertContextAdmission(t, s, runID)
		last = waitRealObservation(t, ctx, executor)
	}
	if _, err := sess.SubmitObservation(ctx, last); err != nil {
		t.Fatalf("last scripted completion must be durable: %v", err)
	}
	sess.Stop()
	adm, err := s.ReadAdmission(ctx, adapterSession)
	if err != nil || adm.ActiveRunID != "" || adm.QueueRevision != 2 || adm.ContextStartRunID != realFirstRun {
		t.Fatal("all runs must finish with the durable queue fence and original context start")
	}
	if executor.calls.Load() != 3 {
		t.Fatal("each admitted run must hand off once")
	}
}

func assertRunStillPlanned(t *testing.T, s *execstore.Store) {
	t.Helper()
	record, err := s.ReadRun(t.Context(), realFirstRun)
	if err != nil || record.LastSequence != 4 {
		t.Fatal("stale incarnation must append no dispatch events")
	}
	if _, found, readErr := s.LookupReceipt(t.Context(), "dispatch-first"); readErr != nil || found {
		t.Fatal("stale dispatch must create no receipt")
	}
}

func assertRunDispatching(t *testing.T, s *execstore.Store) {
	t.Helper()
	state, err := s.RebuildRun(t.Context(), realFirstRun)
	if err != nil {
		t.Fatal("rebuild dispatching run")
	}
	attempt := state.ModelInvocations["model-first"].Attempts["attempt-first"]
	if attempt.Status != kernel.ModelAttemptStatusRequesting || state.LastSequence != 5 {
		t.Fatal("committed dispatch intent must survive unchanged")
	}
}

func assertContextAdmission(t *testing.T, s *execstore.Store, runID string) {
	t.Helper()
	events, err := s.ReadEvents(t.Context(), runID, 1, 4)
	if err != nil || len(events) != 4 {
		t.Fatal("next queued run must be admitted")
	}
	admitted, ok := events[1].(*kernel.RunAdmittedEvent)
	if !ok || admitted.ContextEpoch != 1 || admitted.InitialContextRevision != 1 || admitted.StartRunID != realFirstRun {
		t.Fatal("later admission must continue the durable initial context boundary")
	}
}
