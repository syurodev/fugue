package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secretstore"
)

const (
	// TopicSecrets is the durable Host API topic for secret metadata changes.
	TopicSecrets = "secrets"
	// EventSecretPut is emitted when a secret is created or updated.
	EventSecretPut = "secret_put"
	// EventSecretDeleted is emitted when a secret is deleted.
	EventSecretDeleted = "secret_deleted"

	// SecretScopeGlobal is the global secret scope_kind.
	SecretScopeGlobal = secretstore.ScopeGlobal
	// SecretScopeWorkspace is the workspace secret scope_kind.
	SecretScopeWorkspace = secretstore.ScopeWorkspace

	secretDedupCapacity = 1024
)

// SecretMetaView is the Host API view of one stored secret's metadata.
type SecretMetaView struct {
	SecretID    string
	ScopeKind   string
	ScopeID     string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PutSecretCommand is a PutSecret request's domain fields.
type PutSecretCommand struct {
	Principal   string
	CommandID   string
	ScopeKind   string
	ScopeID     string
	Name        string
	Description string
	Value       []byte
}

// DeleteSecretCommand is a DeleteSecret request's domain fields.
type DeleteSecretCommand struct {
	Principal string
	CommandID string
	SecretID  string
}

type secretDedupEntry struct {
	fingerprint string
	meta        SecretMetaView
	deleted     bool
}

// SecretService manages named user secrets over secretstore with app-only
// principal enforcement, command_id deduplication, redactor refresh, and
// durable metadata-only topic publishing (host-api §5.5).
type SecretService struct {
	store *secretstore.Store
	hub   *EventHub
	clk   clock.Clock

	mu         sync.Mutex
	dedup      map[string]secretDedupEntry
	dedupOrd   []string
	onMutate   func(context.Context)
	onMutateMu sync.RWMutex
}

// NewSecretService builds the SecretService and registers the durable secrets topic.
func NewSecretService(store *secretstore.Store, hub *EventHub, clk clock.Clock) (*SecretService, error) {
	if store == nil || hub == nil {
		return nil, errcode.New(errcode.InvalidArgument, "secret service dependencies are required")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	s := &SecretService{
		store: store,
		hub:   hub,
		clk:   clk,
		dedup: make(map[string]secretDedupEntry),
	}
	if err := hub.RegisterDurable(TopicSecrets, &secretTopic{store: store}); err != nil {
		return nil, err
	}
	return s, nil
}

// SetOnMutate registers a callback invoked after PutSecret or DeleteSecret commits
// (used to rebuild SecretRedactor synchronously).
func (s *SecretService) SetOnMutate(fn func(context.Context)) {
	s.onMutateMu.Lock()
	s.onMutate = fn
	s.onMutateMu.Unlock()
}

func (s *SecretService) notifyMutated(ctx context.Context) {
	s.onMutateMu.RLock()
	fn := s.onMutate
	s.onMutateMu.RUnlock()
	if fn != nil {
		fn(ctx)
	}
}

func requireSecretAppPrincipal(principal string) error {
	if !IsAppPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only the app client may access the secret store")
	}
	return nil
}

// ListSecrets returns unsealed metadata for secrets matching the optional scope filter.
// Succeeds even while the vault is locked (INV-09).
func (s *SecretService) ListSecrets(ctx context.Context, principal, scopeKind, scopeID string) ([]SecretMetaView, error) {
	if err := requireSecretAppPrincipal(principal); err != nil {
		return nil, err
	}
	metas, err := s.store.ListSecrets(ctx, scopeKind, scopeID)
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "list secrets", err)
	}
	out := make([]SecretMetaView, 0, len(metas))
	for i := range metas {
		out = append(out, secretMetaViewFromStore(&metas[i]))
	}
	return out, nil
}

// PutSecret creates or updates a secret, clearing cmd.Value immediately after sealing.
func (s *SecretService) PutSecret(ctx context.Context, cmd *PutSecretCommand) (SecretMetaView, error) {
	if cmd == nil {
		return SecretMetaView{}, errcode.New(errcode.InvalidArgument, "put secret command is required")
	}
	defer clear(cmd.Value)
	if err := requireSecretAppPrincipal(cmd.Principal); err != nil {
		return SecretMetaView{}, err
	}
	if cmd.CommandID == "" {
		return SecretMetaView{}, errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	fp := putSecretFingerprint(cmd)

	s.mu.Lock()
	if prev, ok := s.dedup[cmd.CommandID]; ok {
		s.mu.Unlock()
		if prev.fingerprint != fp {
			return SecretMetaView{}, errcode.New(errcode.InvalidArgument, "command_id was already used for a different secret command")
		}
		return prev.meta, nil
	}
	s.mu.Unlock()

	meta, seq, err := s.store.PutSecret(ctx, cmd.ScopeKind, cmd.ScopeID, cmd.Name, cmd.Description, cmd.Value)
	clear(cmd.Value)
	if err != nil {
		return SecretMetaView{}, errcode.Wrap(errcode.CodeOf(err), "put secret", err)
	}
	view := secretMetaViewFromStore(&meta)
	s.rememberDedup(cmd.CommandID, &secretDedupEntry{fingerprint: fp, meta: view})
	s.notifyMutated(ctx)
	s.publishSecretEvent(seq, EventSecretPut, &meta)
	return view, nil
}

// DeleteSecret deletes a secret by secret_id.
func (s *SecretService) DeleteSecret(ctx context.Context, cmd *DeleteSecretCommand) (bool, error) {
	if cmd == nil {
		return false, errcode.New(errcode.InvalidArgument, "delete secret command is required")
	}
	if err := requireSecretAppPrincipal(cmd.Principal); err != nil {
		return false, err
	}
	if cmd.CommandID == "" {
		return false, errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	fp := deleteSecretFingerprint(cmd.SecretID)

	s.mu.Lock()
	if prev, ok := s.dedup[cmd.CommandID]; ok {
		s.mu.Unlock()
		if prev.fingerprint != fp {
			return false, errcode.New(errcode.InvalidArgument, "command_id was already used for a different secret command")
		}
		return prev.deleted, nil
	}
	s.mu.Unlock()

	meta, seq, err := s.store.DeleteSecret(ctx, cmd.SecretID)
	if err != nil {
		return false, errcode.Wrap(errcode.CodeOf(err), "delete secret", err)
	}
	s.rememberDedup(cmd.CommandID, &secretDedupEntry{fingerprint: fp, deleted: true})
	s.notifyMutated(ctx)
	s.publishSecretEvent(seq, EventSecretDeleted, &meta)
	return true, nil
}

// OpenSecretValue unseals a secret for internal host use only (never exposed via Host API).
func (s *SecretService) OpenSecretValue(ctx context.Context, secretID string) ([]byte, error) {
	val, err := s.store.OpenSecretValue(ctx, secretID)
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "open secret value", err)
	}
	return val, nil
}

// RequireUnlocked fails closed if the vault is locked.
func (s *SecretService) RequireUnlocked() error {
	if err := s.store.RequireUnlocked(); err != nil {
		return errcode.Wrap(errcode.CodeOf(err), "check vault unlocked", err)
	}
	return nil
}

func (s *SecretService) rememberDedup(commandID string, entry *secretDedupEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.dedup[commandID]; !exists {
		if len(s.dedupOrd) >= secretDedupCapacity {
			oldest := s.dedupOrd[0]
			s.dedupOrd = s.dedupOrd[1:]
			delete(s.dedup, oldest)
		}
		s.dedupOrd = append(s.dedupOrd, commandID)
	}
	s.dedup[commandID] = *entry
}

func (s *SecretService) publishSecretEvent(seq int64, eventType string, meta *secretstore.Meta) {
	eventSeq, seqErr := secretstore.ChangeSequence(seq)
	if seqErr != nil {
		slog.Warn("convert secret event sequence", "error_code", string(errcode.CodeOf(seqErr)))
		return
	}
	payload, err := marshalSecretEvent(meta)
	if err != nil {
		slog.Warn("marshal secret event", "error_code", string(errcode.CodeOf(err)))
		return
	}
	if _, err := s.hub.Publish(TopicSecrets, Event{Seq: eventSeq, Type: eventType, Payload: payload}); err != nil {
		slog.Warn("publish secret event", "error_code", string(errcode.CodeOf(err)))
	}
}

func writeLenPrefixed(h hash.Hash, b []byte) {
	var lenBuf [24]byte
	h.Write(strconv.AppendInt(lenBuf[:0], int64(len(b)), 10))
	h.Write([]byte{':'})
	h.Write(b)
}

func putSecretFingerprint(cmd *PutSecretCommand) string {
	h := sha256.New()
	writeLenPrefixed(h, []byte("put"))
	writeLenPrefixed(h, []byte(cmd.ScopeKind))
	writeLenPrefixed(h, []byte(cmd.ScopeID))
	writeLenPrefixed(h, []byte(cmd.Name))
	writeLenPrefixed(h, []byte(cmd.Description))
	writeLenPrefixed(h, cmd.Value)
	return hex.EncodeToString(h.Sum(nil))
}

func deleteSecretFingerprint(secretID string) string {
	h := sha256.New()
	writeLenPrefixed(h, []byte("delete"))
	writeLenPrefixed(h, []byte(secretID))
	return hex.EncodeToString(h.Sum(nil))
}

func secretMetaViewFromStore(m *secretstore.Meta) SecretMetaView {
	return SecretMetaView{
		SecretID:    m.SecretID,
		ScopeKind:   m.ScopeKind,
		ScopeID:     m.ScopeID,
		Name:        m.Name,
		Description: m.Description,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// SecretService returns the secret store service the Host API serves.
func (rt *HostRuntime) SecretService() (*SecretService, error) {
	if rt.secretService == nil {
		return nil, errcode.New(errcode.Unavailable, "the secret service is unavailable")
	}
	return rt.secretService, nil
}
