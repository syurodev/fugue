package hostapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const fakeKindConfirm = "confirm"

type fakeInteractionTopic struct{ backend *fakeBackend }

func (t *fakeInteractionTopic) Snapshot(_ context.Context) ([]byte, app.EventSeq, error) {
	t.backend.pendingMu.Lock()
	defer t.backend.pendingMu.Unlock()
	views := make([]app.PendingInteractionView, 0, len(t.backend.pending))
	for k := range t.backend.pending {
		views = append(views, t.backend.pending[k])
	}
	data, err := json.Marshal(views)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal views: %w", err)
	}
	return data, t.backend.pendingSeq, nil
}

func (t *fakeInteractionTopic) Since(_ context.Context, _ app.EventSeq) ([]app.Event, error) {
	return nil, nil
}

func (f *fakeBackend) ListPendingInteractions(_ context.Context, principal string) ([]app.PendingInteractionView, app.EventSeq, error) {
	if !app.IsLocalPrincipal(principal) {
		return nil, 0, errcode.New(errcode.PermissionDenied, "only local principals may list pending interactions")
	}
	f.pendingMu.Lock()
	defer f.pendingMu.Unlock()
	views := make([]app.PendingInteractionView, 0, len(f.pending))
	for k := range f.pending {
		views = append(views, f.pending[k])
	}
	return views, f.pendingSeq, nil
}

func validateFakeAnswerCmd(cmd *app.AnswerInteractionCommand, inc string) error {
	if !app.IsLocalPrincipal(cmd.Principal) {
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
	if inc != "" && cmd.Incarnation != inc {
		return errcode.New(errcode.InteractionStaleIncarnation, "interaction request belongs to a previous incarnation")
	}
	return nil
}

func validateFakeAnswerSizes(cmd *app.AnswerInteractionCommand) error {
	if cmd.Outcome == app.OutcomeDenied && len(cmd.Text) > app.MaxInteractionNoteBytes {
		return errcode.New(errcode.InteractionTextTooLarge, "interaction deny note exceeds size limit")
	}
	if cmd.Outcome != app.OutcomeDenied && len(cmd.Text) > app.MaxInteractionAnswerBytes {
		return errcode.New(errcode.InteractionTextTooLarge, "interaction answer text exceeds size limit")
	}
	for _, a := range cmd.Answers {
		if len(a.Text) > app.MaxInteractionAnswerBytes {
			return errcode.New(errcode.InteractionTextTooLarge, "question answer text exceeds size limit")
		}
	}
	return nil
}

func validateFakeAnswerPending(req *app.PendingInteractionView, cmd *app.AnswerInteractionCommand) error {
	if len(req.OfferedOutcomes) > 0 && !slices.Contains(req.OfferedOutcomes, cmd.Outcome) {
		return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
	}
	if cmd.Outcome == app.OutcomeAllowedSession && req.Kind == fakeKindConfirm && req.GrantScopeLabel == "" {
		return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
	}
	if cmd.OptionID != "" {
		found := false
		for _, opt := range req.Options {
			if opt.ID == cmd.OptionID {
				found = true
				break
			}
		}
		if !found {
			return errcode.New(errcode.InteractionOptionNotOffered, "option not offered")
		}
	}
	if len(req.Questions) > 0 && len(cmd.Answers) != len(req.Questions) {
		return errcode.New(errcode.InvalidArgument, "question decision must answer every question exactly once")
	}
	return validateFakeAnswerSizes(cmd)
}

func (f *fakeBackend) AnswerInteraction(_ context.Context, cmd *app.AnswerInteractionCommand) (app.AnswerInteractionResult, error) {
	f.pendingMu.Lock()
	defer f.pendingMu.Unlock()
	f.answerCalls++

	if err := validateFakeAnswerCmd(cmd, f.inc); err != nil {
		return app.AnswerInteractionResult{}, err
	}
	if prev, ok := f.dedup[cmd.CommandID]; ok {
		return prev, nil
	}

	req, ok := f.pending[cmd.RequestID]
	if !ok {
		if _, wasAnswered := f.answered[cmd.RequestID]; wasAnswered {
			return app.AnswerInteractionResult{}, errcode.New(errcode.InteractionAlreadyAnswered, "interaction request has already been answered")
		}
		return app.AnswerInteractionResult{}, errcode.New(errcode.InteractionNotFound, "interaction request not found")
	}

	if err := validateFakeAnswerPending(&req, cmd); err != nil {
		return app.AnswerInteractionResult{}, err
	}

	delete(f.pending, cmd.RequestID)
	f.answered[cmd.RequestID] = cmd.Outcome
	f.pendingSeq++

	if f.hub != nil {
		payload, err := json.Marshal(map[string]string{
			"request_id": cmd.RequestID,
			"outcome":    cmd.Outcome,
		})
		if err != nil {
			return app.AnswerInteractionResult{}, fmt.Errorf("marshal payload: %w", err)
		}
		if _, perr := f.hub.Publish(app.TopicInteraction, app.Event{
			Seq:     f.pendingSeq,
			Type:    "resolved",
			Payload: payload,
		}); perr != nil {
			return app.AnswerInteractionResult{}, fmt.Errorf("publish event: %w", perr)
		}
	}

	res := app.AnswerInteractionResult{RequestID: cmd.RequestID, Outcome: cmd.Outcome}
	f.dedup[cmd.CommandID] = res
	return res, nil
}
