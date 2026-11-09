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

const defaultInteractionTopicCap = 256

// resolvedPayload is the payload of an EventInteractionResolved event. It
// contains only metadata and outcome: no summary, prompt, answer or note
// content is ever included (interaction §7, §8, I-E08).
type resolvedPayload struct {
	RequestID string `json:"request_id"`
	Outcome   string `json:"outcome"`
}

// interactionTopic is the DurableTopic implementation for TopicInteraction.
type interactionTopic struct {
	hub      *EventHub
	provider func() []*interaction.Ticket
	capacity int

	mu     sync.Mutex
	seq    EventSeq
	events []Event
}

func newInteractionTopic(hub *EventHub, provider func() []*interaction.Ticket) *interactionTopic {
	return &interactionTopic{
		hub:      hub,
		provider: provider,
		capacity: defaultInteractionTopicCap,
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

// Since returns the committed events newer than seq (DurableTopic port). If seq
// is older than the retained events ring, ErrEventSeqTooOld is returned so the
// subscriber resynchronizes via a fresh snapshot.
func (t *interactionTopic) Since(_ context.Context, seq EventSeq) ([]Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.events) == 0 {
		if seq == t.seq {
			return nil, nil
		}
		if seq < t.seq {
			return nil, ErrEventSeqTooOld
		}
		return nil, nil
	}

	oldest := t.events[0].Seq
	if seq < oldest-1 {
		return nil, ErrEventSeqTooOld
	}

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

func (t *interactionTopic) appendRingLocked(ev Event) {
	if len(t.events) < t.capacity {
		t.events = append(t.events, ev)
		return
	}
	copy(t.events, t.events[1:])
	t.events[len(t.events)-1] = ev
}

// publishRequestedLocked records a requested event and fans it out via the hub.
// Must be called with t.mu held.
func (t *interactionTopic) publishRequestedLocked(view *PendingInteractionView) error {
	payload, err := json.Marshal(view)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal requested event", err)
	}

	t.seq++
	ev := Event{
		Seq:     t.seq,
		Type:    EventInteractionRequested,
		Payload: payload,
	}
	t.appendRingLocked(ev)

	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}

// publishResolvedLocked records a resolved event and fans it out via the hub.
// Must be called with t.mu held.
func (t *interactionTopic) publishResolvedLocked(requestID, outcome string) error {
	payload, err := json.Marshal(resolvedPayload{
		RequestID: requestID,
		Outcome:   outcome,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal resolved event", err)
	}

	t.seq++
	ev := Event{
		Seq:     t.seq,
		Type:    EventInteractionResolved,
		Payload: payload,
	}
	t.appendRingLocked(ev)

	if t.hub != nil {
		if _, perr := t.hub.Publish(TopicInteraction, ev); perr != nil {
			return perr
		}
	}
	return nil
}
