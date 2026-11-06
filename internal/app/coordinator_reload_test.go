package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
	"github.com/syurodev/fugue/internal/kernel"
)

const reloadAcceptCommand = "accept-second"

func TestConfirmedAcceptanceSurvivesFailedReload(t *testing.T) {
	t.Parallel()
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			t.Parallel()
			testConfirmedAcceptanceReload(t, duplicate)
		})
	}
}

func testConfirmedAcceptanceReload(t *testing.T, duplicate bool) {
	t.Helper()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	store := &confirmedReloadStore{Store: NewCoordinatorStore(s), store: s, duplicate: duplicate}
	executor := newRealExecutor(true)
	sess := startRealSessionWith(t, store, clk, "reload", 1, executor)
	submitRealAccept(t, sess, realFirstRun)
	accept := realAccept(realSecondRun)
	accept.Facts.ModelPlan.ModelRef = "caller-model"
	accept.Facts.ModelPlan.ToolDeclarationRefs = []string{"caller-tool"}
	ctx := t.Context()
	reply, err := sess.Submit(ctx, accept)
	if err != nil || reply.Receipt.CommandID != reloadAcceptCommand || reply.Duplicate != duplicate {
		t.Fatalf("confirmed acceptance must return its real receipt despite the failed reload: err=%v command=%q duplicate=%t", err, reply.Receipt.CommandID, reply.Duplicate)
	}
	submitRealAccept(t, sess, realThirdRun)
	completeRealRun(t, ctx, sess, executor, realFirstRun)
	assertRetainedCallerPlan(t, s)
	completeRealRun(t, ctx, sess, executor, realSecondRun)
	completeRealRun(t, ctx, sess, executor, realThirdRun)
	sess.Stop()
	if executor.calls.Load() != 3 {
		t.Fatal("each accepted run must hand off once after reads recover")
	}
	if store.acceptCalls != 1 || store.failedLoads != 1 || store.recoveredLoads != 1 || !store.confirmed {
		t.Fatal("confirmed acceptance must reload actual revisions without being appended again")
	}
	for _, runID := range []string{realFirstRun, realSecondRun, realThirdRun} {
		assertFinishedResponse(t, s, runID)
	}
}

func completeRealRun(t *testing.T, ctx context.Context, sess *coordinator.Session, executor *realExecutor, runID string) {
	t.Helper()
	if _, err := sess.Submit(ctx, realDispatch(runID)); err != nil {
		t.Fatalf("FIFO head must remain eligible for dispatch: %v", err)
	}
	work := waitRealWork(t, ctx, executor)
	if work.Lease.RunID != runID {
		t.Fatal("accepted runs must keep their original FIFO position")
	}
	obs := waitRealObservation(t, ctx, executor)
	// The next admission can supersede this lease before its duplicate arrives.
	if _, err := sess.SubmitObservation(ctx, obs); err != nil && !errors.Is(err, coordinator.ErrStaleLease) {
		t.Fatalf("scripted completion must precede the observation barrier: %v", err)
	}
}

func assertRetainedCallerPlan(t *testing.T, s *execstore.Store) {
	t.Helper()
	events, err := s.ReadEvents(t.Context(), realSecondRun, 1, 20)
	if err != nil || len(events) != 4 {
		t.Fatal("confirmed acceptance must retain its FIFO position ahead of the later run")
	}
	admitted, admittedOK := events[1].(*kernel.RunAdmittedEvent)
	plan, planOK := events[3].(*kernel.ModelRequestPlannedEvent)
	if !admittedOK || admitted.QueueDecisionRef != "decision-second" || !planOK || plan.ModelRef != "caller-model" ||
		len(plan.ToolDeclarationRefs) != 1 || plan.ToolDeclarationRefs[0] != "caller-tool" {
		t.Fatal("confirmed acceptance must preserve the caller admission facts")
	}
	assertRejectedAdmissionHasOnlyAcceptance(t, s, realThirdRun)
}

// Only the state read immediately after a confirmed acceptance is faulted. All
// commits, duplicate checks and receipt resolution use the execution store.
type confirmedReloadStore struct {
	coordinator.Store
	store          *execstore.Store
	duplicate      bool
	failNextLoad   bool
	confirmed      bool
	acceptCalls    int
	failedLoads    int
	recoveredLoads int
}

func (s *confirmedReloadStore) Append(ctx context.Context, tr *coordinator.Transition) (coordinator.AppendResult, error) {
	if tr.CommandID != reloadAcceptCommand {
		return appendRealStore(ctx, s.Store, tr)
	}
	s.acceptCalls++
	if !s.duplicate {
		s.store.SetFault(execstore.FaultAfterCommit, errAdapterFault)
		res, err := appendRealStore(ctx, s.Store, tr)
		s.store.SetFault(execstore.FaultAfterCommit, nil)
		return res, err
	}
	if _, err := appendRealStore(ctx, s.Store, tr); err != nil {
		return coordinator.AppendResult{}, err
	}
	res, err := appendRealStore(ctx, s.Store, tr)
	s.confirmed = err == nil && res.Duplicate && res.Receipt.CommandID == reloadAcceptCommand
	s.failNextLoad = s.confirmed
	return res, err
}

func (s *confirmedReloadStore) ResolveCommit(ctx context.Context, commandID string) (coordinator.CommitResolution, codec.Receipt, error) {
	status, receipt, err := s.Store.ResolveCommit(ctx, commandID)
	if err != nil {
		return status, receipt, fmt.Errorf("resolve acceptance: %w", err)
	}
	if commandID == reloadAcceptCommand && status == coordinator.CommitResolutionCommitted && receipt.CommandID == commandID {
		s.confirmed = true
		s.failNextLoad = true
	}
	return status, receipt, nil
}

func (s *confirmedReloadStore) LoadRun(ctx context.Context, runID string) (*kernel.State, uint64, error) {
	if runID == realSecondRun && s.failNextLoad {
		s.failNextLoad = false
		s.failedLoads++
		return nil, 0, errAdapterFault
	}
	state, rev, err := s.Store.LoadRun(ctx, runID)
	if err != nil {
		return nil, 0, fmt.Errorf("reload acceptance: %w", err)
	}
	if runID == realSecondRun && s.failedLoads > 0 && state.Accepted && !state.Admitted {
		s.recoveredLoads++
	}
	return state, rev, nil
}
