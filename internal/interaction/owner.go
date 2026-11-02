package interaction

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// grantKey scopes a session-level confirmation grant to a single session,
// workspace, tool and target/environment revision.
type grantKey struct {
	sessionID      string
	workspaceID    string
	toolName       string
	targetRevision string
	envRevision    string
	envTag         string
}

func grantKeyFor(b *Binding) grantKey {
	return grantKey{
		sessionID:      b.SessionID,
		workspaceID:    b.WorkspaceID,
		toolName:       b.ToolName,
		targetRevision: b.TargetRevision,
		envRevision:    b.EnvRevision,
		envTag:         b.EnvTag,
	}
}

// Config supplies the ports and optional callbacks for an Owner.
type Config struct {
	Clock       clock.Clock
	IDs         ids.Generator
	Responder   Responder
	Headless    bool
	VaultLocked func() bool
	Audit       func(AuditEvent)
}

// Owner holds the in-memory interaction requests, session grants and one-use
// receipts for a single host incarnation.
type Owner struct {
	clock       clock.Clock
	ids         ids.Generator
	responder   Responder
	headless    bool
	vaultCheck  func() bool
	audit       func(AuditEvent)
	incarnation string

	mu          sync.Mutex
	vaultLocked bool
	byOperation map[string]*Ticket
	byRequest   map[string]*Ticket
	grants      map[grantKey]struct{}
	receipts    map[string]*Receipt
}

// NewOwner creates an Owner with a fresh incarnation ID.
func NewOwner(cfg Config) (*Owner, error) {
	if cfg.Clock == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction clock is required")
	}
	if cfg.IDs == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction ID generator is required")
	}
	incarnation, err := cfg.IDs.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate interaction incarnation failed", err)
	}
	return &Owner{
		clock:       cfg.Clock,
		ids:         cfg.IDs,
		responder:   cfg.Responder,
		headless:    cfg.Headless,
		vaultCheck:  cfg.VaultLocked,
		audit:       cfg.Audit,
		incarnation: incarnation,
		mu:          sync.Mutex{},
		vaultLocked: false,
		byOperation: map[string]*Ticket{},
		byRequest:   map[string]*Ticket{},
		grants:      map[grantKey]struct{}{},
		receipts:    map[string]*Receipt{},
	}, nil
}

// Incarnation returns the unique ID of this Owner instance.
func (o *Owner) Incarnation() string {
	return o.incarnation
}

// Ensure creates or reads back the interaction request for spec.OperationID.
// Calling Ensure again with the same OperationID and Spec returns the current
// Ticket without prompting a second time.
func (o *Owner) Ensure(spec *Spec) (*Ticket, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	o.mu.Lock()
	now := o.clock.Now().UTC()
	if existing, ok := o.byOperation[spec.OperationID]; ok {
		return o.readBackLocked(existing, spec, now)
	}
	ticket, err := o.newTicketLocked(spec, now)
	if err != nil {
		o.mu.Unlock()
		return nil, err
	}
	if resolved, auditEv, err := o.tryImmediateResolutionLocked(ticket, now); resolved || err != nil {
		return o.finishImmediateLocked(ticket, auditEv, err)
	}
	o.byOperation[ticket.OperationID] = ticket
	o.byRequest[ticket.RequestID] = ticket
	cp := cloneTicket(ticket)
	openedEv := o.newAuditEvent("opened", ticket)
	o.mu.Unlock()
	return o.promptAndAudit(ticket, cp, &openedEv, now)
}

func (o *Owner) readBackLocked(existing *Ticket, spec *Spec, now time.Time) (*Ticket, error) {
	if !sameSpec(existing, spec) {
		o.mu.Unlock()
		return nil, errcode.New(errcode.AlreadyExists, "operation ID is already bound to a different request")
	}
	auditEv := o.expireIfDueLocked(existing, now)
	cp := cloneTicket(existing)
	o.mu.Unlock()
	o.emitAudit(auditEv)
	return cp, nil
}

func (o *Owner) newTicketLocked(spec *Spec, now time.Time) (*Ticket, error) {
	requestID, err := o.ids.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate request ID failed", err)
	}
	return &Ticket{
		RequestID:           requestID,
		OperationID:         spec.OperationID,
		Incarnation:         o.incarnation,
		Kind:                spec.Kind,
		Summary:             spec.Summary,
		AllowSessionOffered: spec.Kind == KindConfirm && !requiresEveryTime(spec),
		Options:             slices.Clone(spec.Options),
		Binding:             spec.Binding,
		CreatedAt:           now,
		ExpiresAt:           spec.ExpiresAt,
	}, nil
}

func (o *Owner) finishImmediateLocked(ticket *Ticket, auditEv *AuditEvent, err error) (*Ticket, error) {
	if err != nil {
		o.mu.Unlock()
		return nil, err
	}
	o.byOperation[ticket.OperationID] = ticket
	o.byRequest[ticket.RequestID] = ticket
	cp := cloneTicket(ticket)
	o.mu.Unlock()
	o.emitAudit(auditEv)
	return cp, nil
}

func (o *Owner) promptAndAudit(ticket, cp *Ticket, openedEv *AuditEvent, now time.Time) (*Ticket, error) {
	if err := o.responder.Prompt(cloneTicket(cp)); err != nil {
		o.mu.Lock()
		ticket.Resolved = true
		ticket.ResolvedAt = now
		ticket.Outcome = OutcomeUnavailable
		failedEv := o.newAuditEvent("prompt_failed", ticket)
		o.mu.Unlock()
		o.emitAudit(&failedEv)
		return nil, errcode.Wrap(errcode.Unavailable, "prompt responder failed", err)
	}
	o.emitAudit(openedEv)
	return cp, nil
}

func (o *Owner) tryImmediateResolutionLocked(ticket *Ticket, now time.Time) (bool, *AuditEvent, error) {
	if o.isUnavailableLocked() {
		ticket.Resolved = true
		ticket.ResolvedAt = now
		ticket.Outcome = OutcomeUnavailable
		ev := o.newAuditEvent("unavailable", ticket)
		return true, &ev, nil
	}
	if !ticket.ExpiresAt.IsZero() && !now.Before(ticket.ExpiresAt) {
		ticket.Resolved = true
		ticket.ResolvedAt = now
		ticket.Outcome = OutcomeExpired
		ev := o.newAuditEvent("expired", ticket)
		return true, &ev, nil
	}
	if ticket.AllowSessionOffered {
		if _, ok := o.grants[grantKeyFor(&ticket.Binding)]; ok {
			receipt, err := o.issueReceiptLocked(ticket, "", now)
			if err != nil {
				return false, nil, err
			}
			ticket.Resolved = true
			ticket.ResolvedAt = now
			ticket.Outcome = OutcomeAllowedSession
			ticket.ReceiptID = receipt.ReceiptID
			ev := o.newAuditEvent("session_grant_used", ticket)
			return true, &ev, nil
		}
	}
	return false, nil, nil
}

// LookupOperation returns the current Ticket for operationID, if any.
func (o *Owner) LookupOperation(operationID string) (*Ticket, error) {
	o.mu.Lock()
	ticket, ok := o.byOperation[operationID]
	if !ok {
		o.mu.Unlock()
		return nil, errcode.New(errcode.NotFound, "interaction operation not found")
	}
	ev := o.expireIfDueLocked(ticket, o.clock.Now().UTC())
	cp := cloneTicket(ticket)
	o.mu.Unlock()
	o.emitAudit(ev)
	return cp, nil
}

// LookupRequest returns the current Ticket for requestID, if any.
func (o *Owner) LookupRequest(requestID string) (*Ticket, error) {
	o.mu.Lock()
	ticket, ok := o.byRequest[requestID]
	if !ok {
		o.mu.Unlock()
		return nil, errcode.New(errcode.NotFound, "interaction request not found")
	}
	ev := o.expireIfDueLocked(ticket, o.clock.Now().UTC())
	cp := cloneTicket(ticket)
	o.mu.Unlock()
	o.emitAudit(ev)
	return cp, nil
}

// Pending returns all unresolved, non-expired tickets ordered by creation time.
func (o *Owner) Pending() []*Ticket {
	o.mu.Lock()
	now := o.clock.Now().UTC()
	var expiredEvents []*AuditEvent
	var out []*Ticket
	for _, ticket := range o.byRequest {
		if ev := o.expireIfDueLocked(ticket, now); ev != nil {
			expiredEvents = append(expiredEvents, ev)
		}
		if !ticket.Resolved {
			out = append(out, cloneTicket(ticket))
		}
	}
	o.mu.Unlock()
	for _, ev := range expiredEvents {
		o.emitAudit(ev)
	}
	slices.SortFunc(out, func(a, b *Ticket) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.RequestID, b.RequestID)
	})
	return out
}

// Answer records the human decision for a pending request. A request may be
// answered at most once, and requests from a previous incarnation or invalidated
// by vault lock are rejected.
func (o *Owner) Answer(decision Decision) (*Ticket, error) {
	if decision.RequestID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "interaction request ID is required")
	}
	if decision.Incarnation != "" && decision.Incarnation != o.incarnation {
		return nil, errcode.New(errcode.FailedPrecondition, "interaction request belongs to a previous incarnation")
	}
	o.mu.Lock()
	ticket, ok := o.byRequest[decision.RequestID]
	if !ok {
		o.mu.Unlock()
		return nil, errcode.New(errcode.NotFound, "interaction request not found")
	}
	now := o.clock.Now().UTC()
	if ev := o.expireIfDueLocked(ticket, now); ev != nil {
		o.mu.Unlock()
		o.emitAudit(ev)
		return nil, errcode.New(errcode.DeadlineExceeded, "interaction request has expired")
	}
	if ticket.Resolved {
		o.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "interaction request has already been answered")
	}
	if o.isVaultLockedLocked() && decision.Outcome != OutcomeDenied {
		o.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "vault is locked")
	}
	if err := o.applyDecisionLocked(ticket, decision, now); err != nil {
		o.mu.Unlock()
		return nil, err
	}
	cp := cloneTicket(ticket)
	ev := o.newAuditEvent("answered", ticket)
	o.mu.Unlock()
	o.emitAudit(&ev)
	return cp, nil
}

func (o *Owner) applyDecisionLocked(ticket *Ticket, decision Decision, now time.Time) error {
	switch ticket.Kind {
	case KindConfirm:
		return o.applyConfirmLocked(ticket, decision, now)
	case KindSelect:
		return o.applySelectLocked(ticket, decision, now)
	default:
		return errcode.New(errcode.InvalidArgument, "interaction kind is invalid")
	}
}

func (o *Owner) applyConfirmLocked(ticket *Ticket, decision Decision, now time.Time) error {
	if decision.OptionID != "" {
		return errcode.New(errcode.InvalidArgument, "confirm decision must not specify an option ID")
	}
	switch decision.Outcome {
	case OutcomeAllowedOnce:
		receipt, err := o.issueReceiptLocked(ticket, "", now)
		if err != nil {
			return err
		}
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeAllowedSession:
		if !ticket.AllowSessionOffered {
			return errcode.New(errcode.InvalidArgument, "session grant is not offered for this request")
		}
		receipt, err := o.issueReceiptLocked(ticket, "", now)
		if err != nil {
			return err
		}
		o.grants[grantKeyFor(&ticket.Binding)] = struct{}{}
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeDenied:
	case OutcomeCancelled, OutcomeUnavailable, OutcomeExpired:
		return errcode.New(errcode.InvalidArgument, "confirm decision outcome is not a valid user answer")
	default:
		return errcode.New(errcode.InvalidArgument, "confirm decision outcome is invalid")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

func (o *Owner) applySelectLocked(ticket *Ticket, decision Decision, now time.Time) error {
	switch decision.Outcome {
	case OutcomeAllowedOnce:
		if !hasOption(ticket.Options, decision.OptionID) {
			return errcode.New(errcode.InvalidArgument, "selected option was not offered")
		}
		receipt, err := o.issueReceiptLocked(ticket, decision.OptionID, now)
		if err != nil {
			return err
		}
		ticket.SelectedOptionID = decision.OptionID
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeDenied:
		if decision.OptionID != "" {
			return errcode.New(errcode.InvalidArgument, "denied selection must not specify an option ID")
		}
	case OutcomeAllowedSession, OutcomeCancelled, OutcomeUnavailable, OutcomeExpired:
		return errcode.New(errcode.InvalidArgument, "selection decision outcome is not a valid user answer")
	default:
		return errcode.New(errcode.InvalidArgument, "selection decision outcome is invalid")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

func hasOption(options []Option, id string) bool {
	return slices.ContainsFunc(options, func(opt Option) bool {
		return opt.ID == id
	})
}

func (o *Owner) expireIfDueLocked(ticket *Ticket, now time.Time) *AuditEvent {
	if ticket.Resolved || ticket.ExpiresAt.IsZero() || now.Before(ticket.ExpiresAt) {
		return nil
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = OutcomeExpired
	ev := o.newAuditEvent("expired", ticket)
	return &ev
}

func (o *Owner) isUnavailableLocked() bool {
	return o.headless || o.responder == nil || o.isVaultLockedLocked()
}

func (o *Owner) isVaultLockedLocked() bool {
	return o.vaultLocked || (o.vaultCheck != nil && o.vaultCheck())
}
