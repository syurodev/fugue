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
		protoReqs = append(protoReqs, protoPendingRequest(&views[i]))
	}

	return connect.NewResponse(&interactionv1.ListPendingResponse{
		Requests: protoReqs,
		Sequence: seq,
	}), nil
}

func protoOptions(opts []app.InteractionOption) []*interactionv1.Option {
	out := make([]*interactionv1.Option, 0, len(opts))
	for _, o := range opts {
		out = append(out, &interactionv1.Option{
			Id:          o.ID,
			Label:       o.Label,
			Description: o.Description,
		})
	}
	return out
}

func protoQuestions(qs []app.InteractionQuestion) []*interactionv1.Question {
	out := make([]*interactionv1.Question, 0, len(qs))
	for _, q := range qs {
		out = append(out, &interactionv1.Question{
			QuestionId:  q.QuestionID,
			Prompt:      q.Prompt,
			Options:     protoOptions(q.Options),
			MultiSelect: q.MultiSelect,
			AllowText:   q.AllowText,
		})
	}
	return out
}

func protoPendingRequest(v *app.PendingInteractionView) *interactionv1.PendingRequest {
	return &interactionv1.PendingRequest{
		RequestId:       v.RequestID,
		Incarnation:     v.Incarnation,
		SessionId:       v.SessionID,
		Tool:            v.Tool,
		Kind:            v.Kind,
		Risk:            v.Risk,
		EnvTag:          v.EnvTag,
		Summary:         v.Summary,
		OfferedOutcomes: v.OfferedOutcomes,
		Options:         protoOptions(v.Options),
		GrantScopeLabel: v.GrantScopeLabel,
		Questions:       protoQuestions(v.Questions),
	}
}

func (h *interactionHandler) Answer(ctx context.Context, req *connect.Request[interactionv1.AnswerRequest]) (*connect.Response[interactionv1.AnswerResponse], error) {
	principal, err := h.authAnswer(ctx, req)
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
		Text:        extractText(req.Msg),
		Answers:     buildDomainAnswers(req.Msg.Answers),
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
	return principal, nil
}

func extractText(msg *interactionv1.AnswerRequest) string {
	if msg.Text == nil {
		return ""
	}
	return *msg.Text
}

func buildDomainAnswers(protoAnswers []*interactionv1.QuestionAnswer) []app.InteractionAnswer {
	if len(protoAnswers) == 0 {
		return nil
	}
	answers := make([]app.InteractionAnswer, 0, len(protoAnswers))
	for _, a := range protoAnswers {
		answers = append(answers, app.InteractionAnswer{
			QuestionID: a.QuestionId,
			OptionIDs:  slices.Clone(a.OptionIds),
			Text:       a.Text,
		})
	}
	return answers
}

var _ interactionv1connect.InteractionServiceHandler = (*interactionHandler)(nil)
