package app

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TopicInteraction is the durable topic carrying interaction request lifecycle
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

// interactionTopic is the DurableTopic implementation for TopicInteraction.
type interactionTopic struct {
	hub *EventHub

	mu       sync.Mutex
	seq      EventSeq
	events   []Event
	provider func() []*interaction.Ticket
}

func newInteractionTopic(hub *EventHub, provider func() []*interaction.Ticket) *interactionTopic {
	return &interactionTopic{
		hub:      hub,
		provider: provider,
	}
}

// Snapshot returns the current pending interaction requests and the topic
// sequence that matches them (DurableTopic port). The read is atomic under the
// topic mutex so snapshot and catch-up have no gap.
func (t *interactionTopic) Snapshot(_ context.Context) ([]byte, EventSeq, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	views := t.currentViewsLocked()
	data, err := json.Marshal(views)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal interaction snapshot", err)
	}
	return data, t.seq, nil
}

// Since returns the committed events newer than seq (DurableTopic port).
func (t *interactionTopic) Since(_ context.Context, seq EventSeq) ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []Event
	for _, ev := range t.events {
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// pendingSnapshot returns the current pending views and the topic sequence.
func (t *interactionTopic) pendingSnapshot() ([]PendingInteractionView, EventSeq, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	views := t.currentViewsLocked()
	return views, t.seq, nil
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

// publishRequested records a requested event and fans it out via the hub.
func (t *interactionTopic) publishRequested(view *PendingInteractionView) error {
	payload, err := json.Marshal(view)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal requested event", err)
	}

	t.mu.Lock()
	t.seq++
	ev := Event{
		Seq:     t.seq,
		Type:    EventInteractionRequested,
		Payload: payload,
	}
	t.events = append(t.events, ev)
	t.mu.Unlock()

	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}

// publishResolved records a resolved event and fans it out via the hub.
func (t *interactionTopic) publishResolved(requestID, outcome string) error {
	payload, err := json.Marshal(resolvedPayload{
		RequestID: requestID,
		Outcome:   outcome,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal resolved event", err)
	}

	t.mu.Lock()
	t.seq++
	ev := Event{
		Seq:     t.seq,
		Type:    EventInteractionResolved,
		Payload: payload,
	}
	t.events = append(t.events, ev)
	t.mu.Unlock()

	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}
