package client

// The app-state client (host-api §5.2, F10.20a): the app's window layouts
// and preferences. Documents are the app's own versioned JSON, passed
// through verbatim; the host validates the envelope and never interprets
// them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
)

// errAppStateEventUnknown is the static cause behind an event type this
// client does not decode; it is wrapped, never constructed, at the call site.
var errAppStateEventUnknown = errors.New("the app-state event type is not one the client decodes")

// TopicAppState and its event names mirror the host's durable appstate
// topic. Events carry the window ID and the revision, never a document.
const (
	TopicAppState               = "appstate"
	EventAppStateWindowPut      = "window_put"
	EventAppStateWindowDeleted  = "window_deleted"
	EventAppStatePreferencesPut = "preferences_put"
)

// AppStateWindow is one saved window layout. Layout is the app's own JSON
// document, verbatim; Revision is the CAS token the next Put or Delete
// expects.
type AppStateWindow struct {
	WindowID  string
	Revision  uint64
	Layout    json.RawMessage
	UpdatedAt time.Time
}

// AppStatePreferences is the shared preferences document. Revision 0 with an
// empty Document means the app never saved any.
type AppStatePreferences struct {
	Revision    uint64
	Preferences json.RawMessage
	UpdatedAt   time.Time
}

// AppStateSnapshot is the appstate topic's snapshot: every saved document at
// one sequence.
type AppStateSnapshot struct {
	Sequence    uint64
	Windows     []AppStateWindow
	Preferences *AppStatePreferences
}

// PutAppStateWindowInput carries one PutWindow request.
type PutAppStateWindowInput struct {
	CommandID        string
	WindowID         string
	ExpectedRevision uint64
	Layout           json.RawMessage
}

// PutAppStatePreferencesInput carries one PutPreferences request.
type PutAppStatePreferencesInput struct {
	CommandID        string
	ExpectedRevision uint64
	Preferences      json.RawMessage
}

// ListWindows reads every saved window layout, so the app rebuilds them all.
func (c *Client) ListWindows(ctx context.Context) ([]AppStateWindow, error) {
	res, err := appstatev1connect.NewAppStateServiceClient(c.hc, c.base).ListWindows(ctx,
		connect.NewRequest(&appstatev1.ListWindowsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("list the saved windows: %w", err)
	}
	out := make([]AppStateWindow, 0, len(res.Msg.GetWindows()))
	for _, w := range res.Msg.GetWindows() {
		out = append(out, appStateWindowFromProto(w))
	}
	return out, nil
}

// PutAppStateWindow stores one window layout under CAS: ExpectedRevision 0
// creates it, anything else must match the stored revision. The new revision
// comes back.
func (c *Client) PutAppStateWindow(ctx context.Context, in *PutAppStateWindowInput) (uint64, error) {
	res, err := appstatev1connect.NewAppStateServiceClient(c.hc, c.base).PutWindow(ctx,
		connect.NewRequest(&appstatev1.PutWindowRequest{
			CommandId:        in.CommandID,
			WindowId:         in.WindowID,
			ExpectedRevision: in.ExpectedRevision,
			Layout:           in.Layout,
		}))
	if err != nil {
		return 0, fmt.Errorf("put the window layout: %w", err)
	}
	return res.Msg.GetRevision(), nil
}

// DeleteAppStateWindow deletes one saved window under CAS, for good: closing
// the last window keeps its layout, so a secondary window's delete is the
// only one that removes it.
func (c *Client) DeleteAppStateWindow(ctx context.Context, commandID, windowID string, expectedRevision uint64) error {
	_, err := appstatev1connect.NewAppStateServiceClient(c.hc, c.base).DeleteWindow(ctx,
		connect.NewRequest(&appstatev1.DeleteWindowRequest{
			CommandId:        commandID,
			WindowId:         windowID,
			ExpectedRevision: expectedRevision,
		}))
	if err != nil {
		return fmt.Errorf("delete the saved window: %w", err)
	}
	return nil
}

// GetAppStatePreferences reads the shared preferences document; Revision 0
// means the app never saved any.
func (c *Client) GetAppStatePreferences(ctx context.Context) (AppStatePreferences, error) {
	res, err := appstatev1connect.NewAppStateServiceClient(c.hc, c.base).GetPreferences(ctx,
		connect.NewRequest(&appstatev1.GetPreferencesRequest{}))
	if err != nil {
		return AppStatePreferences{}, fmt.Errorf("read the app preferences: %w", err)
	}
	return appStatePreferencesFromProto(res.Msg.GetRevision(), res.Msg.GetPreferences()), nil
}

// PutAppStatePreferences stores the shared preferences document under CAS,
// like PutAppStateWindow, so two windows cannot clobber each other's theme
// change.
func (c *Client) PutAppStatePreferences(ctx context.Context, in *PutAppStatePreferencesInput) (uint64, error) {
	res, err := appstatev1connect.NewAppStateServiceClient(c.hc, c.base).PutPreferences(ctx,
		connect.NewRequest(&appstatev1.PutPreferencesRequest{
			CommandId:        in.CommandID,
			ExpectedRevision: in.ExpectedRevision,
			Preferences:      in.Preferences,
		}))
	if err != nil {
		return 0, fmt.Errorf("put the app preferences: %w", err)
	}
	return res.Msg.GetRevision(), nil
}

// AppStateCodeOf returns the public code carried in a failed app-state RPC
// error, for example appstate.revision_conflict.
func AppStateCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// AppStateConflictCode is the public code a failed app-state CAS reports.
const AppStateConflictCode = "appstate.revision_conflict"

// AppStateCurrentRevision returns the revision the stored document carries
// now when a call failed with appstate.revision_conflict, so the caller can
// retry its CAS against it. The revision 0 means the document is gone.
func AppStateCurrentRevision(err error) (uint64, bool) {
	detail, ok := errorDetailOf(err)
	if !ok || detail.GetCode() != AppStateConflictCode {
		return 0, false
	}
	return detail.GetCurrentRevision(), true
}

// DecodeAppStateSnapshot decodes the appstate topic's snapshot payload.
func DecodeAppStateSnapshot(payload []byte) (AppStateSnapshot, error) {
	var wire struct {
		Windows []struct {
			WindowID  string          `json:"window_id"`
			Revision  uint64          `json:"revision"`
			Layout    json.RawMessage `json:"layout"`
			UpdatedAt time.Time       `json:"updated_at"`
		} `json:"windows"`
		Preferences *struct {
			Revision    uint64          `json:"revision"`
			Preferences json.RawMessage `json:"preferences"`
		} `json:"preferences"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return AppStateSnapshot{}, fmt.Errorf("decode the app-state snapshot: %w", err)
	}
	out := AppStateSnapshot{Windows: make([]AppStateWindow, 0, len(wire.Windows))}
	for _, w := range wire.Windows {
		out.Windows = append(out.Windows, AppStateWindow{
			WindowID: w.WindowID, Revision: w.Revision, Layout: w.Layout, UpdatedAt: w.UpdatedAt,
		})
	}
	if wire.Preferences != nil {
		out.Preferences = &AppStatePreferences{
			Revision: wire.Preferences.Revision, Preferences: wire.Preferences.Preferences,
		}
	}
	return out, nil
}

// AppStateEvent is one appstate topic event: identity and revision only,
// never a document. WindowID is empty for a preferences event.
type AppStateEvent struct {
	Type     string
	WindowID string
	Revision uint64
}

// DecodeAppStateEvent decodes an appstate topic event of the given type.
func DecodeAppStateEvent(eventType string, payload []byte) (AppStateEvent, error) {
	switch eventType {
	case EventAppStateWindowPut, EventAppStateWindowDeleted:
		var wire struct {
			WindowID string `json:"window_id"`
			Revision uint64 `json:"revision"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			return AppStateEvent{}, fmt.Errorf("decode the app-state window event: %w", err)
		}
		return AppStateEvent{Type: eventType, WindowID: wire.WindowID, Revision: wire.Revision}, nil
	case EventAppStatePreferencesPut:
		var wire struct {
			Revision uint64 `json:"revision"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			return AppStateEvent{}, fmt.Errorf("decode the app-state preferences event: %w", err)
		}
		return AppStateEvent{Type: eventType, Revision: wire.Revision}, nil
	default:
		return AppStateEvent{}, fmt.Errorf("%w: %q", errAppStateEventUnknown, eventType)
	}
}

func appStateWindowFromProto(w *appstatev1.SavedWindow) AppStateWindow {
	if w == nil {
		return AppStateWindow{}
	}
	var updatedAt time.Time
	if w.GetUpdatedAt() != nil {
		updatedAt = w.GetUpdatedAt().AsTime()
	}
	return AppStateWindow{
		WindowID:  w.GetWindowId(),
		Revision:  w.GetRevision(),
		Layout:    json.RawMessage(w.GetLayout()),
		UpdatedAt: updatedAt,
	}
}

func appStatePreferencesFromProto(revision uint64, prefs []byte) AppStatePreferences {
	return AppStatePreferences{Revision: revision, Preferences: json.RawMessage(prefs)}
}
