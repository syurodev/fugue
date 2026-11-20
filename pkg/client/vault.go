package client

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"
)

// Vault lifecycle states reported by VaultStatus and the `vault` topic.
const (
	VaultStateNotConfigured = "not_configured"
	VaultStateLocked        = "locked"
	VaultStateUnlocked      = "unlocked"

	redactedMask = "[redacted]"
)

// VaultStatus describes the host vault's lifecycle state without secrets.
type VaultStatus struct {
	State             string
	Revision          uint64
	Incarnation       string
	RetryAfterSeconds int64
}

// VaultSetupResult returns the new vault status and one-time recovery key.
// Every formatting path redacts RecoveryKey so it never reaches logs or errors.
type VaultSetupResult struct {
	Status      VaultStatus
	RecoveryKey []byte
}

// String implements fmt.Stringer without revealing RecoveryKey.
func (r VaultSetupResult) String() string {
	return fmt.Sprintf("{Status:%+v RecoveryKey:%s}", r.Status, redactedMask)
}

// GoString implements fmt.GoStringer without revealing RecoveryKey.
func (r VaultSetupResult) GoString() string {
	return fmt.Sprintf("client.VaultSetupResult{Status:%#v, RecoveryKey:%s}", r.Status, redactedMask)
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
		slog.String("recovery_key", redactedMask),
	)
}

// Clear zeros RecoveryKey in memory.
func (r VaultSetupResult) Clear() {
	clear(r.RecoveryKey)
}

// VaultStatus reads the host vault's lifecycle state.
func (c *Client) VaultStatus(ctx context.Context) (VaultStatus, error) {
	res, err := vaultv1connect.NewVaultServiceClient(c.hc, c.base).GetStatus(ctx,
		connect.NewRequest(&vaultv1.GetStatusRequest{}))
	if err != nil {
		return VaultStatus{}, fmt.Errorf("read vault status: %w", err)
	}
	return vaultStatusFromProto(res.Msg.GetStatus()), nil
}

// SetupVault creates the vault keyring on first run and returns the one-time
// recovery key, clearing both the caller slice and any internal copy.
func (c *Client) SetupVault(ctx context.Context, passphrase []byte) (VaultSetupResult, error) {
	reqBytes := bytes.Clone(passphrase)
	clear(passphrase)
	defer clear(reqBytes)
	res, err := vaultv1connect.NewVaultServiceClient(c.hc, c.base).Setup(ctx,
		connect.NewRequest(&vaultv1.SetupRequest{Passphrase: reqBytes}))
	if err != nil {
		return VaultSetupResult{}, fmt.Errorf("setup vault: %w", err)
	}
	return VaultSetupResult{
		Status:      vaultStatusFromProto(res.Msg.GetStatus()),
		RecoveryKey: res.Msg.GetRecoveryKey(),
	}, nil
}

// UnlockVault unlocks the host vault with passphrase bytes, clearing both the
// caller slice and any internal copy immediately after the call returns.
func (c *Client) UnlockVault(ctx context.Context, passphrase []byte) (VaultStatus, error) {
	reqBytes := bytes.Clone(passphrase)
	clear(passphrase)
	defer clear(reqBytes)
	res, err := vaultv1connect.NewVaultServiceClient(c.hc, c.base).Unlock(ctx,
		connect.NewRequest(&vaultv1.UnlockRequest{Passphrase: reqBytes}))
	if err != nil {
		return VaultStatus{}, fmt.Errorf("unlock vault: %w", err)
	}
	return vaultStatusFromProto(res.Msg.GetStatus()), nil
}

// UnlockVaultWithRecovery unlocks the host vault with recovery key bytes,
// clearing both the caller slice and any internal copy immediately after return.
func (c *Client) UnlockVaultWithRecovery(ctx context.Context, recoveryKey []byte) (VaultStatus, error) {
	reqBytes := bytes.Clone(recoveryKey)
	clear(recoveryKey)
	defer clear(reqBytes)
	res, err := vaultv1connect.NewVaultServiceClient(c.hc, c.base).UnlockWithRecovery(ctx,
		connect.NewRequest(&vaultv1.UnlockWithRecoveryRequest{RecoveryKey: reqBytes}))
	if err != nil {
		return VaultStatus{}, fmt.Errorf("unlock vault with recovery: %w", err)
	}
	return vaultStatusFromProto(res.Msg.GetStatus()), nil
}

// LockVault locks the host vault and zeros its data key immediately.
func (c *Client) LockVault(ctx context.Context) (VaultStatus, error) {
	res, err := vaultv1connect.NewVaultServiceClient(c.hc, c.base).Lock(ctx,
		connect.NewRequest(&vaultv1.LockRequest{}))
	if err != nil {
		return VaultStatus{}, fmt.Errorf("lock vault: %w", err)
	}
	return vaultStatusFromProto(res.Msg.GetStatus()), nil
}

// VaultCodeOf returns the public code carried in a failed vault RPC error.
func VaultCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// VaultRetryAfter returns the retry delay in seconds when a vault unlock call
// failed with vault.rate_limited.
func VaultRetryAfter(err error) (int64, bool) {
	detail, ok := errorDetailOf(err)
	if !ok || detail.GetRetryAfterSeconds() <= 0 {
		return 0, false
	}
	return detail.GetRetryAfterSeconds(), true
}

func vaultStatusFromProto(st *vaultv1.VaultStatus) VaultStatus {
	if st == nil {
		return VaultStatus{}
	}
	return VaultStatus{
		State:             st.GetState(),
		Revision:          st.GetRevision(),
		Incarnation:       st.GetIncarnation(),
		RetryAfterSeconds: st.GetRetryAfterSeconds(),
	}
}
