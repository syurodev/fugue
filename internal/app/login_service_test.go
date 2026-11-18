package app

// F9.10b evidence: login start, cancel, timeout and the per-vendor slot,
// against fake vendor CLIs written with testutil.WriteExecutable. No test
// here completes or awaits a real vendor login (the Q-112 measurement rule).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
)

// loginEventWait bounds one subscription read: a login outcome is a control
// event the hub never drops, so a missing event is a real failure.
const loginEventWait = 10 * time.Second

// writeLoginFakeCLI is a fake vendor CLI: print the prompt, then linger or
// end. Written with the ForkLock helper so a concurrent fork cannot inherit
// the open write descriptor.
func writeLoginFakeCLI(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-cli")
	testutil.WriteExecutable(t, path, []byte("#!/bin/sh\n"+body))
	return path
}

const fakeLoginPrompt = "Visit https://login.example/device and enter FAKE-CODE-42"

// lingerCLI prints the prompt and stays until it is stopped.
func lingerCLI(t *testing.T) string {
	t.Helper()
	return writeLoginFakeCLI(t, "printf '%s\\n' '"+fakeLoginPrompt+"'\nexec /bin/sleep 300\n")
}

// exitCLI prints the prompt and ends with the given code.
func exitCLI(t *testing.T, code int) string {
	t.Helper()
	return writeLoginFakeCLI(t, "printf '%s\\n' '"+fakeLoginPrompt+"'\nexit "+strconv.Itoa(code)+"\n")
}

// fakeLoginAdapter is a vendor.Adapter for the login tests: its login spawns
// a fake CLI through the real login runner, and its status probe is gated so
// a test decides what the F9.10a re-probe says and when.
type fakeLoginAdapter struct {
	clk           clock.Clock
	needsTerminal bool
	cliPath       string
	entered       chan struct{} // closed once a status probe entered
	release       chan struct{} // the probe answers logged_in only after this closes
	probes        atomic.Int32
	enterOnce     sync.Once

	runMu sync.Mutex
	run   vendor.LoginRun
}

func newFakeLoginAdapter(t *testing.T, clk clock.Clock, cliPath string, needsTerminal bool) *fakeLoginAdapter {
	t.Helper()
	return &fakeLoginAdapter{
		clk: clk, needsTerminal: needsTerminal, cliPath: cliPath,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
}

func (a *fakeLoginAdapter) Probe(ctx context.Context, _ vendor.AccountHome) (vendor.Probe, error) {
	a.probes.Add(1)
	a.enterOnce.Do(func() { close(a.entered) })
	select {
	case <-a.release:
		return vendor.Probe{Path: a.cliPath, Login: vendor.LoginActive, Capability: vendor.Capability{DirectQuota: true}}, nil
	case <-ctx.Done():
		return vendor.Probe{}, errcode.Wrap(errcode.Canceled, "the gated probe wait ended", ctx.Err())
	}
}

func (a *fakeLoginAdapter) Start(context.Context, *vendor.StartSpec) (vendor.Session, error) {
	return nil, errcode.New(errcode.InvalidArgument, "sessions are not part of the login tests")
}

// LoginCommand implements vendor.LoginStarter.
func (a *fakeLoginAdapter) LoginCommand() vendor.LoginCommand {
	return vendor.LoginCommand{NeedsTerminal: a.needsTerminal}
}

// StartLogin implements vendor.LoginStarter: the real login runner over the
// fake CLI, so cancel and timeout exercise a real process.
func (a *fakeLoginAdapter) StartLogin(ctx context.Context, _ vendor.AccountHome) (vendor.LoginRun, error) {
	run, err := vendor.StartLoginProcess(ctx, &vendor.LoginOptions{
		Name:  "fake",
		Path:  a.cliPath,
		Env:   nil,
		Clock: a.clk,
	})
	a.runMu.Lock()
	a.run = run
	a.runMu.Unlock()
	if err != nil {
		return nil, errcode.Wrap(errcode.CodeOf(err), "run the fake login", err)
	}
	return run, nil
}

func (a *fakeLoginAdapter) loginRun() vendor.LoginRun {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return a.run
}

func (a *fakeLoginAdapter) processEnded() bool {
	select {
	case <-a.loginRun().Done():
		return true
	default:
		return false
	}
}

// newLoginService builds an accounts service over the given adapters, with
// a login clock of its own — the only timer armed on it is the login
// timeout, so a test can advance it deterministically — and a monitor bound
// to the test's context.
func newLoginService(t *testing.T, f *transcriptFixture, adapters map[string]vendor.Adapter) (*AccountsService, *vendor.Accounts, *clock.Fake) {
	t.Helper()
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	slices.Sort(names)
	vendors := make([]vendor.AccountVendor, 0, len(names))
	for _, name := range names {
		vendors = append(vendors, vendor.AccountVendor{Name: name, DirectQuota: name == VendorClaude})
	}
	accounts := vendor.NewAccounts(f.clock, vendors)
	// The login clock is separate from the fixture's: nothing else arms
	// timers on it, so BlockUntilPending(1) waits for the login timeout
	// itself and Advance fires exactly that timer.
	loginClk := clock.NewFake(time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	svc, err := newAccountsService(accounts, &VendorService{accounts: accounts, adapters: adapters}, f.hub, accountsServiceDeps{
		Clock: loginClk,
		IDs:   ids.NewUUIDv7(loginClk, ids.CryptoRandom{}),
		GoTask: func(name string, fn func(context.Context) error) *supervise.Task {
			return supervise.Go(t.Context(), name, fn)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A login left running at the test's end must not leak its process: stop
	// every one. CancelLogin on an ended login is a typed NotFound.
	t.Cleanup(func() {
		for _, name := range names {
			if err := svc.CancelLogin(context.Background(), string(KindApp), name); err != nil && errcode.CodeOf(err) != errcode.NotFound {
				t.Logf("cancel the %s login: %v", name, err)
			}
		}
	})
	return svc, accounts, loginClk
}

func subscribeLoginTopic(t *testing.T, f *transcriptFixture) *Subscription {
	t.Helper()
	sub, err := f.hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicLogin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}

// nextLoginProgress reads one login event. A final outcome is a control event
// the hub never drops, so a missing event is a real failure.
func nextLoginProgress(t *testing.T, sub *Subscription) LoginProgress {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), loginEventWait)
	defer cancel()
	ev, ok := sub.Next(ctx)
	if !ok {
		t.Fatal("no login event arrived within the wait")
	}
	var p LoginProgress
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode a login event: %v", err)
	}
	return p
}

// drainUntilOutcome reads login events until the outcome arrives.
func drainUntilOutcome(t *testing.T, sub *Subscription, outcome string) {
	t.Helper()
	for range 8 {
		if p := nextLoginProgress(t, sub); p.State == outcome {
			return
		}
	}
	t.Fatalf("no %s event arrived within 8 login events", outcome)
}

func waitForLoginSpawn(t *testing.T, ad *fakeLoginAdapter) vendor.LoginRun {
	t.Helper()
	if err := testutil.WaitFor(t.Context(), "the login process to spawn", func() bool {
		return ad.loginRun() != nil
	}); err != nil {
		t.Fatal(err)
	}
	return ad.loginRun()
}

func waitForProcessEnd(t *testing.T, ad *fakeLoginAdapter) {
	t.Helper()
	if err := testutil.WaitFor(t.Context(), "the login process to end", ad.processEnded); err != nil {
		t.Fatal(err)
	}
}

func TestStartLoginRelaysTheCLIPromptVerbatim(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	sub := subscribeLoginTopic(t, f)
	view, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != LoginStarted || view.Vendor != VendorClaude || view.LoginID == "" {
		t.Fatalf("StartLogin view = %+v, want a started login with an identity", view)
	}
	started := nextLoginProgress(t, sub)
	if started.State != LoginStarted || started.LoginID != view.LoginID {
		t.Fatalf("first login event = %+v, want started for %s", started, view.LoginID)
	}
	// Every line the CLI printed is relayed exactly as printed, never
	// rewritten, truncated or redacted.
	for range 8 {
		p := nextLoginProgress(t, sub)
		if p.State == LoginWaiting && p.Prompt == fakeLoginPrompt {
			return
		}
	}
	t.Fatalf("no waiting event relayed the prompt verbatim: %q", fakeLoginPrompt)
}

func TestCancelLoginStopsTheProcess(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	sub := subscribeLoginTopic(t, f)
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatal(err)
	}
	run := waitForLoginSpawn(t, ad)
	if err := svc.CancelLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatal(err)
	}
	waitForProcessEnd(t, ad)
	// The stop order ends the CLI by signal, so its end carries no exit code
	// (the same convention as a bounded status command): what matters is that
	// the process is gone and the host said so.
	if end := run.End(); end.Ran {
		t.Fatalf("login end = %+v, want a signal-ended process without an exit code", end)
	}
	drainUntilOutcome(t, sub, LoginCancelled)
	if err := svc.CancelLogin(t.Context(), string(KindApp), VendorClaude); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("second CancelLogin error = %v, want not found", err)
	}
}

func TestLoginTimesOutAndStops(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, loginClk := newLoginService(t, f, map[string]vendor.Adapter{VendorCodex: ad})
	sub := subscribeLoginTopic(t, f)
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorCodex); err != nil {
		t.Fatal(err)
	}
	waitForLoginSpawn(t, ad)
	if err := loginClk.BlockUntilPending(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	loginClk.Advance(LoginTimeout)
	waitForProcessEnd(t, ad)
	drainUntilOutcome(t, sub, LoginTimedOut)
	if err := svc.CancelLogin(t.Context(), string(KindApp), VendorCodex); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("CancelLogin after the timeout error = %v, want not found", err)
	}
}

func TestOnlyOneLoginPerVendor(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	claudeAd := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	codexAd := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: claudeAd, VendorCodex: codexAd})
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatal(err)
	}
	_, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude)
	if errcode.CodeOf(err) != errcode.AlreadyExists {
		t.Fatalf("second StartLogin for claude error = %v, want already exists", err)
	}
	// The slot is per vendor: another vendor's login starts alongside.
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorCodex); err != nil {
		t.Fatalf("StartLogin for another vendor error = %v, want ok", err)
	}
}

func TestSuccessfulLoginReprobesBeforeClearingNeedsLogin(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, exitCLI(t, 0), false)
	svc, accounts, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	sub := subscribeLoginTopic(t, f)
	accounts.ObserveLogin(VendorClaude, vendor.LoginMissing)
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatal(err)
	}
	// The CLI has exited by now, so the only thing that can clear needs_login
	// is the re-probe; the host may call the login a success only after the
	// probe says logged in, and until then the account stays needs_login.
	if err := testutil.WaitFor(t.Context(), "the F9.10a login probe to run", func() bool {
		select {
		case <-ad.entered:
			return true
		default:
			return false
		}
	}); err != nil {
		t.Fatal(err)
	}
	if account, ok := accounts.Account(VendorClaude); !ok || account.State != vendor.AccountNeedsLogin {
		t.Fatalf("account before the probe answer = %+v, want needs_login", account)
	}
	close(ad.release)
	if err := testutil.WaitFor(t.Context(), "the account to read logged in", func() bool {
		account, ok := accounts.Account(VendorClaude)
		return ok && account.State == vendor.AccountOK && account.Login == vendor.LoginActive
	}); err != nil {
		t.Fatal(err)
	}
	drainUntilSucceeded(t, sub)
	if got := ad.probes.Load(); got != 1 {
		t.Fatalf("the login probe ran %d times, want 1", got)
	}
}

// drainUntilSucceeded reads login events until the success outcome arrives,
// and refuses a failure: the probe said logged in, so failed is a defect.
func drainUntilSucceeded(t *testing.T, sub *Subscription) {
	t.Helper()
	for range 8 {
		switch p := nextLoginProgress(t, sub); p.State {
		case LoginSucceeded:
			return
		case LoginFailed:
			t.Fatal("the login ended failed although the probe said logged in")
		}
	}
	t.Fatal("no succeeded event arrived within 8 login events")
}

func TestSessionPrincipalCannotStartALogin(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	for _, principal := range []string{PrincipalSession, "", "someone-else"} {
		if _, err := svc.StartLogin(t.Context(), principal, VendorClaude); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Errorf("principal %q StartLogin error = %v, want permission denied", principal, err)
		}
		if err := svc.CancelLogin(t.Context(), principal, VendorClaude); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Errorf("principal %q CancelLogin error = %v, want permission denied", principal, err)
		}
	}
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatalf("StartLogin as the local app principal error = %v, want ok", err)
	}
}

func TestAgyStartLoginIsUnsupported(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), false)
	svc, _, _ := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad, VendorAGY: &agy.Adapter{}})
	_, err := svc.StartLogin(t.Context(), string(KindApp), VendorAGY)
	if errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("StartLogin for agy error = %v, want failed precondition", err)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("StartLogin for agy error = %v, want a coded error", err)
	}
	// Q-112: the error tells the user where the login actually happens.
	if msg := coded.Message(); !strings.Contains(msg, "no login command") || !strings.Contains(msg, VendorAGY) {
		t.Fatalf("StartLogin for agy message = %q, want the sign-in-from-the-app guidance", msg)
	}
}

func TestClaudeLoginWithoutCallbackEndsNeedsTerminal(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	// claude finishes its login by reading a code pasted into its own stdin
	// (Q-112 B2): the host never relays that, so a login the browser callback
	// did not finish in time ends needs_terminal, telling the user to run the
	// login in their own terminal.
	ad := newFakeLoginAdapter(t, f.clock, lingerCLI(t), true)
	svc, _, loginClk := newLoginService(t, f, map[string]vendor.Adapter{VendorClaude: ad})
	sub := subscribeLoginTopic(t, f)
	if _, err := svc.StartLogin(t.Context(), string(KindApp), VendorClaude); err != nil {
		t.Fatal(err)
	}
	waitForLoginSpawn(t, ad)
	if err := loginClk.BlockUntilPending(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	loginClk.Advance(LoginTimeout)
	waitForProcessEnd(t, ad)
	drainUntilOutcome(t, sub, LoginNeedsTerminal)
}
