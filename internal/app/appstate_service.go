package app

// The app-state service (host-api §5.2, F10.20a): the app's window layouts
// and preferences behind fugue.appstate.v1. The host validates the document
// envelope and stores it verbatim; it never interprets slot contents. The
// documents live outside the vault, so the service works while the vault is
// locked. Owner: application layer. Allowed callers: internal/hostapi and
// application tests.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/appstate"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The appstate topic and its event types (host-api §5.2). Payloads carry the
// window ID and the revision, never a document: a window that needs the
// layout reads it back.
const (
	TopicAppState               = "appstate"
	EventAppStateWindowPut      = "window_put"
	EventAppStateWindowDeleted  = "window_deleted"
	EventAppStatePreferencesPut = "preferences_put"
)

// AppStateRequestMaxBytes bounds one appstate RPC request on the wire: the
// largest legal request carries a 64 KiB layout; everything over the
// document limits fails typed in the service with appstate.too_large.
const AppStateRequestMaxBytes = 96 * 1024

// IsAppPrincipal reports whether p is exactly the app client kind. The
// app-state service is the app's private store (host-api §5.2), so the
// broader IsLocalPrincipal — tui, cli, app-background — and the session
// principal are denied, fail-closed.
func IsAppPrincipal(p string) bool {
	return ClientKind(p) == KindApp
}

// AppStateWindowView is the Host API view of one saved window layout. The
// transport sees only app types, never the store's.
type AppStateWindowView struct {
	WindowID  string
	Revision  uint64
	Layout    []byte
	UpdatedAt time.Time
}

// AppStatePreferencesView is the Host API view of the shared preferences
// document. Revision 0 with an empty Document means the app never saved any.
type AppStatePreferencesView struct {
	Revision  uint64
	Document  []byte
	UpdatedAt time.Time
}

func appStateWindowView(w appstate.Window) AppStateWindowView {
	return AppStateWindowView{WindowID: w.WindowID, Revision: w.Revision, Layout: w.Layout, UpdatedAt: w.UpdatedAt}
}

// PutWindowCommand is a PutWindow request's domain fields.
type PutWindowCommand struct {
	Principal        string
	CommandID        string
	WindowID         string
	ExpectedRevision uint64
	Layout           []byte
}

// DeleteWindowCommand is a DeleteWindow request's domain fields.
type DeleteWindowCommand struct {
	Principal        string
	CommandID        string
	WindowID         string
	ExpectedRevision uint64
}

// PutPreferencesCommand is a PutPreferences request's domain fields.
type PutPreferencesCommand struct {
	Principal        string
	CommandID        string
	ExpectedRevision uint64
	Preferences      []byte
}

// appStateDedupEntry is one replayed command's result and request
// fingerprint, so the same command_id returns the same result and a reused
// command_id for a different command fails typed (host-api §5).
type appStateDedupEntry struct {
	revision    uint64
	fingerprint string
}

// AppStateService stores the app's documents with command dedup and CAS, and
// publishes every committed change on the durable appstate topic.
type AppStateService struct {
	store *appstate.Store
	hub   *EventHub
	clk   clock.Clock

	mu       sync.Mutex
	dedup    map[string]appStateDedupEntry
	dedupOrd []string
}

const appStateDedupCapacity = 1024

// NewAppStateService builds the service and registers the durable appstate
// topic on the hub.
func NewAppStateService(store *appstate.Store, hub *EventHub, clk clock.Clock) (*AppStateService, error) {
	if store == nil || hub == nil {
		return nil, errcode.New(errcode.InvalidArgument, "app state service dependencies are required")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	s := &AppStateService{store: store, hub: hub, clk: clk, dedup: map[string]appStateDedupEntry{}}
	if err := hub.RegisterDurable(TopicAppState, &appStateTopic{store: store}); err != nil {
		return nil, err
	}
	return s, nil
}

func requireAppPrincipal(principal string) error {
	if !IsAppPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only the app client may use the app state")
	}
	return nil
}

// ListWindows returns every saved window, so the app rebuilds them all.
func (s *AppStateService) ListWindows(ctx context.Context, principal string) ([]AppStateWindowView, error) {
	if err := requireAppPrincipal(principal); err != nil {
		return nil, err
	}
	windows, err := s.store.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]AppStateWindowView, 0, len(windows))
	for i := range windows {
		views = append(views, appStateWindowView(windows[i]))
	}
	return views, nil
}

// GetPreferences returns the shared preferences document; revision 0 means
// the app never saved any.
func (s *AppStateService) GetPreferences(ctx context.Context, principal string) (AppStatePreferencesView, error) {
	if err := requireAppPrincipal(principal); err != nil {
		return AppStatePreferencesView{}, err
	}
	prefs, err := s.store.Preferences(ctx)
	if err != nil {
		return AppStatePreferencesView{}, err
	}
	return AppStatePreferencesView{Revision: prefs.Revision, Document: prefs.Document, UpdatedAt: prefs.UpdatedAt}, nil
}

// PutWindow stores one window layout under CAS.
func (s *AppStateService) PutWindow(ctx context.Context, cmd *PutWindowCommand) (uint64, error) {
	if err := validateAppStateCommand(cmd.Principal, cmd.CommandID); err != nil {
		return 0, err
	}
	fp := appStateFingerprint("window", cmd.WindowID, cmd.ExpectedRevision, cmd.Layout)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, cached := s.dedup[cmd.CommandID]; cached {
		if e.fingerprint != fp {
			return 0, errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
		}
		return e.revision, nil
	}
	revision, seq, err := s.store.PutWindow(ctx, cmd.WindowID, cmd.ExpectedRevision, cmd.Layout, s.clk.Now())
	if err != nil {
		return 0, err
	}
	s.publishAppState(seq, EventAppStateWindowPut, cmd.WindowID, revision)
	s.rememberAppStateCommand(cmd.CommandID, revision, fp)
	return revision, nil
}

// DeleteWindow deletes one window under CAS. Closing the last window keeps
// its layout, so the app can restore it later (host-api §5.2).
func (s *AppStateService) DeleteWindow(ctx context.Context, cmd *DeleteWindowCommand) error {
	if err := validateAppStateCommand(cmd.Principal, cmd.CommandID); err != nil {
		return err
	}
	fp := appStateFingerprint("delete", cmd.WindowID, cmd.ExpectedRevision, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, cached := s.dedup[cmd.CommandID]; cached {
		if e.fingerprint != fp {
			return errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
		}
		return nil
	}
	seq, err := s.store.DeleteWindow(ctx, cmd.WindowID, cmd.ExpectedRevision, s.clk.Now())
	if err != nil {
		return err
	}
	s.publishAppState(seq, EventAppStateWindowDeleted, cmd.WindowID, cmd.ExpectedRevision)
	s.rememberAppStateCommand(cmd.CommandID, cmd.ExpectedRevision, fp)
	return nil
}

// PutPreferences stores the shared preferences document under CAS.
func (s *AppStateService) PutPreferences(ctx context.Context, cmd *PutPreferencesCommand) (uint64, error) {
	if err := validateAppStateCommand(cmd.Principal, cmd.CommandID); err != nil {
		return 0, err
	}
	fp := appStateFingerprint("preferences", "", cmd.ExpectedRevision, cmd.Preferences)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, cached := s.dedup[cmd.CommandID]; cached {
		if e.fingerprint != fp {
			return 0, errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
		}
		return e.revision, nil
	}
	revision, seq, err := s.store.PutPreferences(ctx, cmd.ExpectedRevision, cmd.Preferences, s.clk.Now())
	if err != nil {
		return 0, err
	}
	s.publishAppState(seq, EventAppStatePreferencesPut, "", revision)
	s.rememberAppStateCommand(cmd.CommandID, revision, fp)
	return revision, nil
}

func validateAppStateCommand(principal, commandID string) error {
	if err := requireAppPrincipal(principal); err != nil {
		return err
	}
	if commandID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	return nil
}

// appStateFingerprint identifies the exact request a command_id was used
// for. It hashes the document bytes, so they never appear in the dedup table
// or in any error.
func appStateFingerprint(kind, windowID string, expected uint64, doc []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(windowID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(expected, 10)))
	h.Write([]byte{0})
	h.Write(doc)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *AppStateService) rememberAppStateCommand(commandID string, revision uint64, fp string) {
	if len(s.dedupOrd) >= appStateDedupCapacity {
		oldest := s.dedupOrd[0]
		s.dedupOrd = s.dedupOrd[1:]
		delete(s.dedup, oldest)
	}
	s.dedup[commandID] = appStateDedupEntry{revision: revision, fingerprint: fp}
	s.dedupOrd = append(s.dedupOrd, commandID)
}

// publishAppState fans one committed change out on the durable topic. The
// sequence is the journal row the store just committed, so a later catch-up
// serves exactly this event. Publish holds no caller lock that matters and a
// failed publish is logged, not propagated: the committed state is already
// durable and every subscriber catches up through Since.
func (s *AppStateService) publishAppState(seq int64, eventType, windowID string, revision uint64) {
	payload, err := marshalAppStateEvent(eventType, windowID, revision)
	if err != nil {
		slog.Warn("marshal an app-state event", "error_code", string(errcode.CodeOf(err)))
		return
	}
	if _, err := s.hub.Publish(TopicAppState, Event{Seq: uint64(seq), Type: eventType, Payload: payload}); err != nil {
		slog.Warn("publish an app-state event", "error_code", string(errcode.CodeOf(err)))
	}
}

// AppStateService returns the app-state service the Host API serves.
func (rt *HostRuntime) AppStateService() (*AppStateService, error) {
	if rt.appStates == nil {
		return nil, errcode.New(errcode.Unavailable, "the app state service is unavailable")
	}
	return rt.appStates, nil
}
