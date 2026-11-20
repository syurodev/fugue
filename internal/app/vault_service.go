package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	// TopicVault is the ephemeral Host API topic publishing vault state changes.
	TopicVault = "vault"
	// EventVaultChanged is the event type published on TopicVault.
	EventVaultChanged = "vault.changed"
	// VaultRequestMaxBytes bounds one vault RPC request on the wire.
	VaultRequestMaxBytes = 64 * 1024

	vaultTopicBuffer  = 16
	vaultFreeAttempts = 3
	vaultBaseBackoff  = 2 * time.Second
	vaultMaxBackoff   = 60 * time.Second
)

// VaultStatusView is the safe Host API view of the vault lifecycle state.
type VaultStatusView struct {
	State             string `json:"state"`
	Revision          uint64 `json:"revision"`
	Incarnation       string `json:"incarnation"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

// VaultSetupResult returns the new vault status and the one-time recovery key.
// Every formatting path redacts RecoveryKey so it never reaches logs or errors.
type VaultSetupResult struct {
	Status      VaultStatusView
	RecoveryKey []byte
}

// String implements fmt.Stringer without revealing RecoveryKey.
func (r VaultSetupResult) String() string {
	return fmt.Sprintf("{Status:%+v RecoveryKey:%s}", r.Status, redact.Mask)
}

// GoString implements fmt.GoStringer without revealing RecoveryKey.
func (r VaultSetupResult) GoString() string {
	return fmt.Sprintf("app.VaultSetupResult{Status:%#v, RecoveryKey:%s}", r.Status, redact.Mask)
}

// Format implements fmt.Formatter so no verb prints RecoveryKey.
func (r VaultSetupResult) Format(f fmt.State, _ rune) {
	if _, err := f.Write([]byte(r.String())); err != nil {
		return
	}
}

// LogValue implements slog.LogValuer without revealing RecoveryKey.
func (r VaultSetupResult) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("state", r.Status.State),
		slog.Uint64("revision", r.Status.Revision),
		slog.String("recovery_key", redact.Mask),
	)
}

// Clear zeros RecoveryKey in memory.
func (r VaultSetupResult) Clear() {
	clear(r.RecoveryKey)
}

// VaultRateLimitError wraps VaultRateLimited with the retry delay in seconds.
type VaultRateLimitError struct {
	err        error
	retryAfter int64
}

func (e *VaultRateLimitError) Error() string { return e.err.Error() }
func (e *VaultRateLimitError) Unwrap() error { return e.err }

// RetryAfterSeconds returns the wait time before the next unlock attempt.
func (e *VaultRateLimitError) RetryAfterSeconds() int64 { return e.retryAfter }

// VaultService exposes host-side vault operations to local principals with
// exponential backoff on failed unlock attempts and zero secret retention.
// Unlock attempts serialize via attemptMu.TryLock() across the entire
// checkBackoff -> KDF -> recordFailure/resetBackoff sequence so concurrent calls
// never bypass backoff and overlapping attempts fail immediately with
// VaultRateLimited without queuing behind Argon2.
type VaultService struct {
	vault           *vault.Service
	hub             *EventHub
	clk             clock.Clock
	unregisterGuard func()

	attemptMu    sync.Mutex
	mu           sync.Mutex
	failures     int
	blockedUntil time.Time
}

func newVaultService(v *vault.Service, hub *EventHub, clk clock.Clock) (*VaultService, error) {
	if v == nil || hub == nil {
		return nil, errcode.New(errcode.InvalidArgument, "vault service dependencies are required")
	}
	if clk == nil {
		clk = clock.Real{}
	}
	s := &VaultService{vault: v, hub: hub, clk: clk}
	snapshot := func(context.Context) ([]byte, error) {
		return json.Marshal(s.statusView(v.Snapshot()))
	}
	if err := hub.RegisterEphemeral(TopicVault, snapshot, vaultTopicBuffer); err != nil {
		return nil, err
	}
	s.unregisterGuard = v.RegisterLockGuard(s.publishSnapshot)
	return s, nil
}

// Close removes the vault lock guard registered by VaultService.
func (s *VaultService) Close() {
	if s.unregisterGuard != nil {
		s.unregisterGuard()
	}
}

func (s *VaultService) publishSnapshot(snap vault.Snapshot) {
	payload, err := json.Marshal(s.statusView(snap))
	if err != nil {
		slog.Warn("marshal a vault event", "code", errcode.CodeOf(err))
		return
	}
	if _, err := s.hub.Publish(TopicVault, Event{Type: EventVaultChanged, Payload: payload}); err != nil {
		slog.Warn("publish a vault event", "code", errcode.CodeOf(err))
	}
}

func (s *VaultService) statusView(snap vault.Snapshot) VaultStatusView {
	s.mu.Lock()
	retryAfter := s.retryAfterLocked(s.clk.Now())
	s.mu.Unlock()
	return VaultStatusView{
		State:             string(snap.State),
		Revision:          snap.Revision,
		Incarnation:       snap.Incarnation,
		RetryAfterSeconds: retryAfter,
	}
}

func (s *VaultService) retryAfterLocked(now time.Time) int64 {
	if !now.Before(s.blockedUntil) {
		return 0
	}
	rem := s.blockedUntil.Sub(now)
	secs := int64((rem + time.Second - 1) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}

// Status returns the current vault status for a local principal.
func (s *VaultService) Status(_ context.Context, principal string) (VaultStatusView, error) {
	if !IsLocalPrincipal(principal) {
		return VaultStatusView{}, errcode.New(errcode.PermissionDenied, "only local principals may use the vault")
	}
	return s.statusView(s.vault.Snapshot()), nil
}

// Setup configures the vault on first run with passphrase bytes, clearing the
// caller's slice and any temporary copy before returning.
func (s *VaultService) Setup(ctx context.Context, principal string, passphrase []byte) (VaultSetupResult, error) {
	secret := bytes.Clone(passphrase)
	clear(passphrase)
	defer clear(secret)
	if !IsLocalPrincipal(principal) {
		return VaultSetupResult{}, errcode.New(errcode.PermissionDenied, "only local principals may use the vault")
	}
	rec, err := s.vault.SetupBytes(ctx, secret)
	if err != nil {
		return VaultSetupResult{}, mapVaultSetupError(err)
	}
	s.resetBackoff()
	return VaultSetupResult{
		Status:      s.statusView(s.vault.Snapshot()),
		RecoveryKey: rec,
	}, nil
}

// Unlock decrypts the vault DEK with passphrase bytes, enforcing host-side
// exponential backoff on repeated failures and clearing all secret copies.
func (s *VaultService) Unlock(ctx context.Context, principal string, passphrase []byte) (VaultStatusView, error) {
	return s.unlockWith(ctx, principal, passphrase, s.vault.UnlockBytes)
}

// UnlockWithRecovery decrypts the vault DEK with recoveryKey bytes, enforcing
// the same host-side exponential backoff and clearing all secret copies.
func (s *VaultService) UnlockWithRecovery(ctx context.Context, principal string, recoveryKey []byte) (VaultStatusView, error) {
	return s.unlockWith(ctx, principal, recoveryKey, s.vault.UnlockWithRecoveryBytes)
}

func (s *VaultService) unlockWith(ctx context.Context, principal string, raw []byte, fn func(context.Context, []byte) error) (VaultStatusView, error) {
	secret := bytes.Clone(raw)
	clear(raw)
	defer clear(secret)
	if !IsLocalPrincipal(principal) {
		return VaultStatusView{}, errcode.New(errcode.PermissionDenied, "only local principals may use the vault")
	}
	if !s.attemptMu.TryLock() {
		return VaultStatusView{}, &VaultRateLimitError{
			err:        errcode.New(errcode.VaultRateLimited, "too many failed vault unlock attempts; wait before retrying"),
			retryAfter: 1,
		}
	}
	defer s.attemptMu.Unlock()
	if err := s.checkBackoff(); err != nil {
		return VaultStatusView{}, err
	}
	if len(secret) == 0 {
		return VaultStatusView{}, errcode.New(errcode.InvalidArgument, "vault secret is required")
	}
	if err := fn(ctx, secret); err != nil {
		return VaultStatusView{}, s.handleUnlockError(err)
	}
	s.resetBackoff()
	return s.statusView(s.vault.Snapshot()), nil
}

// Lock zeroes the vault DEK immediately and notifies all lock guards/sessions.
func (s *VaultService) Lock(_ context.Context, principal string) (VaultStatusView, error) {
	if !IsLocalPrincipal(principal) {
		return VaultStatusView{}, errcode.New(errcode.PermissionDenied, "only local principals may use the vault")
	}
	s.vault.Lock()
	return s.statusView(s.vault.Snapshot()), nil
}

func (s *VaultService) checkBackoff() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if retry := s.retryAfterLocked(s.clk.Now()); retry > 0 {
		return &VaultRateLimitError{
			err:        errcode.New(errcode.VaultRateLimited, "too many failed vault unlock attempts; wait before retrying"),
			retryAfter: retry,
		}
	}
	return nil
}

func (s *VaultService) resetBackoff() {
	s.mu.Lock()
	s.failures = 0
	s.blockedUntil = time.Time{}
	s.mu.Unlock()
}

func (s *VaultService) handleUnlockError(err error) error {
	code := errcode.CodeOf(err)
	switch {
	case code == errcode.FailedPrecondition && s.vault.Snapshot().State == vault.StateNotConfigured:
		// Only a vault that really has no keyring maps to not_configured: the
		// UI answers that code with the setup screen, so any other precondition
		// failure (an unreadable keyring) must keep its own code.
		return errcode.New(errcode.VaultNotConfigured, "the vault is not configured yet")
	case code == errcode.PermissionDenied:
		s.recordFailure()
		return errcode.New(errcode.VaultInvalidPassphrase, "the vault passphrase or recovery key is invalid")
	default:
		return errcode.Wrap(code, "unlock vault", err)
	}
}

func (s *VaultService) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures < vaultFreeAttempts {
		return
	}
	shift := min(s.failures-vaultFreeAttempts, 10)
	delay := min(vaultBaseBackoff<<shift, vaultMaxBackoff)
	s.blockedUntil = s.clk.Now().Add(delay)
}

func mapVaultSetupError(err error) error {
	code := errcode.CodeOf(err)
	if code == errcode.AlreadyExists {
		return errcode.New(errcode.VaultAlreadyConfigured, "the vault is already configured")
	}
	if code == errcode.InvalidArgument {
		return errcode.New(errcode.InvalidArgument, "passphrase must be at least 8 bytes")
	}
	return errcode.Wrap(code, "setup vault", err)
}
