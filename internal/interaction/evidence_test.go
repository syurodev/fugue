package interaction

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// TestIE06VaultLockFailsClosedBetweenAnswerAndClaimOrDelivery is the I-E06
// evidence of interaction §11: a vault lock between Answer and a receipt
// claim fails closed for native requests, and a vault lock before the answer
// or delivery of a relayed request fails closed and invalidates the request.
func TestIE06VaultLockFailsClosedBetweenAnswerAndClaimOrDelivery(t *testing.T) {
	t.Parallel()
	var vaultLocked atomic.Bool
	newOwner := func(t *testing.T) *Owner {
		t.Helper()
		owner, err := NewOwner(&Config{
			Clock:       clock.NewFake(startTime()),
			IDs:         ids.NewSequence("ie06"),
			Responder:   NopResponder{},
			VaultLocked: vaultLocked.Load,
			TagPolicy:   newStubTagPolicy(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return owner
	}

	assertIE06NativeClaimsFailClosed(t, newOwner, &vaultLocked)
	assertIE06NativeAnswersFailClosed(t, newOwner, &vaultLocked)
	assertIE06RelayedDeliveryFailsClosed(t, newOwner, &vaultLocked)
}

// assertIE06NativeClaimsFailClosed locks the vault between Answer and
// ClaimReceipt and expects the claim to fail closed.
func assertIE06NativeClaimsFailClosed(t *testing.T, newOwner func(*testing.T) *Owner, vaultLocked *atomic.Bool) {
	t.Helper()
	native := newOwner(t)
	ticket := mustEnsure(t, native, confirmSpec("op-ie06-native"))
	answered := mustAnswer(t, native, &Decision{
		Incarnation: native.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	expected := defaultBinding()
	vaultLocked.Store(true)
	_, err := native.ClaimReceipt(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.FailedPrecondition)
}

// assertIE06NativeAnswersFailClosed locks the vault before the answer of a
// native request and expects the answer to fail closed.
func assertIE06NativeAnswersFailClosed(t *testing.T, newOwner func(*testing.T) *Owner, vaultLocked *atomic.Bool) {
	t.Helper()
	vaultLocked.Store(false)
	beforeAnswer := newOwner(t)
	pendingNative := mustEnsure(t, beforeAnswer, confirmSpec("op-ie06-native-lock"))
	vaultLocked.Store(true)
	_, err := beforeAnswer.Answer(&Decision{
		Incarnation: beforeAnswer.Incarnation(),
		RequestID:   pendingNative.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)
}

// assertIE06RelayedDeliveryFailsClosed locks the vault before the answer and
// before the delivery of relayed requests, and expects both to fail closed
// while the pending request is invalidated and the delivery succeeds after
// the vault unlocks.
func assertIE06RelayedDeliveryFailsClosed(t *testing.T, newOwner func(*testing.T) *Owner, vaultLocked *atomic.Bool) {
	t.Helper()
	vaultLocked.Store(false)
	relayed := newOwner(t)
	pendingRelay := mustEnsure(t, relayed, relayedSpec(t, "turn-ie06", "req-1"))
	vaultLocked.Store(true)
	_, err := relayed.Answer(&Decision{
		Incarnation: relayed.Incarnation(),
		RequestID:   pendingRelay.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})
	wantCode(t, err, errcode.FailedPrecondition)
	relayed.SetVaultLocked(true)
	if got := len(relayed.Pending()); got != 0 {
		t.Fatalf("pending after vault lock = %d, want 0", got)
	}
	locked, err := relayed.LookupRequest(pendingRelay.RequestID)
	if err != nil || !locked.Resolved || locked.Outcome != OutcomeCancelled {
		t.Fatalf("relayed request after vault lock = (%+v, %v), want cancelled", locked, err)
	}

	vaultLocked.Store(false)
	answeredRelay := newOwner(t)
	relayTicket := mustEnsure(t, answeredRelay, relayedSpec(t, "turn-ie06-take", "req-1"))
	mustAnswer(t, answeredRelay, &Decision{
		Incarnation: answeredRelay.Incarnation(),
		RequestID:   relayTicket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})
	vaultLocked.Store(true)
	_, err = answeredRelay.TakeRelayed(relayTicket.OperationID)
	wantCode(t, err, errcode.FailedPrecondition)
	vaultLocked.Store(false)
	delivered, err := answeredRelay.TakeRelayed(relayTicket.OperationID)
	if err != nil || delivered.Outcome != OutcomeAnswered || delivered.SelectedOptionID != vendorOptionAllow {
		t.Fatalf("delivered relayed ticket = (%+v, %v)", delivered, err)
	}
}

// TestIE08AuditAndErrorsNeverContainRequestContent is the I-E08 evidence of
// interaction §11: audit events and errors contain none of the planted
// summary, prompt, answer or note text (interaction §4.3, §7).
func TestIE08AuditAndErrorsNeverContainRequestContent(t *testing.T) {
	t.Parallel()
	planted := []string{plantSummary, plantDetail, plantQuestion, plantAnswer, plantNote, plantOption}
	collector := &contentCollector{}
	owner := newAuditOwner(t, collector)

	tickets := ensurePlantedRequests(t, owner)
	answerPlantedRequests(t, owner, tickets)
	recordInvalidContentOps(t, owner, tickets, collector)

	collector.assertClean(t, planted)
}

const (
	plantSummary  = "PLANTED-SUMMARY-mockvaulthook"
	plantDetail   = "PLANTED-DETAIL-mockvaulthook"
	plantQuestion = "PLANTED-QUESTION-mockvaulthook"
	plantAnswer   = "PLANTED-ANSWER-mockvaulthook"
	plantNote     = "PLANTED-NOTE-mockvaulthook"
	plantOption   = "PLANTED-OPTION-mockvaulthook"
)

// contentCollector gathers audit events and errors of the I-E08 run.
type contentCollector struct {
	mu     sync.Mutex
	audits []AuditEvent
	errs   []error
}

func (c *contentCollector) audit(ev *AuditEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.audits = append(c.audits, *ev)
}

func (c *contentCollector) record(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, err)
}

func (c *contentCollector) assertClean(t *testing.T, planted []string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.audits {
		rendered := fmt.Sprintf("%+v", c.audits[i])
		for _, plant := range planted {
			if strings.Contains(rendered, plant) {
				t.Fatalf("audit event carries request content: %s", rendered)
			}
		}
	}
	for _, err := range c.errs {
		for _, plant := range planted {
			if strings.Contains(err.Error(), plant) {
				t.Fatalf("error carries request content: %s", err)
			}
		}
	}
}

// plantedTickets holds the request IDs of the planted I-E08 requests.
type plantedTickets struct {
	confirmID  string
	questionID string
	relayID    string
	operation  string
}

func newAuditOwner(t *testing.T, collector *contentCollector) *Owner {
	t.Helper()
	owner, err := NewOwner(&Config{
		Clock:     clock.NewFake(startTime()),
		IDs:       ids.NewSequence("ie08"),
		Responder: NopResponder{},
		TagPolicy: newStubTagPolicy(),
		Audit: func(ev AuditEvent) {
			collector.audit(&ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func ensurePlantedRequests(t *testing.T, owner *Owner) plantedTickets {
	t.Helper()
	confirm := confirmSpec("op-ie08-confirm")
	confirm.Summary = plantSummary
	confirmTicket, _, err := owner.Ensure(confirm)
	if err != nil {
		t.Fatal(err)
	}
	question := questionSpec("op-ie08-question")
	question.Questions[0].Question = plantQuestion
	question.Binding.SessionID = session2
	questionTicket, _, err := owner.Ensure(question)
	if err != nil {
		t.Fatal(err)
	}
	relay := &Spec{
		OperationID: mustVendorKey(t, "turn-ie08", "req-1"),
		Kind:        KindConfirm,
		Summary:     plantSummary,
		Detail:      plantDetail,
		Relayed:     true,
		Options:     []Option{{ID: vendorOptionAllow, Label: plantOption}},
		Binding:     vendorBinding("turn-ie08", "req-1"),
	}
	relayTicket, _, err := owner.Ensure(relay)
	if err != nil {
		t.Fatal(err)
	}
	return plantedTickets{
		confirmID:  confirmTicket.RequestID,
		questionID: questionTicket.RequestID,
		relayID:    relayTicket.RequestID,
		operation:  confirm.OperationID,
	}
}

func answerPlantedRequests(t *testing.T, owner *Owner, tickets plantedTickets) {
	t.Helper()
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   tickets.confirmID,
		Outcome:     OutcomeDenied,
		Text:        plantNote,
	})
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   tickets.questionID,
		Outcome:     OutcomeAnswered,
		Answers:     []Answer{{QuestionID: "db", Text: plantAnswer}},
	})
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   tickets.relayID,
		Outcome:     OutcomeDenied,
		Text:        plantNote,
	})
}

func recordInvalidContentOps(t *testing.T, owner *Owner, tickets plantedTickets, collector *contentCollector) {
	t.Helper()
	_, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   tickets.confirmID,
		Outcome:     OutcomeAllowedOnce,
	})
	collector.record(err)
	_, err = owner.Answer(&Decision{RequestID: "missing", Outcome: OutcomeAllowedOnce})
	collector.record(err)
	oversized := questionSpec("op-ie08-oversized")
	oversized.Questions[0].Question = strings.Repeat(plantQuestion, 2000)
	_, _, err = owner.Ensure(oversized)
	collector.record(err)
	oversizedRelay := &Spec{
		OperationID: mustVendorKey(t, "turn-ie08-oversized", "req-big"),
		Kind:        KindConfirm,
		Summary:     plantSummary,
		Detail:      strings.Repeat(plantDetail, 12000),
		Relayed:     true,
		Options:     []Option{{ID: vendorOptionAllow, Label: plantOption}},
		Binding:     vendorBinding("turn-ie08-oversized", "req-big"),
	}
	_, _, err = owner.Ensure(oversizedRelay)
	collector.record(err)
	_, err = owner.LookupOperation(tickets.operation)
	collector.record(err)
	_, err = owner.LookupRequest(tickets.relayID)
	collector.record(err)
	_ = owner.Pending()
}

// TestVendorKeyComponentsWithTheSeparatorAreEncoded proves VendorOperationKey
// base64url-encodes untrusted components without padding so separators neither
// reject valid vendor IDs nor collide across component boundaries.
func TestVendorKeyComponentsWithTheSeparatorAreEncoded(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)

	k1, err := VendorOperationKey("sess:1", "turn:2", "toolu:01:abc/+=?")
	if err != nil {
		t.Fatalf("VendorOperationKey with separators failed: %v", err)
	}
	if strings.Contains(k1, "=") {
		t.Fatalf("encoded vendor key %q must not contain base64 padding", k1)
	}
	if parts := strings.Split(k1, keySeparator); len(parts) != 4 || parts[0] != vendorKeyLabel {
		t.Fatalf("encoded vendor key %q must split into 4 segments", k1)
	}

	k2, err := VendorOperationKey("a:b", "c", "d")
	if err != nil {
		t.Fatal(err)
	}
	k3, err := VendorOperationKey("a", "b:c", "d")
	if err != nil {
		t.Fatal(err)
	}
	if k2 == k3 {
		t.Fatalf("distinct components collided on key %q", k2)
	}

	spec := &Spec{
		OperationID: k1,
		Kind:        KindConfirm,
		Summary:     "relay separator request",
		Relayed:     true,
		Options:     []Option{{ID: vendorOptionAllow, Label: "Allow"}},
		Binding: Binding{
			SessionID:       "sess:1",
			TurnID:          "turn:2",
			VendorRequestID: "toolu:01:abc/+=?",
		},
	}
	ticket := mustEnsure(t, owner, spec)
	if ticket.OperationID != k1 {
		t.Fatalf("ticket operation ID = %q, want %q", ticket.OperationID, k1)
	}
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})
	delivered, err := owner.TakeRelayed(k1)
	if err != nil || delivered.SelectedOptionID != vendorOptionAllow {
		t.Fatalf("TakeRelayed(%q) = (%+v, %v)", k1, delivered, err)
	}
}

// TestResolvedUndeliveredRelayedRequestsExpire proves a resolved relayed
// request that is never delivered does not leak in memory: after
// ResolvedRetentionTTL on the injected clock TakeRelayed drops it and fails
// closed, and ResetSession drops resolved undelivered requests immediately.
func TestResolvedUndeliveredRelayedRequestsExpire(t *testing.T) {
	t.Parallel()
	owner, clk, _, _ := newTestOwner(t)

	spec := relayedSpec(t, "turn-expire", "req-expire")
	ticket := mustEnsure(t, owner, spec)
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})

	// Before expiry the resolved ticket remains deliverable.
	clk.Advance(ResolvedRetentionTTL / 2)
	if _, err := owner.LookupOperation(spec.OperationID); err != nil {
		t.Fatalf("LookupOperation before expiry = %v", err)
	}

	// Past ResolvedRetentionTTL on the injected clock, TakeRelayed drops it and fails closed.
	clk.Advance(ResolvedRetentionTTL)
	if _, err := owner.TakeRelayed(spec.OperationID); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("TakeRelayed after expiry = %v, want not_found", err)
	}
	byReq, byOp, receipts := owner.EntryCounts()
	if byReq != 0 || byOp != 0 || receipts != 0 {
		t.Fatalf("EntryCounts after expiry = (%d, %d, %d), want (0, 0, 0)", byReq, byOp, receipts)
	}

	// Session close (ResetSession) drops a resolved undelivered relayed request immediately.
	spec2 := relayedSpec(t, "turn-close", "req-close")
	ticket2 := mustEnsure(t, owner, spec2)
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket2.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
	})
	owner.ResetSession(testSessionID)
	if _, err := owner.TakeRelayed(spec2.OperationID); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("TakeRelayed after ResetSession = %v, want not_found", err)
	}
	byReq, byOp, receipts = owner.EntryCounts()
	if byReq != 0 || byOp != 0 || receipts != 0 {
		t.Fatalf("EntryCounts after ResetSession = (%d, %d, %d), want (0, 0, 0)", byReq, byOp, receipts)
	}
}
