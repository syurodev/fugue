package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secretstore"
)

type secretEventWire struct {
	SecretID  string `json:"secret_id"`
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id,omitempty"`
	Name      string `json:"name"`
}

type secretMetaWire struct {
	SecretID    string    `json:"secret_id"`
	ScopeKind   string    `json:"scope_kind"`
	ScopeID     string    `json:"scope_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type secretSnapshotWire struct {
	Secrets []secretMetaWire `json:"secrets"`
}

func marshalSecretEvent(m *secretstore.Meta) ([]byte, error) {
	encoded, err := json.Marshal(secretEventWire{
		SecretID:  m.SecretID,
		ScopeKind: m.ScopeKind,
		ScopeID:   m.ScopeID,
		Name:      m.Name,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal secret event", err)
	}
	return encoded, nil
}

type secretTopic struct {
	store *secretstore.Store
}

func (t *secretTopic) Snapshot(ctx context.Context) ([]byte, EventSeq, error) {
	snap, err := t.store.Snapshot(ctx)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.CodeOf(err), "read secret snapshot", err)
	}
	seq, seqErr := secretstore.ChangeSequence(snap.Sequence)
	if seqErr != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "convert secret snapshot sequence", seqErr)
	}
	wire := secretSnapshotWire{Secrets: make([]secretMetaWire, 0, len(snap.Secrets))}
	for i := range snap.Secrets {
		m := &snap.Secrets[i]
		wire.Secrets = append(wire.Secrets, secretMetaWire{
			SecretID:    m.SecretID,
			ScopeKind:   m.ScopeKind,
			ScopeID:     m.ScopeID,
			Name:        m.Name,
			Description: m.Description,
			CreatedAt:   m.CreatedAt,
			UpdatedAt:   m.UpdatedAt,
		})
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal secret snapshot", err)
	}
	return payload, seq, nil
}

func (t *secretTopic) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	if seq > math.MaxInt64 {
		return nil, nil
	}
	changes, err := t.store.ChangesSince(ctx, int64(seq))
	if err != nil {
		if errors.Is(err, secretstore.ErrChangeCursorTooOld) {
			return nil, ErrEventSeqTooOld
		}
		return nil, errcode.Wrap(errcode.CodeOf(err), "read secret changes", err)
	}
	out := make([]Event, 0, len(changes))
	for i := range changes {
		c := &changes[i]
		cSeq, cErr := secretstore.ChangeSequence(c.Sequence)
		if cErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "convert secret change sequence", cErr)
		}
		payload, pErr := marshalSecretEvent(&secretstore.Meta{
			SecretID:  c.SecretID,
			ScopeKind: c.ScopeKind,
			ScopeID:   c.ScopeID,
			Name:      c.Name,
		})
		if pErr != nil {
			return nil, pErr
		}
		out = append(out, Event{Seq: cSeq, Type: c.Kind, Payload: payload})
	}
	return out, nil
}
