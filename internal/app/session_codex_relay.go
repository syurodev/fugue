package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	"github.com/syurodev/fugue/internal/vendorstore"
)

var errRelayUnavailable = errors.New(approveUnavailableMessage)

type serverRequestRelayer interface {
	RelayServerRequest(ctx context.Context, sessionID string, req *vendor.ServerRequest, session vendor.Session)
	CancelTurn(sessionID, turnID string)
}

type codexRelayChoice struct {
	decision       vendor.Decision
	selectedOption string
	decisionLabel  string
	note           string
}

// CancelTurn closes pending interaction tickets for (sessionID, turnID) when
// the user interrupts a turn.
func (s *SessionToolService) CancelTurn(sessionID, turnID string) {
	if s.interactions != nil {
		s.interactions.CancelTurn(sessionID, turnID)
	}
}

// RelayServerRequest relays a Codex server request (item/*/requestApproval or
// item/tool/requestUserInput) through the interaction owner and sends the
// user's verbatim choice (or a JSON-RPC error response on any abort/rejection)
// back through session.Resolve at most once (VR-05, interaction v0.16).
func (s *SessionToolService) RelayServerRequest(
	ctx context.Context,
	sessionID string,
	req *vendor.ServerRequest,
	session vendor.Session,
) {
	if req == nil || session == nil || req.RequestID == "" {
		return
	}
	binding, turnID, opID, digest, ok := s.prepareCodexRelay(ctx, sessionID, req)
	if !ok {
		resolveCodexRelayError(ctx, session, req.RequestID)
		return
	}
	if s.replayCodexTombstone(ctx, binding, opID, digest, req.RequestID, session) {
		return
	}
	if !s.tombstones.tryBeginInFlight(binding.SessionID, opID) {
		return
	}
	defer s.tombstones.finishInFlight(binding.SessionID, opID)
	s.executeCodexRelay(ctx, binding, turnID, opID, digest, req, session)
}

func (s *SessionToolService) executeCodexRelay(
	ctx context.Context,
	binding SessionBinding,
	turnID, opID string,
	digest [sha256.Size]byte,
	req *vendor.ServerRequest,
	session vendor.Session,
) {
	spec, err := buildCodexRelaySpec(binding.SessionID, turnID, opID, req)
	if err != nil {
		resolveCodexRelayError(ctx, session, req.RequestID)
		return
	}
	choice, ok := s.awaitAndTakeCodexRelayed(ctx, binding, opID, digest, req, spec)
	if !ok || !s.canApproveNow(binding) {
		resolveCodexRelayError(ctx, session, req.RequestID)
		return
	}
	if !choice.decision.Cancelled {
		if err := s.recordCodexApprovalDecided(ctx, binding, turnID, opID, req, &choice); err != nil || !s.canApproveNow(binding) {
			resolveCodexRelayError(ctx, session, req.RequestID)
			return
		}
	}
	s.recordRelayTombstone(binding.SessionID, opID, digest, req.Method, &choice)
	if err := session.Resolve(context.WithoutCancel(ctx), vendor.ApprovalRef{ID: req.RequestID}, choice.decision); err != nil {
		return
	}
}

func (s *SessionToolService) recordRelayTombstone(
	sessionID, opID string,
	digest [sha256.Size]byte,
	method string,
	choice *codexRelayChoice,
) {
	if isAgyPermissionMethod(method) {
		s.tombstones.putAgy(sessionID, opID, digest, choice.selectedOption, choice.decision.Cancelled)
		return
	}
	s.tombstones.putCodex(sessionID, opID, digest, choice.decision.Allow, choice.selectedOption, choice.decision.Answers)
}

func isAgyPermissionMethod(method string) bool {
	return method == agy.MethodSessionRequestPermission
}

func resolveCodexRelayError(ctx context.Context, session vendor.Session, requestID string) {
	if err := session.Resolve(context.WithoutCancel(ctx), vendor.ApprovalRef{ID: requestID}, vendor.Decision{
		Error: errRelayUnavailable,
	}); err != nil {
		return
	}
}

func (s *SessionToolService) prepareCodexRelay(
	ctx context.Context,
	sessionID string,
	req *vendor.ServerRequest,
) (SessionBinding, string, string, [sha256.Size]byte, bool) {
	binding, ok := s.tokens.bindingFor(sessionID)
	if !ok || len(req.Detail) > interaction.MaxRelayedDetailBytes || !s.canApproveNow(binding) {
		return SessionBinding{}, "", "", [sha256.Size]byte{}, false
	}
	turnID, err := s.recorder.ActiveTurnID(ctx, binding.SessionID)
	if err != nil || turnID == "" {
		return SessionBinding{}, "", "", [sha256.Size]byte{}, false
	}
	opID, err := interaction.VendorOperationKey(binding.SessionID, turnID, req.RequestID)
	if err != nil {
		return SessionBinding{}, "", "", [sha256.Size]byte{}, false
	}
	return binding, turnID, opID, approveArgsDigest(req.Method, req.Detail), true
}

func (s *SessionToolService) replayCodexTombstone(
	ctx context.Context,
	binding SessionBinding,
	opID string,
	digest [sha256.Size]byte,
	requestID string,
	session vendor.Session,
) bool {
	entry, ok := s.tombstones.get(binding.SessionID, opID)
	if !ok {
		return false
	}
	if entry.digest != digest || !s.canApproveNow(binding) {
		resolveCodexRelayError(ctx, session, requestID)
		return true
	}
	if err := session.Resolve(context.WithoutCancel(ctx), vendor.ApprovalRef{ID: requestID}, codexDecisionFromTombstone(&entry)); err != nil {
		return true
	}
	return true
}

func buildCodexRelaySpec(sessionID, turnID, opID string, req *vendor.ServerRequest) (*interaction.Spec, error) {
	summary := req.Summary
	if summary == "" {
		summary = formatApproveSummary(req.Method, req.Detail)
	}
	binding := interaction.Binding{
		SessionID:       sessionID,
		TurnID:          turnID,
		VendorRequestID: req.RequestID,
	}
	switch req.Kind {
	case vendor.ServerRequestConfirm:
		options := buildConfirmRelayOptions(req)
		return &interaction.Spec{
			OperationID: opID,
			Kind:        interaction.KindConfirm,
			Summary:     summary,
			Detail:      req.Detail,
			Relayed:     true,
			Options:     options,
			ExpiresAt:   req.ExpiresAt,
			Binding:     binding,
		}, nil
	case vendor.ServerRequestQuestion:
		questions := buildCodexRelayQuestions(req)
		return &interaction.Spec{
			OperationID: opID,
			Kind:        interaction.KindQuestion,
			Summary:     summary,
			Relayed:     true,
			Questions:   questions,
			ExpiresAt:   req.ExpiresAt,
			Binding:     binding,
		}, nil
	default:
		return nil, errcode.New(errcode.InvalidArgument, "unsupported server request kind")
	}
}

func buildConfirmRelayOptions(req *vendor.ServerRequest) []interaction.Option {
	if len(req.OptionDetails) > 0 {
		options := make([]interaction.Option, 0, len(req.OptionDetails))
		for _, opt := range req.OptionDetails {
			options = append(options, interaction.Option{
				ID:          opt.ID,
				Label:       opt.Name,
				Description: opt.Kind,
			})
		}
		return options
	}
	options := make([]interaction.Option, 0, len(req.Options))
	for _, opt := range req.Options {
		options = append(options, interaction.Option{ID: opt, Label: opt})
	}
	return options
}

func buildCodexRelayQuestions(req *vendor.ServerRequest) []interaction.Question {
	if len(req.Questions) > 0 {
		out := make([]interaction.Question, 0, len(req.Questions))
		for _, q := range req.Questions {
			opts := make([]interaction.Option, 0, len(q.Options))
			for _, o := range q.Options {
				id := o.Key
				if id == "" {
					id = o.Label
				}
				opts = append(opts, interaction.Option{
					ID:          id,
					Label:       o.Label,
					Description: o.Description,
				})
			}
			out = append(out, interaction.Question{
				ID:       q.ID,
				Header:   q.Header,
				Question: q.Question,
				Options:  opts,
			})
		}
		return out
	}
	opts := make([]interaction.Option, 0, len(req.QuestionOptions))
	for _, o := range req.QuestionOptions {
		id := o.Key
		if id == "" {
			id = o.Label
		}
		opts = append(opts, interaction.Option{ID: id, Label: o.Label, Description: o.Description})
	}
	qID := req.ItemID
	if qID == "" {
		qID = req.RequestID
	}
	qText := req.Summary
	if qText == "" {
		qText = "Question"
	}
	return []interaction.Question{{
		ID:       qID,
		Question: qText,
		Options:  opts,
	}}
}

func (s *SessionToolService) awaitAndTakeCodexRelayed(
	ctx context.Context,
	binding SessionBinding,
	opID string,
	digest [sha256.Size]byte,
	req *vendor.ServerRequest,
	spec *interaction.Spec,
) (codexRelayChoice, bool) {
	ticket, err := s.interactions.Ensure(spec)
	if err != nil {
		return codexRelayChoice{}, false
	}
	resolved, err := s.interactions.awaitResolvedUntil(ctx, ticket.RequestID, s.tokens.watch(binding))
	if err != nil || resolved == nil || !resolved.Resolved {
		if !s.tokens.live(binding) {
			if _, invErr := s.interactions.owner.InvalidateOperation(opID); invErr != nil {
				return codexRelayChoice{}, false
			}
		}
		return codexRelayChoice{}, false
	}
	if !s.canApproveNow(binding) {
		return codexRelayChoice{}, false
	}
	if _, ok := translateServerRelayedOutcome(resolved, req); !ok {
		return codexRelayChoice{}, false
	}
	delivered, err := s.interactions.TakeRelayed(opID)
	if err != nil {
		return s.fallbackCodexRelayedTombstone(err, binding, opID, digest, req.Kind)
	}
	return translateServerRelayedOutcome(delivered, req)
}

func translateServerRelayedOutcome(ticket *interaction.Ticket, req *vendor.ServerRequest) (codexRelayChoice, bool) {
	if req != nil && isAgyPermissionMethod(req.Method) {
		return translateAgyRelayedOutcome(ticket)
	}
	var kind vendor.ServerRequestKind
	if req != nil {
		kind = req.Kind
	}
	return translateCodexRelayedOutcome(ticket, kind)
}

func translateAgyRelayedOutcome(ticket *interaction.Ticket) (codexRelayChoice, bool) {
	switch ticket.Outcome {
	case interaction.OutcomeAnswered:
		if ticket.SelectedOptionID == "" {
			return codexRelayChoice{}, false
		}
		return codexRelayChoice{
			decision:       vendor.Decision{Allow: true, Message: ticket.SelectedOptionID},
			selectedOption: ticket.SelectedOptionID,
			decisionLabel:  ticket.SelectedOptionID,
			note:           ticket.Text,
		}, true
	case interaction.OutcomeCancelled:
		if !ticket.TurnCancelled {
			return codexRelayChoice{}, false
		}
		return codexRelayChoice{
			decision: vendor.Decision{Cancelled: true},
		}, true
	case interaction.OutcomeDenied, interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession,
		interaction.OutcomeUnavailable, interaction.OutcomeExpired:
		return codexRelayChoice{}, false
	default:
		return codexRelayChoice{}, false
	}
}

func codexTombstoneLabel(entry *approveTombstoneEntry, kind vendor.ServerRequestKind) string {
	if entry.cancelled {
		return ""
	}
	if kind == vendor.ServerRequestQuestion {
		if entry.allow {
			return string(interaction.OutcomeAnswered)
		}
		return string(interaction.OutcomeDenied)
	}
	if entry.selectedOption != "" {
		return entry.selectedOption
	}
	if entry.allow {
		return codex.DecisionAccept
	}
	return codex.DecisionDecline
}

func (s *SessionToolService) fallbackCodexRelayedTombstone(
	err error,
	binding SessionBinding,
	opID string,
	digest [sha256.Size]byte,
	kind vendor.ServerRequestKind,
) (codexRelayChoice, bool) {
	code := errcode.CodeOf(err)
	if code != errcode.NotFound && code != errcode.InteractionNotFound {
		return codexRelayChoice{}, false
	}
	entry, ok := s.tombstones.get(binding.SessionID, opID)
	if !ok || entry.digest != digest || !s.canApproveNow(binding) {
		return codexRelayChoice{}, false
	}
	return codexRelayChoice{
		decision:       codexDecisionFromTombstone(&entry),
		selectedOption: entry.selectedOption,
		decisionLabel:  codexTombstoneLabel(&entry, kind),
	}, true
}

func translateCodexQuestionAnswers(ticket *interaction.Ticket) map[string][]string {
	answers := make(map[string][]string, len(ticket.Answers))
	for _, ans := range ticket.Answers {
		var vals []string
		vals = append(vals, ans.OptionIDs...)
		if ans.Text != "" {
			vals = append(vals, ans.Text)
		}
		if len(vals) > 0 {
			answers[ans.QuestionID] = vals
		}
	}
	return answers
}

func translateCodexRelayedOutcome(ticket *interaction.Ticket, kind vendor.ServerRequestKind) (codexRelayChoice, bool) {
	switch ticket.Outcome {
	case interaction.OutcomeAnswered:
		if kind == vendor.ServerRequestQuestion {
			return codexRelayChoice{
				decision:      vendor.Decision{Allow: true, Answers: translateCodexQuestionAnswers(ticket)},
				decisionLabel: string(interaction.OutcomeAnswered),
			}, true
		}
		if ticket.SelectedOptionID == "" {
			return codexRelayChoice{}, false
		}
		allow := ticket.SelectedOptionID != codex.DecisionDecline && ticket.SelectedOptionID != codex.DecisionCancel
		return codexRelayChoice{
			decision:       vendor.Decision{Allow: allow, Message: ticket.SelectedOptionID},
			selectedOption: ticket.SelectedOptionID,
			decisionLabel:  ticket.SelectedOptionID,
			note:           ticket.Text,
		}, true
	case interaction.OutcomeDenied:
		if kind == vendor.ServerRequestQuestion {
			return codexRelayChoice{
				decision:      vendor.Decision{Allow: false},
				decisionLabel: string(interaction.OutcomeDenied),
				note:          ticket.Text,
			}, true
		}
		return codexRelayChoice{
			decision:       vendor.Decision{Allow: false, Message: codex.DecisionDecline},
			selectedOption: codex.DecisionDecline,
			decisionLabel:  codex.DecisionDecline,
			note:           ticket.Text,
		}, true
	case interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession,
		interaction.OutcomeCancelled, interaction.OutcomeUnavailable, interaction.OutcomeExpired:
		return codexRelayChoice{}, false
	default:
		return codexRelayChoice{}, false
	}
}

func (s *SessionToolService) recordCodexApprovalDecided(
	ctx context.Context,
	binding SessionBinding,
	turnID, opID string,
	req *vendor.ServerRequest,
	choice *codexRelayChoice,
) error {
	if s.recorder == nil || s.ids == nil {
		return errcode.New(errcode.FailedPrecondition, approveUnavailableMessage)
	}
	itemID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate approval event ID", err)
	}
	sealedBody, err := json.Marshal(struct {
		OperationID string              `json:"operation_id"`
		Method      string              `json:"method"`
		RequestID   string              `json:"request_id"`
		Decision    string              `json:"decision"`
		Note        string              `json:"note,omitempty"`
		Answers     map[string][]string `json:"answers,omitempty"`
	}{
		OperationID: opID,
		Method:      req.Method,
		RequestID:   req.RequestID,
		Decision:    choice.decisionLabel,
		Note:        choice.note,
		Answers:     choice.decision.Answers,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal approval decision", err)
	}
	if recErr := s.recorder.RecordEvent(ctx, binding.SessionID, &vendorstore.Event{
		Type:    vendorstore.ApprovalDecided,
		TurnID:  turnID,
		Payload: sealedBody,
		Item: &vendorstore.Item{
			ID:     itemID,
			TurnID: turnID,
			Kind:   vendorstore.KindApproval,
			Metadata: vendorstore.ItemMetadata{
				RequestKind: req.Method,
				Decision:    choice.decisionLabel,
				Decider:     "user",
			},
		},
	}); recErr != nil {
		return errcode.Wrap(errcode.Internal, "record approval decision", recErr)
	}
	return nil
}
