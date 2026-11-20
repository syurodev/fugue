package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

func setupQuotaTeamAndTasks(ctx context.Context, t *testing.T, f *transcriptFixture, teamID string, taskIDs []string) {
	t.Helper()
	spec := pdMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Quota gate test"})
	roleBytes := pdMarshalJSON(t, teamDecisionPayload{
		Roles: []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionRead)}},
	})
	leadSess := teamID + "-lead-sess"
	if err := f.store.CreateSession(ctx, &vendorstore.Session{
		ID: leadSess, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleLead), State: vendorstore.StateReady, WorkspaceID: "ws-qg", WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatalf("CreateSession lead: %v", err)
	}
	tasks := make([]vendorstore.NewTaskInput, 0, 1+len(taskIDs))
	tasks = append(tasks, vendorstore.NewTaskInput{
		TaskID: teamID + ".lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole,
		Deliverable: vendorstore.DeliverableReport, CreatedEventID: teamID + "-c0", ProtectedSpec: spec,
	})
	for i, tid := range taskIDs {
		tasks = append(tasks, vendorstore.NewTaskInput{
			TaskID: tid, ParentTaskID: teamID + ".lead", HierarchyRole: vendorstore.RoleMember,
			CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport,
			CreatedEventID: fmt.Sprintf("%s-c%d", teamID, i+1), ProtectedSpec: spec,
		})
	}
	if _, _, err := f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: "ws-qg", SessionID: leadSess,
		ProposedEventID: teamID + "-prop", ProposedPayload: spec, Tasks: tasks,
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	seq := int64(2 + len(taskIDs))
	if _, err := f.store.AppendTeamEvent(ctx, teamID, seq, &vendorstore.AppendTeamEventInput{
		EventID: teamID + "-decided", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	}); err != nil {
		t.Fatalf("EventTeamStarted: %v", err)
	}
}

func newQuotaDispatchHarness(t *testing.T, accounts *vendor.Accounts, ramBytes uint64) (*transcriptFixture, *VendorSessions, *VendorService, *teamDispatcher) {
	t.Helper()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	ad := newPDAdapter(f.clock, vendor.Capability{Resume: true}, &pdSendLog{})
	m := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad, Accounts: accounts,
		HostResources: fakeHostResources{ramBytes: ramBytes, cores: 8},
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 0, PromptPacingMaxSeconds: 0}, nil
		},
		Vault: f.vault, Clock: f.clock, IDs: ids.NewSequence("qg-"),
	})
	svc := &VendorService{
		sessions: m, store: f.store, adapter: ad, vault: f.vault,
		ids: ids.NewSequence("qgs-"), ops: newVendorOps(f.clock, m.spawn),
		tokens: newSessionTokens(ids.CryptoRandom{}), accounts: accounts,
	}
	repoDir := t.TempDir()
	initRepo(t, repoDir)
	gm, err := gitops.NewManager(gitBinary(t), filepath.Join(t.TempDir(), "gitdata"), f.clock)
	if err != nil {
		t.Fatal(err)
	}
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.store, Sessions: vendorTaskSessions{svc: svc}, Admit: newSchedulerAdmitter(m, f.store, accounts),
		Roots: pdRootResolver{dir: repoDir}, Git: gm, DataDir: filepath.Join(t.TempDir(), "data"),
		IDs: ids.NewSequence("qgd-"), Vault: f.vault,
	})
	return f, m, svc, disp
}

func TestQuotaGateBlocksTasksAboveEightyFivePercent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, m, _, disp := newQuotaDispatchHarness(t, nil, 16<<30)
	now := f.clock.Now()
	accounts := vendor.NewAccounts(f.clock, []vendor.AccountVendor{{Name: VendorClaude, DirectQuota: true}})
	disp.deps.Admit = newSchedulerAdmitter(m, f.store, accounts)

	// >= 85% blocks admission (task stays queued, never failed).
	accounts.ObserveQuota(VendorClaude, &vendor.RateLimit{Windows: []vendor.Window{{Name: "5h", Utilization: 0.85}}})
	setupQuotaTeamAndTasks(ctx, t, f, "qg-team-1", []string{"qg-team-1.m1"})
	if dispErr := disp.Dispatch(ctx, "qg-team-1"); dispErr != nil {
		t.Fatalf("Dispatch at 85%%: %v", dispErr)
	}
	_, tasks, err := f.store.TeamSnapshot(ctx, "qg-team-1")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[1].State != vendorstore.TaskStateQueued {
		t.Fatalf("task state at 85%% = %s, want queued", tasks[1].State)
	}
	// OR-09: with zero resetsAt, advancing clock never auto-reopens.
	f.clock.Advance(24 * time.Hour)
	if accounts.QuotaAllows(VendorClaude) {
		t.Fatal("account without resetsAt must not auto-reopen")
	}

	// < 85% admits and starts task.
	accounts.ObserveQuota(VendorClaude, &vendor.RateLimit{Windows: []vendor.Window{{Name: "5h", Utilization: 0.84, ResetsAt: now.Add(time.Hour)}}})
	if dispErr := disp.Dispatch(ctx, "qg-team-1"); dispErr != nil {
		t.Fatalf("Dispatch at 84%%: %v", dispErr)
	}
	_, tasks, err = f.store.TeamSnapshot(ctx, "qg-team-1")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[1].State != vendorstore.TaskStateRunning {
		t.Fatalf("task state at 84%% = %s, want running", tasks[1].State)
	}
	mustCloseActorSessions(t, m)
}

func TestPausedAccountReopensAtResetsAtAndDispatchesQueuedTasks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, m, svc, disp := newQuotaDispatchHarness(t, nil, 16<<30)
	accounts := vendor.NewAccounts(f.clock, []vendor.AccountVendor{{Name: VendorClaude, DirectQuota: true}})
	disp.deps.Admit = newSchedulerAdmitter(m, f.store, accounts)
	resetAt := f.clock.Now().Add(5 * time.Minute)

	reopenCalled := make(chan struct{}, 2)
	if _, err := newAccountsService(accounts, svc, f.hub, accountsServiceDeps{
		Clock: f.clock, IDs: ids.NewSequence("asvc-"), GoTask: m.spawn,
		OnQuotaReopen: func() {
			if err := disp.DispatchAll(ctx); err != nil {
				t.Logf("DispatchAll: %v", err)
			}
			select {
			case reopenCalled <- struct{}{}:
			default:
			}
		},
	}); err != nil {
		t.Fatalf("newAccountsService: %v", err)
	}

	accounts.ObserveQuota(VendorClaude, &vendor.RateLimit{Windows: []vendor.Window{{Name: "5h", Utilization: 0.99, ResetsAt: resetAt}}})
	accounts.ObserveFailure(VendorClaude, vendor.FailureQuota)
	setupQuotaTeamAndTasks(ctx, t, f, "reopen-team", []string{"reopen-team.m1"})
	if err := disp.Dispatch(ctx, "reopen-team"); err != nil {
		t.Fatalf("initial Dispatch: %v", err)
	}
	_, tasks, err := f.store.TeamSnapshot(ctx, "reopen-team")
	if err != nil || tasks[1].State != vendorstore.TaskStateQueued {
		t.Fatalf("task state while paused = %s (err=%v), want queued", tasks[1].State, err)
	}

	if waitErr := f.clock.BlockUntilPending(ctx, 1); waitErr != nil {
		t.Fatalf("wait quota reopen timer: %v", waitErr)
	}
	f.clock.Advance(5 * time.Minute)
	<-reopenCalled
	testutil.Spin(t, "queued task dispatched on resetsAt", func() bool {
		_, ts, snapErr := f.store.TeamSnapshot(ctx, "reopen-team")
		return snapErr == nil && ts[1].State == vendorstore.TaskStateRunning
	})

	verifyHostStartQuotaResetRecalculation(ctx, t, f, m, svc)
	mustCloseActorSessions(t, m)
}

func verifyHostStartQuotaResetRecalculation(ctx context.Context, t *testing.T, f *transcriptFixture, m *VendorSessions, svc *VendorService) {
	t.Helper()
	clk2 := clock.NewFake(f.clock.Now())
	accounts2 := vendor.NewAccounts(clk2, []vendor.AccountVendor{{Name: VendorClaude, DirectQuota: true}})
	accounts2.ObserveQuota(VendorClaude, &vendor.RateLimit{Windows: []vendor.Window{{Name: "5h", Utilization: 0.90, ResetsAt: clk2.Now().Add(2 * time.Minute)}}})
	var startReopened atomic.Bool
	if _, err := newAccountsService(accounts2, svc, NewEventHub("restart-inc"), accountsServiceDeps{
		Clock: clk2, IDs: ids.NewSequence("asvc2-"), GoTask: m.spawn,
		OnQuotaReopen: func() { startReopened.Store(true) },
	}); err != nil {
		t.Fatalf("newAccountsService restart: %v", err)
	}
	if err := clk2.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait restarted quota timer: %v", err)
	}
	clk2.Advance(2 * time.Minute)
	testutil.Spin(t, "restarted host quota timer fires", startReopened.Load)
}

func TestTaskTurnFailureQuotaAndAuthNotifiesDelegator(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failure vendor.FailureClass
		want    string
	}{
		{name: "quota", failure: vendor.FailureQuota, want: string(vendor.FailureQuota)},
		{name: "auth", failure: vendor.FailureAuth, want: string(vendor.FailureAuth)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTaskTurnFailureCase(t, tc.name, tc.failure, tc.want)
		})
	}
}

func runTaskTurnFailureCase(t *testing.T, name string, failure vendor.FailureClass, want string) {
	t.Helper()
	subCtx := t.Context()
	f := newEmptySessionsFixture(t)
	ad := newPDAdapter(f.clock, vendor.Capability{Resume: true}, &pdSendLog{})
	m := mustNewVendorSessions(t, subCtx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad,
		Vault: f.vault, Clock: f.clock, IDs: ids.NewSequence("fail-" + name + "-"),
	})
	teamID := "fail-team-" + name
	taskID := teamID + ".m1"
	sessID := "sess-" + name
	setupQuotaTeamAndTasks(subCtx, t, f, teamID, []string{taskID})
	evs := []vendorstore.AppendTeamEventInput{
		{EventID: "assn-" + name, TaskID: taskID, EventType: vendorstore.EventTaskAssigned, SessionID: sessID},
		{EventID: "start-" + name, TaskID: taskID, EventType: vendorstore.EventTaskStarted, SessionID: sessID},
	}
	for i := range evs {
		if _, err := f.store.AppendTeamEvent(subCtx, teamID, int64(4+i), &evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	startPDSession(t, m, sessID, VendorClaude, vendorstore.RoleMember, teamID, taskID, "")

	ttSvc := newTeamTaskService(f.store, vendorTaskSessions{svc: &VendorService{sessions: m, store: f.store}}, nil, nil, "", ids.NewSequence("tt-"+name+"-"), f.vault, nil)
	m.SetTurnFailedHook(func(sessionID, failureCode string) {
		m.spawn("test-turn-fail", func(c context.Context) error {
			ttSvc.OnTurnFailed(c, sessionID, failureCode)
			return nil
		})
	})
	if err := m.Send(subCtx, sessID, vendor.Input{Text: "work", Origin: vendor.OriginUser}); err != nil {
		t.Fatal(err)
	}
	ad.session(sessID).observations <- vendor.Observation{
		TurnEnd: &vendor.TurnEnd{TurnID: fmt.Sprintf("pd-turn-%s-1", sessID), Outcome: string(vendor.OutcomeFailed), Failure: failure},
	}
	testutil.Spin(t, "task failed with "+want, func() bool {
		_, tasks, err := f.store.TeamSnapshot(subCtx, teamID)
		return err == nil && tasks[1].State == vendorstore.TaskStateFailed && tasks[1].FailureCode == want
	})
	pending, err := f.store.PendingMailbox(subCtx)
	if err != nil || len(pending) != 1 || pending[0].RecipientTaskID != teamID+".lead" {
		t.Fatalf("pending mailbox = %+v (err=%v), want 1 notice for delegator lead", pending, err)
	}
	mustCloseActorSessions(t, m)
}

func TestAdmitToReserveSlotRaceKeepsTaskQueuedWithoutTaskAssigned(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	repoDir := t.TempDir()
	initRepo(t, repoDir)
	gm, err := gitops.NewManager(gitBinary(t), filepath.Join(t.TempDir(), "gitdata"), f.clock)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTaskSessions{reserveErr: ErrVendorSessionCapacity}
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.store, Sessions: fake, Admit: newSchedulerAdmitter(fake, f.store, nil),
		Roots: pdRootResolver{dir: repoDir}, Git: gm, DataDir: filepath.Join(t.TempDir(), "data"),
		IDs: ids.NewSequence("raced-"), Vault: f.vault,
	})
	setupQuotaTeamAndTasks(ctx, t, f, "race-team", []string{"race-team.m1"})

	if dispErr := disp.Dispatch(ctx, "race-team"); dispErr != nil {
		t.Fatalf("Dispatch during slot race: %v", dispErr)
	}
	if fake.startCount() != 0 {
		t.Fatalf("StartTaskSession called %d times, want 0", fake.startCount())
	}
	_, tasks, err := f.store.TeamSnapshot(ctx, "race-team")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[1].State != vendorstore.TaskStateQueued {
		t.Fatalf("task state = %s, want queued", tasks[1].State)
	}
	evs, err := f.store.TeamEventsSince(ctx, "race-team", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if evs[i].EventType == vendorstore.EventTaskAssigned && evs[i].TaskID == "race-team.m1" {
			t.Fatalf("unexpected EventTaskAssigned recorded for m1 when slot was lost: %+v", evs[i])
		}
	}
}
