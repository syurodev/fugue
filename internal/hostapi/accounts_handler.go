package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	accountsv1 "github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1"
)

// accountsHandler maps fugue.accounts.v1 onto the app accounts service. The
// principal comes from the connection's hello, never from a request field;
// the service checks it again.
type accountsHandler struct{ backend app.Backend }

type accountsProvider interface {
	AccountsService() (*app.AccountsService, error)
}

func (h *accountsHandler) ListAccounts(ctx context.Context, _ *connect.Request[accountsv1.ListAccountsRequest]) (*connect.Response[accountsv1.ListAccountsResponse], error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "accounts")
	if authErr != nil {
		return nil, authErr
	}
	provider, ok := h.backend.(accountsProvider)
	if !ok {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the accounts service is unavailable"))
	}
	svc, err := provider.AccountsService()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	views, err := svc.List(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := &accountsv1.ListAccountsResponse{Accounts: make([]*accountsv1.Account, 0, len(views))}
	for i := range views {
		out.Accounts = append(out.Accounts, protoAccount(&views[i]))
	}
	return connect.NewResponse(out), nil
}

// StartLogin runs the vendor CLI's own login command (F9.10b) and returns at
// once; the progress streams on the `login` topic, so the response carries
// only the login identity.
func (h *accountsHandler) StartLogin(ctx context.Context, req *connect.Request[accountsv1.StartLoginRequest]) (*connect.Response[accountsv1.StartLoginResponse], error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "accounts")
	if authErr != nil {
		return nil, authErr
	}
	provider, ok := h.backend.(accountsProvider)
	if !ok {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the accounts service is unavailable"))
	}
	svc, err := provider.AccountsService()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	view, err := svc.StartLogin(ctx, principal, vendorName(req.Msg.Vendor))
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&accountsv1.StartLoginResponse{LoginId: view.LoginID}), nil
}

// CancelLogin stops the running login process of one vendor.
func (h *accountsHandler) CancelLogin(ctx context.Context, req *connect.Request[accountsv1.CancelLoginRequest]) (*connect.Response[accountsv1.CancelLoginResponse], error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "accounts")
	if authErr != nil {
		return nil, authErr
	}
	provider, ok := h.backend.(accountsProvider)
	if !ok {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the accounts service is unavailable"))
	}
	svc, err := provider.AccountsService()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	if err := svc.CancelLogin(ctx, principal, vendorName(req.Msg.Vendor)); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&accountsv1.CancelLoginResponse{}), nil
}

// SubmitLoginCode relays a pasted browser code to a running login process's stdin.
func (h *accountsHandler) SubmitLoginCode(ctx context.Context, req *connect.Request[accountsv1.SubmitLoginCodeRequest]) (*connect.Response[accountsv1.SubmitLoginCodeResponse], error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "accounts")
	if authErr != nil {
		return nil, authErr
	}
	provider, ok := h.backend.(accountsProvider)
	if !ok {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the accounts service is unavailable"))
	}
	svc, err := provider.AccountsService()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	code := redact.NewSecret(req.Msg.GetCode())
	if err := svc.SubmitLoginCode(ctx, principal, req.Msg.GetCommandId(), req.Msg.GetLoginId(), code); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&accountsv1.SubmitLoginCodeResponse{}), nil
}

func protoAccount(v *app.AccountView) *accountsv1.Account {
	acc := &accountsv1.Account{
		Vendor: protoVendor(v.Vendor), State: v.State, PausedUntil: v.PausedUntil, Login: v.Login,
		QuotaKnown: v.QuotaKnown, QuotaGate: v.QuotaGate, UpdatedAt: v.UpdatedAt, Windows: make([]*accountsv1.QuotaWindow, 0, len(v.Windows)),
	}
	for _, w := range v.Windows {
		acc.Windows = append(acc.Windows, &accountsv1.QuotaWindow{Name: w.Name, Utilization: w.Utilization, ResetsAt: w.ResetsAt, Status: w.Status})
	}
	return acc
}
