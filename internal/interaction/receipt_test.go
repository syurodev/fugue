package interaction

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestAReceiptCanBeClaimedOnlyOnceAndMatchesItsBinding(t *testing.T) {
	t.Parallel()
	owner, fakeClock, _, _ := newTestOwner(t)
	spec := confirmSpec("op-claim")
	spec.ExpiresAt = startTime().Add(10 * time.Minute)
	ticket := mustEnsure(t, owner, spec)
	answered := mustAnswer(t, owner, Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})

	// Any mismatch in the execution Binding is rejected without consuming the receipt.
	mismatch := defaultBinding()
	mismatch.CheckGeneration = 2
	_, err := owner.ClaimReceipt(answered.ReceiptID, &mismatch)
	wantCode(t, err, errcode.PermissionDenied)

	// Selection claim on a confirm receipt is rejected.
	expected := defaultBinding()
	_, err = owner.ClaimSelection(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.PermissionDenied)

	// Exact binding claims the receipt once.
	claimed, err := owner.ClaimReceipt(answered.ReceiptID, &expected)
	if err != nil || !claimed.Claimed || claimed.ReceiptID != answered.ReceiptID {
		t.Fatalf("claimed receipt = (%+v, %v)", claimed, err)
	}

	// Second claim is rejected.
	_, err = owner.ClaimReceipt(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.FailedPrecondition)

	// Expired receipt cannot be claimed.
	specExp := confirmSpec("op-claim-exp")
	specExp.ExpiresAt = startTime().Add(2 * time.Minute)
	ticketExp := mustEnsure(t, owner, specExp)
	ansExp := mustAnswer(t, owner, Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticketExp.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	fakeClock.Advance(3 * time.Minute)
	_, err = owner.ClaimReceipt(ansExp.ReceiptID, &expected)
	wantCode(t, err, errcode.DeadlineExceeded)
}

func TestSelectionRequestsAcceptOnlyOfferedOptionsAndNeverAuthorizeExecution(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, selectSpec("op-sel"))
	if ticket.AllowSessionOffered {
		t.Fatal("selection request must not offer a session confirmation grant")
	}

	// Reject unoffered option ID, OutcomeAllowedSession and denied-with-option.
	_, err := owner.Answer(Decision{RequestID: ticket.RequestID, Outcome: OutcomeAllowedOnce, OptionID: "db-unknown"})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(Decision{RequestID: ticket.RequestID, Outcome: OutcomeAllowedSession, OptionID: optionDBLocal})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(Decision{RequestID: ticket.RequestID, Outcome: OutcomeDenied, OptionID: optionDBLocal})
	wantCode(t, err, errcode.InvalidArgument)

	answered := mustAnswer(t, owner, Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
		OptionID:    optionDBStage,
	})
	expected := defaultBinding()
	_, err = owner.ClaimReceipt(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.PermissionDenied)

	chosen, err := owner.ClaimSelection(answered.ReceiptID, &expected)
	if err != nil || chosen != optionDBStage {
		t.Fatalf("chosen option = (%q, %v), want %q", chosen, err, optionDBStage)
	}
	_, err = owner.ClaimSelection(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.FailedPrecondition)

	deniedTicket := mustEnsure(t, owner, selectSpec("op-sel-deny"))
	deniedRes := mustAnswer(t, owner, Decision{RequestID: deniedTicket.RequestID, Outcome: OutcomeDenied})
	if deniedRes.Outcome != OutcomeDenied || deniedRes.ReceiptID != "" {
		t.Fatalf("denied selection = %+v", deniedRes)
	}
}

func TestVaultLockInvalidatesPendingRequestsGrantsAndReceipts(t *testing.T) {
	t.Parallel()
	var vaultLocked atomic.Bool
	owner, err := NewOwner(Config{
		Clock:       clock.NewFake(startTime()),
		IDs:         ids.NewSequence("vl"),
		Responder:   NopResponder{},
		VaultLocked: vaultLocked.Load,
	})
	if err != nil {
		t.Fatal(err)
	}

	grantTicket := mustEnsure(t, owner, confirmSpec("op-grant"))
	ansGrant := mustAnswer(t, owner, Decision{RequestID: grantTicket.RequestID, Outcome: OutcomeAllowedSession})
	pendingTicket := mustEnsure(t, owner, selectSpec("op-pending"))

	// Synchronous vaultCheck blocks receipt claims and allowing answers even before SetVaultLocked runs.
	vaultLocked.Store(true)
	expected := defaultBinding()
	_, err = owner.ClaimReceipt(ansGrant.ReceiptID, &expected)
	wantCode(t, err, errcode.FailedPrecondition)
	_, err = owner.Answer(Decision{RequestID: pendingTicket.RequestID, Outcome: OutcomeAllowedOnce, OptionID: optionDBLocal})
	wantCode(t, err, errcode.FailedPrecondition)

	// SetVaultLocked(true) cancels pending requests and wipes unconsumed receipts and session grants.
	owner.SetVaultLocked(true)
	if len(owner.Pending()) != 0 {
		t.Fatalf("pending after vault lock = %d, want 0", len(owner.Pending()))
	}
	lookedUp, err := owner.LookupRequest(pendingTicket.RequestID)
	if err != nil || !lookedUp.Resolved || lookedUp.Outcome != OutcomeCancelled {
		t.Fatalf("pending ticket after vault lock = (%+v, %v), want cancelled", lookedUp, err)
	}

	vaultLocked.Store(false)
	owner.SetVaultLocked(false)
	_, err = owner.ClaimReceipt(ansGrant.ReceiptID, &expected)
	wantCode(t, err, errcode.NotFound)

	afterUnlock := mustEnsure(t, owner, confirmSpec("op-after-unlock"))
	if afterUnlock.Resolved {
		t.Fatalf("session grant survived vault lock: %+v", afterUnlock)
	}
}

func TestInvalidateOperationAndResetSession(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)

	t1 := mustEnsure(t, owner, confirmSpec("op-reset"))
	invalidationID, err := owner.InvalidateOperation("op-reset")
	if err != nil || invalidationID == "" {
		t.Fatalf("InvalidateOperation = (%q, %v)", invalidationID, err)
	}
	oldTicket, err := owner.LookupRequest(t1.RequestID)
	if err != nil || !oldTicket.Resolved || oldTicket.Outcome != OutcomeCancelled {
		t.Fatalf("old ticket after InvalidateOperation = (%+v, %v)", oldTicket, err)
	}

	t2 := mustEnsure(t, owner, confirmSpec("op-reset"))
	ans2 := mustAnswer(t, owner, Decision{RequestID: t2.RequestID, Outcome: OutcomeAllowedOnce})
	if _, invErr := owner.InvalidateOperation("op-reset"); invErr != nil {
		t.Fatal(invErr)
	}
	expected := defaultBinding()
	_, err = owner.ClaimReceipt(ans2.ReceiptID, &expected)
	wantCode(t, err, errcode.NotFound)

	assertResetSessionIsolatesSessions(t, owner)
}

func assertResetSessionIsolatesSessions(t *testing.T, owner *Owner) {
	t.Helper()
	sess1Grant := mustEnsure(t, owner, confirmSpec("op-s1-grant"))
	ansS1 := mustAnswer(t, owner, Decision{RequestID: sess1Grant.RequestID, Outcome: OutcomeAllowedSession})
	sess1Pending := mustEnsure(t, owner, selectSpec("op-s1-pending"))

	otherSpec := confirmSpec("op-s2-pending")
	otherSpec.Binding.SessionID = "sess-2"
	sess2Pending := mustEnsure(t, owner, otherSpec)

	owner.ResetSession(testSessionID)

	expected := defaultBinding()
	_, err := owner.ClaimReceipt(ansS1.ReceiptID, &expected)
	wantCode(t, err, errcode.NotFound)
	s1LookedUp, err := owner.LookupRequest(sess1Pending.RequestID)
	if err != nil || !s1LookedUp.Resolved || s1LookedUp.Outcome != OutcomeCancelled {
		t.Fatalf("sess-1 pending after ResetSession = (%+v, %v)", s1LookedUp, err)
	}
	s2LookedUp, err := owner.LookupRequest(sess2Pending.RequestID)
	if err != nil || s2LookedUp.Resolved {
		t.Fatalf("sess-2 pending should be untouched: (%+v, %v)", s2LookedUp, err)
	}
}

func TestValidationAndGeneratorFailures(t *testing.T) {
	t.Parallel()
	fakeClock := clock.NewFake(startTime())

	_, err := NewOwner(Config{Clock: nil, IDs: ids.NewSequence("x")})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = NewOwner(Config{Clock: fakeClock, IDs: nil})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = NewOwner(Config{Clock: fakeClock, IDs: &failingGenerator{failAfter: 0}})
	wantCode(t, err, errcode.Internal)

	owner, _, _, _ := newTestOwner(t)
	assertSpecValidationErrors(t, owner)
	assertOperationAndDecisionValidationErrors(t, owner)
	assertGeneratorFailures(t, fakeClock)
}

func assertSpecValidationErrors(t *testing.T, owner *Owner) {
	t.Helper()
	badSpecs := []*Spec{
		nil,
		{OperationID: "", Kind: KindConfirm, Binding: defaultBinding()},
		{OperationID: "op", Kind: KindConfirm, Binding: Binding{}},
		{OperationID: "op", Kind: KindConfirm, Options: []Option{{ID: "a", Label: "A"}}, Binding: defaultBinding()},
		{OperationID: "op", Kind: KindSelect, Options: nil, Binding: defaultBinding()},
		{OperationID: "op", Kind: KindSelect, Options: []Option{{ID: "", Label: "A"}}, Binding: defaultBinding()},
		{OperationID: "op", Kind: KindSelect, Options: []Option{{ID: "a", Label: "A"}, {ID: "a", Label: "B"}}, Binding: defaultBinding()},
		{OperationID: "op", Kind: "unknown", Binding: defaultBinding()},
	}
	for _, spec := range badSpecs {
		_, err := owner.Ensure(spec)
		wantCode(t, err, errcode.InvalidArgument)
	}
}

func assertOperationAndDecisionValidationErrors(t *testing.T, owner *Owner) {
	t.Helper()
	_, err := owner.LookupOperation("missing")
	wantCode(t, err, errcode.NotFound)
	_, err = owner.LookupRequest("missing")
	wantCode(t, err, errcode.NotFound)
	_, err = owner.ClaimReceipt("", nil)
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.InvalidateOperation("")
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(Decision{RequestID: ""})
	wantCode(t, err, errcode.InvalidArgument)

	confirmT := mustEnsure(t, owner, confirmSpec("op-invalid-ans"))
	_, err = owner.Answer(Decision{RequestID: confirmT.RequestID, Outcome: OutcomeAllowedOnce, OptionID: "unexpected"})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(Decision{RequestID: confirmT.RequestID, Outcome: OutcomeCancelled})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(Decision{RequestID: confirmT.RequestID, Outcome: "bogus"})
	wantCode(t, err, errcode.InvalidArgument)

	selectT := mustEnsure(t, owner, selectSpec("op-invalid-sel"))
	_, err = owner.Answer(Decision{RequestID: selectT.RequestID, Outcome: "bogus"})
	wantCode(t, err, errcode.InvalidArgument)
}

func assertGeneratorFailures(t *testing.T, fakeClock *clock.Fake) {
	t.Helper()
	o1, err := NewOwner(Config{Clock: fakeClock, IDs: &failingGenerator{failAfter: 1}, Responder: NopResponder{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = o1.Ensure(confirmSpec("op-fail-req-id"))
	wantCode(t, err, errcode.Internal)
	_, err = o1.InvalidateOperation("op-fail-inv-id")
	wantCode(t, err, errcode.Internal)

	o2, err := NewOwner(Config{Clock: fakeClock, IDs: &failingGenerator{failAfter: 2}, Responder: NopResponder{}})
	if err != nil {
		t.Fatal(err)
	}
	tReceiptFail := mustEnsure(t, o2, confirmSpec("op-fail-receipt"))
	_, err = o2.Answer(Decision{RequestID: tReceiptFail.RequestID, Outcome: OutcomeAllowedOnce})
	wantCode(t, err, errcode.Internal)
}
