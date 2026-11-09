package app

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TopicInteraction is the ephemeral topic carrying interaction request lifecycle
// events: new requests and resolved decisions (host-api §6, interaction §8).
const TopicInteraction = "interaction"

// Event types published on TopicInteraction.
const (
	EventInteractionRequested = "requested"
	EventInteractionResolved  = "resolved"
)

// resolvedPayload is the payload of an EventInteractionResolved event. It
// contains only metadata and outcome: no summary, prompt, answer or note
// content is ever included (interaction §7, §8, I-E08).
type resolvedPayload struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
}

// interactionTopic coordinates snapshot and event publishing for TopicInteraction.
type interactionTopic struct {
	hub      *EventHub
	provider func() []*interaction.Ticket

	mu sync.Mutex
}

func newInteractionTopic(hub *EventHub, provider func() []*interaction.Ticket) *interactionTopic {
	return &interactionTopic{
		hub:      hub,
		provider: provider,
	}
}

// snapshot returns the current pending interaction requests serialized as JSON
// for the ephemeral topic snapshot.
func (t *interactionTopic) snapshot(_ context.Context) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	views := t.currentViewsLocked()
	data, err := json.Marshal(views)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal interaction snapshot", err)
	}
	return data, nil
}

func (t *interactionTopic) currentViewsLocked() []PendingInteractionView {
	if t.provider == nil {
		return []PendingInteractionView{}
	}
	tickets := t.provider()
	views := make([]PendingInteractionView, 0, len(tickets))
	for _, ticket := range tickets {
		views = append(views, viewFromTicket(ticket))
	}
	return views
}

// publishRequestedLocked publishes a requested event via the hub.
// Must be called with t.mu held.
func (t *interactionTopic) publishRequestedLocked(view *PendingInteractionView) error {
	payload, err := json.Marshal(view)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal requested event", err)
	}

	ev := Event{
		Type:    EventInteractionRequested,
		Payload: payload,
	}
	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}

// publishResolvedLocked publishes a resolved event via the hub.
// Must be called with t.mu held.
func (t *interactionTopic) publishResolvedLocked(requestID, outcome string) error {
	payload, err := json.Marshal(resolvedPayload{
		RequestID: requestID,
		Outcome:   outcome,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal resolved event", err)
	}

	ev := Event{
		Type:    EventInteractionResolved,
		Payload: payload,
	}
	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}
