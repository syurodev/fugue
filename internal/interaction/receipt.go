package interaction

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func (o *Owner) issueReceiptLocked(ticket *Ticket, selectedOptionID string, answers []Answer, now time.Time) (*Receipt, error) {
	receiptID, err := o.ids.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate receipt ID failed", err)
	}
	receipt := &Receipt{
		ReceiptID:        receiptID,
		RequestID:        ticket.RequestID,
		OperationID:      ticket.OperationID,
		Incarnation:      o.incarnation,
		Kind:             ticket.Kind,
		SelectedOptionID: selectedOptionID,
		Answers:          cloneAnswers(answers),
		Binding:          ticket.Binding,
		IssuedAt:         now,
		ExpiresAt:        ticket.ExpiresAt,
		Claimed:          false,
		ClaimedAt:        time.Time{},
	}
	o.receipts[receiptID] = receipt
	deadline := now.Add(ResolvedRetentionTTL)
	if !receipt.ExpiresAt.IsZero() && receipt.ExpiresAt.Before(deadline) {
		deadline = receipt.ExpiresAt
	}
	o.scheduleReceiptPruneLocked(deadline)
	return receipt, nil
}

// ClaimReceipt atomically validates and consumes a one-use confirmation receipt
// at the tool execution boundary. A receipt may be claimed at most once, fails
// closed while the vault is locked, and is rejected if any field of expected
// differs from the Binding recorded when the receipt was issued.
func (o *Owner) ClaimReceipt(receiptID string, expected *Binding) (*Receipt, error) {
	return o.claimByKind(receiptID, KindConfirm, expected)
}

// ClaimSelection atomically validates and consumes a one-use selection receipt
// during tool preparation, returning the chosen Option ID. Selection receipts
// never satisfy ClaimReceipt at the execution boundary.
func (o *Owner) ClaimSelection(receiptID string, expected *Binding) (string, error) {
	receipt, err := o.claimByKind(receiptID, KindSelect, expected)
	if err != nil {
		return "", err
	}
	return receipt.SelectedOptionID, nil
}

// ClaimAnswers atomically validates and consumes a one-use question receipt,
// returning the answers recorded when the question was resolved. Question
// receipts carry answers for Execute (interaction §4.1) and never authorize
// execution or satisfy ClaimReceipt.
func (o *Owner) ClaimAnswers(receiptID string, expected *Binding) ([]Answer, error) {
	receipt, err := o.claimByKind(receiptID, KindQuestion, expected)
	if err != nil {
		return nil, err
	}
	return cloneAnswers(receipt.Answers), nil
}

func (o *Owner) claimByKind(receiptID string, kind Kind, expected *Binding) (*Receipt, error) {
	if receiptID == "" || expected == nil {
		return nil, errcode.New(errcode.InvalidArgument, "receipt ID and expected binding are required")
	}
	o.mu.Lock()
	if o.isVaultLockedLocked() {
		o.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "vault is locked")
	}
	receipt, ok := o.receipts[receiptID]
	if !ok {
		o.mu.Unlock()
		return nil, errcode.New(errcode.NotFound, "interaction receipt not found")
	}
	now := o.clock.Now().UTC()
	if err := validateReceiptClaim(receipt, kind, expected, now); err != nil {
		o.mu.Unlock()
		return nil, err
	}
	receipt.Claimed = true
	receipt.ClaimedAt = now
	// A claimed receipt makes its resolved ticket immediately evictable.
	o.nextResolvedPrune = now
	o.pruneResolvedLocked(now)
	cp := *receipt
	outcome := OutcomeAllowedOnce
	if kind == KindQuestion {
		outcome = OutcomeAnswered
	}
	ev := AuditEvent{
		Action:      "receipt_claimed",
		Incarnation: o.incarnation,
		OperationID: receipt.OperationID,
		RequestID:   receipt.RequestID,
		ReceiptID:   receipt.ReceiptID,
		SessionID:   receipt.Binding.SessionID,
		ToolName:    receipt.Binding.ToolName,
		Kind:        receipt.Kind,
		Outcome:     outcome,
		OccurredAt:  now,
	}
	o.mu.Unlock()
	o.emitAudit(&ev)
	return &cp, nil
}

func validateReceiptClaim(receipt *Receipt, kind Kind, expected *Binding, now time.Time) error {
	if receipt.Claimed {
		return errcode.New(errcode.FailedPrecondition, "interaction receipt has already been claimed")
	}
	if !receipt.ExpiresAt.IsZero() && !now.Before(receipt.ExpiresAt) {
		return errcode.New(errcode.DeadlineExceeded, "interaction receipt has expired")
	}
	if receipt.Kind != kind {
		return errcode.New(errcode.PermissionDenied, "interaction receipt kind does not match claim")
	}
	if receipt.Binding != *expected {
		return errcode.New(errcode.PermissionDenied, "interaction receipt binding does not match execution context")
	}
	return nil
}

// InvalidateOperation cancels any pending request and drops any unconsumed
// receipt for operationID, returning an owner invalidation receipt ID for
// ToolCheckReset.
func (o *Owner) InvalidateOperation(operationID string) (string, error) {
	if operationID == "" {
		return "", errcode.New(errcode.InvalidArgument, "interaction operation ID is required")
	}
	o.mu.Lock()
	invalidationID, err := o.ids.NewID()
	if err != nil {
		o.mu.Unlock()
		return "", errcode.Wrap(errcode.Internal, "generate invalidation receipt ID failed", err)
	}
	now := o.clock.Now().UTC()
	var auditEv *AuditEvent
	if ticket, ok := o.byOperation[operationID]; ok {
		auditEv = o.cancelOperationTicketLocked(ticket, now)
	}
	o.mu.Unlock()
	o.emitAudit(auditEv)
	return invalidationID, nil
}

func (o *Owner) cancelOperationTicketLocked(ticket *Ticket, now time.Time) *AuditEvent {
	delete(o.byOperation, ticket.OperationID)
	if ticket.ReceiptID != "" {
		if r, exists := o.receipts[ticket.ReceiptID]; exists && !r.Claimed {
			delete(o.receipts, ticket.ReceiptID)
			o.nextResolvedPrune = now
		}
	}
	if ticket.Resolved {
		return nil
	}
	o.markResolvedLocked(ticket, now, OutcomeCancelled)
	ev := o.newAuditEvent("cancelled", ticket)
	o.emitLifecycleLocked(LifecycleEvent{
		Kind:      EventResolved,
		RequestID: ticket.RequestID,
		Outcome:   OutcomeCancelled,
	})
	return &ev
}

// ResetSession drops all session grants, cancels all pending requests and
// invalidates all unconsumed receipts belonging to sessionID.
func (o *Owner) ResetSession(sessionID string) {
	o.mu.Lock()
	now := o.clock.Now().UTC()
	o.nextResolvedPrune = now
	for k := range o.grants {
		if k.sessionID == sessionID {
			delete(o.grants, k)
		}
	}
	var cancelled []*AuditEvent
	for _, ticket := range o.byRequest {
		if ticket.Binding.SessionID != sessionID {
			continue
		}
		delete(o.byOperation, ticket.OperationID)
		if !ticket.Resolved {
			o.markResolvedLocked(ticket, now, OutcomeCancelled)
			ev := o.newAuditEvent("cancelled", ticket)
			cancelled = append(cancelled, &ev)
			o.emitLifecycleLocked(LifecycleEvent{
				Kind:      EventResolved,
				RequestID: ticket.RequestID,
				Outcome:   OutcomeCancelled,
			})
		}
	}
	for id, r := range o.receipts {
		if r.Binding.SessionID == sessionID && !r.Claimed {
			delete(o.receipts, id)
		}
	}
	o.mu.Unlock()
	for _, ev := range cancelled {
		o.emitAudit(ev)
	}
}

// SetVaultLocked updates the vault lock state observed by this Owner. Locking
// the vault immediately cancels all pending requests and wipes all session
// grants and unconsumed receipts in memory (storage-security §8, tool-system §5.1).
func (o *Owner) SetVaultLocked(locked bool) {
	o.mu.Lock()
	o.vaultLocked = locked
	if !locked {
		o.mu.Unlock()
		return
	}
	now := o.clock.Now().UTC()
	clear(o.grants)
	for id, r := range o.receipts {
		if !r.Claimed {
			delete(o.receipts, id)
		}
	}
	o.nextResolvedPrune = now
	var cancelled []*AuditEvent
	for _, ticket := range o.byRequest {
		if !ticket.Resolved {
			o.markResolvedLocked(ticket, now, OutcomeCancelled)
			ev := o.newAuditEvent("vault_locked", ticket)
			cancelled = append(cancelled, &ev)
			o.emitLifecycleLocked(LifecycleEvent{
				Kind:      EventResolved,
				RequestID: ticket.RequestID,
				Outcome:   OutcomeCancelled,
			})
		}
	}
	o.mu.Unlock()
	for _, ev := range cancelled {
		o.emitAudit(ev)
	}
}

func (o *Owner) newAuditEvent(action string, ticket *Ticket) AuditEvent {
	return AuditEvent{
		Action:      action,
		Incarnation: o.incarnation,
		OperationID: ticket.OperationID,
		RequestID:   ticket.RequestID,
		ReceiptID:   ticket.ReceiptID,
		SessionID:   ticket.Binding.SessionID,
		ToolName:    ticket.Binding.ToolName,
		Kind:        ticket.Kind,
		Outcome:     ticket.Outcome,
		OccurredAt:  o.clock.Now().UTC(),
	}
}

func (o *Owner) emitAudit(ev *AuditEvent) {
	if ev != nil && o.audit != nil {
		o.audit(*ev)
	}
}
