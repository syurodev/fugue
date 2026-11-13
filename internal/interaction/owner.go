package interaction

import (
	"cmp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// ResolvedRetentionTTL is how long non-relayed resolved tickets are retained
// in memory before being evicted (matching dedup TTL).
const ResolvedRetentionTTL = 10 * time.Minute

// grantKey scopes a session-level confirmation grant to a single session,
// workspace, grant scope key and target/environment revision.
type grantKey struct {
	sessionID      string
	workspaceID    string
	grantScopeKey  string
	targetRevision string
	envRevision    string
	envTag         string
}

// grantKeyFor derives the session grant key for a binding. The environment
// revision is never taken from the caller: for a binding with a target it is the
// tag registry's current policy revision, so changing a tag's policy (even
// changing it and reverting) invalidates every grant recorded under the old
// revision (interaction §3.4, I-E03). With no TagPolicy a target cannot be
// keyed and the second result is false: targets never get session grants.
func (o *Owner) grantKeyFor(b *Binding) (grantKey, bool) {
	key := grantKey{
		sessionID:      b.SessionID,
		workspaceID:    b.WorkspaceID,
		grantScopeKey:  b.GrantScopeKey,
		targetRevision: b.TargetRevision,
		envRevision:    "",
		envTag:         b.EnvTag,
	}
	if !b.HasTarget {
		return key, true
	}
	if o.tagPolicy == nil {
		return grantKey{}, false
	}
	key.envRevision = strconv.FormatUint(o.tagPolicy.PolicyRevision(b.EnvTag), 10)
	return key, true
}

// Config supplies the ports and optional callbacks for an Owner. Responder
// may be nil: requests then stay pending until a client picks them up
// (interaction §5, I-07); only Headless resolves requests as unavailable.
type Config struct {
	Clock       clock.Clock
	IDs         ids.Generator
	Responder   Responder
	Headless    bool
	VaultLocked func() bool
	Audit       func(AuditEvent)
	TagPolicy   TagPolicy
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
	onLifecycle func(LifecycleEvent)
	tagPolicy   TagPolicy
	incarnation string

	mu                sync.Mutex
	vaultLocked       bool
	byOperation       map[string]*Ticket
	byRequest         map[string]*Ticket
	grants            map[grantKey]struct{}
	receipts          map[string]*Receipt
	nextResolvedPrune time.Time
	nextReceiptPrune  time.Time
	pendingCount      int
	pendingBySession  map[string]int
	pendingIDs        map[string]struct{}
	nextPendingExpiry time.Time
}

// NewOwner creates an Owner with a fresh incarnation ID.
func NewOwner(cfg *Config) (*Owner, error) {
	if cfg == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction config is required")
	}
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
		clock:            cfg.Clock,
		ids:              cfg.IDs,
		responder:        cfg.Responder,
		headless:         cfg.Headless,
		vaultCheck:       cfg.VaultLocked,
		audit:            cfg.Audit,
		onLifecycle:      nil,
		tagPolicy:        cfg.TagPolicy,
		incarnation:      incarnation,
		mu:               sync.Mutex{},
		vaultLocked:      false,
		byOperation:      map[string]*Ticket{},
		byRequest:        map[string]*Ticket{},
		grants:           map[grantKey]struct{}{},
		receipts:         map[string]*Receipt{},
		pendingBySession: map[string]int{},
		pendingIDs:       map[string]struct{}{},
	}, nil
}

// SetOnLifecycle configures or replaces the callback notified on every ticket lifecycle transition.
// The hook is called with o.mu held and MUST be non-blocking: it may only call hub.Publish
// and must take no other locks (interaction §8).
func (o *Owner) SetOnLifecycle(fn func(LifecycleEvent)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onLifecycle = fn
}

func (o *Owner) emitLifecycleLocked(ev LifecycleEvent) {
	if o.onLifecycle != nil {
		o.onLifecycle(ev)
	}
}

// Incarnation returns the unique ID of this Owner instance.
func (o *Owner) Incarnation() string {
	return o.incarnation
}

// Ensure creates or reads back the interaction request for spec.OperationID.
// Calling Ensure again with the same OperationID and Spec returns the current
// Ticket without prompting a second time. It reports whether the ticket was
// freshly created (true) or read back (false).
func (o *Owner) Ensure(spec *Spec) (*Ticket, bool, error) {
	if err := validateSpec(spec, o.tagPolicy); err != nil {
		return nil, false, err
	}
	o.mu.Lock()
	now := o.clock.Now().UTC()
	o.pruneResolvedLocked(now)
	if existing, ok := o.byOperation[spec.OperationID]; ok {
		return o.readBackLocked(existing, spec, now)
	}
	if err := o.checkPendingLimitsLocked(spec, now); err != nil {
		o.mu.Unlock()
		return nil, false, err
	}
	ticket, err := o.newTicketLocked(spec, now)
	if err != nil {
		o.mu.Unlock()
		return nil, false, err
	}
	if resolved, auditEv, res, rerr := o.tryImmediateResolutionLocked(ticket, now); resolved || rerr != nil {
		return o.finishImmediateLocked(ticket, auditEv, res, rerr)
	}
	o.byOperation[ticket.OperationID] = ticket
	o.byRequest[ticket.RequestID] = ticket
	o.addPendingLocked(ticket)
	cp := cloneTicket(ticket)
	o.emitLifecycleLocked(LifecycleEvent{Kind: EventRequested, Ticket: cp})
	openedEv := o.newAuditEvent("opened", ticket)
	respond := o.responder != nil
	o.mu.Unlock()
	if !respond {
		// No live responder: the request waits for a client to pick it up
		// (interaction §5, I-07).
		o.emitAudit(&openedEv)
		return cp, true, nil
	}
	return o.promptAndAudit(ticket, cp, &openedEv, now)
}

func (o *Owner) readBackLocked(existing *Ticket, spec *Spec, now time.Time) (*Ticket, bool, error) {
	if !sameSpec(existing, spec) {
		o.mu.Unlock()
		return nil, false, errcode.New(errcode.AlreadyExists, "operation ID is already bound to a different request")
	}
	auditEv := o.expireIfDueLocked(existing, now)
	cp := cloneTicket(existing)
	o.mu.Unlock()
	o.emitAudit(auditEv)
	return cp, false, nil
}

func (o *Owner) newTicketLocked(spec *Spec, now time.Time) (*Ticket, error) {
	requestID, err := o.ids.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate request ID failed", err)
	}
	return &Ticket{
		RequestID:       requestID,
		OperationID:     spec.OperationID,
		Incarnation:     o.incarnation,
		Kind:            spec.Kind,
		Risk:            spec.Risk,
		GrantScopeLabel: spec.GrantScopeLabel,
		Summary:         spec.Summary,
		Relayed:         spec.Relayed,
		Offers:          slices.Clone(effectiveOffers(spec)),
		Options:         slices.Clone(spec.Options),
		Questions:       cloneQuestions(spec.Questions),
		Body:            spec.Body,
		Binding:         spec.Binding,
		CreatedAt:       now,
		ExpiresAt:       spec.ExpiresAt,
	}, nil
}

func (o *Owner) finishImmediateLocked(ticket *Ticket, auditEv *AuditEvent, res *Resolution, err error) (*Ticket, bool, error) {
	if err != nil {
		o.mu.Unlock()
		return nil, false, err
	}
	o.byOperation[ticket.OperationID] = ticket
	o.byRequest[ticket.RequestID] = ticket
	cp := cloneTicket(ticket)
	if res != nil {
		o.emitLifecycleLocked(LifecycleEvent{
			Kind:      EventResolved,
			RequestID: res.RequestID,
			Outcome:   res.Outcome,
		})
	}
	o.mu.Unlock()
	o.emitAudit(auditEv)
	return cp, true, nil
}

func (o *Owner) promptAndAudit(ticket, cp *Ticket, openedEv *AuditEvent, now time.Time) (*Ticket, bool, error) {
	if err := o.responder.Prompt(cloneTicket(cp)); err != nil {
		o.mu.Lock()
		o.markResolvedLocked(ticket, now, OutcomeUnavailable)
		failedEv := o.newAuditEvent("prompt_failed", ticket)
		res := Resolution{RequestID: ticket.RequestID, Outcome: OutcomeUnavailable}
		o.emitLifecycleLocked(LifecycleEvent{
			Kind:      EventResolved,
			RequestID: res.RequestID,
			Outcome:   res.Outcome,
		})
		o.mu.Unlock()
		o.emitAudit(&failedEv)
		return nil, false, errcode.Wrap(errcode.Unavailable, "prompt responder failed", err)
	}
	o.emitAudit(openedEv)
	return cp, true, nil
}

func (o *Owner) tryImmediateResolutionLocked(ticket *Ticket, now time.Time) (bool, *AuditEvent, *Resolution, error) {
	if o.isUnavailableLocked() {
		o.markResolvedLocked(ticket, now, OutcomeUnavailable)
		ev := o.newAuditEvent("unavailable", ticket)
		res := Resolution{RequestID: ticket.RequestID, Outcome: OutcomeUnavailable}
		return true, &ev, &res, nil
	}
	if !ticket.ExpiresAt.IsZero() && !now.Before(ticket.ExpiresAt) {
		o.markResolvedLocked(ticket, now, OutcomeExpired)
		ev := o.newAuditEvent("expired", ticket)
		res := Resolution{RequestID: ticket.RequestID, Outcome: OutcomeExpired}
		return true, &ev, &res, nil
	}
	if slices.Contains(ticket.Offers, OutcomeAllowedSession) {
		if o.hasGrantLocked(&ticket.Binding) {
			receipt, err := o.issueReceiptLocked(ticket, "", nil, now)
			if err != nil {
				return false, nil, nil, err
			}
			ticket.ReceiptID = receipt.ReceiptID
			o.markResolvedLocked(ticket, now, OutcomeAllowedSession)
			ev := o.newAuditEvent("session_grant_used", ticket)
			res := Resolution{RequestID: ticket.RequestID, Outcome: OutcomeAllowedSession}
			return true, &ev, &res, nil
		}
	}
	return false, nil, nil, nil
}

// LookupOperation returns the current Ticket for operationID, if any.
func (o *Owner) LookupOperation(operationID string) (*Ticket, error) {
	o.mu.Lock()
	ticket, ok := o.byOperation[operationID]
	if !ok {
		o.mu.Unlock()
		return nil, ErrOperationNotFound
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
		return nil, ErrRequestNotFound
	}
	ev := o.expireIfDueLocked(ticket, o.clock.Now().UTC())
	cp := cloneTicket(ticket)
	o.mu.Unlock()
	o.emitAudit(ev)
	return cp, nil
}

// Pending returns all unresolved, non-expired tickets ordered by creation time.
// Reading pending requests is strictly read-only and never resolves tickets.
func (o *Owner) Pending() []*Ticket {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.clock.Now().UTC()
	o.pruneResolvedLocked(now)
	var out []*Ticket
	for _, ticket := range o.byRequest {
		if !ticket.Resolved && (ticket.ExpiresAt.IsZero() || now.Before(ticket.ExpiresAt)) {
			out = append(out, cloneTicket(ticket))
		}
	}
	slices.SortFunc(out, func(a, b *Ticket) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.RequestID, b.RequestID)
	})
	return out
}

// EarliestPendingExpiry returns the earliest non-zero ExpiresAt among all
// currently pending tickets, and whether any pending ticket has an expiry.
func (o *Owner) EarliestPendingExpiry() (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.clock.Now().UTC()
	var earliest time.Time
	hasExpiry := false
	for _, ticket := range o.byRequest {
		if ticket.Resolved || ticket.ExpiresAt.IsZero() {
			continue
		}
		if !now.Before(ticket.ExpiresAt) {
			return ticket.ExpiresAt, true
		}
		if !hasExpiry || ticket.ExpiresAt.Before(earliest) {
			earliest = ticket.ExpiresAt
			hasExpiry = true
		}
	}
	return earliest, hasExpiry
}

// Answer records the human decision for a pending request. A request may be
// answered at most once, requests from a previous incarnation or invalidated
// by vault lock are rejected, and the first of several concurrent answers
// wins. Relayed vendor requests never produce a receipt or a grant.
func (o *Owner) Answer(decision *Decision) (*Ticket, error) {
	if decision == nil || decision.RequestID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "interaction request ID is required")
	}
	if decision.Incarnation != "" && decision.Incarnation != o.incarnation {
		return nil, ErrStaleIncarnation
	}
	o.mu.Lock()
	ticket, ok := o.byRequest[decision.RequestID]
	if !ok {
		o.mu.Unlock()
		return nil, ErrRequestNotFound
	}
	now := o.clock.Now().UTC()
	o.pruneResolvedLocked(now)
	if ev := o.expireIfDueLocked(ticket, now); ev != nil {
		o.mu.Unlock()
		o.emitAudit(ev)
		return nil, errcode.New(errcode.DeadlineExceeded, "interaction request has expired")
	}
	if ticket.Resolved {
		o.mu.Unlock()
		return nil, ErrAlreadyAnswered
	}
	if o.isVaultLockedLocked() && decision.Outcome != OutcomeDenied {
		o.mu.Unlock()
		return nil, ErrVaultLocked
	}
	if err := o.applyDecisionLocked(ticket, decision, now); err != nil {
		o.mu.Unlock()
		return nil, err
	}
	cp := cloneTicket(ticket)
	ev := o.newAuditEvent("answered", ticket)
	o.emitLifecycleLocked(LifecycleEvent{
		Kind:      EventResolved,
		RequestID: ticket.RequestID,
		Outcome:   ticket.Outcome,
	})
	o.mu.Unlock()
	o.emitAudit(&ev)
	return cp, nil
}

// TakeRelayed delivers a resolved relayed request's decision to the vendor
// adapter exactly once and frees the request content from memory
// (vendor-runtime VR-05): the ticket is returned on the first call and
// removed from both indexes, so later lookups and a second take fail typed.
// A still-pending request is not deliverable, and the take fails closed while
// the vault is locked (interaction §5, I-E06).
func (o *Owner) TakeRelayed(operationID string) (*Ticket, error) {
	if operationID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "interaction operation ID is required")
	}
	o.mu.Lock()
	if o.isVaultLockedLocked() {
		o.mu.Unlock()
		return nil, ErrVaultLocked
	}
	ticket, ok := o.byOperation[operationID]
	if !ok {
		o.mu.Unlock()
		return nil, ErrOperationNotFound
	}
	if !ticket.Relayed {
		o.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "operation is not a relayed request")
	}
	if !ticket.Resolved {
		o.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "relayed request is not resolved yet")
	}
	delete(o.byOperation, ticket.OperationID)
	delete(o.byRequest, ticket.RequestID)
	cp := cloneTicket(ticket)
	ev := o.newAuditEvent("delivered", ticket)
	o.mu.Unlock()
	o.emitAudit(&ev)
	return cp, nil
}

func (o *Owner) expireIfDueLocked(ticket *Ticket, now time.Time) *AuditEvent {
	if ticket.Resolved || ticket.ExpiresAt.IsZero() || now.Before(ticket.ExpiresAt) {
		return nil
	}
	o.markResolvedLocked(ticket, now, OutcomeExpired)
	ev := o.newAuditEvent("expired", ticket)
	o.emitLifecycleLocked(LifecycleEvent{
		Kind:      EventResolved,
		RequestID: ticket.RequestID,
		Outcome:   OutcomeExpired,
	})
	return &ev
}

// ExpireExpired resolves any overdue pending tickets and emits resolved lifecycle events.
func (o *Owner) ExpireExpired() []*Ticket {
	o.mu.Lock()
	now := o.clock.Now().UTC()
	var expired []*Ticket
	var auditEvents []*AuditEvent
	for _, ticket := range o.byRequest {
		if ev := o.expireIfDueLocked(ticket, now); ev != nil {
			auditEvents = append(auditEvents, ev)
			expired = append(expired, cloneTicket(ticket))
		}
	}
	o.pruneResolvedLocked(now)
	o.mu.Unlock()
	for _, ev := range auditEvents {
		o.emitAudit(ev)
	}
	return expired
}

// isUnavailableLocked reports whether a new request resolves as unavailable
// immediately: only a headless host (or a locked vault) fails closed at
// creation (interaction §5, I-07). A missing responder leaves the request
// pending instead.
func (o *Owner) isUnavailableLocked() bool {
	return o.headless || o.isVaultLockedLocked()
}

func (o *Owner) isVaultLockedLocked() bool {
	return o.vaultLocked || (o.vaultCheck != nil && o.vaultCheck())
}
