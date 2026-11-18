package app

// The answer path of the interaction service: one answered request resolves
// exactly once, the answer carries the team card decisions (F11.2b), and the
// command dedup makes a retried answer idempotent.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

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

	res, err = s.executeAnswer(ctx, cmd)
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

func (s *InteractionService) executeAnswer(ctx context.Context, cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
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

	// A team card validates before it resolves: an invalid decision must
	// leave the proposal pending with its card still answerable (F11.2b).
	if s.teamCards != nil && isTeamCard(ticket) {
		if verr := s.teamCards.validateTeamCardAnswer(ctx, ticket, decision); verr != nil {
			return AnswerInteractionResult{}, verr
		}
	}

	resolvedTicket, err := s.owner.Answer(decision)
	if err != nil {
		return AnswerInteractionResult{}, mapOwnerError(err)
	}

	// The card is resolved, so the decision now applies; a transient store
	// failure is retried, and a permanent one fails the RPC truthfully while
	// the durable state stays pending (fail closed, F11.2b).
	if s.teamCards != nil && isTeamCard(ticket) {
		if aerr := s.teamCards.applyTeamCardAnswer(ctx, resolvedTicket, decision); aerr != nil {
			return AnswerInteractionResult{}, aerr
		}
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

// CancelPending cancels every pending interaction wait on drain.
func (s *InteractionService) CancelPending() {
	s.owner.CancelPending()
}

// CancelTurn closes all pending interaction requests belonging to (sessionID, turnID).
func (s *InteractionService) CancelTurn(sessionID, turnID string) {
	s.owner.CancelTurn(sessionID, turnID)
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
