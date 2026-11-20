package vendor

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const fiveHour = "five_hour"

func accountEpoch() time.Time { return time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC) }

func newTestAccounts(clk clock.Clock) *Accounts {
	return NewAccounts(clk, []AccountVendor{{Name: "claude", DirectQuota: true}, {Name: "codex", DirectQuota: true}, {Name: "agy"}})
}

func accountOf(t *testing.T, a *Accounts, name string) Account {
	t.Helper()
	accounts := a.Snapshot()
	for i := range accounts {
		if accounts[i].Vendor == name {
			return accounts[i]
		}
	}
	t.Fatalf("no account for %q", name)
	return Account{}
}

func TestAccountNeedsLoginOnLoggedOutAndClearsOnLoggedIn(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	a.ObserveLogin("claude", LoginMissing)
	if got := accountOf(t, a, "claude"); got.State != AccountNeedsLogin || got.Login != LoginMissing {
		t.Fatalf("account = %+v, want needs_login", got)
	}
	a.ObserveLogin("claude", LoginUnknown)
	if got := accountOf(t, a, "claude"); got.State != AccountNeedsLogin {
		t.Fatalf("an unknown probe must not clear needs_login: %+v", got)
	}
	a.ObserveLogin("claude", LoginActive)
	if got := accountOf(t, a, "claude"); got.State != AccountOK {
		t.Fatalf("account = %+v, want ok after login", got)
	}
}

func TestAccountFailedAuthNeedsLoginButTemporaryFailuresDoNot(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	for _, class := range []FailureClass{FailureNone, FailureRateLimited, FailureQuota} {
		a.ObserveFailure("codex", class)
		if got := accountOf(t, a, "codex"); got.State != AccountOK {
			t.Fatalf("failure class %q changed the account to %q", class, got.State)
		}
	}
	a.ObserveFailure("codex", FailureAuth)
	if got := accountOf(t, a, "codex"); got.State != AccountNeedsLogin {
		t.Fatalf("account = %+v, want needs_login after failed(auth)", got)
	}
}

func TestAccountPausesUntilResetsAtAndResumesWhenItPasses(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(accountEpoch())
	a := newTestAccounts(clk)
	reset := accountEpoch().Add(3 * time.Hour)
	a.ObserveQuota("claude", &RateLimit{Windows: []Window{
		{Name: fiveHour, Utilization: 1, ResetsAt: reset, Status: "rejected"},
		{Name: "seven_day", Utilization: 0.4, ResetsAt: accountEpoch().Add(48 * time.Hour), Status: "allowed"},
	}})
	got := accountOf(t, a, "claude")
	if got.State != AccountPaused || !got.PausedUntil.Equal(reset) {
		t.Fatalf("account = %+v, want paused until %v", got, reset)
	}
	clk.Advance(3*time.Hour + time.Second)
	if got := accountOf(t, a, "claude"); got.State != AccountOK || !got.PausedUntil.IsZero() {
		t.Fatalf("account = %+v, want ok once resetsAt passed", got)
	}
}

func TestAccountPauseWaitsForEveryExhaustedWindow(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	later := accountEpoch().Add(30 * time.Hour)
	a.ObserveQuota("codex", &RateLimit{Windows: []Window{
		{Name: "primary", Utilization: 1, ResetsAt: accountEpoch().Add(time.Hour)},
		{Name: "secondary", Utilization: 1, ResetsAt: later},
	}})
	if got := accountOf(t, a, "codex"); got.State != AccountPaused || !got.PausedUntil.Equal(later) {
		t.Fatalf("account = %+v, want paused until the later reset %v", got, later)
	}
	a.ObserveQuota("codex", &RateLimit{Windows: []Window{{Name: "secondary", Utilization: 0.2, ResetsAt: later}}})
	if got := accountOf(t, a, "codex"); got.State == AccountPaused && !got.PausedUntil.Equal(accountEpoch().Add(time.Hour)) {
		t.Fatalf("account = %+v: a recovered window must stop pinning the pause", got)
	}
}

func TestAccountNeedsLoginOutranksPaused(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	a.ObserveQuota("claude", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 1, ResetsAt: accountEpoch().Add(time.Hour)}}})
	a.ObserveFailure("claude", FailureAuth)
	if got := accountOf(t, a, "claude"); got.State != AccountNeedsLogin || !got.PausedUntil.IsZero() {
		t.Fatalf("account = %+v, want needs_login without a pause time", got)
	}
}

func TestAccountDoesNotPauseWithoutAUsableResetTime(t *testing.T) {
	t.Parallel()
	now := accountEpoch()
	for name, reset := range map[string]time.Time{
		"unknown":      {},
		"already past": now.Add(-time.Minute),
		"past horizon": now.Add(maxResetHorizon + time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newTestAccounts(clock.NewFake(now))
			a.ObserveQuota("claude", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 1, ResetsAt: reset, Status: "rejected"}}})
			got := accountOf(t, a, "claude")
			if got.State != AccountOK || len(got.Windows) != 1 || !got.Windows[0].ResetsAt.IsZero() {
				t.Fatalf("account = %+v, want ok with the reset time dropped", got)
			}
		})
	}
}

func TestAccountQuotaDropsUntrustedWindowValues(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	windows := []Window{
		{Name: fiveHour, Utilization: math.NaN()},
		{Name: "seven_day", Utilization: math.Inf(1)},
		{Name: "negative", Utilization: -0.5},
		{Name: "Bad Name!", Utilization: 0.1},
		{Name: "", Utilization: 0.1},
		{Name: "this_name_is_far_too_long_to_be_a_window", Utilization: 0.1},
		{Name: "over", Utilization: 7.5, Status: "<script>"},
	}
	a.ObserveQuota("claude", &RateLimit{Windows: windows})
	got := accountOf(t, a, "claude")
	if len(got.Windows) != 1 || got.Windows[0].Name != "over" || got.Windows[0].Utilization != 1 || got.Windows[0].Status != "" {
		t.Fatalf("windows = %+v, want only the clamped %q window with an unknown status", got.Windows, "over")
	}
}

func TestAccountQuotaBoundsTheWindowCount(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	windows := make([]Window, 0, 50)
	for i := range 50 {
		windows = append(windows, Window{Name: "w" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Utilization: 0.1})
	}
	a.ObserveQuota("codex", &RateLimit{Windows: windows})
	if n := len(accountOf(t, a, "codex").Windows); n != maxQuotaWindows {
		t.Fatalf("windows kept = %d, want %d", n, maxQuotaWindows)
	}
}

func TestAccountVendorWithoutQuotaSignalIsUnknownNotUnlimited(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	got := accountOf(t, a, "agy")
	if got.QuotaKnown || len(got.Windows) != 0 || got.State != AccountOK || got.Login != LoginUnknown {
		t.Fatalf("agy account = %+v, want quota unknown, no windows", got)
	}
	if !accountOf(t, a, "claude").QuotaKnown {
		t.Fatal("claude reports quota directly")
	}
}

func TestAccountUnknownVendorIsIgnored(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	a.ObserveLogin("nope", LoginMissing)
	a.ObserveFailure("nope", FailureAuth)
	a.ObserveQuota("nope", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 1}}})
	a.ObserveQuota("claude", nil)
	if len(a.Snapshot()) != 3 {
		t.Fatalf("accounts = %+v, want the three registered vendors", a.Snapshot())
	}
}

func TestAccountOnChangeFiresOnlyWhenTheViewChanges(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	var seen []Account
	a.SetOnChange(func(acc *Account) { seen = append(seen, *acc) })
	a.ObserveLogin("claude", LoginActive)
	a.ObserveLogin("claude", LoginActive)
	a.ObserveLogin("claude", LoginUnknown)
	a.ObserveFailure("claude", FailureRateLimited)
	if len(seen) != 1 || seen[0].Login != LoginActive {
		t.Fatalf("changes = %+v, want exactly one (login active)", seen)
	}
	a.ObserveFailure("claude", FailureAuth)
	if len(seen) != 2 || seen[1].State != AccountNeedsLogin {
		t.Fatalf("changes = %+v, want the needs_login change", seen)
	}
}

func TestAccountWindowsStayBoundedAcrossObservations(t *testing.T) {
	t.Parallel()
	a := newTestAccounts(clock.NewFake(accountEpoch()))
	names := func(prefix string, n int) []Window {
		out := make([]Window, 0, n)
		for i := range n {
			out = append(out, Window{Name: fmt.Sprintf("%s%d", prefix, i), Utilization: 0.1})
		}
		return out
	}
	a.ObserveQuota("claude", &RateLimit{Windows: names("k", maxQuotaWindows)})
	for round := range 50 {
		a.ObserveQuota("claude", &RateLimit{Windows: names(fmt.Sprintf("n%d_", round), maxQuotaWindows)})
	}
	got := accountOf(t, a, "claude").Windows
	if len(got) != maxQuotaWindows {
		t.Fatalf("held %d windows, want the cap %d", len(got), maxQuotaWindows)
	}
	for _, w := range got {
		if w.Name[0] != 'k' {
			t.Errorf("a new name %q displaced a known window", w.Name)
		}
	}
	a.ObserveQuota("claude", &RateLimit{Windows: []Window{{Name: "k3", Utilization: 0.7}}})
	for _, w := range accountOf(t, a, "claude").Windows {
		if w.Name == "k3" && w.Utilization != 0.7 {
			t.Errorf("known window k3 utilization = %v, want the update 0.7", w.Utilization)
		}
	}
}

func TestAccountQuotaAllowsAndNextReset(t *testing.T) {
	t.Parallel()
	now := accountEpoch()
	clk := clock.NewFake(now)
	a := newTestAccounts(clk)

	if !a.QuotaAllows("claude") || !a.QuotaAllows("agy") || !a.QuotaAllows("unknown") {
		t.Fatal("fresh accounts must be allowed")
	}

	reset1 := now.Add(2 * time.Hour)
	a.ObserveQuota("claude", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 0.84, ResetsAt: reset1}}})
	if !a.QuotaAllows("claude") {
		t.Fatal("0.84 utilization must still be allowed")
	}

	a.ObserveQuota("claude", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 0.85, ResetsAt: reset1}}})
	if a.QuotaAllows("claude") {
		t.Fatal("0.85 utilization must be blocked by quota gate")
	}
	if got := NextQuotaReset(a.Snapshot(), clk.Now()); !got.Equal(reset1) {
		t.Fatalf("NextQuotaReset = %v, want %v", got, reset1)
	}

	clk.Advance(2 * time.Hour)
	if !a.QuotaAllows("claude") {
		t.Fatal("account must reopen once resetsAt is reached")
	}
	if got := NextQuotaReset(a.Snapshot(), clk.Now()); !got.IsZero() {
		t.Fatalf("NextQuotaReset after reset = %v, want zero", got)
	}

	// Missing resetsAt never auto-reopens.
	a.ObserveQuota("codex", &RateLimit{Windows: []Window{{Name: fiveHour, Utilization: 0.90}}})
	if a.QuotaAllows("codex") {
		t.Fatal("0.90 without resetsAt must be blocked")
	}
	if got := NextQuotaReset(a.Snapshot(), clk.Now()); !got.IsZero() {
		t.Fatalf("NextQuotaReset without resetsAt = %v, want zero", got)
	}
}
