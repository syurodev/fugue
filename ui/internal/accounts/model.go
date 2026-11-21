package accounts

import (
	"errors"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/pkg/client"
)

// Additional UI-level account and login states for unknown/unsupported cases.
const (
	AccountStateUnknown   = "unknown"
	LoginStateUnsupported = "unsupported"
)

var (
	errUnknownQuotaEvent = errors.New("unknown quota topic event")
	errUnknownLoginEvent = errors.New("unknown login topic event")
)

// LoginEntry holds the UI state for one vendor's active or recent login flow.
type LoginEntry struct {
	Vendor       string
	LoginID      string
	State        string
	Prompt       string
	ErrorMessage string
	Starting     bool
	Cancelling   bool
}

// Active reports whether the login attempt is currently running or starting.
func (e *LoginEntry) Active() bool {
	return e.Starting || e.State == client.LoginStateStarted || e.State == client.LoginStateWaiting
}

// Model holds vendor accounts, F9.10 quota windows, and CLI login progress
// for the current host incarnation.
type Model struct {
	incarnation string
	hasSnapshot bool
	order       []string
	accounts    map[string]client.Account
	logins      map[string]LoginEntry
}

// NewModel builds an empty accounts model with no guessed states.
func NewModel() *Model {
	return &Model{
		accounts: map[string]client.Account{},
		logins:   map[string]LoginEntry{},
	}
}

// Incarnation returns the host incarnation this model currently tracks.
func (m *Model) Incarnation() string { return m.incarnation }

// HasSnapshot reports whether an account snapshot has been received.
func (m *Model) HasSnapshot() bool { return m.hasSnapshot }

// Accounts returns the accounts in snapshot order with unknown states normalized.
func (m *Model) Accounts() []client.Account {
	out := make([]client.Account, 0, len(m.order))
	for _, v := range m.order {
		acc := m.accounts[v]
		acc.State = NormalizeState(acc.State)
		acc.Windows = slices.Clone(acc.Windows)
		out = append(out, acc)
	}
	return out
}

// Account returns the account for vendor if present.
func (m *Model) Account(vendor string) (client.Account, bool) {
	acc, ok := m.accounts[vendor]
	if !ok {
		return client.Account{}, false
	}
	acc.State = NormalizeState(acc.State)
	acc.Windows = slices.Clone(acc.Windows)
	return acc, true
}

// Login returns the current or latest login entry for vendor if present.
func (m *Model) Login(vendor string) (LoginEntry, bool) {
	e, ok := m.logins[vendor]
	return e, ok
}

// IsLoginRunning reports whether vendor currently has a login attempt starting or running.
func (m *Model) IsLoginRunning(vendor string) bool {
	e, ok := m.logins[vendor]
	return ok && e.Active()
}

// NeedsAttentionCount returns the number of accounts not in the ok state.
func (m *Model) NeedsAttentionCount() int {
	count := 0
	for _, v := range m.order {
		if NormalizeState(m.accounts[v].State) != client.AccountStateOK {
			count++
		}
	}
	return count
}

// NormalizeState preserves known account states and maps any unrecognized
// state to AccountStateUnknown without guessing.
func NormalizeState(state string) string {
	switch state {
	case client.AccountStateOK, client.AccountStatePaused, client.AccountStateNeedsLogin:
		return state
	default:
		return AccountStateUnknown
	}
}

// ApplyListAccounts reconciles the model with a ListAccounts RPC snapshot.
func (m *Model) ApplyListAccounts(incarnation string, accounts []client.Account) {
	m.reconcileAccounts(incarnation, accounts)
}

// ApplyQuotaSnapshot reconciles the model with a `quota` topic snapshot.
func (m *Model) ApplyQuotaSnapshot(incarnation string, payload []byte) error {
	accs, err := client.DecodeQuotaSnapshot(payload)
	if err != nil {
		return fmt.Errorf("apply quota snapshot: %w", err)
	}
	m.reconcileAccounts(incarnation, accs)
	return nil
}

func (m *Model) resetForIncarnation(incarnation string) {
	if incarnation == "" || incarnation == m.incarnation {
		return
	}
	m.incarnation = incarnation
	m.hasSnapshot = false
	m.order = nil
	clear(m.accounts)
	clear(m.logins)
}

func (m *Model) reconcileAccounts(incarnation string, accounts []client.Account) {
	m.resetForIncarnation(incarnation)
	m.hasSnapshot = true
	m.order = make([]string, 0, len(accounts))
	m.accounts = make(map[string]client.Account, len(accounts))
	for i := range accounts {
		acc := accounts[i]
		if acc.Vendor == "" {
			continue
		}
		acc.Windows = slices.Clone(acc.Windows)
		if _, dup := m.accounts[acc.Vendor]; !dup {
			m.order = append(m.order, acc.Vendor)
		}
		m.accounts[acc.Vendor] = acc
	}
}

// ApplyQuotaEvent folds one `quota` topic event into the model.
func (m *Model) ApplyQuotaEvent(eventType string, payload []byte) error {
	if eventType != client.EventQuotaAccountChanged {
		return fmt.Errorf("%w: %q", errUnknownQuotaEvent, eventType)
	}
	acc, err := client.DecodeQuotaAccountChanged(payload)
	if err != nil {
		return fmt.Errorf("apply quota event: %w", err)
	}
	if acc.Vendor == "" {
		return nil
	}
	acc.Windows = slices.Clone(acc.Windows)
	if _, exists := m.accounts[acc.Vendor]; !exists {
		m.order = append(m.order, acc.Vendor)
	}
	m.accounts[acc.Vendor] = acc
	m.hasSnapshot = true
	return nil
}

// ApplyLoginSnapshot reconciles running logins with a `login` topic snapshot.
func (m *Model) ApplyLoginSnapshot(incarnation string, payload []byte) error {
	logins, err := client.DecodeLoginSnapshot(payload)
	if err != nil {
		return fmt.Errorf("apply login snapshot: %w", err)
	}
	m.resetForIncarnation(incarnation)
	running := make(map[string]client.LoginProgress, len(logins))
	for i := range logins {
		p := logins[i]
		if p.Vendor != "" {
			running[p.Vendor] = p
		}
	}
	for v, existing := range m.logins {
		if _, ok := running[v]; !ok && !existing.Starting && isRunningLoginState(existing.State) {
			delete(m.logins, v)
		}
	}
	for v, p := range running {
		prev := m.logins[v]
		m.logins[v] = LoginEntry{
			Vendor:     v,
			LoginID:    p.LoginID,
			State:      p.State,
			Prompt:     p.Prompt,
			Cancelling: prev.Cancelling && isRunningLoginState(p.State),
		}
	}
	return nil
}

// ApplyLoginEvent folds one `login` topic event into the model.
func (m *Model) ApplyLoginEvent(eventType string, payload []byte) error {
	if eventType != client.EventLoginProgress {
		return fmt.Errorf("%w: %q", errUnknownLoginEvent, eventType)
	}
	p, err := client.DecodeLoginProgress(payload)
	if err != nil {
		return fmt.Errorf("apply login event: %w", err)
	}
	if p.Vendor == "" {
		return nil
	}
	prev := m.logins[p.Vendor]
	running := isRunningLoginState(p.State)
	m.logins[p.Vendor] = LoginEntry{
		Vendor:     p.Vendor,
		LoginID:    p.LoginID,
		State:      p.State,
		Prompt:     p.Prompt,
		Cancelling: prev.Cancelling && running,
	}
	return nil
}

// BeginStartLogin marks vendor as starting a login while the RPC runs.
func (m *Model) BeginStartLogin(vendor string) {
	if vendor == "" || m.IsLoginRunning(vendor) {
		return
	}
	m.logins[vendor] = LoginEntry{
		Vendor:   vendor,
		State:    client.LoginStateStarted,
		Starting: true,
	}
}

// ApplyStartLoginResult updates vendor's login state after StartLogin returns.
func (m *Model) ApplyStartLoginResult(res *Result) {
	if res == nil || res.Vendor == "" {
		return
	}
	cur := m.logins[res.Vendor]
	cur.Starting = false
	if res.Err != nil {
		if res.Code == "failed_precondition" {
			cur.State = LoginStateUnsupported
			cur.ErrorMessage = res.Vendor + " has no login command; sign in from the " + res.Vendor + " app."
			cur.Prompt = ""
			m.logins[res.Vendor] = cur
			return
		}
		if res.Code == "already_exists" {
			cur.ErrorMessage = "A login is already running for " + res.Vendor + "."
			m.logins[res.Vendor] = cur
			return
		}
		cur.State = client.LoginStateFailed
		cur.ErrorMessage = res.Err.Error()
		m.logins[res.Vendor] = cur
		return
	}
	cur.ErrorMessage = ""
	if cur.LoginID == "" {
		cur.LoginID = res.StartLogin.LoginID
	}
	if cur.State == "" {
		cur.State = client.LoginStateStarted
	}
	m.logins[res.Vendor] = cur
}

// BeginCancelLogin marks an active login as cancelling while CancelLogin runs.
func (m *Model) BeginCancelLogin(vendor string) {
	if e, ok := m.logins[vendor]; ok {
		e.Cancelling = true
		m.logins[vendor] = e
	}
}

// ApplyCancelLoginResult updates vendor's login state after CancelLogin returns.
func (m *Model) ApplyCancelLoginResult(res *Result) {
	if res == nil || res.Vendor == "" {
		return
	}
	cur, ok := m.logins[res.Vendor]
	if !ok {
		return
	}
	cur.Cancelling = false
	if res.Err != nil {
		if res.Code == "not_found" && isRunningLoginState(cur.State) {
			delete(m.logins, res.Vendor)
			return
		}
		cur.ErrorMessage = res.Err.Error()
	}
	m.logins[res.Vendor] = cur
}

// DismissLogin clears a non-running login entry from view.
func (m *Model) DismissLogin(vendor string) {
	if e, ok := m.logins[vendor]; ok && !e.Active() {
		delete(m.logins, vendor)
	}
}

func isRunningLoginState(state string) bool {
	return state == client.LoginStateStarted || state == client.LoginStateWaiting
}
