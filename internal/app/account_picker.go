package app

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/credentials"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	quotaGatePercent     = 85
	defaultQuotaCooldown = 5 * time.Minute
)

func (s *VendorService) applyAccountHome(info *vendorstore.Session, spec *vendor.StartSpec) {
	if s.accounts == nil {
		return
	}
	if home, ok := s.accounts.AccountHomeFor(info.Vendor, info.AccountID); ok {
		spec.Home = home
	}
}

type vendorPickState struct {
	current    string
	previous   string
	switchedAt time.Time
}

// accountPicker selects vendor accounts at task boundaries using
// credentials.PickAccount when multi-account opt-in is enabled and terms are
// accepted, failing closed to the default account otherwise.
type accountPicker struct {
	accounts *vendor.Accounts
	hostCfg  HostConfigReader
	clk      clock.Clock

	mu    sync.Mutex
	state map[string]*vendorPickState
}

func newAccountPicker(accounts *vendor.Accounts, hostCfg HostConfigReader, clk clock.Clock) *accountPicker {
	if clk == nil {
		clk = clock.Real{}
	}
	return &accountPicker{
		accounts: accounts,
		hostCfg:  hostCfg,
		clk:      clk,
		state:    make(map[string]*vendorPickState),
	}
}

func (p *accountPicker) readSettings(ctx context.Context) (enabled bool, strategy credentials.Strategy, err error) {
	if p.hostCfg == nil {
		if p.accounts != nil {
			enabled = p.accounts.MultiAccountEnabled()
		}
		return enabled, credentials.StrategyOrder, nil
	}
	cfg, err := p.hostCfg(ctx)
	if err != nil {
		return false, "", err
	}
	enabled = cfg.MultiAccountEnabled && cfg.MultiAccountTermsAccepted
	if p.accounts != nil {
		p.accounts.SetMultiAccountEnabled(enabled)
	}
	return enabled, credentials.Strategy(cfg.AccountStrategy), nil
}

// PickForTask chooses and records the account for a new task or reassignment of vendorName.
func (p *accountPicker) PickForTask(ctx context.Context, vendorName, model, excludeAccountID string) (accountID string, allLimited bool, switchedFrom, reason string, err error) {
	return p.pick(ctx, vendorName, model, excludeAccountID, true)
}

func (p *accountPicker) pick(ctx context.Context, vendorName, model, excludeAccountID string, commit bool) (accountID string, allLimited bool, switchedFrom, reason string, err error) {
	if p.accounts == nil {
		return vendorDefaultAccount, false, "", "", nil
	}
	enabled, strategy, err := p.readSettings(ctx)
	if err != nil {
		return "", false, "", "", err
	}
	if !enabled {
		return p.pickDefaultOnly(vendorName, excludeAccountID)
	}
	return p.pickFromPool(ctx, vendorName, model, excludeAccountID, strategy, commit)
}

func (p *accountPicker) pickDefaultOnly(vendorName, excludeAccountID string) (string, bool, string, string, error) {
	if excludeAccountID == vendorDefaultAccount || !p.accounts.QuotaAllowsAccount(vendorName, vendorDefaultAccount) {
		return "", true, "", "default account unavailable", nil
	}
	return vendorDefaultAccount, false, "", "", nil
}

func (p *accountPicker) pickFromPool(ctx context.Context, vendorName, model, excludeAccountID string, strategy credentials.Strategy, commit bool) (string, bool, string, string, error) {
	list := p.accounts.VendorAccounts(vendorName)
	if len(list) == 0 {
		return vendorDefaultAccount, false, "", "", nil
	}
	now := p.clk.Now()
	credAccounts, order := toCredentialAccounts(vendorName, list, excludeAccountID, now)
	cur, prev, switchedAt := p.loadVendorState(vendorName, list, commit)

	in := &credentials.PickInput{
		Policy:     credentials.Policy{Enabled: true, ThresholdPercent: quotaGatePercent, Order: order, Strategy: strategy},
		Accounts:   credAccounts,
		Current:    cur,
		Now:        now,
		SwitchedAt: switchedAt,
		Previous:   prev,
		Model:      model,
	}
	dec := credentials.PickAccount(in)
	if !dec.Switch && !p.accounts.QuotaAllowsAccount(vendorName, dec.Target) {
		// Bypass the 5-minute flip hold when the current account crosses the
		// 85% quota gate or is paused (orchestration §8, OR-09).
		in.SwitchedAt = time.Time{}
		dec = credentials.PickAccount(in)
	}
	return p.applyPickDecision(ctx, vendorName, cur, excludeAccountID, dec, now, commit)
}

func (p *accountPicker) loadVendorState(vendorName string, list []vendor.Account, commit bool) (cur, prev string, switchedAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[vendorName]
	if st == nil {
		if !commit {
			return list[0].ID, "", time.Time{}
		}
		st = &vendorPickState{current: list[0].ID}
		p.state[vendorName] = st
	}
	if !hasAccountID(list, st.current) {
		if !commit {
			return list[0].ID, st.previous, st.switchedAt
		}
		st.current = list[0].ID
	}
	return st.current, st.previous, st.switchedAt
}

func (p *accountPicker) applyPickDecision(ctx context.Context, vendorName, cur, excludeAccountID string, dec credentials.PickDecision, now time.Time, commit bool) (string, bool, string, string, error) {
	if dec.AllLimited || dec.Target == "" || !p.accounts.QuotaAllowsAccount(vendorName, dec.Target) || dec.Target == excludeAccountID {
		return "", true, "", dec.Reason, nil
	}
	if dec.Switch || dec.Target != cur {
		if commit {
			p.recordSwitch(ctx, vendorName, cur, dec.Target, dec.Reason, now)
		}
		return dec.Target, false, cur, dec.Reason, nil
	}
	return dec.Target, false, "", "", nil
}

func (p *accountPicker) recordSwitch(ctx context.Context, vendorName, fromID, toID, reason string, now time.Time) {
	p.mu.Lock()
	p.state[vendorName] = &vendorPickState{current: toID, previous: fromID, switchedAt: now}
	p.mu.Unlock()
	slog.InfoContext(ctx, "switched vendor account at task boundary",
		"vendor", vendorName, "from_account", fromID, "to_account", toID, "reason", reason)
}

// CommitSelection records chosenID as the active account for vendorName after
// slot reservation succeeds at a task boundary.
func (p *accountPicker) CommitSelection(ctx context.Context, vendorName, chosenID string) {
	if p == nil || p.accounts == nil || chosenID == "" {
		return
	}
	p.mu.Lock()
	st := p.state[vendorName]
	cur := ""
	if st != nil {
		cur = st.current
	}
	p.mu.Unlock()
	if cur != "" && cur != chosenID {
		p.recordSwitch(ctx, vendorName, cur, chosenID, "task boundary selection", p.clk.Now())
		return
	}
	if cur == "" {
		p.mu.Lock()
		p.state[vendorName] = &vendorPickState{current: chosenID}
		p.mu.Unlock()
	}
}

// HasEligibleAccountWithCapacity reports (read-only, without mutating picker
// state or logging switches) whether any eligible account for vendorName has
// quota headroom and scheduler slot capacity.
func (p *accountPicker) HasEligibleAccountWithCapacity(ctx context.Context, capReader accountCapacityReader, vendorName, model string) (bool, error) {
	chosen, allLimited, _, _, err := p.pick(ctx, vendorName, model, "", false)
	if err != nil || allLimited {
		return false, err
	}
	room, err := capReader.HasCapacityFor(ctx, vendorName, chosen)
	if err != nil {
		return false, boundaryError("read the session capacity", err)
	}
	if room {
		return true, nil
	}
	return p.hasAlternativeCapacity(ctx, capReader, vendorName, chosen)
}

func (p *accountPicker) hasAlternativeCapacity(ctx context.Context, capReader accountCapacityReader, vendorName, excludeID string) (bool, error) {
	enabled, _, err := p.readSettings(ctx)
	if err != nil || !enabled || p.accounts == nil {
		return false, err
	}
	accounts := p.accounts.VendorAccounts(vendorName)
	for i := range accounts {
		id := accounts[i].ID
		if id == excludeID || !p.accounts.QuotaAllowsAccount(vendorName, id) {
			continue
		}
		ok, capErr := capReader.HasCapacityFor(ctx, vendorName, id)
		if capErr != nil {
			return false, boundaryError("read the session capacity", capErr)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// CandidateAccounts returns eligible accounts for vendorName in preference
// order without mutating picker state, honoring preferAccountID and excluding
// excludeAccountID when multi-account is enabled.
func (p *accountPicker) CandidateAccounts(ctx context.Context, vendorName, model, preferAccountID, excludeAccountID string) ([]string, error) {
	if p == nil || p.accounts == nil {
		return []string{vendorDefaultAccount}, nil
	}
	chosen, allLimited, _, _, err := p.pick(ctx, vendorName, model, excludeAccountID, false)
	if err != nil || allLimited {
		return nil, err
	}
	var out []string
	if preferAccountID != "" && preferAccountID != excludeAccountID && p.accounts.QuotaAllowsAccount(vendorName, preferAccountID) {
		out = append(out, preferAccountID)
	}
	if chosen != "" && !slices.Contains(out, chosen) {
		out = append(out, chosen)
	}
	return p.appendRemainingEligible(ctx, out, vendorName, excludeAccountID)
}

func (p *accountPicker) appendRemainingEligible(ctx context.Context, out []string, vendorName, excludeAccountID string) ([]string, error) {
	enabled, _, err := p.readSettings(ctx)
	if err != nil || !enabled {
		return out, err
	}
	accounts := p.accounts.VendorAccounts(vendorName)
	for i := range accounts {
		id := accounts[i].ID
		if id != excludeAccountID && p.accounts.QuotaAllowsAccount(vendorName, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func toCredentialAccounts(vendorName string, list []vendor.Account, excludeID string, now time.Time) ([]credentials.Account, []string) {
	out := make([]credentials.Account, 0, len(list))
	order := make([]string, 0, len(list))
	for i := range list {
		acc := &list[i]
		exhaustedUntil := acc.PausedUntil
		if acc.ID == excludeID && !exhaustedUntil.After(now) {
			exhaustedUntil = now.Add(time.Minute)
		}
		windows := make([]credentials.LimitWindow, 0, len(acc.Windows))
		for _, w := range acc.Windows {
			pct := w.Utilization * 100
			if w.Status == "rejected" && pct < 100 {
				pct = 100
			}
			windows = append(windows, credentials.LimitWindow{
				Label: w.Name, UsedPercent: pct, ResetsAt: w.ResetsAt.Unix(),
			})
		}
		out = append(out, credentials.Account{
			ID: acc.ID, Provider: credentials.Provider(vendorName), Kind: credentials.KindAPIKey,
			Label: acc.ID, SignedIn: acc.State != vendor.AccountNeedsLogin && acc.Login != vendor.LoginMissing,
			ExhaustedUntil: exhaustedUntil, Usage: windows, ObservedAt: acc.UpdatedAt,
		})
		order = append(order, acc.ID)
	}
	return out, order
}

func hasAccountID(list []vendor.Account, id string) bool {
	for i := range list {
		if list[i].ID == id {
			return true
		}
	}
	return false
}

// syncConfiguredAccounts registers extra vendor accounts from HostConfig when
// multi-account opt-in is enabled and terms are accepted.
func syncConfiguredAccounts(ctx context.Context, accounts *vendor.Accounts, dataDir string, hostCfg HostConfigReader) error {
	if accounts == nil || hostCfg == nil {
		return nil
	}
	cfg, err := hostCfg(ctx)
	if err != nil {
		return err
	}
	enabled := cfg.MultiAccountEnabled && cfg.MultiAccountTermsAccepted
	accounts.SetMultiAccountEnabled(enabled)
	if !enabled {
		return nil
	}
	userHome, homeErr := os.UserHomeDir()
	if homeErr != nil {
		userHome = ""
	}
	for vendorName, ids := range cfg.ExtraAccounts {
		if regErr := registerVendorExtraAccounts(accounts, dataDir, userHome, vendorName, ids); regErr != nil {
			return regErr
		}
	}
	return nil
}

func registerVendorExtraAccounts(accounts *vendor.Accounts, dataDir, userHome, vendorName string, ids []string) error {
	primaryHome := vendor.DefaultPrimaryHomeDir(userHome, vendorName)
	for _, id := range ids {
		if _, exists := accounts.AccountByID(vendorName, id); exists {
			continue
		}
		home, prepErr := vendor.PrepareExtraHome(dataDir, vendorName, id, primaryHome)
		if prepErr != nil {
			return boundaryError("prepare extra vendor home", prepErr)
		}
		if regErr := accounts.RegisterAccount(vendorName, id, home); regErr != nil {
			return boundaryError("register extra vendor account", regErr)
		}
	}
	return nil
}
