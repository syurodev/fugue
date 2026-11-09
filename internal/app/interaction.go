package app

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// PendingInteractionView is the domain view of a pending request, carrying
// exactly the 11 fields permitted by interaction §8.
type PendingInteractionView struct {
	RequestID       string              `json:"request_id"`
	Incarnation     string              `json:"incarnation"`
	SessionID       string              `json:"session_id"`
	Tool            string              `json:"tool"`
	Kind            string              `json:"kind"`
	Risk            string              `json:"risk"`
	EnvTag          string              `json:"env_tag"`
	Summary         string              `json:"summary"`
	OfferedOutcomes []string            `json:"offered_outcomes,omitempty"`
	Options         []InteractionOption `json:"options,omitempty"`
	GrantScopeLabel string              `json:"grant_scope_label,omitempty"`
}

// InteractionOption is an option offered to the user in a pending interaction.
type InteractionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
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

const (
	defaultDedupCapacity = 1024
	defaultDedupTTL      = 10 * time.Minute
)

type dedupRecord struct {
	commandID string
	result    AnswerInteractionResult
	cachedAt  time.Time
}

type commandDedupCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	clock    clock.Clock
	entries  map[string]AnswerInteractionResult
	records  []dedupRecord
}

func newCommandDedupCache(clk clock.Clock, capacity int, ttl time.Duration) *commandDedupCache {
	if capacity <= 0 {
		capacity = defaultDedupCapacity
	}
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	return &commandDedupCache{
		capacity: capacity,
		ttl:      ttl,
		clock:    clk,
		entries:  map[string]AnswerInteractionResult{},
	}
}

func (c *commandDedupCache) Get(commandID string) (AnswerInteractionResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	res, ok := c.entries[commandID]
	return res, ok
}

func (c *commandDedupCache) Put(commandID string, result AnswerInteractionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now().UTC()
	c.evictExpiredLocked(now)

	if len(c.entries) >= c.capacity && len(c.records) > 0 {
		oldest := c.records[0]
		c.records = c.records[1:]
		delete(c.entries, oldest.commandID)
	}

	c.entries[commandID] = result
	c.records = append(c.records, dedupRecord{
		commandID: commandID,
		result:    result,
		cachedAt:  now,
	})
}

func (c *commandDedupCache) evictExpiredLocked(now time.Time) {
	cutoff := now.Add(-c.ttl)
	validIdx := 0
	for i, r := range c.records {
		if !r.cachedAt.Before(cutoff) {
			validIdx = i
			break
		}
		delete(c.entries, r.commandID)
	}
	if validIdx > 0 {
		c.records = c.records[validIdx:]
	}
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
		seenRequests: map[string]struct{}{},
	}
	return s, nil
}

// Incarnation returns the host incarnation ID this service belongs to.
func (s *InteractionService) Incarnation() string {
	return s.inc
}

// Ensure creates or reads back an interaction request and publishes a requested
// event on the interaction topic when a new request is opened.
func (s *InteractionService) Ensure(spec *interaction.Spec) (*interaction.Ticket, error) {
	ticket, err := s.owner.Ensure(spec)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "ensure interaction request", err)
	}

	s.seenMu.Lock()
	_, seen := s.seenRequests[ticket.RequestID]
	if !seen {
		s.seenRequests[ticket.RequestID] = struct{}{}
	}
	s.seenMu.Unlock()

	if !seen && !ticket.Resolved {
		view := viewFromTicket(ticket)
		if perr := s.topic.publishRequested(&view); perr != nil {
			return nil, perr
		}
	} else if !seen && ticket.Resolved {
		if perr := s.topic.publishResolved(ticket.RequestID, string(ticket.Outcome)); perr != nil {
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
	if cmd.Incarnation != "" && cmd.Incarnation != s.inc {
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
func (s *InteractionService) Answer(_ context.Context, cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
	if err := s.validateAnswerCmd(cmd); err != nil {
		return AnswerInteractionResult{}, err
	}
	if prev, ok := s.dedup.Get(cmd.CommandID); ok {
		return prev, nil
	}

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

	if perr := s.topic.publishResolved(resolvedTicket.RequestID, string(resolvedTicket.Outcome)); perr != nil {
		return AnswerInteractionResult{}, errcode.Wrap(errcode.Internal, "publish resolved event", perr)
	}

	res := AnswerInteractionResult{
		RequestID: resolvedTicket.RequestID,
		Outcome:   string(resolvedTicket.Outcome),
	}
	s.dedup.Put(cmd.CommandID, res)
	return res, nil
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
	code := errcode.CodeOf(err)
	msg := err.Error()
	switch {
	case code == errcode.NotFound:
		return errcode.New(errcode.InteractionNotFound, "interaction request not found")
	case strings.Contains(msg, "already been answered"):
		return errcode.New(errcode.InteractionAlreadyAnswered, "interaction request has already been answered")
	case strings.Contains(msg, "previous incarnation"):
		return errcode.New(errcode.InteractionStaleIncarnation, "interaction request belongs to a previous incarnation")
	case strings.Contains(msg, "not offered") || strings.Contains(msg, "session grants"):
		return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
	case strings.Contains(msg, "exceeds its size limit"):
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
	}
}

// NewTestInteractionService builds an InteractionService for testing without storage.
func NewTestInteractionService(clk clock.Clock, idGen ids.Generator, hub *EventHub) (*InteractionService, error) {
	owner, err := interaction.NewOwner(interaction.Config{
		Clock: clk,
		IDs:   idGen,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "create test interaction owner", err)
	}
	return NewInteractionService(owner, hub, clk)
}

// EnsureTestConfirm is a test helper for creating a confirm interaction request.
func (s *InteractionService) EnsureTestConfirm(opID, summary string) (string, error) {
	spec := &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Risk:        "write",
		Summary:     summary,
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID:       "sess-1",
			InvocationID:    "inv-1",
			AttemptID:       "att-1",
			CheckGeneration: 1,
			ToolName:        "file_writer",
			EnvTag:          "dev",
		},
		GrantScopeLabel: "workspace write",
	}
	ticket, err := s.Ensure(spec)
	if err != nil {
		return "", err
	}
	return ticket.RequestID, nil
}

// EnsureTestRelayed is a test helper for creating a relayed confirm interaction request.
func (s *InteractionService) EnsureTestRelayed(sessionID, turnID, vendorReqID, summary string) (string, error) {
	opID, err := interaction.VendorOperationKey(sessionID, turnID, vendorReqID)
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "create test relayed operation key", err)
	}
	spec := &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     summary,
		Relayed:     true,
		Options: []interaction.Option{
			{ID: "allow", Label: "Allow"},
			{ID: "deny", Label: "Deny"},
		},
		Binding: interaction.Binding{
			SessionID:       sessionID,
			TurnID:          turnID,
			VendorRequestID: vendorReqID,
		},
	}
	ticket, err := s.Ensure(spec)
	if err != nil {
		return "", err
	}
	return ticket.RequestID, nil
}
