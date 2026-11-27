package app

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
)

const fiveHourWindow = "five_hour"

func claudeOnlyAccounts(f *transcriptFixture) *vendor.Accounts {
	return vendor.NewAccounts(f.clock, []vendor.AccountVendor{{Name: testVendorName, DirectQuota: true}})
}

func runActorFeedsQuotaAndFailedAuthToTheVendorAccount(t *testing.T, v *vault.Service) {
	t.Helper()
	f := newTranscriptFixtureWithVault(t, v, nil)
	accounts := claudeOnlyAccounts(f)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"), Accounts: accounts,
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "hello")
	reset := f.clock.Now().Add(2 * time.Hour)
	exhausted := &vendor.RateLimit{Windows: []vendor.Window{{Name: fiveHourWindow, Utilization: 1, ResetsAt: reset, Status: "rejected"}}}
	session.observations <- claude.Observation{Quota: exhausted}
	awaitActor(t, func() bool { return accounts.Snapshot()[0].State == vendor.AccountPaused })
	if got := accounts.Snapshot()[0]; !got.PausedUntil.Equal(reset) {
		t.Fatalf("account = %+v, want paused until %v", got, reset)
	}
	// A temporary 429 changes nothing; a failed(auth) turn needs login.
	session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeFailed, Failure: vendor.FailureRateLimited}}
	session.observations <- claude.Observation{Records: []transcript.Item{}, TurnEnd: &claude.TurnEnd{TurnID: "other", Outcome: claude.OutcomeFailed, Failure: vendor.FailureAuth}}
	awaitActor(t, func() bool { return accounts.Snapshot()[0].State == vendor.AccountNeedsLogin })
	mustCloseActorSessions(t, manager)
}

func TestStoredFailureCodeKeepsOnlyAccountRelevantClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		outcome vendor.Outcome
		class   vendor.FailureClass
		want    string
	}{
		{vendor.OutcomeFailed, vendor.FailureAuth, string(vendor.FailureAuth)},
		{vendor.OutcomeFailed, vendor.FailureQuota, string(vendor.FailureQuota)},
		{vendor.OutcomeFailed, vendor.FailureRateLimited, vendorFailureCode},
		{vendor.OutcomeFailed, vendor.FailureNone, vendorFailureCode},
		{vendor.OutcomeCompleted, vendor.FailureAuth, ""},
	}
	for _, tc := range cases {
		if got := storedFailureCode(tc.outcome, tc.class); got != tc.want {
			t.Errorf("storedFailureCode(%s, %q) = %q, want %q", tc.outcome, tc.class, got, tc.want)
		}
	}
}

func newTestAccountsService(t *testing.T, f *transcriptFixture) (*AccountsService, *vendor.Accounts) {
	t.Helper()
	return newTestAccountsServiceDeps(t, f, accountsServiceDeps{})
}

// newTestAccountsServiceDeps builds the accounts service with one claude
// account and the given login seams.
func newTestAccountsServiceDeps(t *testing.T, f *transcriptFixture, deps accountsServiceDeps) (*AccountsService, *vendor.Accounts) {
	t.Helper()
	accounts := claudeOnlyAccounts(f)
	svc, err := newAccountsService(accounts, &VendorService{accounts: accounts, adapters: map[string]vendor.Adapter{}}, f.hub, deps)
	if err != nil {
		t.Fatal(err)
	}
	return svc, accounts
}

func TestQuotaTopicServesSnapshotThenCatchUpAndLiveEvents(t *testing.T) {
	t.Parallel()
	f := newHubOnlyFixture(t)
	_, accounts := newTestAccountsService(t, f)
	accounts.ObserveLogin(testVendorName, vendor.LoginActive)
	sub, err := f.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicQuota})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	state, snapSeq, ok := sub.Snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	assertSnapshotLogin(t, state, "logged_in")
	accounts.ObserveFailure(testVendorName, vendor.FailureAuth)
	assertNeedsLoginEvent(t, sub)
	// A client that kept the snapshot's cursor catches up on what it missed.
	late, err := f.hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindApp), Topic: TopicQuota,
		Cursor: &EventCursor{Topic: TopicQuota, Incarnation: testIncarn, Sequence: snapSeq},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(late.Close)
	assertNeedsLoginEvent(t, late)
}

func assertSnapshotLogin(t *testing.T, state []byte, login string) {
	t.Helper()
	var body quotaSnapshot
	if err := json.Unmarshal(state, &body); err != nil || len(body.Accounts) != 1 || body.Accounts[0].Login != login {
		t.Fatalf("snapshot = %s (%v), want one account with login %q", state, err, login)
	}
}

func assertNeedsLoginEvent(t *testing.T, sub *Subscription) {
	t.Helper()
	ev, ok := sub.Next(t.Context())
	if !ok || ev.Type != EventQuotaAccountChanged || ev.Key != testVendorName {
		t.Fatalf("event = %+v (%v), want quota.account_changed for the vendor", ev, ok)
	}
	var changed AccountView
	if err := json.Unmarshal(ev.Payload, &changed); err != nil || changed.State != string(vendor.AccountNeedsLogin) {
		t.Fatalf("event payload = %s (%v), want needs_login", ev.Payload, err)
	}
}

func TestQuotaTopicAndListAccountsDenyNonLocalPrincipals(t *testing.T) {
	t.Parallel()
	f := newHubOnlyFixture(t)
	svc, _ := newTestAccountsService(t, f)
	for _, principal := range []string{PrincipalSession, "", "someone-else"} {
		if _, err := f.hub.Subscribe(t.Context(), SubscribeParams{Principal: principal, Topic: TopicQuota}); err == nil {
			t.Errorf("principal %q subscribed to the quota topic", principal)
		}
		if _, err := svc.List(t.Context(), principal); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Errorf("principal %q ListAccounts error = %v, want permission denied", principal, err)
		}
	}
	if _, err := svc.List(t.Context(), string(KindApp)); err != nil {
		t.Fatalf("a local principal must list accounts: %v", err)
	}
}

func TestAccountViewCarriesNoIdentityAndFormatsTimes(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 11, 10, 12, 40, 0, 0, time.UTC)
	view := accountView(&vendor.Account{
		Vendor: "claude", State: vendor.AccountPaused, PausedUntil: reset, Login: vendor.LoginActive, QuotaKnown: true,
		Windows: []vendor.Window{{Name: fiveHourWindow, Utilization: 1, ResetsAt: reset, Status: "rejected"}},
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if view.PausedUntil != "2026-11-10T12:40:00Z" || view.Windows[0].ResetsAt != "2026-11-10T12:40:00Z" {
		t.Fatalf("view = %s", raw)
	}
	if view.QuotaGate != vendor.QuotaGateThreshold {
		t.Fatalf("view.QuotaGate = %v, want %v", view.QuotaGate, vendor.QuotaGateThreshold)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"email", "org", "path", "home"} {
		for k := range keys {
			if strings.Contains(k, banned) {
				t.Fatalf("account view exposes %q", k)
			}
		}
	}
}

// TestAccountCodeNeverTouchesTheFilesystem is the local half of VR-E09 and
// VR-03 for F9.10a: the account domain, the login probes and the accounts
// service call no os function at all, so no vendor credential path (the
// Claude, Codex or agy home, a keychain) can be opened, read or stat'ed from
// them. internal/arch scans every file for the known credential paths.
func TestAccountCodeNeverTouchesTheFilesystem(t *testing.T) {
	t.Parallel()
	files := []string{
		"accounts_service.go",
		"../vendor/account.go",
		"../vendoradapter/claude/login.go",
		"../vendoradapter/codex/login.go",
	}
	for _, name := range files {
		assertNoOSAccess(t, name)
	}
}

func assertNoOSAccess(t *testing.T, name string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		switch imp.Path.Value {
		case `"os"`, `"io/ioutil"`, `"os/exec"`, `"path/filepath"`, `"io/fs"`:
			t.Errorf("%s imports %s", name, imp.Path.Value)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, isIdent := sel.X.(*ast.Ident); isIdent && id.Name == "os" {
				t.Errorf("%s calls os.%s", name, sel.Sel.Name)
			}
		}
		return true
	})
}

// quotaProbeAdapter counts the status commands of ListAccounts; the first one
// blocks until released so a burst of calls overlaps it.
type quotaProbeAdapter struct {
	vendor.Adapter
	probes  *atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (a quotaProbeAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	if a.probes.Add(1) == 1 {
		close(a.entered)
	}
	<-a.release
	return vendor.Probe{Login: vendor.LoginActive}, nil
}

func TestConcurrentListAccountsShareOneRefresh(t *testing.T) {
	t.Parallel()
	f := newHubOnlyFixture(t)
	accounts := claudeOnlyAccounts(f)
	adapter := quotaProbeAdapter{probes: new(atomic.Int32), entered: make(chan struct{}), release: make(chan struct{})}
	vendors := &VendorService{accounts: accounts, adapters: map[string]vendor.Adapter{testVendorName: adapter}}
	svc, err := newAccountsService(accounts, vendors, f.hub, accountsServiceDeps{})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	var entered atomic.Int32
	tasks := make([]*supervise.Task, 0, callers)
	call := func(ctx context.Context) error {
		entered.Add(1)
		views, err := svc.List(ctx, string(KindApp))
		if err == nil && (len(views) != 1 || views[0].Login != "logged_in") {
			err = errcode.New(errcode.Internal, "unexpected list result")
		}
		return err
	}
	tasks = append(tasks, supervise.Go(t.Context(), "test.list_accounts", call))
	<-adapter.entered
	for range callers - 1 {
		tasks = append(tasks, supervise.Go(t.Context(), "test.list_accounts", call))
	}
	// Release the status command only once every call has entered List while
	// a refresh is in flight: the owner is blocked inside the probe, so every
	// later call must have joined its flight rather than starting a new one.
	testutil.Spin(t, "the refresh in flight", func() bool {
		svc.flightMu.Lock()
		defer svc.flightMu.Unlock()
		return svc.flight != nil
	})
	if err := testutil.WaitFor(t.Context(), "every call entered List", func() bool {
		return entered.Load() == callers
	}); err != nil {
		t.Fatal(err)
	}
	close(adapter.release)
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if got := adapter.probes.Load(); got != 1 {
		t.Fatalf("status command ran %d times for %d overlapping calls, want 1", got, callers)
	}
}

func TestQuotaTopicNeverEndsOnAStaleView(t *testing.T) {
	t.Parallel()
	f := newHubOnlyFixture(t)
	names := []string{"va", "vb"}
	const sentinel = "vc"
	accounts := vendor.NewAccounts(f.clock, []vendor.AccountVendor{
		{Name: names[0], DirectQuota: true}, {Name: names[1], DirectQuota: true}, {Name: sentinel, DirectQuota: true},
	})
	svc, err := newAccountsService(accounts, &VendorService{accounts: accounts, adapters: map[string]vendor.Adapter{}}, f.hub, accountsServiceDeps{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := f.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicQuota})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	observeConcurrently(t.Context(), t, accounts, names)
	// A notification that lost the race to a newer observation carries an
	// outdated copy; publishing it must still end on the current view.
	stale, _ := accounts.Account(names[0])
	accounts.ObserveQuota(names[0], &vendor.RateLimit{Windows: []vendor.Window{{Name: fiveHourWindow, Utilization: 0.9995}}})
	svc.publish(&stale)
	// The sentinel key is published after every other event, so draining to it
	// sees the last event of each observed vendor.
	accounts.ObserveFailure(sentinel, vendor.FailureAuth)
	last := drainQuotaEventsUntil(t, sub, sentinel)
	for _, account := range accounts.Snapshot() {
		if account.Vendor == sentinel {
			continue
		}
		if diff := cmp.Diff(accountView(&account), last[account.Vendor]); diff != "" {
			t.Errorf("last event of %s differs from the registry (-registry +event):\n%s", account.Vendor, diff)
		}
	}
}

// observeConcurrently feeds each vendor from several goroutines. Every value is
// distinct, so every observation changes the view and publishes.
func observeConcurrently(ctx context.Context, t *testing.T, accounts *vendor.Accounts, names []string) {
	t.Helper()
	tasks := make([]*supervise.Task, 0, 4*len(names))
	for _, name := range names {
		for worker := range 4 {
			tasks = append(tasks, supervise.Go(ctx, "test.observe_quota", func(context.Context) error {
				for i := range 40 {
					u := float64(worker*40+i+1) / 1000
					accounts.ObserveQuota(name, &vendor.RateLimit{Windows: []vendor.Window{{Name: fiveHourWindow, Utilization: u}}})
				}
				return nil
			}))
		}
	}
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func drainQuotaEventsUntil(t *testing.T, sub *Subscription, stop string) map[string]AccountView {
	t.Helper()
	last := map[string]AccountView{}
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatal("subscription ended before the sentinel event")
		}
		if ev.Key == stop {
			return last
		}
		var view AccountView
		if err := json.Unmarshal(ev.Payload, &view); err != nil {
			t.Fatal(err)
		}
		last[ev.Key] = view
	}
}
