package coordinator

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func acceptCommand(r *Request) *kernel.AcceptRunCommand {
	cmd, ok := r.Command.(*kernel.AcceptRunCommand)
	if !ok {
		return nil
	}
	return cmd
}

func idleAdmission() Admission {
	return Admission{
		SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1,
		ContextEpoch: 3, ContextRevision: 4, ContextStartRunID: "run-start",
	}
}

func TestAcceptRunWithoutCallerDataIsRejected(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Request){
		"input ref":          func(r *Request) { acceptCommand(r).InputRef = "" },
		"queue item ref":     func(r *Request) { acceptCommand(r).QueueItemRef = "" },
		"protocol binding":   func(r *Request) { acceptCommand(r).ProtocolBinding = "" },
		"queue decision ref": func(r *Request) { r.QueueDecisionRef = "" },
		"model plan":         func(r *Request) { r.Facts.ModelPlan = nil },
		"foreign session":    func(r *Request) { acceptCommand(r).SessionID = "other-session" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			store.admission = idleAdmission()
			ctx, sess := startTestSession(t, store, newFakeExecutor(), "incomplete-accept", 1)
			defer sess.Stop()

			req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
			mutate(&req)
			if _, err := sess.Submit(ctx, &req); !errors.Is(err, ErrInvalidAccept) {
				t.Fatalf("expected ErrInvalidAccept, got %v", err)
			}
			if count := store.appendCount(); count != 0 {
				t.Fatalf("an incomplete AcceptRun appended %d transitions", count)
			}
		})
	}
}

func TestAdmissionRecordsCallerFactsAndTheAdmissionRowContext(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "admission-facts", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m-caller", "a-caller")
	req.QueueDecisionRef = "decision-from-caller"
	req.Facts.ModelPlan.ModelRef = "model-from-caller"
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}

	admit := admitTransition(store)
	if admit == nil {
		t.Fatal("the run was not admitted")
	}
	admitted, ok := admit.Events[0].(*kernel.RunAdmittedEvent)
	if !ok || admitted.QueueDecisionRef != "decision-from-caller" ||
		admitted.ContextEpoch != 3 || admitted.InitialContextRevision != 4 || admitted.StartRunID != "run-start" {
		t.Fatalf("admission evidence %+v does not come from the request and the admission row", admitted)
	}
	planned, ok := admit.Events[len(admit.Events)-1].(*kernel.ModelRequestPlannedEvent)
	if !ok || planned.InvocationID != "m-caller" || planned.FirstAttemptID != "a-caller" ||
		planned.ModelRef != "model-from-caller" {
		t.Fatalf("the first step is not the caller's plan: %+v", planned)
	}
}

func admitTransition(store *fakeStore) *Transition {
	for _, tr := range store.transitionList() {
		if tr.Kind == testKindAdmitRun {
			return tr
		}
	}
	return nil
}

func requireAdmittedContext(t *testing.T, tr *Transition, epoch, revision uint64, startRunID string) {
	t.Helper()
	admitted, ok := tr.Events[0].(*kernel.RunAdmittedEvent)
	if !ok {
		t.Fatalf("first event of %s is %T, want RunAdmitted", tr.RunID, tr.Events[0])
	}
	if admitted.ContextEpoch != epoch || admitted.InitialContextRevision != revision || admitted.StartRunID != startRunID {
		t.Fatalf("run %s admitted with epoch %d revision %d start %q, want %d %d %q",
			tr.RunID, admitted.ContextEpoch, admitted.InitialContextRevision, admitted.StartRunID, epoch, revision, startRunID)
	}
}

func TestFreshSessionAdmitsItsFirstRunAtEpochOneRevisionOne(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, OwnerIncarnation: 1, Revision: 1, QueueRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "fresh-session", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	admit := admitTransition(store)
	if admit == nil {
		t.Fatal("the first run of a session with no history was not admitted")
	}
	requireAdmittedContext(t, admit, 1, 1, testRunID1)
}

// execstore creates the admission row with the first AcceptRun: no owner, no
// token, and context epoch and revision still 0 (insertInitialAdmission).
func TestAdmissionRowAsExecstoreCreatesItAdmitsTheFirstRun(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = Admission{SessionID: testSessionID, Revision: 1, QueueRevision: 1}
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "execstore-row", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if sess.admission.ActiveRunID != testRunID1 {
		t.Fatalf("the first run stayed queued behind a zero context row: active=%q", sess.admission.ActiveRunID)
	}
}

func TestRowWithContextHistoryKeepsItsEpochRevisionAndStart(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "history-row", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	requireAdmittedContext(t, admitTransition(store), 3, 4, "run-start")
}

func TestRowWithEpochButNoStartRunKeepsTheRunQueued(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.admission = idleAdmission()
	store.admission.ContextStartRunID = ""
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "no-start-run", 1)
	defer sess.Stop()

	req := acceptRunRequest(testRunID1, testCmdAccept1, "m1", "a1")
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if count := store.appendCount(); count != 1 {
		t.Fatalf("only the acceptance may be appended when the epoch's start run is unknown, got %d appends", count)
	}
	if sess.queue.empty() {
		t.Fatal("the run must stay queued")
	}
}

func TestReservationRefsAreUniqueAcrossSlotSets(t *testing.T) {
	t.Parallel()

	clk := clock.NewFake(testNow())
	first := newSlots(2, ids.NewUUIDv7(clk, ids.NewDeterministic("incarnation-1")))
	second := newSlots(2, ids.NewUUIDv7(clk, ids.NewDeterministic("incarnation-2")))

	seen := make(map[string]bool)
	for _, set := range []*slots{first, second} {
		for range 2 {
			ref, err := set.reserve()
			if err != nil {
				t.Fatalf("reserve: %v", err)
			}
			if seen[ref] {
				t.Fatalf("reservation ref %q was handed out twice", ref)
			}
			seen[ref] = true
		}
	}
}
