package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/credentials"
	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

type multiAcctSpec struct {
	sessionID string
	homeDir   string
	resumeID  string
	model     string
}

type multiAcctAdapter struct {
	*pdAdapter
	mu     sync.Mutex
	starts []multiAcctSpec
}

func (a *multiAcctAdapter) Start(ctx context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	a.mu.Lock()
	a.starts = append(a.starts, multiAcctSpec{
		sessionID: spec.SessionID,
		homeDir:   spec.Home.ConfigDir,
		resumeID:  spec.ResumeSessionID,
		model:     spec.Model,
	})
	a.mu.Unlock()
	return a.pdAdapter.Start(ctx, spec)
}

func (a *multiAcctAdapter) recordedStarts() []multiAcctSpec {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]multiAcctSpec, len(a.starts))
	copy(out, a.starts)
	return out
}

type multiAcctHarness struct {
	f         *transcriptFixture
	m         *VendorSessions
	svc       *VendorService
	disp      *teamDispatcher
	tt        *teamTaskService
	accounts  *vendor.Accounts
	ad        *multiAcctAdapter
	sends     *pdSendLog
	dataDir   string
	repoDir   string
	extraHome string
	hostCfg   workspace.HostConfig
	cfgMu     sync.Mutex
}

func newMultiAccountHarness(t *testing.T, initialCfg *workspace.HostConfig) *multiAcctHarness {
	t.Helper()
	f := newEmptySessionsFixture(t)
	sends := &pdSendLog{}
	ad := &multiAcctAdapter{pdAdapter: newPDAdapter(f.clock, vendor.Capability{Resume: true, DirectQuota: true}, sends)}
	accounts := vendor.NewAccounts(f.clock, []vendor.AccountVendor{{Name: VendorClaude, DirectQuota: true}})
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	extraHome, err := vendor.PrepareExtraHome(dataDir, VendorClaude, "work-2", "")
	if err != nil {
		t.Fatalf("PrepareExtraHome: %v", err)
	}
	if regErr := accounts.RegisterAccount(VendorClaude, "work-2", extraHome); regErr != nil {
		t.Fatalf("RegisterAccount: %v", regErr)
	}
	h := &multiAcctHarness{
		f: f, accounts: accounts, ad: ad, sends: sends,
		dataDir: dataDir, extraHome: extraHome.ConfigDir, hostCfg: *initialCfg,
	}
	initMultiAccountServices(t, h)
	return h
}

func initMultiAccountServices(t *testing.T, h *multiAcctHarness) {
	t.Helper()
	ctx := t.Context()
	readHostCfg := func(context.Context) (workspace.HostConfig, error) {
		h.cfgMu.Lock()
		defer h.cfgMu.Unlock()
		return h.hostCfg, nil
	}
	m := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: h.f.store, Transcripts: h.f.service, Adapter: h.ad, Accounts: h.accounts,
		HostResources: fakeHostResources{ramBytes: 16 << 30, cores: 8},
		HostConfig:    readHostCfg,
		Vault:         h.f.vault, Clock: h.f.clock, IDs: ids.NewSequence("ma-"),
	})
	svc := &VendorService{
		sessions: m, store: h.f.store, adapter: h.ad, vault: h.f.vault,
		ids: ids.NewSequence("mas-"), ops: newVendorOps(h.f.clock, m.spawn),
		tokens: newSessionTokens(ids.CryptoRandom{}), accounts: h.accounts,
	}
	repoDir := t.TempDir()
	initRepo(t, repoDir)
	gm, gmErr := gitops.NewManager(gitBinary(t), filepath.Join(t.TempDir(), "gitdata"), h.f.clock)
	if gmErr != nil {
		t.Fatal(gmErr)
	}
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: h.f.store, Sessions: vendorTaskSessions{svc: svc},
		Admit:      newSchedulerAdmitterWithConfig(m, h.f.store, h.accounts, readHostCfg, h.f.clock),
		Roots:      pdRootResolver{dir: repoDir},
		Git:        gm,
		DataDir:    h.dataDir,
		IDs:        ids.NewSequence("mad-"),
		Vault:      h.f.vault,
		Clock:      h.f.clock,
		HostConfig: readHostCfg,
		Accounts:   h.accounts,
	})
	tt := newTeamTaskService(h.f.store, vendorTaskSessions{svc: svc}, pdRootResolver{dir: repoDir}, gm, h.dataDir, ids.NewSequence("matt-"), h.f.vault, nil)
	tt.SetRuntime(disp, nil)
	tt.SetAccounts(h.accounts, readHostCfg)
	tt.SetClock(h.f.clock)
	h.m, h.svc, h.disp, h.tt, h.repoDir = m, svc, disp, tt, repoDir
}

func mustTeamTasks(t *testing.T, ctx context.Context, h *multiAcctHarness, teamID string) []vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := h.f.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatalf("TeamSnapshot(%s): %v", teamID, err)
	}
	return tasks
}

func mustSessionSnapshot(t *testing.T, ctx context.Context, h *multiAcctHarness, sessionID string) vendorstore.Session {
	t.Helper()
	sess, err := h.f.store.Snapshot(ctx, sessionID)
	if err != nil {
		t.Fatalf("Snapshot(%s): %v", sessionID, err)
	}
	return sess
}

func setupMultiAccountTeam(t *testing.T, ctx context.Context, h *multiAcctHarness, teamID string, taskIDs []string) {
	t.Helper()
	spec, err := json.Marshal(teams.ProtectedTaskContent{Goal: "Implement multi-account feature", WriteScope: []string{"app.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	roleBytes, err := json.Marshal(teamDecisionPayload{
		Roles: []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Model: "claude-sonnet-4", Permission: string(teams.PermissionWrite)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	leadSess := teamID + "-lead-sess"
	if createErr := h.f.store.CreateSession(ctx, &vendorstore.Session{
		ID: leadSess, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleLead), State: vendorstore.StateReady, WorkspaceID: "ws-ma", WorkspaceRoot: teamTestRootName,
	}); createErr != nil {
		t.Fatalf("CreateSession lead: %v", createErr)
	}
	tasks := make([]vendorstore.NewTaskInput, 0, 1+len(taskIDs))
	tasks = append(tasks, vendorstore.NewTaskInput{
		TaskID: teamID + ".lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole,
		Deliverable: vendorstore.DeliverableDiff, CreatedEventID: teamID + "-c0", ProtectedSpec: spec,
	})
	for _, tid := range taskIDs {
		tasks = append(tasks, vendorstore.NewTaskInput{
			TaskID: tid, ParentTaskID: teamID + ".lead", HierarchyRole: vendorstore.RoleMember,
			CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff,
			CreatedEventID: tid + "-c", ProtectedSpec: spec,
		})
	}
	if _, _, teamErr := h.f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: "ws-ma", SessionID: leadSess,
		ProposedEventID: teamID + "-prop", ProposedPayload: spec, Tasks: tasks,
	}); teamErr != nil {
		t.Fatalf("CreateTeam: %v", teamErr)
	}
	if _, startErr := h.f.store.AppendTeamEvent(ctx, teamID, int64(2+len(taskIDs)), &vendorstore.AppendTeamEventInput{
		EventID: teamID + "-started", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	}); startErr != nil {
		t.Fatalf("EventTeamStarted: %v", startErr)
	}
}

// waitIntroPersisted waits until the actor has finished the intro send, not
// only until the adapter saw it: the actor appends the turn and the user item
// to the session log after Send returns, and a test append in that window
// makes the actor fail the send and the task (failed(intro)).
func waitIntroPersisted(t *testing.T, ctx context.Context, h *multiAcctHarness, sessionID string) {
	t.Helper()
	testutil.Spin(t, "initial intro persisted", func() bool {
		if len(h.sends.snapshot()) < 1 {
			return false
		}
		items, err := h.f.store.Transcript(ctx, sessionID, 0, 10)
		if err != nil {
			t.Fatalf("Transcript(%s): %v", sessionID, err)
		}
		return len(items) > 0
	})
}

func seedQuotaWorktreeAndProgress(t *testing.T, ctx context.Context, h *multiAcctHarness, origTask *vendorstore.TaskRecord) {
	t.Helper()
	wt1, wtErr := gitops.TaskWorktreePath(h.dataDir, repoIDFor(h.repoDir), gitTaskID(origTask.TaskID))
	if wtErr != nil {
		t.Fatal(wtErr)
	}
	if wErr := os.WriteFile(filepath.Join(wt1, "app.txt"), []byte("partial work from account 1\n"), 0o600); wErr != nil {
		t.Fatal(wErr)
	}
	for attempt := range 3 {
		sess := mustSessionSnapshot(t, ctx, h, origTask.SessionID)
		_, appErr := h.f.store.AppendBatch(ctx, origTask.SessionID, sess.LastSequence, []*vendorstore.Event{
			{Type: vendorstore.VendorTurnStarted, TurnID: "or8-turn-1", TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateRunning},
			{Type: vendorstore.TranscriptItemRecorded, Item: &vendorstore.Item{
				ID: "prog-1", TurnID: "or8-turn-1", Kind: vendorstore.KindAssistantMessage,
				Content: []byte(`{"Text":"Implemented step 1 in app.txt; step 2 remaining."}`),
			}},
		})
		if appErr == nil {
			return
		}
		if attempt == 2 {
			t.Fatalf("AppendBatch assistant progress: %v", appErr)
		}
	}
}

func readWorktreeFile(t *testing.T, wtDir, name string) string {
	t.Helper()
	root, openErr := os.OpenRoot(wtDir)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() {
		if cErr := root.Close(); cErr != nil {
			t.Errorf("close root: %v", cErr)
		}
	}()
	f, fErr := root.Open(name)
	if fErr != nil {
		t.Fatal(fErr)
	}
	defer func() {
		if cErr := f.Close(); cErr != nil {
			t.Errorf("close file: %v", cErr)
		}
	}()
	raw, rErr := io.ReadAll(f)
	if rErr != nil {
		t.Fatal(rErr)
	}
	return string(raw)
}

func assertReassignedWorktreeAndIntro(t *testing.T, h *multiAcctHarness, reassignedTaskID string) {
	t.Helper()
	starts := h.ad.recordedStarts()
	if len(starts) != 2 {
		t.Fatalf("recorded starts = %d, want 2", len(starts))
	}
	if starts[1].homeDir != h.extraHome || starts[1].model != "claude-sonnet-4" || starts[1].resumeID != "" {
		t.Fatalf("reassigned start spec = %+v, want home=%q model=claude-sonnet-4 resumeID=\"\"", starts[1], h.extraHome)
	}
	wt2, wtErr := gitops.TaskWorktreePath(h.dataDir, repoIDFor(h.repoDir), gitTaskID(reassignedTaskID))
	if wtErr != nil {
		t.Fatal(wtErr)
	}
	if gotFile := readWorktreeFile(t, wt2, "app.txt"); gotFile != "partial work from account 1\n" {
		t.Fatalf("reassigned worktree app.txt = %q, want carried diff", gotFile)
	}
	testutil.Spin(t, "reassigned intro delivered", func() bool {
		return len(h.sends.snapshot()) >= 2
	})
	intro2 := h.sends.snapshot()[1].text
	if !strings.Contains(intro2, "Implemented step 1 in app.txt") || !strings.Contains(intro2, "partial work from account 1") {
		t.Fatalf("reassigned intro missing progress summary or diff:\n%s", intro2)
	}
}

func TestQuotaExhaustionReassignsTaskToAnotherAccountWithDiffAndSummary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newMultiAccountHarness(t, &workspace.HostConfig{
		MultiAccountEnabled: true, MultiAccountTermsAccepted: true, AccountStrategy: string(credentials.StrategyOrder),
	})
	defer mustCloseActorSessions(t, h.m)
	setupMultiAccountTeam(t, ctx, h, "or8-team", []string{"or8-team.m1"})
	if err := h.disp.Dispatch(ctx, "or8-team"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	origTask := mustTeamTasks(t, ctx, h, "or8-team")[1]
	if origTask.State != vendorstore.TaskStateRunning {
		t.Fatalf("initial task state = %s, want running", origTask.State)
	}
	waitIntroPersisted(t, ctx, h, origTask.SessionID)
	sess1 := mustSessionSnapshot(t, ctx, h, origTask.SessionID)
	if sess1.AccountID != vendorDefaultAccount {
		t.Fatalf("initial session AccountID = %q, want %q", sess1.AccountID, vendorDefaultAccount)
	}
	seedQuotaWorktreeAndProgress(t, ctx, h, &origTask)

	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 1.0, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
	})
	h.tt.OnTurnFailed(ctx, origTask.SessionID, string(vendor.FailureQuota))

	tasksAfter := mustTeamTasks(t, ctx, h, "or8-team")
	if len(tasksAfter) != 3 || tasksAfter[1].State != vendorstore.TaskStateStopped || tasksAfter[1].FailureCode != string(vendor.FailureQuota) {
		t.Fatalf("unexpected tasks after quota reassignment: %+v", tasksAfter)
	}
	reassigned := tasksAfter[2]
	if reassigned.TaskID != "or8-team.m1~r1" || reassigned.State != vendorstore.TaskStateRunning {
		t.Fatalf("reassigned task = (%s, %s), want (or8-team.m1~r1, running)", reassigned.TaskID, reassigned.State)
	}
	sess2 := mustSessionSnapshot(t, ctx, h, reassigned.SessionID)
	if sess2.AccountID != "work-2" || sess2.Vendor != VendorClaude {
		t.Fatalf("reassigned session = (%s, %s), want (claude, work-2)", sess2.Vendor, sess2.AccountID)
	}
	assertReassignedWorktreeAndIntro(t, h, reassigned.TaskID)
}

func TestMultiAccountOptInAndTermsGatingFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  workspace.HostConfig
	}{
		{name: "opt_in_disabled", cfg: workspace.HostConfig{MultiAccountEnabled: false, MultiAccountTermsAccepted: true}},
		{name: "terms_not_accepted", cfg: workspace.HostConfig{MultiAccountEnabled: true, MultiAccountTermsAccepted: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			subCtx := t.Context()
			cfg := tc.cfg
			h := newMultiAccountHarness(t, &cfg)
			defer mustCloseActorSessions(t, h.m)
			h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
				Windows: []vendor.Window{{Name: "5h", Utilization: 0.90, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
			})
			setupMultiAccountTeam(t, subCtx, h, "gate-"+tc.name, []string{"gate-" + tc.name + ".m1"})
			if err := h.disp.Dispatch(subCtx, "gate-"+tc.name); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			tasks := mustTeamTasks(t, subCtx, h, "gate-"+tc.name)
			if tasks[1].State != vendorstore.TaskStateQueued {
				t.Fatalf("task state = %s, want queued (fail closed when %s)", tasks[1].State, tc.name)
			}
		})
	}

	t.Run("vault_locked", func(t *testing.T) {
		t.Parallel()
		h := newMultiAccountHarness(t, &workspace.HostConfig{MultiAccountEnabled: true, MultiAccountTermsAccepted: true})
		defer mustCloseActorSessions(t, h.m)
		h.f.vault.Lock()
		if h.tt.tryReassignOnQuota(t.Context(), &vendorstore.TaskRecord{TeamID: "t", TaskID: "t.m1"}, "sess") {
			t.Fatal("tryReassignOnQuota must fail closed when vault is locked")
		}
		if h.f.vault.Snapshot().State != vault.StateLocked {
			t.Fatal("expected vault to remain locked")
		}
	})
}

func TestAccountSwitchOccursOnlyAtTaskBoundaries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newMultiAccountHarness(t, &workspace.HostConfig{
		MultiAccountEnabled: true, MultiAccountTermsAccepted: true, AccountStrategy: string(credentials.StrategyOrder),
	})
	defer mustCloseActorSessions(t, h.m)
	setupMultiAccountTeam(t, ctx, h, "tb-team", []string{"tb-team.m1", "tb-team.m2"})
	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.20, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
	})
	if err := h.disp.Dispatch(ctx, "tb-team"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	tasks1 := mustTeamTasks(t, ctx, h, "tb-team")
	s1 := mustSessionSnapshot(t, ctx, h, tasks1[1].SessionID)
	s2 := mustSessionSnapshot(t, ctx, h, tasks1[2].SessionID)
	if s1.AccountID != vendorDefaultAccount || s2.AccountID != vendorDefaultAccount {
		t.Fatalf("initial tasks = (%s, %s), want both on default", s1.AccountID, s2.AccountID)
	}

	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.88, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
	})
	if s1After := mustSessionSnapshot(t, ctx, h, tasks1[1].SessionID); s1After.AccountID != vendorDefaultAccount {
		t.Fatalf("running task switched mid-task to %q", s1After.AccountID)
	}
	setupMultiAccountTeam(t, ctx, h, "tb-team-2", []string{"tb-team-2.m1"})
	if err := h.disp.Dispatch(ctx, "tb-team-2"); err != nil {
		t.Fatalf("Dispatch team-2: %v", err)
	}
	tasks2 := mustTeamTasks(t, ctx, h, "tb-team-2")
	if s3 := mustSessionSnapshot(t, ctx, h, tasks2[1].SessionID); s3.AccountID != "work-2" {
		t.Fatalf("new task after 85%% gate = %q, want work-2", s3.AccountID)
	}
}

func TestPickAccountStrategiesAndStickyHold(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newMultiAccountHarness(t, &workspace.HostConfig{
		MultiAccountEnabled: true, MultiAccountTermsAccepted: true, AccountStrategy: "best",
	})
	defer mustCloseActorSessions(t, h.m)
	extra3, prepErr := vendor.PrepareExtraHome(h.dataDir, VendorClaude, "work-3", "")
	if prepErr != nil {
		t.Fatal(prepErr)
	}
	if regErr := h.accounts.RegisterAccount(VendorClaude, "work-3", extra3); regErr != nil {
		t.Fatal(regErr)
	}

	now := h.f.clock.Now()
	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.90, ResetsAt: now.Add(time.Hour)}},
	})
	h.accounts.ObserveAccountQuota(VendorClaude, "work-2", &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.60, ResetsAt: now.Add(time.Hour)}},
	})
	h.accounts.ObserveAccountQuota(VendorClaude, "work-3", &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.10, ResetsAt: now.Add(time.Hour)}},
	})

	picker := h.disp.picker
	chosen, allLim, _, _, err := picker.PickForTask(ctx, VendorClaude, "claude-sonnet-4", "")
	if err != nil || allLim || chosen != "work-3" {
		t.Fatalf("PickForTask best = (%q, allLim=%v, err=%v), want work-3", chosen, allLim, err)
	}

	h.f.clock.Advance(2 * time.Minute)
	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.05, ResetsAt: now.Add(time.Hour)}},
	})
	chosen2, _, _, _, err := picker.PickForTask(ctx, VendorClaude, "claude-sonnet-4", "")
	if err != nil || chosen2 != "work-3" {
		t.Fatalf("sticky hold = %q, want work-3", chosen2)
	}

	h.accounts.ObserveAccountQuota(VendorClaude, "work-3", &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.86, ResetsAt: now.Add(time.Hour)}},
	})
	chosen3, _, _, _, err := picker.PickForTask(ctx, VendorClaude, "claude-sonnet-4", "")
	if err != nil || chosen3 != vendorDefaultAccount {
		t.Fatalf("after 85%% gate = %q, want %q", chosen3, vendorDefaultAccount)
	}
}

func TestQuotaFailureWithNoAlternateAccountNotifiesDelegator(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newMultiAccountHarness(t, &workspace.HostConfig{
		MultiAccountEnabled: true, MultiAccountTermsAccepted: true, AccountStrategy: string(credentials.StrategyOrder),
	})
	defer mustCloseActorSessions(t, h.m)
	h.accounts.ObserveAccountQuota(VendorClaude, "work-2", &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 0.95, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
	})
	setupMultiAccountTeam(t, ctx, h, "noalt-team", []string{"noalt-team.m1"})
	if err := h.disp.Dispatch(ctx, "noalt-team"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	tasks := mustTeamTasks(t, ctx, h, "noalt-team")
	h.accounts.ObserveAccountQuota(VendorClaude, vendorDefaultAccount, &vendor.RateLimit{
		Windows: []vendor.Window{{Name: "5h", Utilization: 1.0, ResetsAt: h.f.clock.Now().Add(time.Hour)}},
	})
	h.tt.OnTurnFailed(ctx, tasks[1].SessionID, string(vendor.FailureQuota))

	tasksAfter := mustTeamTasks(t, ctx, h, "noalt-team")
	if len(tasksAfter) != 2 || tasksAfter[1].State != vendorstore.TaskStateFailed || tasksAfter[1].FailureCode != string(vendor.FailureQuota) {
		t.Fatalf("tasks after unrecoverable quota failure = %+v, want failed(quota) with no retry", tasksAfter)
	}
	pending, err := h.f.store.PendingMailboxRows(ctx, "noalt-team", "noalt-team.lead")
	if err != nil || len(pending) == 0 {
		t.Fatalf("expected delegator digest notification, pending=%d err=%v", len(pending), err)
	}
}

func TestRepeatedQuotaFailuresTerminateWithoutPingPong(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newMultiAccountHarness(t, &workspace.HostConfig{
		MultiAccountEnabled: true, MultiAccountTermsAccepted: true, AccountStrategy: string(credentials.StrategyOrder),
	})
	defer mustCloseActorSessions(t, h.m)

	// Read-only Admit check must not mutate picker state.
	if ok, err := h.disp.picker.HasEligibleAccountWithCapacity(ctx, h.m, VendorClaude, "claude-sonnet-4"); err != nil || !ok {
		t.Fatalf("HasEligibleAccountWithCapacity = (%v, %v), want (true, nil)", ok, err)
	}
	h.disp.picker.mu.Lock()
	stateLen := len(h.disp.picker.state)
	h.disp.picker.mu.Unlock()
	if stateLen != 0 {
		t.Fatalf("read-only Admit mutated picker state: %d entries", stateLen)
	}

	setupMultiAccountTeam(t, ctx, h, "ping-team", []string{"ping-team.m1"})
	if err := h.disp.Dispatch(ctx, "ping-team"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	t1 := mustTeamTasks(t, ctx, h, "ping-team")[1]
	// Fail default with FailureQuota without any >= 85% window observation.
	h.tt.OnTurnFailed(ctx, t1.SessionID, string(vendor.FailureQuota))

	tasks2 := mustTeamTasks(t, ctx, h, "ping-team")
	if len(tasks2) != 3 || tasks2[2].State != vendorstore.TaskStateRunning {
		t.Fatalf("expected one reassignment to work-2, got %+v", tasks2)
	}
	// Fail work-2 with FailureQuota without any >= 85% window observation:
	// default is still in cooldown, so reassignment must terminate instead of ping-ponging.
	h.tt.OnTurnFailed(ctx, tasks2[2].SessionID, string(vendor.FailureQuota))
	tasks3 := mustTeamTasks(t, ctx, h, "ping-team")
	if len(tasks3) != 3 || tasks3[2].State != vendorstore.TaskStateFailed || tasks3[2].FailureCode != string(vendor.FailureQuota) {
		t.Fatalf("expected termination at ~r1 failed(quota) without ping-pong, got %+v", tasks3)
	}
}
