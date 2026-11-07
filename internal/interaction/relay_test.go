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
	spec := relayedSpec("op-relay")
	ticket := mustEnsure(t, owner, spec)
	if ticket.Resolved {
		t.Fatalf("relayed request must wait: %+v", ticket)
	}
	if diff := cmp.Diff(vendorOptions(), ticket.Options); diff != "" {
		t.Fatalf("vendor options not verbatim (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]Outcome{OutcomeAllowedOnce}, ticket.Offers); diff != "" {
		t.Fatalf("relayed offers (-want +got):\n%s", diff)
	}
	if got := responder.count(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}

	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
		OptionID:    vendorOptionAlways,
	})
	if answered.Outcome != OutcomeAllowedOnce || answered.SelectedOptionID != vendorOptionAlways {
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
	ticket := mustEnsure(t, owner, relayedSpec("op-relay-once"))

	first := &Decision{Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeAllowedOnce, OptionID: vendorOptionAllow}
	second := &Decision{Incarnation: owner.Incarnation(), RequestID: ticket.RequestID, Outcome: OutcomeAllowedOnce, OptionID: vendorOptionDeny}

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

func TestARelayedDenialDeliversTheNote(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, relayedSpec("op-relay-deny"))
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
	ticket := mustEnsure(t, owner, relayedSpec("op-relay-invalid"))
	requestID := ticket.RequestID

	_, err := owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAllowedOnce, OptionID: "never"})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAllowedOnce})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAllowedSession, OptionID: vendorOptionAllow})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeDenied, OptionID: vendorOptionAllow})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{RequestID: requestID, Outcome: OutcomeAnswered})
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

	// A constructed vendor key works as a relayed request's operation ID.
	owner, _, _, _ := newTestOwner(t)
	key, keyErr := VendorOperationKey(testSessionID, "turn-1", "req-1")
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	if ensured := mustEnsure(t, owner, relayedSpec(key)); ensured.OperationID != key {
		t.Fatalf("operation ID = %q, want %q", ensured.OperationID, key)
	}
}
