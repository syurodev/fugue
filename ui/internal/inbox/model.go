package inbox

import (
	"errors"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/pkg/client"
)

var errUnknownInteractionEvent = errors.New("unknown interaction topic event")

// Pin uniquely identifies one pending interaction ticket by (RequestID, Incarnation).
// An answer is dispatched only for an exact matching pair.
type Pin struct{ RequestID, Incarnation string }

// Valid reports whether both parts of the pin are non-empty.
func (p Pin) Valid() bool { return p.RequestID != "" && p.Incarnation != "" }

// String formats the pin for diagnostics without content.
func (p Pin) String() string { return p.Incarnation + "/" + p.RequestID }

// Model holds pending interaction tickets for the current host incarnation.
type Model struct {
	incarnation string
	seq         uint64
	hasSnapshot bool
	items       map[Pin]client.PendingInteraction
	order       []Pin
}

// NewModel builds an empty inbox model.
func NewModel() *Model { return &Model{items: map[Pin]client.PendingInteraction{}} }

// Incarnation returns the host incarnation this model currently tracks.
func (m *Model) Incarnation() string { return m.incarnation }

// Sequence returns the highest interaction topic sequence reflected in the model.
func (m *Model) Sequence() uint64 { return m.seq }

// PendingCount returns the total number of pending tickets.
func (m *Model) PendingCount() int { return len(m.order) }

// PendingCountForSession returns the number of pending tickets for sessionID.
func (m *Model) PendingCountForSession(sessionID string) int {
	if sessionID == "" {
		return 0
	}
	count := 0
	for _, pin := range m.order {
		if m.items[pin].SessionID == sessionID {
			count++
		}
	}
	return count
}

// Items returns the pending interactions in arrival order.
func (m *Model) Items() []client.PendingInteraction {
	out := make([]client.PendingInteraction, 0, len(m.order))
	for _, pin := range m.order {
		out = append(out, m.items[pin])
	}
	return out
}

// Lookup returns the pending ticket for pin if it belongs to the active incarnation.
func (m *Model) Lookup(pin Pin) (client.PendingInteraction, bool) {
	if !pin.Valid() || pin.Incarnation != m.incarnation {
		return client.PendingInteraction{}, false
	}
	item, ok := m.items[pin]
	return item, ok
}

// CanAnswer reports whether pin is still pending in the active incarnation.
func (m *Model) CanAnswer(pin Pin) bool {
	_, ok := m.Lookup(pin)
	return ok
}

// Remove deletes pin from the model when resolved or stale.
func (m *Model) Remove(pin Pin) {
	if _, ok := m.items[pin]; !ok {
		return
	}
	delete(m.items, pin)
	m.order = slices.DeleteFunc(m.order, func(p Pin) bool { return p == pin })
}

// ApplyTopicSnapshot reconciles the model with an `interaction` topic snapshot.
func (m *Model) ApplyTopicSnapshot(incarnation string, seq uint64, payload []byte) error {
	reqs, err := client.DecodeInteractionSnapshot(payload)
	if err != nil {
		return fmt.Errorf("apply interaction topic snapshot: %w", err)
	}
	m.reconcileSnapshot(incarnation, seq, reqs)
	return nil
}

// ApplyListPending reconciles the model with a ListPending RPC snapshot.
func (m *Model) ApplyListPending(incarnation string, list client.PendingInteractionList) {
	m.reconcileSnapshot(incarnation, list.Sequence, list.Requests)
}

func (m *Model) reconcileSnapshot(incarnation string, seq uint64, reqs []client.PendingInteraction) {
	if incarnation == "" && len(reqs) > 0 {
		incarnation = reqs[0].Incarnation
	}
	if incarnation != "" && incarnation != m.incarnation {
		m.incarnation, m.seq, m.hasSnapshot = incarnation, 0, false
		clear(m.items)
		m.order = nil
	}
	if m.hasSnapshot && seq < m.seq {
		return
	}
	m.hasSnapshot, m.seq = true, seq
	m.items = make(map[Pin]client.PendingInteraction, len(reqs))
	m.order = make([]Pin, 0, len(reqs))
	for i := range reqs {
		pin := Pin{RequestID: reqs[i].RequestID, Incarnation: reqs[i].Incarnation}
		if !pin.Valid() || (m.incarnation != "" && pin.Incarnation != m.incarnation) {
			continue
		}
		if _, dup := m.items[pin]; !dup {
			m.order = append(m.order, pin)
		}
		m.items[pin] = reqs[i]
	}
}

// ApplyTopicEvent folds one `interaction` topic event (`requested` or `resolved`).
func (m *Model) ApplyTopicEvent(seq uint64, eventType string, payload []byte) error {
	if m.hasSnapshot && seq > 0 && seq <= m.seq {
		return nil
	}
	switch eventType {
	case client.EventInteractionRequested:
		req, err := client.DecodeInteractionRequested(payload)
		if err != nil {
			return fmt.Errorf("apply interaction requested event: %w", err)
		}
		m.applyRequested(&req)
	case client.EventInteractionResolved:
		ev, err := client.DecodeInteractionResolved(payload)
		if err != nil {
			return fmt.Errorf("apply interaction resolved event: %w", err)
		}
		m.removeByRequestID(ev.RequestID)
	default:
		return fmt.Errorf("%w: %q", errUnknownInteractionEvent, eventType)
	}
	if seq > m.seq {
		m.seq = seq
	}
	return nil
}

func (m *Model) applyRequested(req *client.PendingInteraction) {
	pin := Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
	if !pin.Valid() {
		return
	}
	if m.incarnation == "" {
		m.incarnation = pin.Incarnation
	}
	if pin.Incarnation != m.incarnation {
		return
	}
	if _, exists := m.items[pin]; !exists {
		m.order = append(m.order, pin)
	}
	m.items[pin] = *req
}

func (m *Model) removeByRequestID(requestID string) {
	if requestID == "" {
		return
	}
	if m.incarnation != "" {
		m.Remove(Pin{RequestID: requestID, Incarnation: m.incarnation})
		return
	}
	for _, pin := range slices.Clone(m.order) {
		if pin.RequestID == requestID {
			m.Remove(pin)
		}
	}
}
