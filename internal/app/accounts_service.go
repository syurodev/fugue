package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// TopicQuota is the account and quota topic (host-api §5.1): an ephemeral
// topic, so a snapshot of the accounts plus a bounded catch-up ring of
// changes. Account state is held in memory in F9.10a, so nothing durable
// backs it. Payloads carry state and quota windows only, never an identity.
const TopicQuota = "quota"

// EventQuotaAccountChanged is the only quota event: the full latest view of
// one account, keyed by vendor so a slow subscriber may coalesce it.
const EventQuotaAccountChanged = "quota.account_changed"

const (
	quotaTopicBuffer = 64
	// AccountsRefreshLimit bounds the login refresh of one ListAccounts call:
	// each vendor's status command has its own shorter bound.
	AccountsRefreshLimit = 20 * time.Second
)

// QuotaWindowView is one quota window as the Host API shows it.
type QuotaWindowView struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at,omitempty"`
	Status      string  `json:"status,omitempty"`
}

// AccountView is the safe shape of one account.
type AccountView struct {
	Vendor      string            `json:"vendor"`
	State       string            `json:"state"`
	PausedUntil string            `json:"paused_until,omitempty"`
	Login       string            `json:"login"`
	QuotaKnown  bool              `json:"quota_known"`
	Windows     []QuotaWindowView `json:"windows"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

type quotaSnapshot struct {
	Accounts []AccountView `json:"accounts"`
}

// AccountsService is the Host API boundary of vendor accounts (F9.10a).
type AccountsService struct {
	accounts *vendor.Accounts
	vendors  *VendorService
	hub      *EventHub

	// notifyMu orders publication: the view is re-read under it, so the last
	// event of a vendor is never older than the registry. It is never held
	// while the registry lock is, and the registry never calls into it.
	notifyMu sync.Mutex

	flightMu sync.Mutex
	flight   *refreshFlight
}

// refreshFlight is one in-flight login refresh that concurrent List calls
// share, so a burst runs each vendor's status command once.
type refreshFlight struct {
	done    chan struct{}
	waiters int // guarded by flightMu
}

// newAccountsService registers the quota topic and publishes every account
// change on it.
func newAccountsService(accounts *vendor.Accounts, vendors *VendorService, hub *EventHub) (*AccountsService, error) {
	s := &AccountsService{accounts: accounts, vendors: vendors, hub: hub}
	snapshot := func(context.Context) ([]byte, error) {
		return json.Marshal(quotaSnapshot{Accounts: accountViews(accounts.Snapshot())})
	}
	if err := hub.RegisterEphemeral(TopicQuota, snapshot, quotaTopicBuffer); err != nil {
		return nil, err
	}
	accounts.SetOnChange(s.publish)
	return s, nil
}

// publish sends the current view of the changed vendor. It ignores the
// callback's copy, which a concurrent observation may already have outdated.
func (s *AccountsService) publish(changed *vendor.Account) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	account, ok := s.accounts.Account(changed.Vendor)
	if !ok {
		return
	}
	view := accountView(&account)
	payload, err := json.Marshal(view)
	if err != nil {
		slog.Warn("marshal a quota event", "code", errcode.CodeOf(err))
		return
	}
	if _, err := s.hub.Publish(TopicQuota, Event{Type: EventQuotaAccountChanged, Key: view.Vendor, Payload: payload, LatestForKey: true}); err != nil {
		slog.Warn("publish a quota event", "code", errcode.CodeOf(err))
	}
}

// List refreshes each vendor's login state through the CLI's own status
// command and returns the accounts. A vendor whose CLI cannot be probed (not
// installed, unusable) keeps its last known state; the list never fails for it.
func (s *AccountsService) List(ctx context.Context, principal string) ([]AccountView, error) {
	if !IsLocalPrincipal(principal) {
		return nil, errcode.New(errcode.PermissionDenied, "only local principals may use accounts")
	}
	ctx, cancel := context.WithTimeout(ctx, AccountsRefreshLimit)
	defer cancel()
	s.refresh(ctx)
	return accountViews(s.accounts.Snapshot()), nil
}

// refresh runs one login refresh, or waits for the one already running. A
// waiter whose context ends first returns and leaves the refresh to its owner.
func (s *AccountsService) refresh(ctx context.Context) {
	s.flightMu.Lock()
	if cur := s.flight; cur != nil {
		cur.waiters++
		s.flightMu.Unlock()
		select {
		case <-cur.done:
		case <-ctx.Done():
		}
		return
	}
	cur := &refreshFlight{done: make(chan struct{})}
	s.flight = cur
	s.flightMu.Unlock()
	defer func() {
		s.flightMu.Lock()
		s.flight = nil
		s.flightMu.Unlock()
		close(cur.done)
	}()
	accounts := s.accounts.Snapshot()
	for i := range accounts {
		if ctx.Err() != nil {
			return
		}
		s.vendors.refreshLogin(ctx, accounts[i].Vendor)
	}
}

func accountViews(accounts []vendor.Account) []AccountView {
	out := make([]AccountView, 0, len(accounts))
	for i := range accounts {
		out = append(out, accountView(&accounts[i]))
	}
	return out
}

func accountView(a *vendor.Account) AccountView {
	view := AccountView{
		Vendor: a.Vendor, State: string(a.State), Login: string(a.Login), QuotaKnown: a.QuotaKnown,
		Windows: make([]QuotaWindowView, 0, len(a.Windows)),
	}
	if a.State == vendor.AccountPaused {
		view.PausedUntil = a.PausedUntil.UTC().Format(time.RFC3339)
	}
	if !a.UpdatedAt.IsZero() {
		view.UpdatedAt = a.UpdatedAt.UTC().Format(time.RFC3339)
	}
	for _, w := range a.Windows {
		wv := QuotaWindowView{Name: w.Name, Utilization: w.Utilization, Status: w.Status}
		if !w.ResetsAt.IsZero() {
			wv.ResetsAt = w.ResetsAt.UTC().Format(time.RFC3339)
		}
		view.Windows = append(view.Windows, wv)
	}
	return view
}
