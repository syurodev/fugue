package app

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func hasSecretQuestionItem(questions []vendor.ServerRequestQuestionItem) bool {
	for _, q := range questions {
		if q.Secret {
			return true
		}
	}
	return false
}

func nonSecretCodexAnswers(questions []vendor.ServerRequestQuestionItem, answers map[string][]string) map[string][]string {
	secretIDs := make(map[string]struct{}, len(questions))
	for _, q := range questions {
		if q.Secret {
			secretIDs[q.ID] = struct{}{}
		}
	}
	out := make(map[string][]string, len(answers))
	for qid, vals := range answers {
		if _, isSec := secretIDs[qid]; isSec {
			continue
		}
		out[qid] = vals
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *SessionToolService) recordCodexApprovalDecided(ctx context.Context, binding SessionBinding, turnID, opID string, req *vendor.ServerRequest, choice *codexRelayChoice) error {
	if s.recorder == nil || s.ids == nil {
		return errcode.New(errcode.FailedPrecondition, approveUnavailableMessage)
	}
	itemID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate approval event ID", err)
	}
	var persistedAnswers map[string][]string
	if len(choice.decision.Answers) > 0 && !hasSecretQuestionItem(req.Questions) {
		persistedAnswers = choice.decision.Answers
	} else if len(choice.decision.Answers) > 0 {
		persistedAnswers = nonSecretCodexAnswers(req.Questions, choice.decision.Answers)
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
		Answers:     persistedAnswers,
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
				Decider:     decidedByUser,
			},
		},
	}); recErr != nil {
		return errcode.Wrap(errcode.Internal, "record approval decision", recErr)
	}
	return nil
}
