package vendor

import (
	"math"
	"slices"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// AccountState is the state of a vendor account (orchestration §8).
type AccountState string

// Account states.
const (
	AccountOK         AccountState = "ok"
	AccountPaused     AccountState = "paused"
	AccountNeedsLogin AccountState = "needs_login"
)

// FailureClass is how an adapter classifies a failed turn for the account
// (orchestration §8, vendor-runtime §11). Only Auth and Quota change account
// state; a temporary 429 is the vendor's own retry and Fugue stays out of it.
type FailureClass string

// Failure classes. The empty class is any other failure.
const (
	FailureNone        FailureClass = ""
	FailureAuth        FailureClass = "auth"
	FailureQuota       FailureClass = "quota"
	FailureRateLimited FailureClass = "rate_limited"
)

// Bounds on vendor-reported quota data, which is untrusted input.
const (
	maxQuotaWindows  = 8
	maxWindowNameLen = 32
	maxResetHorizon  = 400 * 24 * time.Hour
)

// Account is one vendor's default account as the host reports it. Windows is
// empty and QuotaKnown false for a vendor with no quota signal (agy): the
// scheduler treats that as "unknown", never as "unlimited".
type Account struct {
	Vendor      string
	State       AccountState
	PausedUntil time.Time
	Login       LoginState
	QuotaKnown  bool
	Windows     []Window
	UpdatedAt   time.Time
}

// AccountVendor declares one vendor the registry tracks.
type AccountVendor struct {
	Name        string
	DirectQuota bool
}

type accountRecord struct {
	vendor     AccountVendor
	login      LoginState
	needsLogin bool
	windows    map[string]Window
	updatedAt  time.Time
}

// Accounts holds the default account of each vendor in memory (F9.10a; more
// accounts per vendor are F11.8). State is derived from what was observed: a
// login status from the CLI's own status command, a failed(auth) turn, and the
// vendor's quota windows. Nothing here reads a vendor credential (VR-03).
type Accounts struct {
	clk      clock.Clock
	mu       sync.Mutex
	records  map[string]*accountRecord
	order    []string
	onChange func(*Account)
}

// NewAccounts builds a registry with one account per named vendor.
func NewAccounts(clk clock.Clock, vendors []AccountVendor) *Accounts {
	a := &Accounts{clk: clk, records: make(map[string]*accountRecord, len(vendors))}
	for _, v := range vendors {
		a.records[v.Name] = &accountRecord{vendor: v, login: LoginUnknown, windows: map[string]Window{}}
		a.order = append(a.order, v.Name)
	}
	return a
}

// SetOnChange registers the callback that receives an account after an
// observation changed what Snapshot reports for it. It runs on the observing
// goroutine without the registry lock.
func (a *Accounts) SetOnChange(fn func(*Account)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onChange = fn
}

// ObserveLogin records the login status the CLI reported. A logged-out status
// needs login; a logged-in one clears it; unknown changes nothing.
func (a *Accounts) ObserveLogin(vendor string, login LoginState) {
	a.observe(vendor, func(r *accountRecord) {
		switch login {
		case LoginMissing:
			r.login, r.needsLogin = login, true
		case LoginActive:
			r.login, r.needsLogin = login, false
		case LoginUnknown:
		}
	})
}

// ObserveQuota merges the windows of one quota observation into the account.
// Windows are validated first: a window with a bad name or a non-finite or
// negative utilization is dropped, utilization is clamped to [0, 1], and a
// reset time that is zero, in the past or past the horizon is kept as unknown.
func (a *Accounts) ObserveQuota(vendor string, limit *RateLimit) {
	if limit == nil {
		return
	}
	now := a.clk.Now()
	a.observe(vendor, func(r *accountRecord) {
		for i := range min(len(limit.Windows), maxQuotaWindows) {
			if w, ok := sanitizeWindow(&limit.Windows[i], now); ok {
				r.windows[w.Name] = w
			}
		}
	})
}

// ObserveFailure applies a classified failed turn. failed(auth) needs login.
// failed(quota) changes nothing by itself: the pause comes from the vendor's
// exhausted window and its reset time, and a quota failure with no known
// reset cannot be bounded. A temporary rate limit is left to the vendor.
func (a *Accounts) ObserveFailure(vendor string, class FailureClass) {
	if class != FailureAuth {
		return
	}
	a.observe(vendor, func(r *accountRecord) { r.needsLogin = true })
}

// Snapshot returns every account in registration order. A pause that reached
// its reset time reads as ok.
func (a *Accounts) Snapshot() []Account {
	now := a.clk.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Account, 0, len(a.order))
	for _, name := range a.order {
		out = append(out, a.records[name].view(now))
	}
	return out
}

func (a *Accounts) observe(vendor string, apply func(*accountRecord)) {
	now := a.clk.Now()
	a.mu.Lock()
	r, ok := a.records[vendor]
	if !ok {
		a.mu.Unlock()
		return
	}
	before := r.view(now)
	apply(r)
	r.updatedAt = now
	after := r.view(now)
	notify := a.onChange
	a.mu.Unlock()
	if notify != nil && !sameAccount(&before, &after) {
		notify(&after)
	}
}

func (r *accountRecord) view(now time.Time) Account {
	out := Account{
		Vendor: r.vendor.Name, State: AccountOK, Login: r.login,
		QuotaKnown: r.vendor.DirectQuota, UpdatedAt: r.updatedAt,
		Windows: make([]Window, 0, len(r.windows)),
	}
	for _, w := range r.windows {
		out.Windows = append(out.Windows, w)
		if exhausted(&w) && w.ResetsAt.After(now) && w.ResetsAt.After(out.PausedUntil) {
			out.PausedUntil = w.ResetsAt
		}
	}
	slices.SortFunc(out.Windows, func(x, y Window) int {
		switch {
		case x.Name < y.Name:
			return -1
		case x.Name > y.Name:
			return 1
		}
		return 0
	})
	switch {
	case r.needsLogin:
		out.State, out.PausedUntil = AccountNeedsLogin, time.Time{}
	case !out.PausedUntil.IsZero():
		out.State = AccountPaused
	}
	return out
}

func exhausted(w *Window) bool {
	return w.Status == "rejected" || w.Utilization >= 1
}

func sameAccount(x, y *Account) bool {
	return x.State == y.State && x.Login == y.Login && x.PausedUntil.Equal(y.PausedUntil) &&
		slices.EqualFunc(x.Windows, y.Windows, func(p, q Window) bool {
			return p.Name == q.Name && p.Utilization == q.Utilization && p.ResetsAt.Equal(q.ResetsAt) && p.Status == q.Status
		})
}

func sanitizeWindow(w *Window, now time.Time) (Window, bool) {
	if !validWindowName(w.Name) || math.IsNaN(w.Utilization) || math.IsInf(w.Utilization, 0) || w.Utilization < 0 {
		return Window{}, false
	}
	out := Window{Name: w.Name, Utilization: min(w.Utilization, 1)}
	if w.ResetsAt.After(now) && w.ResetsAt.Before(now.Add(maxResetHorizon)) {
		out.ResetsAt = w.ResetsAt.UTC()
	}
	switch w.Status {
	case "allowed", "allowed_warning", "rejected":
		out.Status = w.Status
	}
	return out, true
}

func validWindowName(name string) bool {
	if name == "" || len(name) > maxWindowNameLen {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
