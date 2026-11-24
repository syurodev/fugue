package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
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

// TopicLogin is the login progress topic (F9.10b): ephemeral, a snapshot of
// the running logins plus a bounded catch-up ring. A payload relays the URL
// and code the vendor CLI printed verbatim — untrusted data that never
// reaches logs, errors or audit metadata.
const TopicLogin = "login"

// EventLoginProgress is the only login event: the full latest view of one
// login attempt, keyed by vendor. A final outcome (succeeded, failed,
// cancelled, timed_out, needs_terminal) is published as a control event that
// the hub never drops.
const EventLoginProgress = "login.progress"

const (
	quotaTopicBuffer = 64
	// AccountsRefreshLimit bounds the login refresh of one ListAccounts call:
	// each vendor's status command has its own shorter bound.
	AccountsRefreshLimit = 20 * time.Second
	// LoginTimeout bounds one login attempt (Q-112): fifteen minutes, the
	// codex device-code expiry, past which the code can no longer complete.
	LoginTimeout = 15 * time.Minute
	// LoginCancelLimit bounds one CancelLogin stop of the login process.
	LoginCancelLimit = 12 * time.Second
	// LoginPromptLimit bounds the verbatim relay of what the CLI printed: the
	// prompt lines arrive in the first moments of a login, so anything past
	// the cap is flood, not prompt.
	LoginPromptLimit = 4 << 10
	// QuotaGateThreshold is the host's team quota gate fraction.
	QuotaGateThreshold = vendor.QuotaGateThreshold
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
	QuotaGate   float64           `json:"quota_gate,omitempty"`
	Windows     []QuotaWindowView `json:"windows"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

type quotaSnapshot struct {
	Accounts []AccountView `json:"accounts"`
}

// AccountsService is the Host API boundary of vendor accounts (F9.10a) and
// of the vendor CLIs' own login commands (F9.10b).
type AccountsService struct {
	accounts *vendor.Accounts
	vendors  *VendorService
	hub      *EventHub
	clk      clock.Clock
	ids      ids.Generator
	goTask   func(name string, fn func(context.Context) error) *supervise.Task

	// notifyMu orders publication: the view is re-read under it, so the last
	// event of a vendor is never older than the registry. It is never held
	// while the registry lock is, and the registry never calls into it.
	notifyMu      sync.Mutex
	blockedMu     sync.Mutex
	lastBlocked   map[string]bool
	reopenMu      sync.Mutex
	reopenRunning bool
	reopenWake    chan struct{}
	onQuotaReopen func()

	flightMu sync.Mutex
	flight   *refreshFlight

	// loginsMu guards the running login attempts, at most one per vendor.
	loginsMu sync.Mutex
	logins   map[string]*loginRun

	loginCmdMu    sync.Mutex
	loginDedup    *dedupCache[loginCodeResult]
	loginInflight map[string]*inflightCommand[loginCodeResult]
}

// refreshFlight is one in-flight login refresh that concurrent List calls
// share, so a burst runs each vendor's status command once.
type refreshFlight struct {
	done chan struct{}
}

// accountsServiceDeps are the injected seams of the login half. A nil Clock
// and IDs fall back to the production defaults; a nil GoTask disables the
// login half (the host always supplies its supervised task spawner).
type accountsServiceDeps struct {
	Clock clock.Clock
	IDs   ids.Generator
	// GoTask spawns a supervised goroutine on the host's base context; the
	// login monitor outlives the StartLogin RPC that started it.
	GoTask func(name string, fn func(context.Context) error) *supervise.Task
	// OnQuotaReopen runs on the host lifetime when a paused or quota-gated
	// account reaches its resetsAt boundary.
	OnQuotaReopen func()
}

// newAccountsService registers the quota and login topics and publishes every
// account change on the first.
func newAccountsService(accounts *vendor.Accounts, vendors *VendorService, hub *EventHub, deps accountsServiceDeps) (*AccountsService, error) {
	clk := deps.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	idGen := deps.IDs
	if idGen == nil {
		idGen = ids.NewUUIDv7(clk, ids.CryptoRandom{})
	}
	s := &AccountsService{
		accounts: accounts, vendors: vendors, hub: hub,
		clk: clk, ids: idGen, goTask: deps.GoTask,
		lastBlocked:   make(map[string]bool),
		reopenWake:    make(chan struct{}, 1),
		onQuotaReopen: deps.OnQuotaReopen,
		logins:        make(map[string]*loginRun),
		loginDedup:    newDedupCache[loginCodeResult](clk, defaultDedupCapacity, defaultDedupTTL),
		loginInflight: make(map[string]*inflightCommand[loginCodeResult]),
	}
	snapshot := func(context.Context) ([]byte, error) {
		return json.Marshal(quotaSnapshot{Accounts: accountViews(accounts.Snapshot())})
	}
	if err := hub.RegisterEphemeral(TopicQuota, snapshot, quotaTopicBuffer); err != nil {
		return nil, err
	}
	if err := hub.RegisterEphemeral(TopicLogin, s.loginSnapshot, quotaTopicBuffer); err != nil {
		return nil, err
	}
	s.seedBlockedSnapshot()
	accounts.SetOnChange(s.publish)
	s.ensureReopenLoop()
	return s, nil
}

func (s *AccountsService) seedBlockedSnapshot() {
	s.blockedMu.Lock()
	defer s.blockedMu.Unlock()
	snap := s.accounts.Snapshot()
	for i := range snap {
		acc := &snap[i]
		s.lastBlocked[acc.Vendor] = !s.accounts.QuotaAllows(acc.Vendor)
	}
}

// publish sends the current view of the changed vendor. It ignores the
// callback's copy, which a concurrent observation may already have outdated.
func (s *AccountsService) publish(changed *vendor.Account) {
	s.notifyMu.Lock()
	account, ok := s.accounts.AccountByID(changed.Vendor, changed.ID)
	if ok {
		s.blockedMu.Lock()
		s.lastBlocked[account.Vendor] = !s.accounts.QuotaAllows(account.Vendor)
		s.blockedMu.Unlock()
		view := accountView(&account)
		eventKey := view.Vendor
		if account.ID != "" && account.ID != vendor.DefaultAccountID {
			eventKey = view.Vendor + ":" + account.ID
		}
		payload, err := json.Marshal(view)
		if err != nil {
			slog.Warn("marshal a quota event", "code", errcode.CodeOf(err))
		} else if _, err := s.hub.Publish(TopicQuota, Event{Type: EventQuotaAccountChanged, Key: eventKey, Payload: payload, LatestForKey: true}); err != nil {
			slog.Warn("publish a quota event", "code", errcode.CodeOf(err))
		}
	}
	s.notifyMu.Unlock()
	if ok {
		s.ensureReopenLoop()
	}
}

func (s *AccountsService) ensureReopenLoop() {
	if s.goTask == nil || s.onQuotaReopen == nil {
		return
	}
	s.reopenMu.Lock()
	nextReset := vendor.NextQuotaReset(s.accounts.Snapshot(), s.clk.Now())
	if nextReset.IsZero() {
		s.reopenMu.Unlock()
		return
	}
	already := s.reopenRunning
	if !already {
		s.reopenRunning = true
	}
	s.reopenMu.Unlock()
	if already {
		s.wakeReopenLoop()
		return
	}
	s.goTask("accounts-quota-reopen", s.quotaReopenLoop)
}

func (s *AccountsService) wakeReopenLoop() {
	if s.reopenWake == nil {
		return
	}
	select {
	case s.reopenWake <- struct{}{}:
	default:
	}
}

func (s *AccountsService) quotaReopenLoop(ctx context.Context) error {
	defer func() {
		s.reopenMu.Lock()
		s.reopenRunning = false
		s.reopenMu.Unlock()
	}()
	for {
		s.reopenMu.Lock()
		now := s.clk.Now()
		nextReset := vendor.NextQuotaReset(s.accounts.Snapshot(), now)
		if nextReset.IsZero() {
			s.reopenRunning = false
			s.reopenMu.Unlock()
			return nil
		}
		s.reopenMu.Unlock()
		timer := s.clk.NewTimer(nextReset.Sub(now))
		var fired bool
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-s.reopenWake:
			timer.Stop()
		case <-timer.C():
			timer.Stop()
			fired = true
		}
		if fired {
			s.handleQuotaResetTick()
		}
	}
}

func (s *AccountsService) handleQuotaResetTick() {
	var reopened []vendor.Account
	s.blockedMu.Lock()
	snap := s.accounts.Snapshot()
	for i := range snap {
		acc := &snap[i]
		wasBlocked := s.lastBlocked[acc.Vendor]
		allowed := s.accounts.QuotaAllows(acc.Vendor)
		if wasBlocked && allowed {
			reopened = append(reopened, *acc)
		}
	}
	s.blockedMu.Unlock()
	for i := range reopened {
		s.publish(&reopened[i])
	}
	if s.onQuotaReopen != nil {
		s.onQuotaReopen()
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

// snapshotViews returns the current account views without a refresh. The
// team proposal card uses it: the card shows what is known now, and a vendor
// with no known account reads as unknown (F11.2b).
func (s *AccountsService) snapshotViews() []AccountView {
	return accountViews(s.accounts.Snapshot())
}

// refresh runs one login refresh, or waits for the one already running. A
// waiter whose context ends first returns and leaves the refresh to its owner.
func (s *AccountsService) refresh(ctx context.Context) {
	s.flightMu.Lock()
	if cur := s.flight; cur != nil {
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
		QuotaGate: vendor.QuotaGateThreshold, Windows: make([]QuotaWindowView, 0, len(a.Windows)),
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
