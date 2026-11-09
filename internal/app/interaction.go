package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// PendingInteractionView is the domain view of a pending request, carrying
// the fields permitted by interaction §8 plus additive multi-question support.
type PendingInteractionView struct {
	RequestID       string                `json:"request_id"`
	Incarnation     string                `json:"incarnation"`
	SessionID       string                `json:"session_id"`
	Tool            string                `json:"tool"`
	Kind            string                `json:"kind"`
	Risk            string                `json:"risk"`
	EnvTag          string                `json:"env_tag"`
	Summary         string                `json:"summary"`
	OfferedOutcomes []string              `json:"offered_outcomes,omitempty"`
	Options         []InteractionOption   `json:"options,omitempty"`
	GrantScopeLabel string                `json:"grant_scope_label,omitempty"`
	Questions       []InteractionQuestion `json:"questions,omitempty"`
}

// InteractionOption is an option offered to the user in a pending interaction.
type InteractionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// InteractionQuestion is one question within a question-kind interaction request.
type InteractionQuestion struct {
	QuestionID  string              `json:"question_id"`
	Prompt      string              `json:"prompt"`
	Options     []InteractionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multi_select,omitempty"`
	AllowText   bool                `json:"allow_text,omitempty"`
}

// InteractionAnswer is an answer to a question in an interaction request.
type InteractionAnswer struct {
	QuestionID string
	OptionIDs  []string
	Text       string
}

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
	owner *interaction.Owner
	topic *interactionTopic
	hub   *EventHub
	dedup *commandDedupCache
	clk   clock.Clock
	inc   string

	cmdMu    sync.Mutex
	inflight map[string]*inflightCmd

	seenMu       sync.Mutex
	seenRequests map[string]struct{}
}

// NewInteractionService creates an InteractionService and binds it to owner and hub.
func NewInteractionService(owner *interaction.Owner, hub *EventHub, clk clock.Clock) (*InteractionService, error) {
	if owner == nil {
		return nil, errcode.New(errcode.InvalidArgument, "interaction owner is required")
	}
	if clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "clock is required")
	}

	topic := newInteractionTopic(hub, owner.Pending)
	if hub != nil {
		if err := hub.RegisterDurable(TopicInteraction, topic); err != nil {
			return nil, err
		}
	}

	s := &InteractionService{
		owner:        owner,
		topic:        topic,
		hub:          hub,
		dedup:        newCommandDedupCache(clk, defaultDedupCapacity, defaultDedupTTL),
		clk:          clk,
		inc:          owner.Incarnation(),
		inflight:     map[string]*inflightCmd{},
		seenRequests: map[string]struct{}{},
	}
	return s, nil
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

	s.topic.mu.Lock()
	defer s.topic.mu.Unlock()

	ticket, err := s.owner.Ensure(spec)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	s.seenMu.Lock()
	_, seen := s.seenRequests[ticket.RequestID]
	if !seen {
		s.seenRequests[ticket.RequestID] = struct{}{}
	}
	s.seenMu.Unlock()

	if !seen && !ticket.Resolved {
		view := viewFromTicket(ticket)
		if perr := s.topic.publishRequestedLocked(&view); perr != nil {
			return nil, perr
		}
	} else if !seen && ticket.Resolved {
		if perr := s.topic.publishResolvedLocked(ticket.RequestID, string(ticket.Outcome)); perr != nil {
			return nil, perr
		}
	}
	return ticket, nil
}

// ListPending returns the pending interaction views and the topic sequence.
// Only local principals may list pending interactions (host-api §7).
func (s *InteractionService) ListPending(_ context.Context, principal string) ([]PendingInteractionView, EventSeq, error) {
	if !IsLocalPrincipal(principal) {
		return nil, 0, errcode.New(errcode.PermissionDenied, "only local principals may list pending interactions")
	}
	return s.topic.pendingSnapshot()
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
		return prev, nil
	}
	if inf, ok := s.inflight[cmd.CommandID]; ok {
		s.cmdMu.Unlock()
		select {
		case <-inf.done:
			return inf.result, inf.err
		case <-ctx.Done():
			return AnswerInteractionResult{}, fmt.Errorf("context canceled: %w", ctx.Err())
		}
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

func (s *InteractionService) executeAnswer(cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
	s.topic.mu.Lock()
	defer s.topic.mu.Unlock()

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

	if perr := s.topic.publishResolvedLocked(resolvedTicket.RequestID, string(resolvedTicket.Outcome)); perr != nil {
		return AnswerInteractionResult{}, errcode.Wrap(errcode.Internal, "publish resolved event", perr)
	}

	s.seenMu.Lock()
	delete(s.seenRequests, resolvedTicket.RequestID)
	s.seenMu.Unlock()

	return AnswerInteractionResult{
		RequestID: resolvedTicket.RequestID,
		Outcome:   string(resolvedTicket.Outcome),
	}, nil
}

// TakeRelayed delivers a resolved relayed request once.
func (s *InteractionService) TakeRelayed(operationID string) (*interaction.Ticket, error) {
	ticket, err := s.owner.TakeRelayed(operationID)
	if err != nil {
		return nil, mapOwnerError(err)
	}
	s.seenMu.Lock()
	delete(s.seenRequests, ticket.RequestID)
	s.seenMu.Unlock()
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

func hasOption(options []interaction.Option, id string) bool {
	return slices.ContainsFunc(options, func(opt interaction.Option) bool {
		return opt.ID == id
	})
}

func viewFromTicket(t *interaction.Ticket) PendingInteractionView {
	summary := t.Summary
	if summary == "" && len(t.Questions) > 0 {
		summary = t.Questions[0].Question
	}

	rawOptions := t.Options
	if len(rawOptions) == 0 && len(t.Questions) > 0 {
		rawOptions = t.Questions[0].Options
	}

	options := make([]InteractionOption, 0, len(rawOptions))
	for _, o := range rawOptions {
		options = append(options, InteractionOption{
			ID:          o.ID,
			Label:       o.Label,
			Description: o.Description,
		})
	}

	questions := make([]InteractionQuestion, 0, len(t.Questions))
	for _, q := range t.Questions {
		qOpts := make([]InteractionOption, 0, len(q.Options))
		for _, o := range q.Options {
			qOpts = append(qOpts, InteractionOption{
				ID:          o.ID,
				Label:       o.Label,
				Description: o.Description,
			})
		}
		questions = append(questions, InteractionQuestion{
			QuestionID:  q.ID,
			Prompt:      q.Question,
			Options:     qOpts,
			MultiSelect: q.MultiSelect,
			AllowText:   true,
		})
	}

	return PendingInteractionView{
		RequestID:       t.RequestID,
		Incarnation:     t.Incarnation,
		SessionID:       t.Binding.SessionID,
		Tool:            t.Binding.ToolName,
		Kind:            string(t.Kind),
		Risk:            t.Risk,
		EnvTag:          t.Binding.EnvTag,
		Summary:         summary,
		OfferedOutcomes: offeredOutcomesFor(t),
		Options:         options,
		GrantScopeLabel: t.GrantScopeLabel,
		Questions:       questions,
	}
}
