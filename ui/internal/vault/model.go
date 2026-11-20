package vault

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/pkg/client"
)

// TopicVault and EventVaultChanged mirror the host's vault topic contract.
const (
	TopicVault        = "vault"
	EventVaultChanged = "vault.changed"
)

var errUnknownVaultEvent = errors.New("unknown vault event type")

// Status is the UI-side snapshot of the host vault lifecycle state.
type Status struct {
	State             string `json:"state"`
	Revision          uint64 `json:"revision"`
	Incarnation       string `json:"incarnation"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

// Model tracks the host vault's lifecycle state on the window goroutine.
type Model struct {
	known  bool
	status Status
}

// NewModel builds an unknown-state vault model (fails closed as locked).
func NewModel() *Model {
	return &Model{}
}

// Known reports whether at least one snapshot or status read has landed.
func (m *Model) Known() bool { return m.known }

// Status returns the latest vault status.
func (m *Model) Status() Status { return m.status }

// Locked reports whether the vault is not confirmed unlocked.
func (m *Model) Locked() bool {
	return !m.known || m.status.State != client.VaultStateUnlocked
}

// Unlocked reports whether the vault is known to be unlocked.
func (m *Model) Unlocked() bool { return !m.Locked() }

// ApplySnapshot replaces the model with the `vault` topic's initial snapshot.
func (m *Model) ApplySnapshot(payload []byte) error {
	var st Status
	if err := json.Unmarshal(payload, &st); err != nil {
		return fmt.Errorf("decode the vault snapshot: %w", err)
	}
	m.known = true
	m.status = st
	return nil
}

// ApplyEvent folds one `vault` topic event into the model, ignoring stale
// revisions within the same vault incarnation.
func (m *Model) ApplyEvent(eventType string, payload []byte) error {
	if eventType != EventVaultChanged {
		return fmt.Errorf("%w: %q", errUnknownVaultEvent, eventType)
	}
	var st Status
	if err := json.Unmarshal(payload, &st); err != nil {
		return fmt.Errorf("decode the vault event: %w", err)
	}
	m.ApplyStatus(st)
	return nil
}

// ApplyStatus updates the model from an RPC reply or decoded event, ignoring
// stale revisions within the same incarnation.
func (m *Model) ApplyStatus(st Status) {
	if m.known && m.status.Incarnation == st.Incarnation && st.Revision < m.status.Revision {
		return
	}
	m.known = true
	m.status = st
}
