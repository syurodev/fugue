package appwin

import (
	"errors"
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
)

// The failure UX's model tests (F10.5e, desktop-app §6): the banners derive
// from the read model, so the specs are the contract — what each failure
// names and which action it offers. The DA-E05 end-to-end runs live in
// ui/internal/hostconn.

// TestFailureUXProbeFailureNamesTheCLIProblem pins the missing-CLI row: the
// probe's typed failure renders as the danger banner with install guidance.
func TestFailureUXProbeFailureNamesTheCLIProblem(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	f.applyProbe(nil, errProbeNotFound)
	specs := f.specs(hostconn.StateConnected, "", "", false, session.Session{})
	if len(specs) != 1 || specs[0].title != "Vendor CLI unavailable" {
		t.Fatalf("specs = %+v, want the CLI banner", specs)
	}
	if !strings.Contains(strings.ToLower(specs[0].message), "install or upgrade") {
		t.Fatalf("message %q misses the install guidance", specs[0].message)
	}
	if specs[0].dismissible {
		t.Fatal("the CLI banner is dismissible while the CLI is still missing")
	}
}

// wantBanner is the banner a probe answer must render, or an empty spec for
// silence.
type wantBanner struct {
	title       string
	action      string
	dismissible bool
	contains    string
}

// TestFailureUXProbeCarriesIntoBanners pins the probe answers: an untested
// version warns with the tested range, a signed-out CLI offers the accounts
// route, and a healthy probe stays quiet.
func TestFailureUXProbeCarriesIntoBanners(t *testing.T) {
	t.Parallel()
	table := map[string]struct {
		probe client.VendorProbe
		want  wantBanner
	}{
		"untested version warns": {
			probe: client.VendorProbe{Vendor: client.VendorClaude, Version: "1.0.5", VersionWarning: "tested range is 2.1.300..2.1.400"},
			want:  wantBanner{title: "claude 1.0.5 is outside the tested range", dismissible: true, contains: "2.1.300..2.1.400"},
		},
		"signed-out CLI offers the accounts route": {
			probe: client.VendorProbe{Vendor: client.VendorClaude, Version: fakeProbedVersion, VersionTested: true, Login: client.AccountLoginLoggedOut},
			want:  wantBanner{title: "claude is not signed in", action: "Open Accounts", dismissible: true, contains: "Accounts"},
		},
		"healthy probe stays quiet": {
			probe: client.VendorProbe{Vendor: client.VendorClaude, Version: fakeProbedVersion, VersionTested: true, Login: client.AccountLoginLoggedIn},
		},
	}
	for name, tc := range table {
		f := newFailureUX()
		f.applyProbe([]client.VendorProbe{tc.probe}, nil)
		assertProbeBanner(t, name, f, tc.want)
	}
}

// assertProbeBanner folds one probe answer into a failure UX and compares the
// one banner the answer may render against the want.
func assertProbeBanner(t *testing.T, name string, f *failureUX, want wantBanner) {
	t.Helper()
	specs := f.specs(hostconn.StateConnected, "", "", false, session.Session{})
	if want.title == "" {
		if len(specs) != 0 {
			t.Fatalf("%s: banners %+v, want none", name, specs)
		}
		return
	}
	if len(specs) != 1 || specs[0].title != want.title {
		t.Fatalf("%s: banners %+v, want %q", name, specs, want.title)
	}
	spec := specs[0]
	if spec.actionLabel != want.action || spec.dismissible != want.dismissible {
		t.Fatalf("%s: action %q dismissible %v, want %q %v", name, spec.actionLabel, spec.dismissible, want.action, want.dismissible)
	}
	if !strings.Contains(strings.ToLower(spec.message), strings.ToLower(want.contains)) {
		t.Fatalf("%s: message %q misses %q", name, spec.message, want.contains)
	}
}

// TestFailureUXDismissalPinsToTheAnswer pins the dismissal rule: the banner
// stays gone for the answer it was dismissed on and returns for a different
// one.
func TestFailureUXDismissalPinsToTheAnswer(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	f := newFailureUX()
	f.applyProbe([]client.VendorProbe{{Vendor: client.VendorClaude, Version: fakeProbedVersion, VersionTested: true, Login: client.AccountLoginLoggedOut}}, nil)
	specs := f.specs(hostconn.StateConnected, "", "", false, session.Session{})
	for _, spec := range specs {
		if spec.title == "claude is not signed in" {
			f.applyEvents(spec, kit.BannerEvents{Dismissed: true}, sc)
		}
	}
	// The same answer stays dismissed; a different vendor's sign-out shows.
	if titles := visibleTitles(f, false, session.Session{}); len(titles) != 0 {
		t.Fatalf("banners after dismissal = %v, want none", titles)
	}
	f.applyProbe([]client.VendorProbe{{Vendor: client.VendorCodex, Login: client.AccountLoginLoggedOut}}, nil)
	titles := visibleTitles(f, false, session.Session{})
	if len(titles) != 1 || titles[0] != "codex is not signed in" {
		t.Fatalf("banners for a new answer = %v, want the codex sign-in banner", titles)
	}
}

// TestFailureUXDisconnectedNamesDataDir pins the lost-connection banner: it
// names the reason and the data directory a manual `fugue serve` start needs
// (activation §3).
func TestFailureUXDisconnectedNamesDataDir(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	specs := f.specs(hostconn.StateDisconnected, "host not running", "/tmp/fugue-data", false, session.Session{})
	if len(specs) != 1 || specs[0].title != "Connection to the Fugue core lost" {
		t.Fatalf("specs = %+v, want the connection banner", specs)
	}
	if !strings.Contains(specs[0].message, "host not running") || !strings.Contains(specs[0].message, "/tmp/fugue-data") {
		t.Fatalf("message %q misses the reason or the data directory", specs[0].message)
	}
	if specs[0].dismissible {
		t.Fatal("the connection banner is dismissible; the reader could dismiss the outage")
	}
	if titles := visibleTitles(f, false, session.Session{}); len(titles) != 0 {
		t.Fatalf("banners while connected = %v, want none", titles)
	}
}

// TestFailureUXDetachedBannerFollowsTheOpenSession pins the detached row
// (desktop-app §6): the open detached session explains itself and offers
// resume; other states stay quiet.
func TestFailureUXDetachedBannerFollowsTheOpenSession(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	specs := f.specs(hostconn.StateConnected, "", "", true, session.Session{ID: "s1", State: stateDetached})
	if len(specs) != 1 || specs[0].title != "Session detached" || specs[0].actionLabel != "Resume session" {
		t.Fatalf("specs = %+v, want the detached banner with the resume action", specs)
	}
	if titles := visibleTitles(f, true, session.Session{ID: "s1", State: stateReady}); len(titles) != 0 {
		t.Fatalf("banners for a ready session = %v, want none", titles)
	}
	if titles := visibleTitles(f, false, session.Session{}); len(titles) != 0 {
		t.Fatalf("banners without an open session = %v, want none", titles)
	}
}

// TestFailureUXActiveSessionCountAndCloseConfirm pins the last-window rule
// (DA-E08): live turns ask, the confirmed close passes the next destroy, and
// the safe choice asks again.
func TestFailureUXActiveSessionCountAndCloseConfirm(t *testing.T) {
	t.Parallel()
	sessions := []session.Session{
		{ID: "s1", State: stateRunning},
		{ID: "s2", State: stateAwaitingApproval},
		{ID: "s3", State: stateReady},
		{ID: "s4", State: stateWaiting, WaitingReason: reasonVaultLocked},
		{ID: "s5", State: stateDetached},
	}
	if n := activeSessionCount(sessions); n != 2 {
		t.Fatalf("active count = %d, want 2", n)
	}
	d := newCloseConfirmDialog()
	if d.shouldAsk(2) != true || d.shouldAsk(0) != false {
		t.Fatalf("shouldAsk = %v/%v, want true/false", d.shouldAsk(2), d.shouldAsk(0))
	}
	closes := 0
	d.requestClose = func() { closes++ }
	d.open(2)
	if !d.dialog.IsOpen() {
		t.Fatal("the confirm dialog did not open")
	}
	d.confirmed()
	if d.dialog.IsOpen() || !d.armed || closes != 1 {
		t.Fatalf("confirmed: open %v, armed %v, closes %d; want closed, armed, 1", d.dialog.IsOpen(), d.armed, closes)
	}
	if d.shouldAsk(2) {
		t.Fatal("the confirmed close was asked again")
	}
	d2 := newCloseConfirmDialog()
	d2.open(1)
	d2.cancelled()
	if d2.dialog.IsOpen() || d2.armed || !d2.shouldAsk(1) {
		t.Fatalf("cancelled: open %v, armed %v, ask %v; want closed, unarmed, asking", d2.dialog.IsOpen(), d2.armed, d2.shouldAsk(1))
	}
}

// TestFailureUXVaultWaitingCount pins the unlock card's count: only sessions
// waiting on the vault count (activation §3).
func TestFailureUXVaultWaitingCount(t *testing.T) {
	t.Parallel()
	sessions := []session.Session{
		{ID: "s1", State: stateWaiting, WaitingReason: reasonVaultLocked},
		{ID: "s2", State: stateWaiting, WaitingReason: "quota"},
		{ID: "s3", State: stateDetached},
	}
	if n := vaultWaitingCount(sessions); n != 1 {
		t.Fatalf("vault waiting count = %d, want 1", n)
	}
	if n := vaultWaitingCount(nil); n != 0 {
		t.Fatalf("empty count = %d, want 0", n)
	}
}

// TestFailureUXClosePostReachesTheHostAndFoldsBack proves the detached
// session's close action end to end at the fake host: the poster's close
// carries its own command id, and a failed close lands in the composer's
// error line.
func TestFailureUXClosePostReachesTheHostAndFoldsBack(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateDetached, client.Capability{})
	fx.poster.CloseSession("s1")
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].method != "close" || calls[0].session != "s1" || calls[0].commandID == "" {
		t.Fatalf("host calls = %+v, want one identified close on s1", calls)
	}
	// A failed close folds into the composer's own error line.
	fx.sc.applyPostResult(session.PostResult{Kind: session.PostClose, Session: "s1", Err: errCloseRefused})
	if st := fx.state(); st.lastErr != errCloseRefused.Error() {
		t.Fatalf("composer error = %q, want %q", st.lastErr, errCloseRefused.Error())
	}
}

// TestStartFlowBlocksAPausedAccount pins the paused pre-check (desktop-app
// §6): the reason names the vendor-reported reset and never guesses one, and
// a usable account reports nothing.
func TestStartFlowBlocksAPausedAccount(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.accounts.ApplyListAccounts("inc-1", []client.Account{{
		Vendor: client.VendorClaude, State: client.AccountStatePaused, PausedUntil: "2026-11-25T10:00:00Z",
	}})
	f := sc.composer.start
	reason := f.pausedReason(sc)
	if !f.startBlocked(sc) {
		t.Fatalf("paused reason %q did not block the start", reason)
	}
	if !strings.Contains(reason, "paused by the vendor") || !strings.Contains(reason, "2026-11-25 10:00 UTC") {
		t.Fatalf("paused reason %q misses the vendor's reset time", reason)
	}
	sc.accounts.ApplyListAccounts("inc-2", []client.Account{{
		Vendor: client.VendorClaude, State: client.AccountStateOK,
	}})
	if reason := f.pausedReason(sc); reason != "" || f.startBlocked(sc) {
		t.Fatalf("usable account blocked the start: %q", reason)
	}
}

// TestFailureUXFormatPausedUntil pins the reset-time rendering: only the
// vendor's parseable timestamps render.
func TestFailureUXFormatPausedUntil(t *testing.T) {
	t.Parallel()
	if got := formatPausedUntil("2026-11-25T10:00:00Z"); got != "2026-11-25 10:00 UTC" {
		t.Fatalf("format = %q, want the vendor's time", got)
	}
	if got := formatPausedUntil(""); got != "" {
		t.Fatalf("empty format = %q, want empty", got)
	}
	if got := formatPausedUntil("not-a-time"); got != "" {
		t.Fatalf("garbage format = %q, want empty", got)
	}
}

// TestFailureUXGateShowsWaitingSessions pins the unlock card's waiting line
// wiring: the gate computes the count from the sidebar the deliveries feed.
func TestFailureUXGateShowsWaitingSessions(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "s1", State: stateWaiting, WaitingReason: reasonVaultLocked},
		{ID: "s2", State: stateRunning},
	}})
	if n := vaultWaitingCount(sc.sidebar.List()); n != 1 {
		t.Fatalf("gate waiting count = %d, want 1", n)
	}
}

// visibleTitles renders the specs' visible titles for a connected host with
// no CLI problems, the tests' view of the state banners.
func visibleTitles(f *failureUX, hasOpen bool, sess session.Session) []string {
	var titles []string
	for _, spec := range f.specs(hostconn.StateConnected, "", "", hasOpen, sess) {
		if spec.visible {
			titles = append(titles, spec.title)
		}
	}
	return titles
}

// fakeProbedVersion is the tested CLI version the banner tests probe with.
const fakeProbedVersion = "2.1.319"

// The static causes behind the failures the tests feed in; err113 bans
// errors.New inside functions, so the causes live here as package vars.
var (
	errProbeNotFound = errors.New("probe the vendors: claude binary not found")
	errCloseRefused  = errors.New("close the vendor session: the session refuses the close")
)
