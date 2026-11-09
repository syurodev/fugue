package interaction

import (
	"slices"
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
	approvedSpec := confirmSpec("op-2")
	approvedSpec.Binding.SessionID = session2
	approvedBeforeRestart := mustEnsure(t, ownerBeforeRestart, approvedSpec)
	answeredBeforeRestart := mustAnswer(t, ownerBeforeRestart, &Decision{
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
	_, err = ownerAfterRestart.Answer(&Decision{
		Incarnation: pendingBeforeRestart.Incarnation,
		RequestID:   pendingBeforeRestart.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	// Answering without an explicit incarnation still cannot find the old request in memory.
	_, err = ownerAfterRestart.Answer(&Decision{
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
	resolved := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if !resolved.Resolved || resolved.Outcome != OutcomeAllowedOnce || resolved.ReceiptID == "" {
		t.Fatalf("resolved ticket = %+v", resolved)
	}

	// A second answer with the same or a different outcome is rejected.
	_, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	_, err = owner.Answer(&Decision{
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

	// Changing the kind, binding, offers or options under the same operationID is rejected.
	_, _, err := owner.Ensure(selectSpec("op-readback"))
	wantCode(t, err, errcode.AlreadyExists)

	conflictBinding := confirmSpec("op-readback")
	conflictBinding.Binding.ArgsDigest = "sha256:different"
	_, _, err = owner.Ensure(conflictBinding)
	wantCode(t, err, errcode.AlreadyExists)

	conflictOffers := confirmSpec("op-readback")
	conflictOffers.Offers = []Outcome{OutcomeAllowedOnce}
	_, _, err = owner.Ensure(conflictOffers)
	wantCode(t, err, errcode.AlreadyExists)

	conflictSummary := confirmSpec("op-readback")
	conflictSummary.Summary = "Write other.go"
	_, _, err = owner.Ensure(conflictSummary)
	wantCode(t, err, errcode.AlreadyExists)

	relayFirst := mustEnsure(t, owner, relayedSpec(t, "turn-readback", "req-1"))
	relaySecond := mustEnsure(t, owner, relayedSpec(t, "turn-readback", "req-1"))
	if diff := cmp.Diff(relayFirst, relaySecond); diff != "" {
		t.Fatalf("relayed read-back ticket differed (-first +second):\n%s", diff)
	}

	conflictRelay := relayedSpec(t, "turn-readback", "req-1")
	conflictRelay.Options = vendorOptions()[:2]
	_, _, err = owner.Ensure(conflictRelay)
	wantCode(t, err, errcode.AlreadyExists)
}

func TestEnvTagHardRuleAndOffersControlSessionGrants(t *testing.T) {
	t.Parallel()
	owner, _, responder, _ := newTestOwner(t)

	devTicket := mustEnsure(t, owner, confirmSpec("op-dev-1"))
	if !slices.Contains(devTicket.Offers, OutcomeAllowedSession) {
		t.Fatalf("offers = %v, want the session grant offered for a dev confirm", devTicket.Offers)
	}
	mustAnswer(t, owner, &Decision{
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

	// A confirm that does not offer allowed_session never grants.
	onceSpec := confirmSpec("op-once")
	onceSpec.Offers = []Outcome{OutcomeAllowedOnce}
	onceTicket := mustEnsure(t, owner, onceSpec)
	if slices.Contains(onceTicket.Offers, OutcomeAllowedSession) {
		t.Fatalf("offers = %v, want no session grant", onceTicket.Offers)
	}
	_, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   onceTicket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})
	wantCode(t, err, errcode.InvalidArgument)

	// The hard rule refuses a session grant offer up front and, with only the
	// single-use offer, still asks every time for prod and unknown tags.
	for _, tag := range []string{"prod", "unknown-env"} {
		offered := confirmSpec("op-env-offered-" + tag)
		offered.Binding.EnvTag = tag
		_, _, err := owner.Ensure(offered)
		wantCode(t, err, errcode.InvalidArgument)

		spec := confirmSpec("op-env-" + tag)
		spec.Binding.EnvTag = tag
		spec.Binding.SessionID = "sess-env-" + tag
		spec.Offers = []Outcome{OutcomeAllowedOnce}
		ticket := mustEnsure(t, owner, spec)
		if ticket.Resolved || !slices.Equal(ticket.Offers, []Outcome{OutcomeAllowedOnce}) {
			t.Fatalf("env %q must ask with only allowed_once: %+v", tag, ticket)
		}
		_, err = owner.Answer(&Decision{
			Incarnation: owner.Incarnation(),
			RequestID:   ticket.RequestID,
			Outcome:     OutcomeAllowedSession,
		})
		wantCode(t, err, errcode.InvalidArgument)
	}
}

// TestOnlyHeadlessResolvesUnavailableWhileNilResponderWaits states the I-07
// contract of interaction §4.2: a nil Responder no longer fails closed and
// only a host configured Headless answers unavailable. It replaces
// TestNilResponderAndHeadlessFailClosed.
func TestOnlyHeadlessResolvesUnavailableWhileNilResponderWaits(t *testing.T) {
	t.Parallel()
	fakeClock := clock.NewFake(startTime())

	nilOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("nil"), Responder: nil})
	if err != nil {
		t.Fatal(err)
	}
	assertRequestsWaitWithoutResponder(t, nilOwner)

	headlessOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("hl"), Responder: NopResponder{}, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	assertHeadlessResolvesUnavailable(t, headlessOwner)

	failingResponder := &recordingResponder{err: errcode.New(errcode.Unavailable, "ui disconnected")}
	brokenOwner, err := NewOwner(Config{Clock: fakeClock, IDs: ids.NewSequence("broken"), Responder: failingResponder})
	if err != nil {
		t.Fatal(err)
	}
	assertBrokenPromptResolvesUnavailable(t, brokenOwner)
}

func assertRequestsWaitWithoutResponder(t *testing.T, owner *Owner) {
	t.Helper()
	t1 := mustEnsure(t, owner, confirmSpec("op-nil"))
	if t1.Resolved || t1.Outcome != "" || t1.ReceiptID != "" {
		t.Fatalf("nil responder ticket = %+v, want pending without receipt", t1)
	}
	r1 := mustEnsure(t, owner, relayedSpec(t, "turn-nil", "req-1"))
	if r1.Resolved {
		t.Fatalf("relayed request with nil responder = %+v, want pending", r1)
	}
	qSpec := questionSpec("op-nil-question")
	qSpec.Binding.SessionID = session2
	if q1 := mustEnsure(t, owner, qSpec); q1.Resolved {
		t.Fatalf("question with nil responder = %+v, want pending", q1)
	}
}

func assertHeadlessResolvesUnavailable(t *testing.T, owner *Owner) {
	t.Helper()
	t2 := mustEnsure(t, owner, confirmSpec("op-headless"))
	if !t2.Resolved || t2.Outcome != OutcomeUnavailable {
		t.Fatalf("headless ticket = %+v, want unavailable", t2)
	}
	r2 := mustEnsure(t, owner, relayedSpec(t, "turn-headless", "req-1"))
	if !r2.Resolved || r2.Outcome != OutcomeUnavailable {
		t.Fatalf("headless relayed ticket = %+v, want unavailable", r2)
	}
}

func assertBrokenPromptResolvesUnavailable(t *testing.T, owner *Owner) {
	t.Helper()
	_, _, err := owner.Ensure(confirmSpec("op-broken"))
	wantCode(t, err, errcode.Unavailable)
	readBack, err := owner.LookupOperation("op-broken")
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
	ticketB := mustEnsure(t, owner, relayedSpec(t, "turn-b", "req-1"))

	fakeClock.Advance(time.Minute)
	specC := confirmSpec("op-c")
	specC.Binding.SessionID = "sess-2"
	ticketC := mustEnsure(t, owner, specC)

	pending := owner.Pending()
	if len(pending) != 3 || pending[0].RequestID != ticketA.RequestID || pending[1].RequestID != ticketB.RequestID || pending[2].RequestID != ticketC.RequestID {
		t.Fatalf("unexpected pending list: %+v", pending)
	}

	// Advance past specA's deadline.
	fakeClock.Advance(5 * time.Minute)
	_, err := owner.Answer(&Decision{
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
