package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TopicVendorSessions is the durable session-list topic (Q-58 A): a snapshot
// of every session and its state, then created and state-changed events. The
// sequence is the store's cross-session change journal, so catch-up from a
// cursor survives a host restart. Payloads carry identity and state only.
const TopicVendorSessions = "vendor-sessions"

// The vendor-sessions event types.
const (
	EventVendorSessionCreated      = "vendor_session.created"
	EventVendorSessionStateChanged = "vendor_session.state_changed"
)

// vendorSessionChange is one event payload.
type vendorSessionChange struct {
	SessionID        string `json:"session_id"`
	State            string `json:"state"`
	WaitingReason    string `json:"waiting_reason,omitempty"`
	CommandsRevision int64  `json:"commands_revision"`
}

type vendorSessionsSnapshot struct {
	Sessions []VendorSessionView `json:"sessions"`
}

// vendorSessionsTopic serves snapshot and catch-up from the store and
// publishes committed changes in journal order.
type vendorSessionsTopic struct {
	store     *vendorstore.Store
	hub       *EventHub
	mu        sync.Mutex
	published EventSeq
}

func newVendorSessionsTopic(ctx context.Context, store *vendorstore.Store, hub *EventHub) (*vendorSessionsTopic, error) {
	_, last, err := store.SessionList(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the vendor session journal: %w", err)
	}
	published, err := storeSeq(last)
	if err != nil {
		return nil, err
	}
	t := &vendorSessionsTopic{store: store, hub: hub, published: published}
	if err := hub.RegisterDurable(TopicVendorSessions, t); err != nil {
		return nil, err
	}
	return t, nil
}

// Snapshot answers from one read transaction: the list reflects exactly the
// returned sequence.
func (t *vendorSessionsTopic) Snapshot(ctx context.Context) ([]byte, EventSeq, error) {
	sessions, last, err := t.store.SessionList(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("read the vendor session list: %w", err)
	}
	seq, err := storeSeq(last)
	if err != nil {
		return nil, 0, err
	}
	snap := vendorSessionsSnapshot{Sessions: make([]VendorSessionView, 0, len(sessions))}
	for i := range sessions {
		snap.Sessions = append(snap.Sessions, vendorSessionView(&sessions[i]))
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal the vendor sessions snapshot", err)
	}
	return payload, seq, nil
}

// Since maps the change journal onto events; the journal is never trimmed.
func (t *vendorSessionsTopic) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	cursor, err := storeCursor(seq)
	if err != nil {
		return nil, err
	}
	var out []Event
	for {
		changes, readErr := t.store.SessionChangesSince(ctx, cursor, eventPageLimit)
		if readErr != nil {
			return nil, fmt.Errorf("read the vendor session changes: %w", readErr)
		}
		for i := range changes {
			event, eventErr := vendorChangeEvent(&changes[i])
			if eventErr != nil {
				return nil, eventErr
			}
			out = append(out, event)
		}
		if len(changes) < eventPageLimit {
			return out, nil
		}
		cursor = changes[len(changes)-1].Sequence
	}
}

func vendorChangeEvent(change *vendorstore.SessionChange) (Event, error) {
	seq, err := storeSeq(change.Sequence)
	if err != nil {
		return Event{}, err
	}
	eventType := EventVendorSessionStateChanged
	if change.Kind == vendorstore.ChangeCreated {
		eventType = EventVendorSessionCreated
	}
	return marshalEvent(eventType, seq, vendorSessionChange{SessionID: change.SessionID, State: change.State, WaitingReason: change.WaitingReason, CommandsRevision: change.Sequence})
}

// notify publishes every committed change not yet published. Commits happen
// before notify runs, so a published event is always durable.
func (t *vendorSessionsTopic) notify(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	events, err := t.Since(ctx, t.published)
	if err != nil {
		slog.WarnContext(ctx, "reading vendor session changes for publish failed", "error_code", string(errcode.CodeOf(err)))
		return
	}
	for _, event := range events {
		if _, err := t.hub.Publish(TopicVendorSessions, event); err != nil {
			slog.WarnContext(ctx, "vendor session event publish failed", "error_code", string(errcode.CodeOf(err)))
			return
		}
		t.published = event.Seq
	}
}

// topicNotifyingStore is the actor's store: after a committed creation or
// state change it lets the topic publish, without the actor knowing the topic.
type topicNotifyingStore struct {
	*vendorstore.Store
	topic *vendorSessionsTopic
}

func (s topicNotifyingStore) CreateSession(ctx context.Context, session *vendorstore.Session) error {
	if err := s.Store.CreateSession(ctx, session); err != nil {
		return fmt.Errorf("create vendor session: %w", err)
	}
	s.topic.notify(ctx)
	return nil
}

func (s topicNotifyingStore) Append(ctx context.Context, sessionID string, expected int64, event *vendorstore.Event) (int64, error) {
	next, err := s.Store.Append(ctx, sessionID, expected, event)
	if err != nil {
		return 0, fmt.Errorf("append vendor event: %w", err)
	}
	if event.State != "" || event.Type == vendorstore.VendorSessionClosed {
		s.topic.notify(ctx)
	}
	return next, nil
}
