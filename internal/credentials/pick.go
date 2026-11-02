package credentials

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Strategy ranks the accounts a session may move to. It only ranks the accounts
// that are fine to move to; whether to move at all is the thresholds' decision,
// so a healthy account is never left for a better one.
type Strategy string

// The strategies. An unknown or empty strategy is StrategyOrder.
const (
	// StrategyOrder takes the first fine account in the person's order.
	StrategyOrder Strategy = "order"
	// StrategyBest takes the fine account whose fullest window is the emptiest.
	StrategyBest Strategy = "best"
	// StrategyConsumeFirst takes the fine account whose weekly window resets
	// soonest, so quota that is about to renew is spent before the rest.
	StrategyConsumeFirst Strategy = "consumeFirst"
)

const (
	// DefaultThresholdPercent is the usage at which an account is left when the
	// policy sets none.
	DefaultThresholdPercent = 90
	// flipHold is how long a session stays on the account it just moved to,
	// whatever the numbers say (a real refusal still moves it).
	flipHold = 5 * time.Minute
	// returnGap is how far under the threshold an account must be before a
	// session goes back to the one it left.
	returnGap = 10.0
	// modelWindowPrefix starts the label of a window that belongs to one model.
	modelWindowPrefix = "Weekly · "
)

// Policy says when and how a session leaves an account.
type Policy struct {
	Enabled bool
	// ThresholdPercent: an account at or over this in any window is left. Zero or
	// less means DefaultThresholdPercent.
	ThresholdPercent int
	// WeeklyThresholdPercent replaces ThresholdPercent for weekly windows; 0 = same.
	WeeklyThresholdPercent int
	// Order is the person's preference, first used first.
	Order    []string
	Strategy Strategy
	// AccountThresholds replaces ThresholdPercent for one account, by id.
	AccountThresholds map[string]int
}

// PickInput is everything PickAccount decides from.
type PickInput struct {
	Policy Policy
	// Accounts may mix providers and kinds; only the failover pool of Current's
	// provider is considered (see PickAccount).
	Accounts []Account
	// Current is the account the session is bound to.
	Current string
	Now     time.Time
	// SwitchedAt is when this session last switched; zero = never.
	SwitchedAt time.Time
	// Previous is the account id the session left at that switch; "" = none.
	Previous string
	// Model is the model the session runs ("claude-fable-5-1", an alias, any
	// spelling that names it); "" when it is not known. A model's own window
	// ("Weekly · Fable") counts only for a session on that model.
	Model string
}

// PickDecision says which account the next attempt runs as.
type PickDecision struct {
	// Target is the account id the next attempt runs as, EXCEPT on the early
	// "stay" returns (disabled policy, fewer than two accounts, Current unknown
	// or on a ChatGPT plan), where it is Current exactly as given. Callers decide
	// on Switch and AllLimited, never on Target != Current.
	Target string
	Switch bool
	// Reason is why the current account was left ("Weekly 92% ≥ 90%"), or why
	// nothing could be chosen.
	Reason string
	// AllLimited: every signed-in account is marked exhausted. RetryAt is when
	// the first comes back.
	AllLimited bool
	RetryAt    time.Time
}

// PickAccount is pure. A reading is used as it is: a window's usage only grows
// until it resets, so an old reading over the threshold is still true, and an old
// one under it proves nothing and leaves the account eligible.
//
// Failover happens among the API keys of one provider (model-adapter M-04). An
// account on a ChatGPT plan neither fails over nor receives a session (M-09):
// when it is the current account PickAccount stays, and the run waits for the
// person to choose.
//
// An account that is signed out is not usable, current or not. As a candidate it
// is skipped. As the CURRENT account it makes the session move, at once (no flip
// hold: a dead sign-in is not a number that may drop again), to the best
// signed-in account that is fine and not cooling down, with the reason
// "signed out". With no such account the session stays: a sign-out alone is
// never AllLimited (no account reached a limit), and nothing is moved to an
// account that is no better, so a session never thrashes between dead logins.
func PickAccount(in *PickInput) PickDecision {
	stay := PickDecision{Target: in.Current}
	pool := failoverPool(in)
	if !in.Policy.Enabled || len(pool) < 2 {
		return stay
	}
	ordered := orderedAccounts(in.Policy.Order, pool)
	cur := findAccount(ordered, in.Current)
	if cur == nil {
		return stay
	}
	stay.Target = cur.ID
	ok, why := currentFine(cur, in)
	if ok || flipHeld(cur, in) {
		return stay
	}
	return leave(in, ordered, cur, why)
}

// currentFine reports whether the session may stay on its account, and if not,
// why. A signed-out account is never fine.
func currentFine(cur *Account, in *PickInput) (bool, string) {
	if !cur.SignedIn {
		return false, "signed out"
	}
	return accountFine(cur, &in.Policy, in.Now, 0, in.Model)
}

// flipHeld reports whether the session moved too recently to move again. A
// real refusal or a sign-out is not a number that may drop again, so it is
// never held.
func flipHeld(cur *Account, in *PickInput) bool {
	refused := cur.ExhaustedUntil.After(in.Now) || !cur.SignedIn
	return !refused && !in.SwitchedAt.IsZero() && in.Now.Sub(in.SwitchedAt) < flipHold
}

// leave decides where a session goes once its account is no longer fine.
func leave(in *PickInput, ordered []Account, cur *Account, why string) PickDecision {
	stay := PickDecision{Target: cur.ID}
	fine, skipped := candidates(ordered, cur, in)
	if len(fine) > 0 {
		reason := why
		if len(skipped) > 0 {
			reason += "; skipped " + strings.Join(skipped, ", ") + " (signed out)"
		}
		return PickDecision{Target: bestTarget(fine, in).ID, Switch: true, Reason: reason}
	}
	if !cur.SignedIn {
		// Nothing better to move to. No account reached a limit, so this is
		// not AllLimited: the attempt runs where it is and the provider answers.
		stay.Reason = why
		return stay
	}
	if retry, all := earliestReturn(ordered, in.Now); all {
		return PickDecision{Target: cur.ID, AllLimited: true, RetryAt: retry, Reason: why}
	}
	// Exhausted, with the only alternative cooling down: stay, and do not say
	// AllLimited. The attempt runs on the refused account and the provider's own
	// refusal is seen, because a just-failed attempt is the strongest evidence
	// about the target.
	stay.Reason = why
	return stay
}

// failoverPool is the API-key accounts of the current account's provider, or
// nothing when the current account is not one of them.
func failoverPool(in *PickInput) []Account {
	var provider Provider
	for i := range in.Accounts {
		if in.Accounts[i].ID == in.Current {
			if in.Accounts[i].Kind != KindAPIKey {
				return nil
			}
			provider = in.Accounts[i].Provider
		}
	}
	var pool []Account
	for i := range in.Accounts {
		if in.Accounts[i].Provider == provider && in.Accounts[i].Kind == KindAPIKey {
			pool = append(pool, in.Accounts[i])
		}
	}
	return pool
}

// candidates are the accounts a session may move to, in the person's order,
// and the labels of the signed-out ones that were skipped.
func candidates(ordered []Account, cur *Account, in *PickInput) (fine []Account, skipped []string) {
	for i := range ordered {
		a := &ordered[i]
		if a.ID == cur.ID {
			continue
		}
		if !a.SignedIn {
			name := a.Label
			if name == "" {
				name = a.ID
			}
			skipped = append(skipped, name)
			continue
		}
		// A cooldown keeps a candidate out, not the account a session already
		// runs on: another session's failed attempt says nothing about this one.
		if a.CooldownUntil.After(in.Now) {
			continue
		}
		margin := 0.0
		if a.ID == in.Previous {
			margin = returnGap
		}
		if ok, _ := accountFine(a, &in.Policy, in.Now, margin, in.Model); ok {
			fine = append(fine, *a)
		}
	}
	return fine, skipped
}

// accountFine reports whether an account may run an attempt: not marked
// exhausted and every window not yet reset under its threshold (lowered by
// margin). A window that belongs to one model counts only for a session on that
// model (windowAppliesToModel). A cooldown is not read here: PickAccount applies
// it to candidates only. A weekly window (isWeeklyLabel) uses the policy's
// weekly threshold in place of the main one when that is set. A window with
// ResetsAt == 0 has no known reset and stays live.
func accountFine(a *Account, p *Policy, now time.Time, margin float64, model string) (bool, string) {
	if a.ExhaustedUntil.After(now) {
		return false, "usage limit reached"
	}
	threshold := float64(p.ThresholdPercent)
	if own := p.AccountThresholds[a.ID]; own > 0 {
		threshold = float64(own)
	}
	if threshold <= 0 {
		threshold = DefaultThresholdPercent
	}
	for _, w := range liveWindows(a, now, model) {
		limit := threshold
		if p.WeeklyThresholdPercent > 0 && isWeeklyLabel(w.Label) {
			limit = float64(p.WeeklyThresholdPercent)
		}
		if w.UsedPercent >= limit-margin {
			return false, fmt.Sprintf("%s %.0f%% ≥ %.0f%%", w.Label, w.UsedPercent, limit)
		}
	}
	return true, ""
}

// orderedAccounts returns a copy of accounts sorted by order: accounts missing
// from order come after the ordered ones, in input order.
func orderedAccounts(order []string, accounts []Account) []Account {
	rank := make(map[string]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	out := append([]Account(nil), accounts...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i].ID]
		rj, jok := rank[out[j].ID]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return false
	})
	return out
}

func findAccount(accounts []Account, id string) *Account {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

// earliestReturn reports whether every signed-in account is marked exhausted
// and, if so, when the first comes back. An account that is only cooling down
// is not exhausted, so it makes this report false: nothing has reached a limit.
func earliestReturn(accounts []Account, now time.Time) (time.Time, bool) {
	var first time.Time
	signedIn := 0
	for i := range accounts {
		a := &accounts[i]
		if !a.SignedIn {
			continue
		}
		signedIn++
		if !a.ExhaustedUntil.After(now) {
			return time.Time{}, false
		}
		if first.IsZero() || a.ExhaustedUntil.Before(first) {
			first = a.ExhaustedUntil
		}
	}
	return first, signedIn > 0
}

// bestTarget picks, among accounts that are fine to move to (already in the
// person's order), the one the policy's strategy prefers. A tie, or nothing to
// compare, keeps the order: sort.SliceStable never reorders equal elements.
func bestTarget(fine []Account, in *PickInput) Account {
	rank := fine
	switch in.Policy.Strategy {
	case StrategyOrder:
		// The person's order already ranks the accounts.
	case StrategyBest:
		rank = rankedBy(fine, func(a *Account) (float64, bool) { return fullestWindow(a, in.Now, in.Model) })
	case StrategyConsumeFirst:
		rank = rankedBy(fine, func(a *Account) (float64, bool) { return soonestWeeklyReset(a, in.Now, in.Model) })
	}
	return rank[0]
}

// rankedBy orders accounts by a key, lowest first, with the accounts that have
// no key after those that do.
func rankedBy(accounts []Account, key func(*Account) (float64, bool)) []Account {
	out := append([]Account(nil), accounts...)
	sort.SliceStable(out, func(i, j int) bool {
		ki, iok := key(&out[i])
		kj, jok := key(&out[j])
		switch {
		case iok && jok:
			return ki < kj
		case iok != jok:
			return iok
		}
		return false
	})
	return out
}

// liveWindows are the readings that still say something now: not reset yet and
// applicable to the model (windowAppliesToModel).
func liveWindows(a *Account, now time.Time, model string) []LimitWindow {
	var out []LimitWindow
	for _, w := range a.Usage {
		if (w.ResetsAt != 0 && w.ResetsAt <= now.Unix()) || !windowAppliesToModel(w.Label, model) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// fullestWindow is the highest percent among the live windows; false without one.
func fullestWindow(a *Account, now time.Time, model string) (float64, bool) {
	windows := liveWindows(a, now, model)
	if len(windows) == 0 {
		return 0, false
	}
	top := windows[0].UsedPercent
	for _, w := range windows[1:] {
		top = math.Max(top, w.UsedPercent)
	}
	return top, true
}

// soonestWeeklyReset is the earliest known reset (Unix seconds) among the live
// weekly windows; false when none has a known reset.
func soonestWeeklyReset(a *Account, now time.Time, model string) (float64, bool) {
	var soonest int64
	for _, w := range liveWindows(a, now, model) {
		if w.ResetsAt > 0 && isWeeklyLabel(w.Label) && (soonest == 0 || w.ResetsAt < soonest) {
			soonest = w.ResetsAt
		}
	}
	return float64(soonest), soonest > 0
}

// isWeeklyLabel reports whether a window is one of the weekly ones ("Weekly",
// "Weekly · Opus", "7-day").
func isWeeklyLabel(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "week") || strings.HasPrefix(l, "7-day")
}

// windowAppliesToModel reports whether a window counts for a session on model. A
// window that names no model always does. A model's own window counts only when
// the model's id or alias contains its name, ignoring case and anything that is
// not a letter or a digit ("Sonnet 4.5" is in "claude-sonnet-4-5"); with no known
// model it does not count, because a full Fable window says nothing about an Opus
// turn and a refusal still marks the account if the guess was wrong.
func windowAppliesToModel(label, model string) bool {
	name, ok := strings.CutPrefix(label, modelWindowPrefix)
	if !ok {
		return true
	}
	name, model = alphanumeric(name), alphanumeric(model)
	return name != "" && model != "" && strings.Contains(model, name)
}

func alphanumeric(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
