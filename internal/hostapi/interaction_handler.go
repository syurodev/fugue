package hostapi

import (
	"context"
	"slices"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"
)

// interactionHandler maps the interaction proto contract to the app backend.
// It implements interactionv1connect.InteractionServiceHandler.
type interactionHandler struct {
	backend app.Backend
}

func (h *interactionHandler) ListPending(ctx context.Context, _ *connect.Request[interactionv1.ListPendingRequest]) (*connect.Response[interactionv1.ListPendingResponse], error) {
	st, serr := connectionState(ctx)
	if serr != nil {
		return nil, toConnectError(ctx, h.backend, serr)
	}
	if err := requireHelloed(ctx); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	principal := string(st.principal())
	if !app.IsLocalPrincipal(principal) {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.PermissionDenied, "only local principals may list pending interactions"))
	}

	views, seq, err := h.backend.ListPendingInteractions(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}

	protoReqs := make([]*interactionv1.PendingRequest, 0, len(views))
	for i := range views {
		v := &views[i]
		opts := make([]*interactionv1.Option, 0, len(v.Options))
		for _, o := range v.Options {
			opts = append(opts, &interactionv1.Option{
				Id:          o.ID,
				Label:       o.Label,
				Description: o.Description,
			})
		}
		protoReqs = append(protoReqs, &interactionv1.PendingRequest{
			RequestId:       v.RequestID,
			Incarnation:     v.Incarnation,
			SessionId:       v.SessionID,
			Tool:            v.Tool,
			Kind:            v.Kind,
			Risk:            v.Risk,
			EnvTag:          v.EnvTag,
			Summary:         v.Summary,
			OfferedOutcomes: v.OfferedOutcomes,
			Options:         opts,
			GrantScopeLabel: v.GrantScopeLabel,
		})
	}

	return connect.NewResponse(&interactionv1.ListPendingResponse{
		Requests: protoReqs,
		Sequence: seq,
	}), nil
}

func (h *interactionHandler) Answer(ctx context.Context, req *connect.Request[interactionv1.AnswerRequest]) (*connect.Response[interactionv1.AnswerResponse], error) {
	principal, err := h.authAnswer(ctx, req)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}

	text, err := extractAndValidateText(req.Msg)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}

	answers, err := buildDomainAnswers(req.Msg.Answers)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}

	var optionID string
	if req.Msg.OptionId != nil {
		optionID = *req.Msg.OptionId
	}

	res, err := h.backend.AnswerInteraction(ctx, &app.AnswerInteractionCommand{
		CommandID:   req.Msg.CommandId,
		Principal:   principal,
		Incarnation: req.Msg.Incarnation,
		RequestID:   req.Msg.RequestId,
		Outcome:     req.Msg.Outcome,
		OptionID:    optionID,
		Text:        text,
		Answers:     answers,
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}

	return connect.NewResponse(&interactionv1.AnswerResponse{
		RequestId: res.RequestID,
		Outcome:   res.Outcome,
	}), nil
}

func (h *interactionHandler) authAnswer(ctx context.Context, req *connect.Request[interactionv1.AnswerRequest]) (string, error) {
	st, serr := connectionState(ctx)
	if serr != nil {
		return "", serr
	}
	if err := requireHelloed(ctx); err != nil {
		return "", err
	}
	principal := string(st.principal())
	if !app.IsLocalPrincipal(principal) {
		return "", errcode.New(errcode.PermissionDenied, "only local principals may answer interaction requests")
	}
	if req.Msg == nil {
		return "", errcode.New(errcode.InvalidArgument, "interaction answer request is required")
	}
	if req.Msg.CommandId == "" {
		return "", errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if req.Msg.RequestId == "" {
		return "", errcode.New(errcode.InvalidArgument, "interaction request ID is required")
	}
	return principal, nil
}

func extractAndValidateText(msg *interactionv1.AnswerRequest) (string, error) {
	if msg.Text == nil {
		return "", nil
	}
	text := *msg.Text
	if msg.Outcome == app.OutcomeDenied && len(text) > app.MaxInteractionNoteBytes {
		return "", errcode.New(errcode.InteractionTextTooLarge, "interaction deny note exceeds size limit")
	}
	if msg.Outcome != app.OutcomeDenied && len(text) > app.MaxInteractionAnswerBytes {
		return "", errcode.New(errcode.InteractionTextTooLarge, "interaction answer text exceeds size limit")
	}
	return text, nil
}

func buildDomainAnswers(protoAnswers []*interactionv1.QuestionAnswer) ([]app.InteractionAnswer, error) {
	if len(protoAnswers) > app.MaxInteractionOptions {
		return nil, errcode.New(errcode.InvalidArgument, "number of question answers exceeds limit")
	}
	answers := make([]app.InteractionAnswer, 0, len(protoAnswers))
	for _, a := range protoAnswers {
		if len(a.Text) > app.MaxInteractionAnswerBytes {
			return nil, errcode.New(errcode.InteractionTextTooLarge, "interaction question answer text exceeds size limit")
		}
		if len(a.OptionIds) > app.MaxInteractionOptions {
			return nil, errcode.New(errcode.InvalidArgument, "number of selected options exceeds limit")
		}
		answers = append(answers, app.InteractionAnswer{
			QuestionID: a.QuestionId,
			OptionIDs:  slices.Clone(a.OptionIds),
			Text:       a.Text,
		})
	}
	return answers, nil
}

var _ interactionv1connect.InteractionServiceHandler = (*interactionHandler)(nil)
