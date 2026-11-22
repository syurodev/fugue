package appwin

import (
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type rawTestClock struct {
	mu     sync.Mutex
	timers []*rawTestTimer
	armed  chan struct{}
}

func newRawTestClock() *rawTestClock {
	return &rawTestClock{armed: make(chan struct{}, 16)}
}

func (c *rawTestClock) NewTimer(d time.Duration) hostconn.Timer {
	t := &rawTestTimer{ch: make(chan time.Time, 1), d: d}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	select {
	case c.armed <- struct{}{}:
	default:
	}
	return t
}

const (
	testRawWSName       = "alpha"
	testRawHashV1       = "hash-v1"
	testRawHashExternal = "hash-external"
)

func (c *rawTestClock) fireLatest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.timers) - 1; i >= 0; i-- {
		if !c.timers[i].stopped {
			c.timers[i].ch <- time.Time{}
			return
		}
	}
}

type rawTestTimer struct {
	ch      chan time.Time
	d       time.Duration
	stopped bool
}

func (t *rawTestTimer) C() <-chan time.Time { return t.ch }
func (t *rawTestTimer) Stop() bool {
	was := !t.stopped
	t.stopped = true
	return was
}

func newRawTestScreens(t *testing.T, fc *fakeAppSettingsClient, clk hostconn.Clock) (*screens, *settings.Worker) {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testSettingsInc})
	sc.workspaces.ApplyList([]workspace.Summary{{ID: testSettingsWSID, Name: testRawWSName, ConfigRevision: 1}})
	w := settings.NewWorker(func() settings.Client { return fc }, nil)
	if clk != nil {
		w.SetClock(clk)
	}
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.settingsWorker = w
	sc.openSettings()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w), awaitSettingsResult(t, w)})
	sc.settingsView.activeTab = tabRaw
	return sc, w
}

// TestSettingsRawTOMLSaveUsesCASAndRoutesPolicyToQ64 verifies that raw TOML
// saves route policy-key edits through explicit Q-64 confirmation, keep the
// user's text and offer a diff on CAS conflict, and route pending out-of-band
// policy changes to ConfirmWorkspaceConfig / RejectWorkspaceConfig.
func TestSettingsRawTOMLSaveUsesCASAndRoutesPolicyToQ64(t *testing.T) {
	t.Parallel()
	fc := &fakeAppSettingsClient{
		ws: client.Workspace{
			ID: testSettingsWSID, Name: testRawWSName, ConfigHash: testRawHashV1, ConfigRevision: 1,
			RawTOML: []byte("[policy]\napproval_mode = \"auto\"\n"),
		},
		eff: client.EffectiveWorkspaceConfig{
			Settings: client.WorkspaceSettings{ApprovalMode: testApprovalAuto, SandboxProfile: testSandboxWrite},
		},
	}
	sc, w := newRawTestScreens(t, fc, newRawTestClock())
	assertRawPolicyConfirmAndCASConflict(t, sc, w, fc)
	assertPendingOutOfBandPolicyConfirmReject(t, sc, w, fc)
}

func assertRawPolicyConfirmAndCASConflict(t *testing.T, sc *screens, w *settings.Worker, fc *fakeAppSettingsClient) {
	t.Helper()
	userTOML := "[policy]\napproval_mode = \"never\"\n"
	sc.onRawTOMLChanged(userTOML)
	sc.settingsView.syncRawFromModel(sc.settings)

	sc.submitRawTOML(false)
	if !sc.settings.PolicyConfirmArmed || len(sc.settings.PendingRawPolicyDiff) == 0 || fc.updateCalls != 0 {
		t.Fatalf("expected policy confirm armed with diff and 0 update calls; armed=%v diff=%+v calls=%d",
			sc.settings.PolicyConfirmArmed, sc.settings.PendingRawPolicyDiff, fc.updateCalls)
	}

	fc.mu.Lock()
	fc.ws.ConfigHash = testRawHashExternal
	fc.ws.RawTOML = []byte("[policy]\napproval_mode = \"ask\"\n")
	fc.mu.Unlock()

	sc.submitRawTOML(true)
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	if !sc.settings.RawConflict || sc.settings.ConflictHash != testRawHashExternal {
		t.Fatalf("expected RawConflict with %s, got conflict=%v hash=%q", testRawHashExternal, sc.settings.RawConflict, sc.settings.ConflictHash)
	}
	if sc.settings.RawDraft != userTOML {
		t.Fatalf("CAS conflict overwrote user RawDraft: got %q, want %q", sc.settings.RawDraft, userTOML)
	}
	diff := settings.BuildConflictUnifiedDiff(string(sc.settings.RawTOML), sc.settings.RawDraft)
	if !strings.Contains(diff, "-approval_mode = \"ask\"") || !strings.Contains(diff, "+approval_mode = \"never\"") {
		t.Fatalf("conflict diff missing expected lines:\n%s", diff)
	}
	sc.settings.ShowConflictDiff = true
	runAppFrame(newInboxTestEnv(t), sc, new(input.Router), image.Pt(shotWindowW, shotWindowH))
	if sc.lastErr != "" {
		t.Fatalf("render conflict diff frame error: %s", sc.lastErr)
	}
}

func assertPendingOutOfBandPolicyConfirmReject(t *testing.T, sc *screens, w *settings.Worker, fc *fakeAppSettingsClient) {
	t.Helper()
	fc.mu.Lock()
	fc.ws.PendingPolicyConfirmation = true
	fc.ws.ConfigHash = "hash-oob-1"
	fc.ws.PolicyDiff = []client.PolicyDiffEntry{{Key: testPolicyKey, OldValue: testApprovalAuto, NewValue: "never"}}
	fc.mu.Unlock()
	sc.requestReloadSettings()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w), awaitSettingsResult(t, w)})
	if !sc.settings.PendingPolicyConfirmation || len(sc.settings.PendingPolicyDiff) != 1 {
		t.Fatalf("expected pending policy confirmation, got %v %+v", sc.settings.PendingPolicyConfirmation, sc.settings.PendingPolicyDiff)
	}
	sc.confirmPendingPolicy()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	if fc.confirmCalls != 1 || sc.settings.PendingPolicyConfirmation {
		t.Fatalf("confirm failed: calls=%d pending=%v", fc.confirmCalls, sc.settings.PendingPolicyConfirmation)
	}

	fc.mu.Lock()
	fc.ws.PendingPolicyConfirmation = true
	fc.ws.ConfigHash = "hash-oob-2"
	fc.mu.Unlock()
	sc.requestReloadSettings()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w), awaitSettingsResult(t, w)})
	sc.rejectPendingPolicy()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	if fc.rejectCalls != 1 || sc.settings.PendingPolicyConfirmation {
		t.Fatalf("reject failed: calls=%d pending=%v", fc.rejectCalls, sc.settings.PendingPolicyConfirmation)
	}
}

// TestRawTOMLValidationIsDebouncedAndAnnotatesLines verifies that live
// validation is debounced on the injected clock (no Host call per keystroke),
// annotates offending lines, and converts line:col into editor caret offsets.
func TestRawTOMLValidationIsDebouncedAndAnnotatesLines(t *testing.T) {
	t.Parallel()
	fc := &fakeAppSettingsClient{
		ws:     client.Workspace{ID: testSettingsWSID, Name: testRawWSName, ConfigHash: testRawHashV1, ConfigRevision: 1, RawTOML: []byte("name = \"alpha\"\n")},
		issues: []client.WorkspaceConfigIssue{{Key: "policy.bogus", Line: 2, Column: 3, Message: "unknown key"}},
	}
	clk := newRawTestClock()
	sc, w := newRawTestScreens(t, fc, clk)

	sc.onRawTOMLChanged("name = \"alpha\"\nbogus = 1\n")
	<-clk.armed
	sc.onRawTOMLChanged("name = \"alpha\"\nbogus = 2\n")
	<-clk.armed

	fc.mu.Lock()
	before := fc.validateCalls
	fc.mu.Unlock()
	if before != 0 {
		t.Fatalf("ValidateConfig called before debounce elapsed (%d calls)", before)
	}

	clk.fireLatest()
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	fc.mu.Lock()
	after := fc.validateCalls
	fc.mu.Unlock()
	if after != 1 {
		t.Fatalf("ValidateConfig calls after debounce = %d, want 1", after)
	}
	if len(sc.settings.ValidationIssues) != 1 || len(sc.settings.IssueLines()) != 1 || sc.settings.IssueLines()[0] != 2 {
		t.Fatalf("unexpected validation issues/lines: issues=%+v lines=%v", sc.settings.ValidationIssues, sc.settings.IssueLines())
	}

	env := newInboxTestEnv(t)
	runAppFrame(env, sc, new(input.Router), image.Pt(shotWindowW, shotWindowH))
	if sc.lastErr != "" {
		t.Fatalf("render validation error frame failed: %s", sc.lastErr)
	}
	if got := settings.LineColToRuneOffset(sc.settings.RawDraft, 2, 3); got != 17 {
		t.Fatalf("LineColToRuneOffset(2,3) = %d, want 17", got)
	}
}
