package client

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	accountsv1 "github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/accounts/v1/accountsv1connect"
)

// TopicQuota, TopicLogin, and their event names mirror the host's accounts
// topic contracts (host-api §5.1, F9.10a, F9.10b).
const (
	TopicQuota               = "quota"
	EventQuotaAccountChanged = "quota.account_changed"
	TopicLogin               = "login"
	EventLoginProgress       = "login.progress"
)

// Account states, probe login states, quota window statuses, and login progress
// states reported by the host accounts service.
const (
	AccountStateOK         = "ok"
	AccountStatePaused     = "paused"
	AccountStateNeedsLogin = "needs_login"

	AccountLoginUnknown   = "unknown"
	AccountLoginLoggedIn  = "logged_in"
	AccountLoginLoggedOut = "logged_out"

	QuotaStatusAllowed        = "allowed"
	QuotaStatusAllowedWarning = "allowed_warning"
	QuotaStatusRejected       = "rejected"

	LoginStateStarted       = "started"
	LoginStateWaiting       = "waiting"
	LoginStateSucceeded     = "succeeded"
	LoginStateFailed        = "failed"
	LoginStateCancelled     = "cancelled"
	LoginStateTimedOut      = "timed_out"
	LoginStateNeedsTerminal = "needs_terminal"
)

// QuotaWindow is one rate-limit or usage window reported by a vendor.
type QuotaWindow struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
	Status      string  `json:"status,omitempty"`
}

// Account is the Host API view of one vendor's default account.
type Account struct {
	Vendor      string        `json:"vendor"`
	State       string        `json:"state"`
	PausedUntil string        `json:"paused_until,omitempty"`
	Login       string        `json:"login"`
	QuotaKnown  bool          `json:"quota_known"`
	QuotaGate   float64       `json:"quota_gate,omitempty"`
	Windows     []QuotaWindow `json:"windows"`
	UpdatedAt   string        `json:"updated_at,omitempty"`
}

// LoginProgress is the `login` topic view of one CLI login attempt. Prompt is
// relayed verbatim from the CLI as untrusted text.
type LoginProgress struct {
	Vendor  string `json:"vendor"`
	LoginID string `json:"login_id"`
	State   string `json:"state"`
	Prompt  string `json:"prompt,omitempty"`
}

// StartLoginResult is the reply to StartLogin.
type StartLoginResult struct {
	LoginID string
}

type quotaSnapshotPayload struct {
	Accounts []Account `json:"accounts"`
}

type loginSnapshotPayload struct {
	Logins []LoginProgress `json:"logins"`
}

// ListAccounts refreshes vendor login status on the host and returns the
// current account snapshot.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	res, err := accountsv1connect.NewAccountServiceClient(c.hc, c.base).ListAccounts(ctx,
		connect.NewRequest(&accountsv1.ListAccountsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	accs := res.Msg.GetAccounts()
	out := make([]Account, 0, len(accs))
	for _, a := range accs {
		out = append(out, accountFromProto(a))
	}
	return out, nil
}

// StartLogin starts the named vendor CLI's own login command on the host.
// Progress streams on TopicLogin; the host never opens a browser or touches
// vendor credentials.
func (c *Client) StartLogin(ctx context.Context, vendor string) (StartLoginResult, error) {
	res, err := accountsv1connect.NewAccountServiceClient(c.hc, c.base).StartLogin(ctx,
		connect.NewRequest(&accountsv1.StartLoginRequest{Vendor: vendorProto(vendor)}))
	if err != nil {
		return StartLoginResult{}, fmt.Errorf("start login: %w", err)
	}
	return StartLoginResult{LoginID: res.Msg.GetLoginId()}, nil
}

// CancelLogin stops the running login process for vendor on the host.
func (c *Client) CancelLogin(ctx context.Context, vendor string) error {
	_, err := accountsv1connect.NewAccountServiceClient(c.hc, c.base).CancelLogin(ctx,
		connect.NewRequest(&accountsv1.CancelLoginRequest{Vendor: vendorProto(vendor)}))
	if err != nil {
		return fmt.Errorf("cancel login: %w", err)
	}
	return nil
}

// AccountsCodeOf returns the public error code carried by a failed accounts RPC.
func AccountsCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// DecodeQuotaSnapshot decodes the `quota` topic snapshot payload.
func DecodeQuotaSnapshot(payload []byte) ([]Account, error) {
	snap, err := decodeJSON[quotaSnapshotPayload](payload, "quota snapshot")
	if err != nil {
		return nil, err
	}
	return snap.Accounts, nil
}

// DecodeQuotaAccountChanged decodes a `quota.account_changed` event payload.
func DecodeQuotaAccountChanged(payload []byte) (Account, error) {
	return decodeJSON[Account](payload, "quota account changed event")
}

// DecodeLoginSnapshot decodes the `login` topic snapshot payload.
func DecodeLoginSnapshot(payload []byte) ([]LoginProgress, error) {
	snap, err := decodeJSON[loginSnapshotPayload](payload, "login snapshot")
	if err != nil {
		return nil, err
	}
	return snap.Logins, nil
}

// DecodeLoginProgress decodes a `login.progress` event payload.
func DecodeLoginProgress(payload []byte) (LoginProgress, error) {
	return decodeJSON[LoginProgress](payload, "login progress event")
}

func accountFromProto(a *accountsv1.Account) Account {
	if a == nil {
		return Account{}
	}
	ws := a.GetWindows()
	windows := make([]QuotaWindow, 0, len(ws))
	for _, w := range ws {
		windows = append(windows, QuotaWindow{
			Name:        w.GetName(),
			Utilization: w.GetUtilization(),
			ResetsAt:    w.GetResetsAt(),
			Status:      w.GetStatus(),
		})
	}
	return Account{
		Vendor:      vendorString(a.GetVendor()),
		State:       a.GetState(),
		PausedUntil: a.GetPausedUntil(),
		Login:       a.GetLogin(),
		QuotaKnown:  a.GetQuotaKnown(),
		QuotaGate:   a.GetQuotaGate(),
		Windows:     windows,
		UpdatedAt:   a.GetUpdatedAt(),
	}
}
