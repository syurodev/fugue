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
		owner, err := NewOwner(Config{
			Clock:       clock.NewFake(startTime()),
			IDs:         ids.NewSequence("ie06"),
			Responder:   NopResponder{},
			VaultLocked: vaultLocked.Load,
		})
		if err != nil {
			t.Fatal(err)
		}
		return owner
	}

	// Native: a lock between Answer and ClaimReceipt makes the claim fail closed.
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

	// Native: a lock before the answer fails closed as well.
	vaultLocked.Store(false)
	beforeAnswer := newOwner(t)
	pendingNative := mustEnsure(t, beforeAnswer, confirmSpec("op-ie06-native-lock"))
	vaultLocked.Store(true)
	_, err = beforeAnswer.Answer(&Decision{
		Incarnation: beforeAnswer.Incarnation(),
		RequestID:   pendingNative.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	wantCode(t, err, errcode.FailedPrecondition)

	// Relayed: a lock between the answer and its delivery fails closed; the
	// (created and ensured while the vault is unlocked).
	// pending request is invalidated and cannot be answered anymore.
	vaultLocked.Store(false)
	relayed := newOwner(t)
	pendingRelay := mustEnsure(t, relayed, relayedSpec("op-ie06-relay"))
	vaultLocked.Store(true)
	_, err = relayed.Answer(&Decision{
		Incarnation: relayed.Incarnation(),
		RequestID:   pendingRelay.RequestID,
		Outcome:     OutcomeAllowedOnce,
		OptionID:    "allow",
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
}

// TestIE08AuditAndErrorsNeverContainRequestContent is the I-E08 evidence of
// interaction §11: audit events and errors contain none of the planted
// summary, prompt, answer or note text (interaction §4.3, §7).
func TestIE08AuditAndErrorsNeverContainRequestContent(t *testing.T) {
	t.Parallel()
	planted := []string{plantSummary, plantQuestion, plantAnswer, plantNote, plantOption}
	collector := &contentCollector{}
	owner := newAuditOwner(t, collector)

	tickets := ensurePlantedRequests(t, owner)
	answerPlantedRequests(t, owner, tickets)
	recordInvalidContentOps(t, owner, tickets, collector)

	collector.assertClean(t, planted)
}

const (
	plantSummary  = "PLANTED-SUMMARY-mockvaulthook"
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
	owner, err := NewOwner(Config{
		Clock:     clock.NewFake(startTime()),
		IDs:       ids.NewSequence("ie08"),
		Responder: NopResponder{},
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
	confirmTicket, err := owner.Ensure(confirm)
	if err != nil {
		t.Fatal(err)
	}
	question := questionSpec("op-ie08-question")
	question.Questions[0].Question = plantQuestion
	question.Binding.SessionID = session2
	questionTicket, err := owner.Ensure(question)
	if err != nil {
		t.Fatal(err)
	}
	relay := &Spec{
		OperationID: "op-ie08-relay",
		Kind:        KindConfirm,
		Summary:     plantSummary,
		Relayed:     true,
		Options:     []Option{{ID: vendorOptionAllow, Label: plantOption}},
		Binding:     defaultBinding(),
	}
	relayTicket, err := owner.Ensure(relay)
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
	_, err = owner.Ensure(oversized)
	collector.record(err)
	_, err = owner.LookupOperation(tickets.operation)
	collector.record(err)
	_, err = owner.LookupRequest(tickets.relayID)
	collector.record(err)
	_ = owner.Pending()
}
