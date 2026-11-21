package app

// The durable appstate topic's snapshot and catch-up (host-api §5.2, §6).
// Snapshot pairs every stored document with the journal sequence that
// produced it; Since maps the committed journal onto events. Payloads carry
// identity and revision only — never a document.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/appstate"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// appStateWindowEvent is the window_put and window_deleted payload shape.
type appStateWindowEvent struct {
	WindowID string `json:"window_id"`
	Revision uint64 `json:"revision"`
}

// appStatePreferencesEvent is the preferences_put payload shape.
type appStatePreferencesEvent struct {
	Revision uint64 `json:"revision"`
}

func marshalAppStateEvent(eventType, windowID string, revision uint64) ([]byte, error) {
	switch eventType {
	case EventAppStatePreferencesPut:
		return json.Marshal(appStatePreferencesEvent{Revision: revision})
	default:
		return json.Marshal(appStateWindowEvent{WindowID: windowID, Revision: revision})
	}
}

// The snapshot's wire shape. Documents are the app's own JSON, embedded
// verbatim, so a layout change never touches the contract.
type appStateSnapshotWire struct {
	Windows     []appStateWindowWire     `json:"windows"`
	Preferences *appStatePreferencesWire `json:"preferences,omitempty"`
}

type appStateWindowWire struct {
	WindowID  string          `json:"window_id"`
	Revision  uint64          `json:"revision"`
	Layout    json.RawMessage `json:"layout"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type appStatePreferencesWire struct {
	Revision    uint64          `json:"revision"`
	Preferences json.RawMessage `json:"preferences"`
}

// appStateTopic serves the durable topic from the store.
type appStateTopic struct {
	store *appstate.Store
}

// Snapshot answers from one read transaction: the documents reflect exactly
// the returned sequence.
func (t *appStateTopic) Snapshot(ctx context.Context) ([]byte, EventSeq, error) {
	snap, err := t.store.Snapshot(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("read the app-state snapshot: %w", err)
	}
	wire := appStateSnapshotWire{Windows: make([]appStateWindowWire, 0, len(snap.Windows))}
	for i := range snap.Windows {
		w := &snap.Windows[i]
		wire.Windows = append(wire.Windows, appStateWindowWire{
			WindowID:  w.WindowID,
			Revision:  w.Revision,
			Layout:    json.RawMessage(w.Layout),
			UpdatedAt: w.UpdatedAt,
		})
	}
	if snap.Preferences.Revision != 0 {
		wire.Preferences = &appStatePreferencesWire{
			Revision:    snap.Preferences.Revision,
			Preferences: json.RawMessage(snap.Preferences.Document),
		}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal the app-state snapshot", err)
	}
	return payload, EventSeq(snap.Sequence), nil
}

// Since maps the committed journal onto events. A cursor older than the
// retained journal is a gap the live queue can never fill, so it surfaces as
// the hidden resync signal and the hub answers with a fresh snapshot.
func (t *appStateTopic) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	changes, err := t.store.ChangesSince(ctx, int64(seq))
	if err != nil {
		if errors.Is(err, appstate.ErrChangeCursorTooOld) {
			return nil, ErrEventSeqTooOld
		}
		return nil, fmt.Errorf("read the app-state changes: %w", err)
	}
	out := make([]Event, 0, len(changes))
	for i := range changes {
		c := &changes[i]
		payload, marshalErr := marshalAppStateEvent(c.Kind, c.WindowID, c.Revision)
		if marshalErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "marshal an app-state event", marshalErr)
		}
		out = append(out, Event{Seq: EventSeq(c.Sequence), Type: c.Kind, Payload: payload})
	}
	return out, nil
}
