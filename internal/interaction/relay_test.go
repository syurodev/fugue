package interaction

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestRelayedVendorOptionsRoundTripVerbatimWithoutGrantOrReceipt(t *testing.T) {
	t.Parallel()
	owner, _, responder, _ := newTestOwner(t)
	spec := relayedSpec(t, "turn-1", "req-1")
	ticket := mustEnsure(t, owner, spec)
	if ticket.Resolved {
		t.Fatalf("relayed request must wait: %+v", ticket)
	}
	if diff := cmp.Diff(vendorOptions(), ticket.Options); diff != "" {
		t.Fatalf("vendor options not verbatim (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]Outcome{OutcomeAnswered, OutcomeDenied}, ticket.Offers); diff != "" {
		t.Fatalf("relayed offers (-want +got):\n%s", diff)
	}
	if got := responder.count(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}

	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAlways,
	})
	if answered.Outcome != OutcomeAnswered || answered.SelectedOptionID != vendorOptionAlways {
		t.Fatalf("relayed answer = %+v", answered)
	}
	// No receipt and no grant is ever created for a relayed request.
	if answered.ReceiptID != "" || len(owner.receipts) != 0 || len(owner.grants) != 0 {
		t.Fatalf("relayed answer created state: receipt %q, receipts %d, grants %d",
			answered.ReceiptID, len(owner.receipts), len(owner.grants))
	}

	// The vendor's "always allow" never becomes a Fugue session grant: the next
	// dev confirm offering allowed_session must still ask.
	next := mustEnsure(t, owner, confirmSpec("op-after-relay"))
	if next.Resolved {
		t.Fatalf("relay answer must not create a session grant: %+v", next)
	}
}

func TestARelayedRequestIsAnsweredAtMostOnce(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, relayedSpec(t, "turn-once", "req-1"))

	first := &Decision{Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeAnswered, OptionID: vendorOptionAllow}
	second := &Decision{Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeAnswered, OptionID: vendorOptionDeny}

	successes := make(chan struct{}, 2)
	ctx := t.Context()
	tasks := make([]*supervise.Task, 0, 2)
	for _, decision := range []*Decision{first, second} {
		d := decision
		tasks = append(tasks, supervise.Go(ctx, "test.relay-answer", func(_ context.Context) error {
			if _, err := owner.Answer(d); err == nil {
				successes <- struct{}{}
			}
			return nil
		}))
	}
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatalf("relay answer goroutine: %v", err)
		}
	}
	close(successes)

	if got := len(successes); got != 1 {
		t.Fatalf("successful answers = %d, want 1", got)
	}

	// Any later answer is rejected as well.
	_, err := owner.Answer(&Decision{Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeDenied})
	wantCode(t, err, errcode.FailedPrecondition)

	resolved, err := owner.LookupRequest(ticket.RequestID)
	if err != nil || !resolved.Resolved {
		t.Fatalf("relayed ticket after concurrent answers = (%+v, %v)", resolved, err)
	}
	if resolved.SelectedOptionID != vendorOptionAllow && resolved.SelectedOptionID != vendorOptionDeny {
		t.Fatalf("winning option = %q, want one of the vendor options", resolved.SelectedOptionID)
	}
}

func TestARelayedQuestionKeepsItsStructureAndIssuesNoReceipt(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	spec := relayedQuestionSpec(t, "turn-q", "req-1")
	ticket := mustEnsure(t, owner, spec)
	if diff := cmp.Diff(spec.Questions, ticket.Questions); diff != "" {
		t.Fatalf("relayed questions not verbatim (-want +got):\n%s", diff)
	}
	if ticket.Body != spec.Body {
		t.Fatalf("relayed body = %q, want %q", ticket.Body, spec.Body)
	}

	want := []Answer{{QuestionID: "db", OptionIDs: []string{optionDBLocal}}}
	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers:     want,
	})
	if answered.Outcome != OutcomeAnswered || answered.ReceiptID != "" {
		t.Fatalf("relayed question answer = %+v, want answered without receipt", answered)
	}
	if diff := cmp.Diff(want, answered.Answers); diff != "" {
		t.Fatalf("relayed answers round trip (-want +got):\n%s", diff)
	}
	// No receipt is ever created: the answers live on the resolved ticket only.
	if len(owner.receipts) != 0 {
		t.Fatalf("relayed question created %d receipts", len(owner.receipts))
	}

	// Unoffered options and oversized answer text are rejected by the shared
	// question validation.
	invalidTicket := mustEnsure(t, owner, relayedQuestionSpec(t, "turn-q", "req-2"))
	_, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   invalidTicket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers:     []Answer{{QuestionID: "db", OptionIDs: []string{questionUnofferedID}}},
	})
	wantCode(t, err, errcode.InvalidArgument)
	if invalidTicket.Resolved {
		t.Fatalf("failed relayed answers must leave the request pending: %+v", invalidTicket)
	}

	// The Fugue-level deny with a note is still available.
	deniedTicket := mustEnsure(t, owner, relayedQuestionSpec(t, "turn-q", "req-3"))
	denied := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   deniedTicket.RequestID,
		Outcome:     OutcomeDenied,
		Text:        relayDenyNote,
	})
	if denied.Outcome != OutcomeDenied || denied.Text != relayDenyNote || denied.ReceiptID != "" {
		t.Fatalf("relayed question denial = %+v", denied)
	}
}

func TestTakeRelayedDeliversAResolvedRelayedRequestOnce(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, relayedSpec(t, "turn-take", "req-1"))

	// A pending request is not deliverable.
	_, err := owner.TakeRelayed(ticket.OperationID)
	wantCode(t, err, errcode.FailedPrecondition)

	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})

	delivered, err := owner.TakeRelayed(ticket.OperationID)
	if err != nil || delivered.Outcome != OutcomeAnswered || delivered.SelectedOptionID != vendorOptionAllow {
		t.Fatalf("delivered relayed ticket = (%+v, %v)", delivered, err)
	}

	// The second take and the lookups fail: the content is gone from memory.
	_, err = owner.TakeRelayed(ticket.OperationID)
	wantCode(t, err, errcode.NotFound)
	if _, err = owner.LookupOperation(ticket.OperationID); err == nil {
		t.Fatal("LookupOperation succeeded after delivery")
	}
	if _, err = owner.LookupRequest(ticket.RequestID); err == nil {
		t.Fatal("LookupRequest succeeded after delivery")
	}
	if got := len(owner.Pending()); got != 0 {
		t.Fatalf("pending after delivery = %d, want 0", got)
	}

	// Native requests are not deliverable through TakeRelayed.
	native := mustEnsure(t, owner, confirmSpec("op-take-native"))
	_, err = owner.TakeRelayed(native.OperationID)
	wantCode(t, err, errcode.FailedPrecondition)
}

func TestRelayedSpecsRequireVendorShapedBindingsAndTheirVendorKey(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)

	// A relayed spec with native binding fields is rejected.
	nativeShaped := relayedSpec(t, "turn-bind", "req-1")
	nativeShaped.Binding = defaultBinding()
	_, _, err := owner.Ensure(nativeShaped)
	wantCode(t, err, errcode.InvalidArgument)

	// A native spec with vendor binding fields is rejected.
	vendorShaped := confirmSpec("op-native-vendor-binding")
	vendorShaped.Binding.TurnID = "turn-1"
	_, _, err = owner.Ensure(vendorShaped)
	wantCode(t, err, errcode.InvalidArgument)

	// A relayed spec whose operation ID is not its vendor key is rejected.
	mismatched := relayedSpec(t, "turn-bind", "req-2")
	mismatched.OperationID = "vendor:other-session:turn:req"
	_, _, err = owner.Ensure(mismatched)
	wantCode(t, err, errcode.InvalidArgument)
}

func TestARelayedDenialDeliversTheNote(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, relayedSpec(t, "turn-deny", "req-1"))
	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeDenied,
		Text:        relayDenyNote,
	})
	if answered.Outcome != OutcomeDenied || answered.Text != relayDenyNote || answered.SelectedOptionID != "" {
		t.Fatalf("relayed denial = %+v", answered)
	}
	delivered, err := owner.LookupRequest(ticket.RequestID)
	if err != nil || delivered.Text != relayDenyNote {
		t.Fatalf("delivered note = (%+v, %v)", delivered, err)
	}
}

func TestRelayedAnswersRejectUnofferedOptionsAndSessionGrants(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, relayedSpec(t, "turn-invalid", "req-1"))
	requestID := ticket.RequestID

	_, err := owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAnswered, OptionID: "never"})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAnswered})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAllowedSession, OptionID: vendorOptionAllow})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeDenied, OptionID: vendorOptionAllow})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeCancelled})
	wantCode(t, err, errcode.InvalidArgument)
	if ticket.Resolved {
		t.Fatalf("failed relayed answers must leave the request pending: %+v", ticket)
	}
}

func TestOperationKeysHaveNeutralFormats(t *testing.T) {
	t.Parallel()
	if native, err := NativeOperationKey("inv-1", "att-2", 3); err != nil || native != "native:inv-1:att-2:3" {
		t.Fatalf("native key = (%q, %v)", native, err)
	}
	if vendor, err := VendorOperationKey("sess-1", "turn-7", "req-9"); err != nil || vendor != "vendor:sess-1:turn-7:req-9" {
		t.Fatalf("vendor key = (%q, %v)", vendor, err)
	}
	if key, err := NativeOperationKey("", "att", 1); err == nil {
		t.Fatalf("empty invocation accepted: %q", key)
	}
	if key, err := NativeOperationKey("inv", "att", 0); err == nil {
		t.Fatalf("zero generation accepted: %q", key)
	}
	if key, err := VendorOperationKey("sess", "", "req"); err == nil {
		t.Fatalf("empty turn accepted: %q", key)
	}
	if key, err := VendorOperationKey("sess", "turn", "re:req"); err == nil {
		t.Fatalf("separator in component accepted: %q", key)
	}

	// A relayed request's operation ID is its vendor key.
	owner, _, _, _ := newTestOwner(t)
	key := mustVendorKey(t, "turn-1", "req-1")
	if ensured := mustEnsure(t, owner, relayedSpec(t, "turn-1", "req-1")); ensured.OperationID != key {
		t.Fatalf("operation ID = %q, want %q", ensured.OperationID, key)
	}
}
