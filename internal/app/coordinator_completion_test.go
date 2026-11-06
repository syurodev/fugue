package app

import (
	"testing"

	"github.com/syurodev/fugue/internal/execstore"
)

func TestFreshAdmissionCompletesScriptedResponse(t *testing.T) {
	t.Parallel()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	executor := newRealExecutor(true)
	sess := startRealSessionWith(t, NewCoordinatorStore(s), clk, "fresh", 1, executor)
	submitRealAccept(t, sess, realFirstRun)
	ctx := t.Context()
	if _, err := sess.Submit(ctx, realDispatch(realFirstRun)); err != nil {
		t.Fatalf("fresh admission must dispatch normally: %v", err)
	}
	waitRealWork(t, ctx, executor)
	obs := waitRealObservation(t, ctx, executor)
	if reply, err := sess.SubmitObservation(ctx, obs); err != nil || !reply.Duplicate {
		t.Fatalf("fresh admission must commit the scripted response before its duplicate: err=%v duplicate=%t", err, reply.Duplicate)
	}
	sess.Stop()
	assertFinishedResponse(t, s, realFirstRun)
	if executor.calls.Load() != 1 {
		t.Fatal("one fresh run must hand off exactly one Work")
	}
}

func assertFinishedResponse(t *testing.T, s *execstore.Store, runID string) {
	t.Helper()
	receipt, found, err := s.LookupReceipt(t.Context(), "response-"+runID)
	if err != nil || !found || receipt.CommandID != "response-"+runID {
		t.Fatal("scripted response must have a durable receipt")
	}
	state, err := s.RebuildRun(t.Context(), runID)
	if err != nil || !state.Finished {
		t.Fatal("scripted response must durably finish the run")
	}
}
