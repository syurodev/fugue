package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// AwaitResolved blocks until the request is resolved and returns its ticket.
// It waits as long as ctx lives: with no client connected the request stays
// pending (interaction §5, I-07). The waiter is registered before the state
// is read, so a resolution in between is never missed.
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

func (s *InteractionService) validateAnswerCmd(cmd *AnswerInteractionCommand) error {
	if !IsLocalPrincipal(cmd.Principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may answer interaction requests")
	}
	if cmd.CommandID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if cmd.RequestID == "" {
		return errcode.New(errcode.InvalidArgument, "interaction request ID is required")
	}
	if cmd.Incarnation == "" {
		return errcode.New(errcode.InvalidArgument, "incarnation is required")
	}
	if cmd.Incarnation != s.inc {
		return errcode.New(errcode.InteractionStaleIncarnation, "interaction request belongs to a previous incarnation")
	}
	return nil
}

func offeredOutcomesFor(t *interaction.Ticket) []string {
	if t.Relayed {
		return []string{string(interaction.OutcomeAnswered), string(interaction.OutcomeDenied)}
	}
	switch t.Kind {
	case interaction.KindConfirm:
		offers := make([]string, 0, len(t.Offers)+1)
		for _, o := range t.Offers {
			offers = append(offers, string(o))
		}
		if !slices.Contains(t.Offers, interaction.OutcomeDenied) {
			offers = append(offers, string(interaction.OutcomeDenied))
		}
		return offers
	case interaction.KindSelect:
		return []string{string(interaction.OutcomeAllowedOnce), string(interaction.OutcomeDenied)}
	case interaction.KindQuestion:
		return []string{string(interaction.OutcomeAnswered), string(interaction.OutcomeCancelled)}
	default:
		return nil
	}
}

func (s *InteractionService) checkAnswerLimits(cmd *AnswerInteractionCommand, ticket *interaction.Ticket) error {
	outcome := interaction.Outcome(cmd.Outcome)
	offered := offeredOutcomesFor(ticket)
	if !slices.Contains(offered, string(outcome)) {
		return errcode.New(errcode.InteractionOptionNotOffered, "outcome not offered")
	}
	if cmd.OptionID != "" && !hasOption(ticket.Options, cmd.OptionID) {
		return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
	}
	if outcome == interaction.OutcomeDenied {
		if len(cmd.Text) > interaction.MaxNoteBytes {
			return errcode.New(errcode.InteractionTextTooLarge, "deny note exceeds size limit")
		}
	} else if len(cmd.Text) > interaction.MaxAnswerBytes {
		return errcode.New(errcode.InteractionTextTooLarge, "answer text exceeds size limit")
	}
	return checkAnswerItems(cmd.Answers)
}

func checkAnswerItems(answers []InteractionAnswer) error {
	if len(answers) > interaction.MaxOptions {
		return errcode.New(errcode.InvalidArgument, "number of answers exceeds limit")
	}
	for _, a := range answers {
		if len(a.Text) > interaction.MaxAnswerBytes {
			return errcode.New(errcode.InteractionTextTooLarge, "question answer text exceeds size limit")
		}
		if len(a.OptionIDs) > interaction.MaxOptions {
			return errcode.New(errcode.InvalidArgument, "number of selected options exceeds limit")
		}
	}
	return nil
}

// Answer records a human decision for a pending request.
func (s *InteractionService) Answer(ctx context.Context, cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
	if err := s.validateAnswerCmd(cmd); err != nil {
		return AnswerInteractionResult{}, err
	}

	s.cmdMu.Lock()
	if prev, ok := s.dedup.Get(cmd.CommandID); ok {
		s.cmdMu.Unlock()
		if prev.RequestID != cmd.RequestID || prev.Outcome != cmd.Outcome {
			return AnswerInteractionResult{}, errcode.New(errcode.InvalidArgument, "command ID was previously used with different request ID or outcome")
		}
		return prev, nil
	}
	if inf, ok := s.inflight[cmd.CommandID]; ok {
		s.cmdMu.Unlock()
		return s.awaitInflight(ctx, cmd, inf)
	}
	inf := &inflightCmd{done: make(chan struct{})}
	s.inflight[cmd.CommandID] = inf
	s.cmdMu.Unlock()

	var (
		res AnswerInteractionResult
		err error
	)
	defer func() {
		s.cmdMu.Lock()
		delete(s.inflight, cmd.CommandID)
		inf.result = res
		inf.err = err
		close(inf.done)
		s.cmdMu.Unlock()
	}()

	res, err = s.executeAnswer(cmd)
	if err == nil {
		s.dedup.Put(cmd.CommandID, res)
	}
	return res, err
}

func (s *InteractionService) awaitInflight(ctx context.Context, cmd *AnswerInteractionCommand, inf *inflightCmd) (AnswerInteractionResult, error) {
	select {
	case <-inf.done:
		if inf.err != nil {
			return AnswerInteractionResult{}, inf.err
		}
		if inf.result.RequestID != cmd.RequestID || inf.result.Outcome != cmd.Outcome {
			return AnswerInteractionResult{}, errcode.New(errcode.InvalidArgument, "command ID was previously used with different request ID or outcome")
		}
		return inf.result, nil
	case <-ctx.Done():
		return AnswerInteractionResult{}, fmt.Errorf("context canceled: %w", ctx.Err())
	}
}

func (s *InteractionService) executeAnswer(cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
	ticket, err := s.owner.LookupRequest(cmd.RequestID)
	if err != nil {
		return AnswerInteractionResult{}, errcode.New(errcode.InteractionNotFound, "interaction request not found")
	}
	if ticket.Resolved {
		return AnswerInteractionResult{}, errcode.New(errcode.InteractionAlreadyAnswered, "interaction request has already been answered")
	}
	if lerr := s.checkAnswerLimits(cmd, ticket); lerr != nil {
		return AnswerInteractionResult{}, lerr
	}

	domainAnswers := make([]interaction.Answer, 0, len(cmd.Answers))
	for _, a := range cmd.Answers {
		domainAnswers = append(domainAnswers, interaction.Answer{
			QuestionID: a.QuestionID,
			OptionIDs:  a.OptionIDs,
			Text:       a.Text,
		})
	}
	decision := &interaction.Decision{
		Incarnation: cmd.Incarnation,
		RequestID:   cmd.RequestID,
		Outcome:     interaction.Outcome(cmd.Outcome),
		OptionID:    cmd.OptionID,
		Text:        cmd.Text,
		Answers:     domainAnswers,
	}

	resolvedTicket, err := s.owner.Answer(decision)
	if err != nil {
		return AnswerInteractionResult{}, mapOwnerError(err)
	}

	return AnswerInteractionResult{
		RequestID: resolvedTicket.RequestID,
		Outcome:   string(resolvedTicket.Outcome),
	}, nil
}

// ExpireExpired resolves any overdue pending requests and emits resolved events.
func (s *InteractionService) ExpireExpired() []*interaction.Ticket {
	return s.owner.ExpireExpired()
}

// PruneResolved prunes non-relayed resolved tickets past retention TTL.
func (s *InteractionService) PruneResolved() {
	s.owner.PruneResolved()
}

// CancelPending cancels every pending interaction wait so a graceful drain
// never hangs on an unanswered approval.
func (s *InteractionService) CancelPending() {
	s.owner.CancelPending()
}

// TakeRelayed delivers a resolved relayed request once.
func (s *InteractionService) TakeRelayed(operationID string) (*interaction.Ticket, error) {
	ticket, err := s.owner.TakeRelayed(operationID)
	if err != nil {
		return nil, mapOwnerError(err)
	}
	return ticket, nil
}

func mapOwnerError(err error) error {
	switch {
	case errors.Is(err, interaction.ErrRequestNotFound) || errors.Is(err, interaction.ErrOperationNotFound):
		return errcode.New(errcode.InteractionNotFound, "interaction request not found")
	case errors.Is(err, interaction.ErrAlreadyAnswered):
		return errcode.New(errcode.InteractionAlreadyAnswered, "interaction request has already been answered")
	case errors.Is(err, interaction.ErrStaleIncarnation):
		return errcode.New(errcode.InteractionStaleIncarnation, "interaction request belongs to a previous incarnation")
	case errors.Is(err, interaction.ErrOptionNotOffered) || errors.Is(err, interaction.ErrOutcomeNotOffered) ||
		errors.Is(err, interaction.ErrSessionGrantDenied) || errors.Is(err, interaction.ErrRelayedGrantDenied):
		return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
	case errors.Is(err, interaction.ErrTextTooLarge):
		return errcode.New(errcode.InteractionTextTooLarge, "interaction text exceeds size limit")
	default:
		return err
	}
}
