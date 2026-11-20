package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The workspaces topic's event names; the constants mirror the host's app
// workspace service (F5.6c).
const (
	EventCreated        = "created"
	EventConfigChanged  = "config_changed"
	EventPendingConfirm = "pending_confirmation"
	EventConfirmed      = "confirmed"
	EventRejected       = "rejected"
	EventRootAdded      = "root_added"
	EventRootRemoved    = "root_removed"
	EventRootUpdated    = "root_updated"
	EventDeleted        = "deleted"
)

// errUnknownWorkspaceEvent is the static cause behind an event type the
// workspaces topic does not define; it is wrapped, never constructed, at the
// call site.
var errUnknownWorkspaceEvent = errors.New("unknown workspace event type")

// Summary is one workspace as the `workspaces` topic reports it: identity and
// the flags the sidebar shows. It carries no roots — the detail read does.
type Summary struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	MissingRoots              []string `json:"missing_roots"`
	ConfigRevision            int64    `json:"config_revision"`
	PendingPolicyConfirmation bool     `json:"pending_policy_confirmation"`
}

// Root is one root of a workspace's detail.
type Root struct {
	Name, Path, Access string
	Missing            bool
}

// DiffEntry is one policy key's reviewed change (Q-64): what the accepted
// policy holds and what the out-of-band edit proposed.
type DiffEntry struct{ Key, OldValue, NewValue string }

// Detail is one workspace's read state: the roots a session can start in and
// name in a dialog, the hash every mutation is a compare-and-swap against,
// and the pending policy change's diff. A detail-affecting topic event drops
// it, and the next read rebuilds it.
type Detail struct {
	ID, Name, ConfigHash string
	ConfigRevision       int64
	Roots                []Root
	MissingRoots         []string
	Pending              bool
	Diff                 []DiffEntry
	// Generation matches Model.Generation(ID) at the time the detail read was
	// armed so stale reads finished after an out-of-band edit or delete are
	// dropped.
	Generation uint64
}

// Model is the app's workspace state: the summaries the topic reports and
// the details the reads fold in. It changes only on the window goroutine.
type Model struct {
	byID   map[string]Summary
	order  []string
	detail map[string]Detail
	gen    map[string]uint64
}

// NewModel builds an empty workspace model.
func NewModel() *Model {
	return &Model{byID: map[string]Summary{}, detail: map[string]Detail{}, gen: map[string]uint64{}}
}

// ApplySnapshot replaces the list with the topic snapshot's summaries and
// drops the details: a fresh connection's state starts from the snapshot. Any
// already-known workspace advances its generation so pre-reconnect reads land
// stale and are dropped.
func (m *Model) ApplySnapshot(payload []byte) error {
	var summaries []Summary
	if err := json.Unmarshal(payload, &summaries); err != nil {
		return fmt.Errorf("decode the workspace snapshot: %w", err)
	}
	for id := range m.byID {
		m.bumpGen(id)
	}
	byID := make(map[string]Summary, len(summaries))
	order := make([]string, 0, len(summaries))
	for _, s := range summaries {
		byID[s.ID] = s
		order = append(order, s.ID)
	}
	m.byID, m.order = byID, order
	m.detail = map[string]Detail{}
	return nil
}

// ApplyEvent applies one topic event, whose payload is the workspace's
// refreshed summary. An event that changes the roots, the configuration or
// the pending policy change drops the workspace's detail and advances its
// generation so earlier reads are ignored; a deleted event drops the workspace
// whole.
func (m *Model) ApplyEvent(eventType string, payload []byte) error {
	var summary Summary
	if err := json.Unmarshal(payload, &summary); err != nil {
		return fmt.Errorf("decode the workspace event: %w", err)
	}
	switch eventType {
	case EventCreated:
		if _, ok := m.byID[summary.ID]; !ok {
			m.order = append(m.order, summary.ID)
		}
		m.byID[summary.ID] = summary
	case EventRootAdded, EventRootRemoved, EventRootUpdated, EventConfigChanged,
		EventPendingConfirm, EventConfirmed, EventRejected:
		if _, ok := m.byID[summary.ID]; !ok {
			m.order = append(m.order, summary.ID)
		}
		m.byID[summary.ID] = summary
		delete(m.detail, summary.ID)
		m.bumpGen(summary.ID)
	case EventDeleted:
		delete(m.byID, summary.ID)
		delete(m.detail, summary.ID)
		m.bumpGen(summary.ID)
		for i, id := range m.order {
			if id == summary.ID {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	default:
		return fmt.Errorf("%w: %q", errUnknownWorkspaceEvent, eventType)
	}
	return nil
}

// ApplyList replaces the list with a ListWorkspaces read, keeping cached
// details whose revision and pending flag still match, and advancing generation
// whenever a workspace leaves or has its detail dropped or invalidated.
func (m *Model) ApplyList(summaries []Summary) {
	byID := make(map[string]Summary, len(summaries))
	order := make([]string, 0, len(summaries))
	detail := make(map[string]Detail, len(summaries))
	for _, s := range summaries {
		byID[s.ID] = s
		order = append(order, s.ID)
		if det, ok := m.detail[s.ID]; ok && det.ConfigRevision >= s.ConfigRevision && det.Pending == s.PendingPolicyConfirmation {
			detail[s.ID] = det
			continue
		}
		if prev, existed := m.byID[s.ID]; existed {
			if _, hadDet := m.detail[s.ID]; hadDet || prev.ConfigRevision != s.ConfigRevision || prev.PendingPolicyConfirmation != s.PendingPolicyConfirmation {
				m.bumpGen(s.ID)
			}
		}
	}
	for id := range m.byID {
		if _, ok := byID[id]; !ok {
			m.bumpGen(id)
		}
	}
	m.byID, m.order, m.detail = byID, order, detail
}

// ApplyDetail folds one workspace read into the model. A late detail for a
// workspace no longer in byID (deleted while in flight), whose armed
// generation no longer matches (for example after an out-of-band edit
// event that kept ConfigRevision unchanged), or older than the current
// ConfigRevision is ignored.
func (m *Model) ApplyDetail(d *Detail) {
	if d == nil {
		return
	}
	s, ok := m.byID[d.ID]
	if !ok {
		return
	}
	if d.Generation != m.gen[d.ID] {
		return
	}
	if d.ConfigRevision < s.ConfigRevision {
		return
	}
	if prev, hasPrev := m.detail[d.ID]; hasPrev && d.ConfigRevision < prev.ConfigRevision {
		return
	}
	s.ID, s.Name = d.ID, d.Name
	s.MissingRoots, s.PendingPolicyConfirmation = d.MissingRoots, d.Pending
	if d.ConfigRevision > s.ConfigRevision {
		s.ConfigRevision = d.ConfigRevision
	}
	m.byID[d.ID] = s
	m.detail[d.ID] = *d
}

// Generation returns the workspace's current detail-read generation, bumped on
// every event or list refresh that invalidates an earlier detail read.
func (m *Model) Generation(id string) uint64 {
	return m.gen[id]
}

// bumpGen advances the workspace's generation counter.
func (m *Model) bumpGen(id string) {
	m.gen[id]++
}

// List returns the summaries in list order.
func (m *Model) List() []Summary {
	out := make([]Summary, 0, len(m.order))
	for _, id := range m.order {
		if s, ok := m.byID[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Lookup returns one workspace's summary.
func (m *Model) Lookup(id string) (Summary, bool) {
	s, ok := m.byID[id]
	return s, ok
}

// DetailFor returns one workspace's detail, when a read has delivered it.
func (m *Model) DetailFor(id string) (Detail, bool) {
	d, ok := m.detail[id]
	return d, ok
}

// NeedsDetail names the workspaces a read should fill next: every summary
// whose detail the model does not hold, ordered by the list. The window arms
// one read per name until its detail arrives, and re-arms after an event
// drops one.
func (m *Model) NeedsDetail() []string {
	out := make([]string, 0, len(m.order))
	for _, id := range m.order {
		if _, ok := m.detail[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
