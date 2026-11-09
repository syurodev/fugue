package app

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ListPendingInteractions returns the pending interaction views and the sequence of
// the interaction topic.
func (rt *HostRuntime) ListPendingInteractions(ctx context.Context, principal string) ([]PendingInteractionView, EventSeq, error) {
	if rt.interactions == nil {
		return nil, 0, errcode.New(errcode.Unavailable, "interaction service is unavailable")
	}
	return rt.interactions.ListPending(ctx, principal)
}

// AnswerInteraction records a decision for a pending interaction request.
func (rt *HostRuntime) AnswerInteraction(ctx context.Context, cmd *AnswerInteractionCommand) (AnswerInteractionResult, error) {
	if rt.interactions == nil {
		return AnswerInteractionResult{}, errcode.New(errcode.Unavailable, "interaction service is unavailable")
	}
	return rt.interactions.Answer(ctx, cmd)
}

// SetInteractions configures the interaction service on the runtime.
func (rt *HostRuntime) SetInteractions(is *InteractionService) {
	rt.interactions = is
}

// Interactions returns the interaction service bound to this runtime.
func (rt *HostRuntime) Interactions() *InteractionService {
	return rt.interactions
}
