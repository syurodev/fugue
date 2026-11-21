package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// Limit constants for interaction transport checks (interaction §9).
const (
	MaxInteractionNoteBytes   = interaction.MaxNoteBytes
	MaxInteractionAnswerBytes = interaction.MaxAnswerBytes
	MaxInteractionOptions     = interaction.MaxOptions
	MaxRiskBytes              = 32
	MaxGrantScopeLabelBytes   = 256
	OutcomeAllowedOnce        = string(interaction.OutcomeAllowedOnce)
	OutcomeAllowedSession     = string(interaction.OutcomeAllowedSession)
	OutcomeDenied             = string(interaction.OutcomeDenied)
	OutcomeAnswered           = string(interaction.OutcomeAnswered)
	OutcomeCancelled          = string(interaction.OutcomeCancelled)
	OutcomeExpired            = string(interaction.OutcomeExpired)
	OutcomeUnavailable        = string(interaction.OutcomeUnavailable)
)

// AnswerInteractionCommand carries the input to Answer (interaction §8).
type AnswerInteractionCommand struct {
	CommandID   string
	Principal   string
	Incarnation string
	RequestID   string
	Outcome     string
	OptionID    string
	Text        string
	Answers     []InteractionAnswer
}

// AnswerInteractionResult carries the outcome of an answered request.
type AnswerInteractionResult struct {
	RequestID string
	Outcome   string
}

// InteractionService coordinates interaction requests, durable topic events
// and deduplication for a host incarnation.
type InteractionService struct {
	owner      *interaction.Owner
	hub        *EventHub
	dedup      *commandDedupCache
	clk        clock.Clock
	inc        string
	wakeExpiry chan struct{}

	taskMu sync.Mutex
	task   *supervise.Task
	cancel context.CancelFunc

	cmdMu    sync.Mutex
	inflight map[string]*inflightCmd

	// waitMu is a leaf lock: the lifecycle hook takes it under the owner's
	// mutex, and nothing that holds it calls the owner.
	waitMu  sync.Mutex
	waiters map[string][]chan struct{}

	// teamCards consumes the answers of team proposal and amend cards. It is
	// wired once during startup, before the transport serves (F11.2b).
	teamCards teamCardResolver
	// teamApprovalHook records the F11.10b approval reference lines; set once
	// at startup and called for every lifecycle event.
	teamApprovalHook func(interaction.LifecycleEvent)
	// taskQuestions consumes the answers of user question cards opened by
	// fugue_task_ask or fugue_task_answer(forward_to=user) (F11.3d).
	taskQuestions taskQuestionResolver
	// teamDeadlines consumes the answers of team deadline extension/cancel
	// cards (F11.6c).
	teamDeadlines teamDeadlineResolver

	// betweenListReads is a test seam: ListPending calls it between reading
	// the topic sequence and reading the pending views. Always nil in
	// production.
	betweenListReads func()
}

// NewInteractionService creates an InteractionService and binds it to owner and hub.
func NewInteractionService(owner *interaction.Owner, hub *EventHub, clk clock.Clock) (*InteractionService, error) {
	if owner == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction owner is required")
	}
	if clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "clock is required")
	}

	s := &InteractionService{
		owner:      owner,
		hub:        hub,
		dedup:      newCommandDedupCache(clk, defaultDedupCapacity, defaultDedupTTL),
		clk:        clk,
		inc:        owner.Incarnation(),
		wakeExpiry: make(chan struct{}, 1),
		inflight:   map[string]*inflightCmd{},
		waiters:    map[string][]chan struct{}{},
	}

	if hub != nil {
		if err := hub.RegisterEphemeral(TopicInteraction, s.snapshot, defaultEphemeralBufferCap); err != nil {
			return nil, err
		}
	}

	owner.SetOnLifecycle(s.handleLifecycle)
	return s, nil
}

func (s *InteractionService) handleLifecycle(ev interaction.LifecycleEvent) {
	if s.teamApprovalHook != nil {
		s.teamApprovalHook(ev)
	}
	if s.hub != nil {
		switch ev.Kind {
		case interaction.EventRequested:
			s.publishRequested(ev.Ticket)
		case interaction.EventResolved:
			s.publishResolved(ev.RequestID, ev.Outcome)
		}
	}
	if ev.Kind == interaction.EventResolved {
		s.wakeWaiters(ev.RequestID)
	}
	s.nudgeExpiry()
}

func (s *InteractionService) wakeWaiters(requestID string) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	for _, ch := range s.waiters[requestID] {
		close(ch)
	}
	delete(s.waiters, requestID)
}

// AwaitResolved waits for requestID to resolve without creating a new request.
// It waits as long as ctx lives: with no client connected the request stays
// pending (interaction §5, I-07). The waiter is registered before the state
// is read, so a resolution in between is never missed.
func (s *InteractionService) AwaitResolved(ctx context.Context, requestID string) (*interaction.Ticket, error) {
	return s.awaitResolvedUntil(ctx, requestID, nil)
}

func (s *InteractionService) awaitResolvedUntil(ctx context.Context, requestID string, aborted <-chan struct{}) (*interaction.Ticket, error) {
	ch := make(chan struct{})
	s.waitMu.Lock()
	s.waiters[requestID] = append(s.waiters[requestID], ch)
	s.waitMu.Unlock()
	ticket, err := s.owner.LookupRequest(requestID)
	if err == nil && !ticket.Resolved {
		select {
		case <-ch:
			ticket, err = s.owner.LookupRequest(requestID)
		case <-aborted:
			err = errSessionEnded()
		case <-ctx.Done():
			err = errcode.Wrap(errcode.Canceled, "the wait for the interaction answer ended", ctx.Err())
		}
	}
	s.waitMu.Lock()
	s.waiters[requestID] = slices.DeleteFunc(s.waiters[requestID], func(c chan struct{}) bool { return c == ch })
	if len(s.waiters[requestID]) == 0 {
		delete(s.waiters, requestID)
	}
	s.waitMu.Unlock()
	if err != nil {
		return nil, mapOwnerError(err)
	}
	return ticket, nil
}

// IsVaultLocked reports whether the interaction owner currently observes the vault locked.
func (s *InteractionService) IsVaultLocked() bool {
	return s.owner.IsVaultLocked()
}

func (s *InteractionService) publishRequested(ticket *interaction.Ticket) {
	view := viewFromTicket(ticket)
	payload, err := json.Marshal(view)
	if err != nil {
		slog.Warn("marshal requested event failed", "request_id", view.RequestID, "outcome", "", "err", err)
		return
	}
	if _, perr := s.hub.Publish(TopicInteraction, Event{
		Type:    EventInteractionRequested,
		Payload: payload,
	}); perr != nil {
		slog.Warn("publish requested event failed", "request_id", view.RequestID, "outcome", "", "err", perr)
	}
}

func (s *InteractionService) publishResolved(requestID string, outcome interaction.Outcome) {
	payload, err := json.Marshal(resolvedPayload{
		RequestID: requestID,
		Outcome:   string(outcome),
	})
	if err != nil {
		slog.Warn("marshal resolved event failed", "request_id", requestID, "outcome", string(outcome), "err", err)
		return
	}
	if _, perr := s.hub.Publish(TopicInteraction, Event{
		Type:    EventInteractionResolved,
		Payload: payload,
	}); perr != nil {
		slog.Warn("publish resolved event failed", "request_id", requestID, "outcome", string(outcome), "err", perr)
	}
}

func (s *InteractionService) nudgeExpiry() {
	select {
	case s.wakeExpiry <- struct{}{}:
	default:
	}
}

// TicketOf returns the current ticket of one request, resolved or pending
// (F11.10b: the approval reference lines need the binding a resolved
// lifecycle event does not carry).
func (s *InteractionService) TicketOf(requestID string) (*interaction.Ticket, error) {
	ticket, err := s.owner.LookupRequest(requestID)
	if err != nil {
		return nil, errcode.Wrap(errcode.NotFound, "the interaction request does not exist", err)
	}
	return ticket, nil
}

// Incarnation returns the host incarnation ID this service belongs to.
func (s *InteractionService) Incarnation() string {
	return s.inc
}

// Ensure creates or reads back an interaction request. F9.6 is the first caller outside tests.
func (s *InteractionService) Ensure(spec *interaction.Spec) (*interaction.Ticket, error) {
	if spec == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction spec is required")
	}
	if len(spec.Risk) > MaxRiskBytes {
		return nil, errcode.New(errcode.InvalidArgument, "interaction risk exceeds size limit")
	}
	if len(spec.GrantScopeLabel) > MaxGrantScopeLabelBytes {
		return nil, errcode.New(errcode.InvalidArgument, "interaction grant scope label exceeds size limit")
	}

	ticket, _, err := s.owner.Ensure(spec)
	if err != nil {
		var coded *errcode.Error
		if errors.As(err, &coded) {
			return nil, errcode.Wrap(coded.Code(), "interaction ensure", err)
		}
		return nil, errcode.Wrap(errcode.Internal, "interaction ensure", err)
	}
	return ticket, nil
}

func (s *InteractionService) snapshot(_ context.Context) ([]byte, error) {
	views := s.currentPendingViews()
	data, err := json.Marshal(views)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal interaction snapshot", err)
	}
	return data, nil
}

func (s *InteractionService) currentPendingViews() []PendingInteractionView {
	tickets := s.owner.Pending()
	views := make([]PendingInteractionView, 0, len(tickets))
	for _, ticket := range tickets {
		views = append(views, viewFromTicket(ticket))
	}
	return views
}

// ListPending returns the pending interaction views and the topic sequence.
// Only local principals may list pending interactions (host-api §7).
func (s *InteractionService) ListPending(_ context.Context, principal string) ([]PendingInteractionView, EventSeq, error) {
	if !IsLocalPrincipal(principal) {
		return nil, 0, errcode.New(errcode.PermissionDenied, "only local principals may list pending interactions")
	}
	// The sequence is read before the views, like EventHub's topicSnapshot: a
	// request published in the gap is then in the views, or in the stream
	// after seq, or both. The overlap is harmless because the payloads are
	// idempotent. The reverse order would let a client subscribe from a seq
	// that already counts an event whose ticket the views do not contain, and
	// miss that request.
	var seq EventSeq
	if s.hub != nil {
		seq = s.hub.TopicSequence(TopicInteraction)
	}
	if s.betweenListReads != nil {
		s.betweenListReads()
	}
	return s.currentPendingViews(), seq, nil
}
