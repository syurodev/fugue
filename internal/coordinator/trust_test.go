package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
)

const testOriginModel = "model"

func requireRejection(t *testing.T, err error, want kernel.RejectionCode) {
	t.Helper()
	var rej kernel.Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("expected kernel rejection %v, got %T: %v", want, err, err)
	}
	if rej.Code != want {
		t.Fatalf("expected rejection %v, got %v", want, rej.Code)
	}
}

func extendBudgetRequest(commandOrigin, trustedOrigin string) Request {
	return Request{
		Command: &kernel.ExtendBudgetCommand{
			CommandID: "cmd-extend", RunID: testRunID1, Origin: commandOrigin, Token: 1,
			Kind: kernel.BudgetKindSteps, ExtensionAmount: 10, Actor: testOriginUser,
		},
		Origin:    trustedOrigin,
		Kind:      "ExtendBudget",
		Principal: trustedOrigin,
		Target:    testRunID1,
	}
}

func TestCommandOriginCannotForgeBudgetAuthority(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "forged-origin", 1)
	defer sess.Stop()

	forged := extendBudgetRequest(testOriginUser, testOriginModel)
	_, err := sess.Submit(ctx, &forged)
	requireRejection(t, err, kernel.RejectionCodeCommandRejected)
	if count := store.appendCount(); count != 0 {
		t.Fatalf("a forged origin appended %d transitions", count)
	}

	// With the host-set origin the same command passes the authority check and
	// is refused later, because the run is not waiting on a budget.
	genuine := extendBudgetRequest(testOriginModel, testOriginUser)
	_, err = sess.Submit(ctx, &genuine)
	requireRejection(t, err, kernel.RejectionCodeInvalidTransition)
}

func TestSubmitWithoutTrustedOriginIsRejected(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "no-origin", 1)
	defer sess.Stop()

	req := extendBudgetRequest(testOriginUser, "")
	req.Principal = testOriginUser
	_, err := sess.Submit(ctx, &req)
	if !errors.Is(err, ErrOriginRequired) {
		t.Fatalf("expected ErrOriginRequired, got %v", err)
	}
	if count := store.appendCount(); count != 0 {
		t.Fatalf("a request without origin appended %d transitions", count)
	}
}

func startDispatchedSession(t *testing.T, store *fakeStore, seed string) (context.Context, *Session) {
	t.Helper()
	setupDispatchedRun(store, 1)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), seed, 1)
	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-trust")
	req.Facts.ModelDispatch.ExecutorFence = 5
	if _, err := sess.Submit(ctx, &req); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	return ctx, sess
}

func TestWorkerObservationIsAlwaysRecordedAsWorker(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := startDispatchedSession(t, store, "worker-origin")
	defer sess.Stop()
	before := store.appendCount()

	obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "cmd-obs-claims-user", 1, 1, 5)
	if cmd, ok := obs.Command.(*kernel.ObserveModelResponseCommand); ok {
		cmd.Origin = testOriginUser
	}
	if _, err := sess.SubmitObservation(ctx, &obs); err != nil {
		t.Fatalf("observation failed: %v", err)
	}

	store.mu.Lock()
	tx := store.transitions[before]
	store.mu.Unlock()
	if tx.Principal != testOriginWorker {
		t.Fatalf("expected principal %q, got %q", testOriginWorker, tx.Principal)
	}
	for _, ev := range tx.Events {
		if got := ev.EventEnvelope().Origin; got != testOriginWorker {
			t.Fatalf("event %s recorded origin %q, want %q", ev.EventName(), got, testOriginWorker)
		}
	}
}

func TestObservationRejectsCommandsThatAreNotObservations(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	ctx, sess := startDispatchedSession(t, store, "observation-allowlist")
	defer sess.Stop()
	before := store.appendCount()

	commands := map[string]kernel.Command{
		"ExtendBudget": &kernel.ExtendBudgetCommand{
			CommandID: "cmd-x1", RunID: testRunID1, Origin: testOriginUser, Token: 1,
			Kind: kernel.BudgetKindSteps, ExtensionAmount: 10, Actor: testOriginUser,
		},
		"RequestStop": &kernel.RequestStopCommand{
			CommandID: "cmd-x2", RunID: testRunID1, Origin: testOriginUser, Token: 1,
			Reason: kernel.StopReasonBudget, Mode: kernel.StopModeTerminal, Actor: testOriginUser, Target: "steps",
			RequestTime: testNow(),
		},
		"AdvanceRun": &kernel.AdvanceRunCommand{CommandID: "cmd-x3", RunID: testRunID1, Origin: testOriginUser, Token: 1, StepIndex: 2},
	}
	for name, cmd := range commands {
		obs := makeModelResponseObservation(testSessionID, testRunID1, "m1", "a1", "", 1, 1, 5)
		obs.Command = cmd
		_, err := sess.SubmitObservation(ctx, &obs)
		if !errors.Is(err, ErrObservationNotAllowed) {
			t.Fatalf("%s: expected ErrObservationNotAllowed, got %v", name, err)
		}
	}
	if count := store.appendCount(); count != before {
		t.Fatalf("rejected observations appended %d transitions", count-before)
	}
}

func TestMissingDispatchAuthorizationRejectsAndReleasesSlot(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	setupAdmittedRun(store)
	ctx, sess := startTestSession(t, store, newFakeExecutor(), "no-authorization", 1)
	defer sess.Stop()

	req := makeModelDispatchRequest(testRunID1, "m1", "a1", "cmd-disp-noauth")
	req.Facts.DispatchAuthorizationRef = ""
	_, err := sess.Submit(ctx, &req)
	requireRejection(t, err, kernel.RejectionCodeCommandRejected)
	if count := store.appendCount(); count != 0 {
		t.Fatalf("a dispatch without authorization appended %d transitions", count)
	}
	ref, ok := reserveSlot(sess.slots)
	if !ok {
		t.Fatal("the reserved slot was not released")
	}
	if err = sess.slots.release(ref); err != nil {
		t.Fatalf("release: %v", err)
	}
}
