package interaction

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestAnOldRequestCannotBeAnsweredAfterRestart(t *testing.T) {
	t.Parallel()
	sharedGen := ids.NewSequence("inc")
	ownerBeforeRestart, err := NewOwner(Config{Clock: clock.NewFake(startTime()), IDs: sharedGen, Responder: NopResponder{}})
	if err != nil {
		t.Fatal(err)
	}
	pendingBeforeRestart := mustEnsure(t, ownerBeforeRestart, confirmSpec("op-1"))
	approvedBeforeRestart := mustEnsure(t, ownerBeforeRestart, confirmSpec("op-2"))
	answeredBeforeRestart := mustAnswer(t, ownerBeforeRestart, Decision{
		Incarnation: pendingBeforeRestart.Incarnation,
		RequestID:   approvedBeforeRestart.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})

	// Simulate a process restart by constructing a new Owner instance.
	ownerAfterRestart, err := NewOwner(Config{Clock: clock.NewFake(startTime()), IDs: sharedGen, Responder: NopResponder{}})
	if err != nil {
		t.Fatal(err)
	}
	if ownerAfterRestart.Incarnation() == ownerBeforeRestart.Incarnation() {
		t.Fatalf("incarnations collided: %q", ownerAfterRestart.Incarnation())
	}

	// Answering with the old incarnation is rejected as stale.
	_, err = ownerAfterRestart.Answer(Decision{
		Incarnation: pendingBeforeRestart.Incarnation,
		RequestID:   pendingBeforeRestart.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	// Answering without an explicit incarnation still cannot find the old request in memory.
	_, err = ownerAfterRestart.Answer(Decision{
		RequestID: pendingBeforeRestart.RequestID,
		Outcome:   OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.NotFound)

	// A receipt issued before the restart does not exist in the new incarnation.
	b := defaultBinding()
	_, err = ownerAfterRestart.ClaimReceipt(answeredBeforeRestart.ReceiptID, &b)
	wantCode(t, err, errcode.NotFound)
}

func TestADuplicateAnswerIsRejected(t *testing.T) {
	t.Parallel()
	owner, _, _, audits := newTestOwner(t)
	ticket := mustEnsure(t, owner, confirmSpec("op-dup"))
	resolved := mustAnswer(t, owner, Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if !resolved.Resolved || resolved.Outcome != OutcomeAllowedOnce || resolved.ReceiptID == "" {
		t.Fatalf("resolved ticket = %+v", resolved)
	}

	// A second answer with the same or a different outcome is rejected.
	_, err := owner.Answer(Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	_, err = owner.Answer(Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeDenied,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	if len(*audits) != 2 {
		t.Fatalf("audits = %d, want 2 (opened, answered)", len(*audits))
	}
}

func TestEnsureReadsBackByOperationIDAndRejectsConflictingSpecs(t *testing.T) {
	t.Parallel()
	owner, _, responder, _ := newTestOwner(t)
	first := mustEnsure(t, owner, confirmSpec("op-readback"))
	second := mustEnsure(t, owner, confirmSpec("op-readback"))
	if diff := cmp.Diff(first, second); diff != "" {
		t.Fatalf("read-back ticket differed (-first +second):\n%s", diff)
	}
	if got := responder.count(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}

	// Changing the kind, binding or options under the same operationID is rejected.
	_, err := owner.Ensure(selectSpec("op-readback"))
	wantCode(t, err, errcode.AlreadyExists)

	conflictBinding := confirmSpec("op-readback")
	conflictBinding.Binding.ArgsDigest = "sha256:different"
	_, err = owner.Ensure(conflictBinding)
	wantCode(t, err, errcode.AlreadyExists)

	selFirst := mustEnsure(t, owner, selectSpec("op-select-readback"))
	selSecond := mustEnsure(t, owner, selectSpec("op-select-readback"))
	if selFirst.RequestID != selSecond.RequestID {
		t.Fatalf("select readback IDs = %q vs %q", selFirst.RequestID, selSecond.RequestID)
	}

	conflictOpts := selectSpec("op-select-readback")
	conflictOpts.Options[1].Label = "Changed label"
	_, err = owner.Ensure(conflictOpts)
	wantCode(t, err, errcode.AlreadyExists)
}

func TestImportantRequestsAlwaysAskAndRefuseSessionGrants(t *testing.T) {
	t.Parallel()
	owner, _, responder, _ := newTestOwner(t)

	devTicket := mustEnsure(t, owner, confirmSpec("op-dev-1"))
	if !devTicket.AllowSessionOffered {
		t.Fatal("expected AllowSessionOffered for non-important dev request")
	}
	mustAnswer(t, owner, Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   devTicket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})

	devSpec2 := confirmSpec("op-dev-2")
	devSpec2.Binding.AttemptID = "att-2"
	autoTicket := mustEnsure(t, owner, devSpec2)
	if !autoTicket.Resolved || autoTicket.Outcome != OutcomeAllowedSession || autoTicket.ReceiptID == "" {
		t.Fatalf("expected automatic session grant resolution, got %+v", autoTicket)
	}
	if got := responder.count(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}

	assertImportantRequestRefusesSessionGrant(t, owner)
}

func assertImportantRequestRefusesSessionGrant(t *testing.T, owner *Owner) {
	t.Helper()
	importantSpec := confirmSpec("op-important")
	importantSpec.Important = true
	importantTicket := mustEnsure(t, owner, importantSpec)
	if importantTicket.Resolved || importantTicket.AllowSessionOffered {
		t.Fatalf("important request must ask and not offer session grant: %+v", importantTicket)
	}
	_, err := owner.Answer(Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   importantTicket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})
	wantCode(t, err, errcode.InvalidArgument)

	for _, tag := range []string{"prod", "unknown-env"} {
		spec := confirmSpec("op-env-" + tag)
		spec.Binding.EnvTag = tag
		ticket := mustEnsure(t, owner, spec)
		if ticket.Resolved || ticket.AllowSessionOffered {
			t.Fatalf("env %q must ask and refuse session grant: %+v", tag, ticket)
		}
	}
}

func TestNilResponderAndHeadlessFailClosed(t *testing.T) {
	t.Parallel()
	fakeClock := clock.NewFake(startTime())

	nilOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("nil"), Responder: nil})
	if err != nil {
		t.Fatal(err)
	}
	t1 := mustEnsure(t, nilOwner, confirmSpec("op-nil"))
	if !t1.Resolved || t1.Outcome != OutcomeUnavailable || t1.ReceiptID != "" {
		t.Fatalf("nil responder ticket = %+v, want unavailable without receipt", t1)
	}

	headlessOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("hl"), Responder: NopResponder{}, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t2 := mustEnsure(t, headlessOwner, confirmSpec("op-headless"))
	if !t2.Resolved || t2.Outcome != OutcomeUnavailable {
		t.Fatalf("headless ticket = %+v, want unavailable", t2)
	}

	failingResponder := &recordingResponder{err: errcode.New(errcode.Unavailable, "ui disconnected")}
	brokenOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("broken"), Responder: failingResponder})
	if err != nil {
		t.Fatal(err)
	}
	_, err = brokenOwner.Ensure(confirmSpec("op-broken"))
	wantCode(t, err, errcode.Unavailable)
	readBack, err := brokenOwner.LookupOperation("op-broken")
	if err != nil || !readBack.Resolved || readBack.Outcome != OutcomeUnavailable {
		t.Fatalf("broken prompt readback = (%+v, %v), want unavailable", readBack, err)
	}
}

func TestPendingOrderingAndExpiry(t *testing.T) {
	t.Parallel()
	owner, fakeClock, _, _ := newTestOwner(t)

	specA := confirmSpec("op-a")
	specA.ExpiresAt = startTime().Add(5 * time.Minute)
	ticketA := mustEnsure(t, owner, specA)
	ticketB := mustEnsure(t, owner, confirmSpec("op-b"))

	fakeClock.Advance(time.Minute)
	ticketC := mustEnsure(t, owner, confirmSpec("op-c"))

	pending := owner.Pending()
	if len(pending) != 3 || pending[0].RequestID != ticketA.RequestID || pending[1].RequestID != ticketB.RequestID || pending[2].RequestID != ticketC.RequestID {
		t.Fatalf("unexpected pending list: %+v", pending)
	}

	// Advance past specA's deadline.
	fakeClock.Advance(5 * time.Minute)
	_, err := owner.Answer(Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticketA.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.DeadlineExceeded)

	lookedUp, err := owner.LookupRequest(ticketA.RequestID)
	if err != nil || !lookedUp.Resolved || lookedUp.Outcome != OutcomeExpired {
		t.Fatalf("expired ticket = (%+v, %v)", lookedUp, err)
	}
	if got := len(owner.Pending()); got != 2 {
		t.Fatalf("pending after expiry = %d, want 2", got)
	}

	alreadyExpired := confirmSpec("op-already-expired")
	alreadyExpired.ExpiresAt = fakeClock.Now()
	expiredTicket := mustEnsure(t, owner, alreadyExpired)
	if !expiredTicket.Resolved || expiredTicket.Outcome != OutcomeExpired {
		t.Fatalf("already-expired ticket = %+v", expiredTicket)
	}
}
