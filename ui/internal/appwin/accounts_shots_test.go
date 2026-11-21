package appwin

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotAccounts = "accounts"
	testAccInc   = "inc-accounts-shot"
)

var errTestUnsupportedLogin = errors.New("agy has no login command; sign in from the agy app")

type fakeAppAccountsClient struct {
	mu          sync.Mutex
	inc         string
	accounts    []client.Account
	startCalls  []string
	cancelCalls []string
}

func (f *fakeAppAccountsClient) HostStatus(context.Context) (client.HostStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.HostStatus{Incarnation: f.inc}, nil
}

func (f *fakeAppAccountsClient) ListAccounts(context.Context) ([]client.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts, nil
}

func (f *fakeAppAccountsClient) StartLogin(_ context.Context, vendor string) (client.StartLoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, vendor)
	return client.StartLoginResult{LoginID: "login-" + vendor}, nil
}

func (f *fakeAppAccountsClient) CancelLogin(_ context.Context, vendor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, vendor)
	return nil
}

func accountsShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotAccounts + "-light-ui100", scene: shotAccounts, light: true, uiScale: 1.0},
		{name: shotAccounts + "-dark-ui100", scene: shotAccounts, light: false, uiScale: 1.0},
	}
}

func buildAccountsShotScreens(t *testing.T, ac accounts.Client) (*screens, *[]string) {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testAccInc})
	var opened []string
	sc.accountsView.openURL = func(u string) error {
		opened = append(opened, u)
		return nil
	}
	if ac != nil {
		w := accounts.NewWorker(func() accounts.Client { return ac }, nil)
		w.Start(t.Context())
		t.Cleanup(w.Stop)
		sc.accountsWorker = w
	}
	seedAccountsShotData(t, sc)
	return sc, &opened
}

func seedAccountsShotData(t *testing.T, sc *screens) {
	t.Helper()
	resetAt := time.Unix(1_700_000_000, 0).Add(95 * time.Minute).UTC().Format(time.RFC3339)
	accs := []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateOK, Login: client.AccountLoginLoggedIn, QuotaKnown: true,
			Windows: []client.QuotaWindow{
				{Name: "five_hour", Utilization: 0.42, ResetsAt: resetAt, Status: client.QuotaStatusAllowed},
				{Name: "seven_day", Utilization: 0.88, ResetsAt: resetAt, Status: client.QuotaStatusAllowedWarning},
			},
		},
		{
			Vendor: client.VendorCodex, State: client.AccountStatePaused, PausedUntil: resetAt,
			Login: client.AccountLoginLoggedIn, QuotaKnown: true,
			Windows: []client.QuotaWindow{
				{Name: "300m", Utilization: 1.0, ResetsAt: resetAt, Status: client.QuotaStatusRejected},
			},
		},
		{
			Vendor: client.VendorAGY, State: "unexpected_state", Login: client.AccountLoginUnknown, QuotaKnown: false,
		},
	}
	snap, err := json.Marshal(map[string]any{"accounts": accs})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyQuotaDelivery(hostconn.Update{
		Topic:    topicQuota,
		Snapshot: &client.Snapshot{Incarnation: testAccInc, Sequence: 1, State: snap},
	})
	loginEv, err := json.Marshal(client.LoginProgress{
		Vendor:  client.VendorCodex,
		LoginID: "login-codex-1",
		State:   client.LoginStateWaiting,
		Prompt:  "Open https://auth.openai.com/codex/device and enter CODE-9421\nDo not auto-open http://evil.invalid or [link](https://trap.example)",
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyLoginDelivery(hostconn.Update{
		Topic: topicLogin,
		Event: &client.Event{Sequence: 2, Type: client.EventLoginProgress, Payload: loginEv},
	})
	sc.accounts.BeginStartLogin(client.VendorAGY)
	sc.accounts.ApplyStartLoginResult(&accounts.Result{
		Kind: accounts.OpStartLogin, Vendor: client.VendorAGY, Code: "failed_precondition", Err: errTestUnsupportedLogin,
	})
	sc.accountsOpen = true
}

func renderAccountsShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	env := newAccountsTestEnv(t, spec.light, spec.uiScale)
	sc, opened := buildAccountsShotScreens(t, nil)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if sc.lastErr != "" {
		t.Fatalf("accounts shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	if len(*opened) != 0 {
		t.Fatalf("render opened URLs automatically: %+v", *opened)
	}
	return img
}

func newAccountsTestEnv(t *testing.T, light bool, uiScale float32) *kit.Env {
	t.Helper()
	env := newInboxTestEnv(t)
	if !light {
		th := theme.Dark(env.Sh)
		env.Th = &th
	}
	env.UIScale = uiScale
	return env
}

// TestAccountsViewRendersWindowsAndLoginFlow verifies account rows, unknown
// states, F9.10 rate-limit windows, explicit-click HTTPS URL handling, the
// Q-112 login outcomes (unsupported/needs_terminal/timed_out), and light/dark
// render shots.
func TestAccountsViewRendersWindowsAndLoginFlow(t *testing.T) {
	t.Parallel()
	assertAccountsViewStatesAndLinks(t)

	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	specs := accountsShotSpecs()
	lightImg := renderAccountsShot(t, win, specs[0])
	darkImg := renderAccountsShot(t, win, specs[1])
	if isUniformRGBA(lightImg) || isUniformRGBA(darkImg) {
		t.Fatal("accounts shot rendered uniform/blank image")
	}
	if !imagesDifferRGBA(lightImg, darkImg) {
		t.Fatal("accounts light and dark shots are identical")
	}
}

func assertAccountsViewStatesAndLinks(t *testing.T) {
	t.Helper()
	ac := &fakeAppAccountsClient{inc: testAccInc}
	sc, opened := buildAccountsShotScreens(t, ac)
	env := newInboxTestEnv(t)
	router := new(input.Router)

	sc.accountsOpen = false
	sc.inboxOpen = true
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	router.Queue(key.Event{Name: key.Name("A"), Modifiers: key.ModShortcut | key.ModShift, State: key.Press})
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if !sc.accountsOpen || sc.inboxOpen {
		t.Fatalf("Ctrl/Cmd+Shift+A accountsOpen=%v inboxOpen=%v, want true/false", sc.accountsOpen, sc.inboxOpen)
	}
	if agyAcc, ok := sc.accounts.Account(client.VendorAGY); !ok || agyAcc.State != accounts.AccountStateUnknown {
		t.Fatalf("unknown agy state = %+v, want unknown", agyAcc)
	}

	assertAccountsHTTPSLinks(t, sc, opened)
	assertLoginOutcomesAndCancel(t, env, sc, router, ac)
}

func assertAccountsHTTPSLinks(t *testing.T, sc *screens, opened *[]string) {
	t.Helper()
	if len(*opened) != 0 {
		t.Fatalf("URL opened without explicit click: %+v", *opened)
	}
	urls := extractHTTPSURLs("Visit http://evil.invalid and https://auth.openai.com/codex/device")
	if len(urls) != 1 || urls[0] != "https://auth.openai.com/codex/device" {
		t.Fatalf("extractHTTPSURLs = %+v, want only https URL", urls)
	}
	sc.accountsView.openHTTPS("http://evil.invalid")
	if len(*opened) != 0 || sc.accountsView.openErr == "" {
		t.Fatalf("non-https URL was not blocked: opened=%+v err=%q", *opened, sc.accountsView.openErr)
	}
	btn := sc.accountsView.widgetsFor(client.VendorCodex).linkButtons["https://auth.openai.com/codex/device"]
	if btn == nil {
		t.Fatal("missing Open URL button for visible https URL")
	}
	btn.Tag()
	sc.accountsView.openHTTPS("https://auth.openai.com/codex/device")
	if len(*opened) != 1 || (*opened)[0] != "https://auth.openai.com/codex/device" {
		t.Fatalf("explicit https open = %+v", *opened)
	}
}

func assertLoginOutcomesAndCancel(t *testing.T, env *kit.Env, sc *screens, router *input.Router, ac *fakeAppAccountsClient) {
	t.Helper()
	sc.accounts.BeginCancelLogin(client.VendorCodex)
	req := accounts.Request{Kind: accounts.OpCancelLogin, Vendor: client.VendorCodex}
	sc.accountsWorker.Do(&req)
	select {
	case res := <-sc.accountsWorker.Results():
		sc.applyAccountsResults([]accounts.Result{res})
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for CancelLogin: %v", t.Context().Err())
	}
	ac.mu.Lock()
	cancels := len(ac.cancelCalls)
	ac.mu.Unlock()
	if cancels != 1 {
		t.Fatalf("CancelLogin calls = %d, want 1", cancels)
	}

	for _, tc := range []struct {
		vendor, state, want string
	}{
		{client.VendorAGY, accounts.LoginStateUnsupported, "agy has no login command"},
		{client.VendorClaude, client.LoginStateNeedsTerminal, "claude auth login"},
		{client.VendorCodex, client.LoginStateTimedOut, "15 minutes"},
	} {
		if tc.state != accounts.LoginStateUnsupported {
			payload, err := json.Marshal(client.LoginProgress{Vendor: tc.vendor, LoginID: "l-" + tc.vendor, State: tc.state})
			if err != nil {
				t.Fatal(err)
			}
			sc.applyLoginDelivery(hostconn.Update{
				Topic: topicLogin,
				Event: &client.Event{Sequence: 99, Type: client.EventLoginProgress, Payload: payload},
			})
		}
		entry, ok := sc.accounts.Login(tc.vendor)
		if !ok {
			t.Fatalf("missing login entry for %s", tc.vendor)
		}
		_, desc := loginStatePresentation(tc.vendor, &entry)
		if tc.state == client.LoginStateNeedsTerminal {
			desc = terminalLoginCommand(tc.vendor)
		}
		if !strings.Contains(desc, tc.want) {
			t.Fatalf("%s state %s presentation %q missing %q", tc.vendor, tc.state, desc, tc.want)
		}
	}
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if sc.lastErr != "" {
		t.Fatalf("unexpected screen error rendering login outcomes: %s", sc.lastErr)
	}
}
