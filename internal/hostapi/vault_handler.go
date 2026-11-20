package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"
)

// vaultHandler maps fugue.vault.v1 onto the app VaultService. Every RPC is
// restricted to local principals (host-api §7, docs/security.md) and clears
// request secret slices immediately.
type vaultHandler struct{ backend app.Backend }

type vaultProvider interface {
	VaultService() (*app.VaultService, error)
}

func (h *vaultHandler) service(ctx context.Context) (string, *app.VaultService, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "vault")
	if authErr != nil {
		return "", nil, authErr
	}
	provider, ok := h.backend.(vaultProvider)
	if !ok {
		return "", nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the vault service is unavailable"))
	}
	svc, err := provider.VaultService()
	if err != nil {
		return "", nil, toConnectError(ctx, h.backend, err)
	}
	return principal, svc, nil
}

func (h *vaultHandler) GetStatus(ctx context.Context, _ *connect.Request[vaultv1.GetStatusRequest]) (*connect.Response[vaultv1.GetStatusResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	st, err := svc.Status(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&vaultv1.GetStatusResponse{Status: protoVaultStatus(st)}), nil
}

func (h *vaultHandler) Setup(ctx context.Context, req *connect.Request[vaultv1.SetupRequest]) (*connect.Response[vaultv1.SetupResponse], error) {
	if req != nil && req.Msg != nil {
		defer clear(req.Msg.Passphrase)
	}
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	var pass []byte
	if req != nil && req.Msg != nil {
		pass = req.Msg.Passphrase
	}
	res, err := svc.Setup(ctx, principal, pass)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&vaultv1.SetupResponse{
		Status:      protoVaultStatus(res.Status),
		RecoveryKey: res.RecoveryKey,
	}), nil
}

func (h *vaultHandler) Unlock(ctx context.Context, req *connect.Request[vaultv1.UnlockRequest]) (*connect.Response[vaultv1.UnlockResponse], error) {
	st, err := h.unlockSecret(ctx, req.Msg.GetPassphrase(), false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&vaultv1.UnlockResponse{Status: st}), nil
}

func (h *vaultHandler) UnlockWithRecovery(ctx context.Context, req *connect.Request[vaultv1.UnlockWithRecoveryRequest]) (*connect.Response[vaultv1.UnlockWithRecoveryResponse], error) {
	st, err := h.unlockSecret(ctx, req.Msg.GetRecoveryKey(), true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&vaultv1.UnlockWithRecoveryResponse{Status: st}), nil
}

func (h *vaultHandler) unlockSecret(ctx context.Context, raw []byte, recovery bool) (*vaultv1.VaultStatus, error) {
	defer clear(raw)
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	var (
		st  app.VaultStatusView
		err error
	)
	if recovery {
		st, err = svc.UnlockWithRecovery(ctx, principal, raw)
	} else {
		st, err = svc.Unlock(ctx, principal, raw)
	}
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return protoVaultStatus(st), nil
}

func (h *vaultHandler) Lock(ctx context.Context, _ *connect.Request[vaultv1.LockRequest]) (*connect.Response[vaultv1.LockResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	st, err := svc.Lock(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&vaultv1.LockResponse{Status: protoVaultStatus(st)}), nil
}

func protoVaultStatus(st app.VaultStatusView) *vaultv1.VaultStatus {
	return &vaultv1.VaultStatus{
		State:             st.State,
		Revision:          st.Revision,
		Incarnation:       st.Incarnation,
		RetryAfterSeconds: st.RetryAfterSeconds,
	}
}

var _ vaultv1connect.VaultServiceHandler = (*vaultHandler)(nil)
