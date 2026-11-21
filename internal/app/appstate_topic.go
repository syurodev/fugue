package app

// The durable appstate topic's snapshot and catch-up (host-api §5.2, §6).
// Snapshot pairs every stored document with the journal sequence that
// produced it; Since maps the committed journal onto events. Payloads carry
// identity and revision only — never a document.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/syurodev/fugue/internal/appstate"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// stateWindowEvent is the window_put and window_deleted payload shape.
type stateWindowEvent struct {
	WindowID string `json:"window_id"`
	Revision uint64 `json:"revision"`
}

// statePreferencesEvent is the preferences_put payload shape.
type statePreferencesEvent struct {
	Revision uint64 `json:"revision"`
}

func marshalAppStateEvent(eventType, windowID string, revision uint64) ([]byte, error) {
	var payload any
	switch eventType {
	case EventAppStatePreferencesPut:
		payload = statePreferencesEvent{Revision: revision}
	default:
		payload = stateWindowEvent{WindowID: windowID, Revision: revision}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal an app-state event", err)
	}
	return encoded, nil
}

// The snapshot's wire shape. Documents are the app's own JSON, embedded
// verbatim, so a layout change never touches the contract.
type stateSnapshotWire struct {
	Windows     []stateWindowWire     `json:"windows"`
	Preferences *statePreferencesWire `json:"preferences,omitempty"`
}

type stateWindowWire struct {
	WindowID  string          `json:"window_id"`
	Revision  uint64          `json:"revision"`
	Layout    json.RawMessage `json:"layout"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type statePreferencesWire struct {
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
		return nil, 0, errcode.Wrap(errcode.CodeOf(err), "read the app-state snapshot", err)
	}
	seq, seqErr := appstate.ChangeSequence(snap.Sequence)
	if seqErr != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "convert the app-state snapshot sequence", seqErr)
	}
	wire := stateSnapshotWire{Windows: make([]stateWindowWire, 0, len(snap.Windows))}
	for i := range snap.Windows {
		w := &snap.Windows[i]
		wire.Windows = append(wire.Windows, stateWindowWire{
			WindowID:  w.WindowID,
			Revision:  w.Revision,
			Layout:    json.RawMessage(w.Layout),
			UpdatedAt: w.UpdatedAt,
		})
	}
	if snap.Preferences.Revision != 0 {
		wire.Preferences = &statePreferencesWire{
			Revision:    snap.Preferences.Revision,
			Preferences: json.RawMessage(snap.Preferences.Document),
		}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Internal, "marshal the app-state snapshot", err)
	}
	return payload, seq, nil
}

// Since maps the committed journal onto events. A cursor older than the
// retained journal is a gap the live queue can never fill, so it surfaces as
// the hidden resync signal and the hub answers with a fresh snapshot. A
// cursor beyond every sequence has nothing after it.
func (t *appStateTopic) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	if seq > math.MaxInt64 {
		return nil, nil
	}
	changes, err := t.store.ChangesSince(ctx, int64(seq))
	if err != nil {
		if errors.Is(err, appstate.ErrChangeCursorTooOld) {
			return nil, ErrEventSeqTooOld
		}
		return nil, errcode.Wrap(errcode.CodeOf(err), "read the app-state changes", err)
	}
	out := make([]Event, 0, len(changes))
	for i := range changes {
		event, eventErr := appStateChangeEvent(&changes[i])
		if eventErr != nil {
			return nil, eventErr
		}
		out = append(out, event)
	}
	return out, nil
}

// appStateChangeEvent maps one journal row onto a topic event.
func appStateChangeEvent(c *appstate.Change) (Event, error) {
	seq, err := appstate.ChangeSequence(c.Sequence)
	if err != nil {
		return Event{}, errcode.Wrap(errcode.Internal, "convert an app-state change sequence", err)
	}
	payload, err := marshalAppStateEvent(c.Kind, c.WindowID, c.Revision)
	if err != nil {
		return Event{}, err
	}
	return Event{Seq: seq, Type: c.Kind, Payload: payload}, nil
}
