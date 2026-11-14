package session

import (
	"encoding/json"
	"fmt"

	"github.com/syurodev/fugue/pkg/client"
)

// The vendor-sessions topic's event types and payload shapes. The host writes
// identity and state only (Q-58 A); a created event carries no vendor or
// timestamps, so the sidebar learns them from the next list read.
const (
	EventSessionCreated      = "vendor_session.created"
	EventSessionStateChanged = "vendor_session.state_changed"
)

// sessionView is one session in the host's snapshot payload.
type sessionView struct {
	SessionID     string `json:"session_id"`
	Vendor        string `json:"vendor"`
	State         string `json:"state"`
	WaitingReason string `json:"waiting_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// sessionsSnapshot is the topic's snapshot payload.
type sessionsSnapshot struct {
	Sessions []sessionView `json:"sessions"`
}

// sessionChange is one created or state-changed event payload.
type sessionChange struct {
	SessionID        string `json:"session_id"`
	State            string `json:"state"`
	WaitingReason    string `json:"waiting_reason,omitempty"`
	CommandsRevision int64  `json:"commands_revision"`
}

// Session is one sidebar row: a vendor session's identity and state.
type Session struct {
	ID, Vendor, State, WaitingReason, CreatedAt, UpdatedAt string
}

// Sidebar is the session-list model: the sessions the vendor-sessions topic
// reports, in list order. It changes only on the window goroutine.
type Sidebar struct {
	byID  map[string]*Session
	order []string
}

// NewSidebar builds an empty sidebar.
func NewSidebar() *Sidebar {
	return &Sidebar{byID: map[string]*Session{}}
}

// ApplySnapshot replaces the list with the snapshot payload's sessions. A
// payload the host did not write is an error the caller surfaces.
func (s *Sidebar) ApplySnapshot(payload []byte) error {
	var snap sessionsSnapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return fmt.Errorf("decode the session snapshot: %w", err)
	}
	byID := make(map[string]*Session, len(snap.Sessions))
	order := make([]string, 0, len(snap.Sessions))
	for i := range snap.Sessions {
		v := &snap.Sessions[i]
		byID[v.SessionID] = &Session{
			ID: v.SessionID, Vendor: v.Vendor, State: v.State,
			WaitingReason: v.WaitingReason, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		}
		order = append(order, v.SessionID)
	}
	s.byID, s.order = byID, order
	return nil
}

// ApplyList replaces the list with a ListSessions read, which names the
// vendor and timestamps a created event on its own does not carry.
func (s *Sidebar) ApplyList(list client.SessionList) {
	byID := make(map[string]*Session, len(list.Sessions))
	order := make([]string, 0, len(list.Sessions))
	for _, sess := range list.Sessions {
		v := sess
		byID[v.ID] = &Session{
			ID: v.ID, Vendor: v.Vendor, State: v.State,
			WaitingReason: v.WaitingReason, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		}
		order = append(order, v.ID)
	}
	s.byID, s.order = byID, order
}

// ApplyEvent applies one created or state-changed event. A created session
// starts with its state only; a state change updates an existing session and
// ignores a session the list has not seen — the next snapshot or list read
// names it.
func (s *Sidebar) ApplyEvent(eventType string, payload []byte) error {
	var change sessionChange
	if err := json.Unmarshal(payload, &change); err != nil {
		return fmt.Errorf("decode the session event: %w", err)
	}
	switch eventType {
	case EventSessionCreated:
		if _, ok := s.byID[change.SessionID]; ok {
			return nil
		}
		s.byID[change.SessionID] = &Session{ID: change.SessionID, State: change.State, WaitingReason: change.WaitingReason}
		s.order = append(s.order, change.SessionID)
	case EventSessionStateChanged:
		if existing, ok := s.byID[change.SessionID]; ok {
			existing.State = change.State
			existing.WaitingReason = change.WaitingReason
		}
	default:
		return fmt.Errorf("unknown session event type %q", eventType)
	}
	return nil
}

// List returns the sessions in list order.
func (s *Sidebar) List() []Session {
	out := make([]Session, 0, len(s.order))
	for _, id := range s.order {
		if session, ok := s.byID[id]; ok {
			out = append(out, *session)
		}
	}
	return out
}

// Lookup returns one session of the list.
func (s *Sidebar) Lookup(id string) (Session, bool) {
	session, ok := s.byID[id]
	if !ok {
		return Session{}, false
	}
	return *session, true
}
