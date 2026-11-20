package client

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	interactionv1 "github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/interaction/v1/interactionv1connect"
)

// TopicInteraction and its event names mirror the host's interaction topic
// contract (interaction §8, host-api §6).
const (
	TopicInteraction          = "interaction"
	EventInteractionRequested = "requested"
	EventInteractionResolved  = "resolved"
)

// Supported interaction request kinds and user-visible outcomes (interaction
// §4.1, §8).
const (
	InteractionKindConfirm  = "confirm"
	InteractionKindSelect   = "select"
	InteractionKindQuestion = "question"

	InteractionOutcomeAllowedOnce    = "allowed_once"
	InteractionOutcomeAllowedSession = "allowed_session"
	InteractionOutcomeDenied         = "denied"
	InteractionOutcomeAnswered       = "answered"
	InteractionOutcomeCancelled      = "cancelled"
	InteractionOutcomeExpired        = "expired"
	InteractionOutcomeUnavailable    = "unavailable"
)

// InteractionOption is one selectable choice offered by a select request, a
// question, or a relayed vendor request. Vendor labels pass through untouched.
type InteractionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// InteractionQuestion is one question within a question-kind interaction
// request. Prompts and options pass through untouched.
type InteractionQuestion struct {
	QuestionID  string              `json:"question_id"`
	Prompt      string              `json:"prompt"`
	Options     []InteractionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multi_select,omitempty"`
	AllowText   bool                `json:"allow_text,omitempty"`
}

// PendingInteraction is the Host API view of one pending approval, selection,
// or question request pinned to (RequestID, Incarnation).
type PendingInteraction struct {
	RequestID       string                `json:"request_id"`
	Incarnation     string                `json:"incarnation"`
	SessionID       string                `json:"session_id"`
	Tool            string                `json:"tool"`
	Kind            string                `json:"kind"`
	Risk            string                `json:"risk"`
	EnvTag          string                `json:"env_tag"`
	Summary         string                `json:"summary"`
	Detail          string                `json:"detail,omitempty"`
	Body            string                `json:"body,omitempty"`
	OfferedOutcomes []string              `json:"offered_outcomes,omitempty"`
	Options         []InteractionOption   `json:"options,omitempty"`
	GrantScopeLabel string                `json:"grant_scope_label,omitempty"`
	Questions       []InteractionQuestion `json:"questions,omitempty"`
}

// PendingInteractionList is the Answer to ListPendingInteractions: every pending
// request of the current host incarnation and the interaction topic sequence.
type PendingInteractionList struct {
	Requests []PendingInteraction
	Sequence uint64
}

// InteractionQuestionAnswer carries the user's reply to one question of a
// question-kind interaction request.
type InteractionQuestionAnswer struct {
	QuestionID string
	OptionIDs  []string
	Text       string
}

// AnswerInteractionInput carries one human decision for a pinned
// (RequestID, Incarnation) request.
type AnswerInteractionInput struct {
	CommandID   string
	Incarnation string
	RequestID   string
	Outcome     string
	OptionID    string
	Text        string
	Answers     []InteractionQuestionAnswer
}

// InteractionAnswerResult confirms the outcome recorded by the host.
type InteractionAnswerResult struct {
	RequestID string
	Outcome   string
}

// InteractionResolvedEvent is the payload of an `interaction` topic `resolved`
// event (metadata only, no content).
type InteractionResolvedEvent struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
}

// ListPendingInteractions reads the snapshot of pending interaction requests
// for the current host incarnation and the matching topic sequence.
func (c *Client) ListPendingInteractions(ctx context.Context) (PendingInteractionList, error) {
	res, err := interactionv1connect.NewInteractionServiceClient(c.hc, c.base).ListPending(ctx,
		connect.NewRequest(&interactionv1.ListPendingRequest{}))
	if err != nil {
		return PendingInteractionList{}, fmt.Errorf("list pending interactions: %w", err)
	}
	reqs := res.Msg.GetRequests()
	out := PendingInteractionList{
		Requests: make([]PendingInteraction, 0, len(reqs)),
		Sequence: res.Msg.GetSequence(),
	}
	for _, r := range reqs {
		out.Requests = append(out.Requests, pendingInteractionFromProto(r))
	}
	return out, nil
}

// AnswerInteraction submits a human decision for the pinned
// (RequestID, Incarnation) pair.
func (c *Client) AnswerInteraction(ctx context.Context, in *AnswerInteractionInput) (InteractionAnswerResult, error) {
	req := &interactionv1.AnswerRequest{
		CommandId:   in.CommandID,
		Incarnation: in.Incarnation,
		RequestId:   in.RequestID,
		Outcome:     in.Outcome,
		Answers:     protoQuestionAnswers(in.Answers),
	}
	if in.OptionID != "" {
		opt := in.OptionID
		req.OptionId = &opt
	}
	if in.Text != "" {
		txt := in.Text
		req.Text = &txt
	}
	res, err := interactionv1connect.NewInteractionServiceClient(c.hc, c.base).Answer(ctx, connect.NewRequest(req))
	if err != nil {
		return InteractionAnswerResult{}, fmt.Errorf("answer interaction: %w", err)
	}
	return InteractionAnswerResult{
		RequestID: res.Msg.GetRequestId(),
		Outcome:   res.Msg.GetOutcome(),
	}, nil
}

// InteractionCodeOf returns the public error code carried by a failed
// interaction RPC error, for example interaction.stale_incarnation.
func InteractionCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// DecodeInteractionSnapshot decodes the `interaction` topic snapshot payload.
func DecodeInteractionSnapshot(payload []byte) ([]PendingInteraction, error) {
	var out []PendingInteraction
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode interaction snapshot: %w", err)
	}
	return out, nil
}

// DecodeInteractionRequested decodes an `interaction` topic `requested` event.
func DecodeInteractionRequested(payload []byte) (PendingInteraction, error) {
	var out PendingInteraction
	if err := json.Unmarshal(payload, &out); err != nil {
		return PendingInteraction{}, fmt.Errorf("decode interaction requested event: %w", err)
	}
	return out, nil
}

// DecodeInteractionResolved decodes an `interaction` topic `resolved` event.
func DecodeInteractionResolved(payload []byte) (InteractionResolvedEvent, error) {
	var out InteractionResolvedEvent
	if err := json.Unmarshal(payload, &out); err != nil {
		return InteractionResolvedEvent{}, fmt.Errorf("decode interaction resolved event: %w", err)
	}
	return out, nil
}

func pendingInteractionFromProto(r *interactionv1.PendingRequest) PendingInteraction {
	if r == nil {
		return PendingInteraction{}
	}
	return PendingInteraction{
		RequestID:       r.GetRequestId(),
		Incarnation:     r.GetIncarnation(),
		SessionID:       r.GetSessionId(),
		Tool:            r.GetTool(),
		Kind:            r.GetKind(),
		Risk:            r.GetRisk(),
		EnvTag:          r.GetEnvTag(),
		Summary:         r.GetSummary(),
		Detail:          r.GetDetail(),
		Body:            r.GetBody(),
		OfferedOutcomes: slices.Clone(r.GetOfferedOutcomes()),
		Options:         interactionOptionsFromProto(r.GetOptions()),
		GrantScopeLabel: r.GetGrantScopeLabel(),
		Questions:       interactionQuestionsFromProto(r.GetQuestions()),
	}
}

func interactionOptionsFromProto(opts []*interactionv1.Option) []InteractionOption {
	if len(opts) == 0 {
		return nil
	}
	out := make([]InteractionOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, InteractionOption{
			ID:          o.GetId(),
			Label:       o.GetLabel(),
			Description: o.GetDescription(),
		})
	}
	return out
}

func interactionQuestionsFromProto(qs []*interactionv1.Question) []InteractionQuestion {
	if len(qs) == 0 {
		return nil
	}
	out := make([]InteractionQuestion, 0, len(qs))
	for _, q := range qs {
		out = append(out, InteractionQuestion{
			QuestionID:  q.GetQuestionId(),
			Prompt:      q.GetPrompt(),
			Options:     interactionOptionsFromProto(q.GetOptions()),
			MultiSelect: q.GetMultiSelect(),
			AllowText:   q.GetAllowText(),
		})
	}
	return out
}

func protoQuestionAnswers(answers []InteractionQuestionAnswer) []*interactionv1.QuestionAnswer {
	if len(answers) == 0 {
		return nil
	}
	out := make([]*interactionv1.QuestionAnswer, 0, len(answers))
	for _, a := range answers {
		out = append(out, &interactionv1.QuestionAnswer{
			QuestionId: a.QuestionID,
			OptionIds:  slices.Clone(a.OptionIDs),
			Text:       a.Text,
		})
	}
	return out
}
