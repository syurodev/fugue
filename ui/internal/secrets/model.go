package secrets

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/pkg/client"
)

var errUnknownSecretEvent = errors.New("unknown secrets topic event")

// Model holds unsealed secret metadata for the Secret Store screen and
// Inbox secret question picker (desktop-app §5.6). It never holds plaintext.
type Model struct {
	hasSnapshot bool
	items       []client.SecretMeta
}

// NewModel builds an empty Secret Store model.
func NewModel() *Model {
	return &Model{}
}

// HasSnapshot reports whether at least one snapshot or ListSecrets result has landed.
func (m *Model) HasSnapshot() bool { return m.hasSnapshot }

// All returns a sorted copy of all known secret metadata items.
func (m *Model) All() []client.SecretMeta {
	return slices.Clone(m.items)
}

// GlobalSecrets returns all secrets with scope_kind = "global".
func (m *Model) GlobalSecrets() []client.SecretMeta {
	var out []client.SecretMeta
	for i := range m.items {
		if m.items[i].ScopeKind == client.SecretScopeGlobal {
			out = append(out, m.items[i])
		}
	}
	return out
}

// WorkspaceSecrets returns all secrets scoped to workspaceID.
func (m *Model) WorkspaceSecrets(workspaceID string) []client.SecretMeta {
	if workspaceID == "" {
		return nil
	}
	var out []client.SecretMeta
	for i := range m.items {
		if m.items[i].ScopeKind == client.SecretScopeWorkspace && m.items[i].ScopeID == workspaceID {
			out = append(out, m.items[i])
		}
	}
	return out
}

// AllForScope returns Global secrets plus Workspace secrets for workspaceID.
func (m *Model) AllForScope(workspaceID string) []client.SecretMeta {
	out := m.GlobalSecrets()
	if workspaceID != "" {
		out = append(out, m.WorkspaceSecrets(workspaceID)...)
	}
	return out
}

// Lookup returns the metadata for secretID if present.
func (m *Model) Lookup(secretID string) (client.SecretMeta, bool) {
	for i := range m.items {
		if m.items[i].SecretID == secretID {
			return m.items[i], true
		}
	}
	return client.SecretMeta{}, false
}

// ApplyList replaces the model's metadata list from a ListSecrets RPC reply.
func (m *Model) ApplyList(list []client.SecretMeta) {
	m.hasSnapshot = true
	m.items = slices.Clone(list)
	sortSecrets(m.items)
}

// ApplySnapshot decodes and applies a `secrets` topic snapshot payload.
func (m *Model) ApplySnapshot(payload []byte) error {
	snap, err := client.DecodeSecretsSnapshot(payload)
	if err != nil {
		return fmt.Errorf("apply secrets snapshot: %w", err)
	}
	m.ApplyList(snap.Secrets)
	return nil
}

// ApplyEvent decodes and applies a `secrets` topic event (`secret_put` or `secret_deleted`).
func (m *Model) ApplyEvent(eventType string, payload []byte) error {
	ev, err := client.DecodeSecretEvent(payload)
	if err != nil {
		return fmt.Errorf("apply secrets event: %w", err)
	}
	switch eventType {
	case client.EventSecretPut:
		m.upsertFromEvent(&ev)
	case client.EventSecretDeleted:
		m.RemoveByID(ev.SecretID)
	default:
		return fmt.Errorf("%w: %q", errUnknownSecretEvent, eventType)
	}
	return nil
}

// UpsertMeta inserts or updates one SecretMeta record.
func (m *Model) UpsertMeta(meta *client.SecretMeta) {
	if meta == nil || meta.SecretID == "" {
		return
	}
	for i := range m.items {
		if m.items[i].SecretID == meta.SecretID {
			m.items[i] = *meta
			sortSecrets(m.items)
			return
		}
	}
	m.items = append(m.items, *meta)
	sortSecrets(m.items)
}

// RemoveByID removes the secret with secretID if present.
func (m *Model) RemoveByID(secretID string) {
	if secretID == "" {
		return
	}
	m.items = slices.DeleteFunc(m.items, func(s client.SecretMeta) bool {
		return s.SecretID == secretID
	})
}

func (m *Model) upsertFromEvent(ev *client.SecretTopicEvent) {
	if ev.SecretID == "" {
		return
	}
	for i := range m.items {
		if m.items[i].SecretID == ev.SecretID {
			m.items[i].ScopeKind = ev.ScopeKind
			m.items[i].ScopeID = ev.ScopeID
			m.items[i].Name = ev.Name
			sortSecrets(m.items)
			return
		}
	}
	m.items = append(m.items, client.SecretMeta{
		SecretID:  ev.SecretID,
		ScopeKind: ev.ScopeKind,
		ScopeID:   ev.ScopeID,
		Name:      ev.Name,
	})
	sortSecrets(m.items)
}

func sortSecrets(items []client.SecretMeta) {
	slices.SortStableFunc(items, func(a, b client.SecretMeta) int {
		if c := cmp.Compare(scopeOrder(a.ScopeKind), scopeOrder(b.ScopeKind)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.SecretID, b.SecretID)
	})
}

func scopeOrder(kind string) int {
	if kind == client.SecretScopeGlobal {
		return 0
	}
	return 1
}
