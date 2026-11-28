package memory

import (
	"errors"
	"fmt"
	"maps"

	"github.com/syurodev/fugue/pkg/client"
)

var errUnknownMemoryEvent = errors.New("unknown memory topic event")

// Model tracks the per-workspace unverified memory entry counts delivered over
// the Host API `memory` topic.
type Model struct {
	hasSnapshot bool
	unverified  map[string]int
}

// NewModel builds an empty workspace memory topic model.
func NewModel() *Model {
	return &Model{unverified: map[string]int{}}
}

// HasSnapshot reports whether at least one snapshot has landed.
func (m *Model) HasSnapshot() bool { return m.hasSnapshot }

// UnverifiedCount returns the number of valid, unverified entries in workspaceID.
func (m *Model) UnverifiedCount(workspaceID string) int {
	if workspaceID == "" || m.unverified == nil {
		return 0
	}
	return m.unverified[workspaceID]
}

// SnapshotCopy returns a copy of all non-zero workspace unverified counts.
func (m *Model) SnapshotCopy() map[string]int {
	return maps.Clone(m.unverified)
}

// ApplySnapshot decodes and replaces the unverified counts from the `memory` topic snapshot.
func (m *Model) ApplySnapshot(payload []byte) error {
	snap, err := client.DecodeMemoryTopicSnapshot(payload)
	if err != nil {
		return fmt.Errorf("apply memory snapshot: %w", err)
	}
	m.hasSnapshot = true
	next := make(map[string]int, len(snap.Unverified))
	for wsID, count := range snap.Unverified {
		if count > 0 {
			next[wsID] = count
		}
	}
	m.unverified = next
	return nil
}

// ApplyEvent decodes a `MemoryEntryWritten` event and updates the workspace's unverified count.
func (m *Model) ApplyEvent(eventType string, payload []byte) (client.MemoryWrittenEvent, error) {
	if eventType != client.EventMemoryEntryWritten {
		return client.MemoryWrittenEvent{}, fmt.Errorf("%w: %q", errUnknownMemoryEvent, eventType)
	}
	ev, err := client.DecodeMemoryWrittenEvent(payload)
	if err != nil {
		return client.MemoryWrittenEvent{}, fmt.Errorf("apply memory event: %w", err)
	}
	if m.unverified == nil {
		m.unverified = map[string]int{}
	}
	if ev.WorkspaceID != "" {
		if ev.UnverifiedCount > 0 {
			m.unverified[ev.WorkspaceID] = ev.UnverifiedCount
		} else {
			delete(m.unverified, ev.WorkspaceID)
		}
	}
	return ev, nil
}
