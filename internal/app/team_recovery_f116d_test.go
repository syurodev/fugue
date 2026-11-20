package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	recOR09TeamID   = "team-ore09"
	recOR09LeadTask = "task-ore09-lead"
	recOR09MemTask  = "task-ore09-mem"
	recOR09LeadSess = "sess-rec-lead"
	recQTeamID      = "team-q"
	recQLeadTask    = "task-q-lead"
	recQUserTask    = "task-q-user"
	recQParentTask  = "task-q-parent"
	recQOpenSess    = "sess-open-prop"
	recQAskUserSess = "sess-ask-user"
)

// TestHostRestartLeavesRunningTasksUnknownAndNotifiesDelegator verifies OR-E09
// (Q-151 A): host restart marks running tasks unknown without rerunning them;
// unlocking the vault inspects each recovered task's worktree and sends its
// direct delegator one mailbox notice; resuming the detached lead session
// moves the lead task back through EventTaskRecoveryDecided("resume") ->
// EventTaskStarted and delivers the notice.
func TestHostRestartLeavesRunningTasksUnknownAndNotifiesDelegator(t *testing.T) {
	t.Parallel()
	dataDir := cheapVaultDataDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))

	headSHA := setupOR09Host1(t, dataDir, fugueHome, clk)

	clk.Advance(time.Minute)
	hp2 := startRecoveryHost(t, dataDir, fugueHome, clk)
	defer closeHostAdvanceClock(t, hp2, clk)

	verifyOR09LockedState(t, hp2)
	verifyOR09UnlockedNotice(t, hp2, headSHA)
	verifyLeadResumeTransitionsUnknownToRunning(t, hp2)
}

// TestResumeSucceedsAndRetriesLeadRecoveryWhenHookFails verifies that a
// transient OnSessionResumed hook failure after a successful resume logs
// without marking the live session held, and a subsequent wake trigger retries
// lead recovery so the lead task reaches running.
func TestResumeSucceedsAndRetriesLeadRecoveryWhenHookFails(t *testing.T) {
	t.Parallel()
	dataDir := cheapVaultDataDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC))

	_ = setupOR09Host1(t, dataDir, fugueHome, clk)

	clk.Advance(time.Minute)
	hp2 := startRecoveryHost(t, dataDir, fugueHome, clk)
	defer closeHostAdvanceClock(t, hp2, clk)

	var hookCalls atomic.Int32
	hp2.rt.vendors.SetOnSessionResumed(func(resCtx context.Context, sid string) error {
		if hookCalls.Add(1) == 1 {
			return errcode.New(errcode.Unavailable, "transient sequence conflict")
		}
		return hp2.rt.teamTasks.OnSessionResumed(resCtx, sid)
	})
	unlockCheapVault(t, hp2)

	testutil.Spin(t, "resumed lead stays unheld and retries to running", func() bool {
		if hookCalls.Load() < 1 {
			return false
		}
		snap, snapErr := hp2.vendorStore.Snapshot(t.Context(), recOR09LeadSess)
		if snapErr != nil || snap.State != vendorstore.StateReady {
			return false
		}
		_, tasks, teamErr := hp2.vendorStore.TeamSnapshot(t.Context(), recOR09TeamID)
		lead, ok := findTask(tasks, recOR09LeadTask)
		return teamErr == nil && ok && lead.State == vendorstore.TaskStateRunning
	})
	if held := hp2.rt.teamWake.HeldRecipients(); len(held) != 0 {
		t.Fatalf("HeldRecipients after hook failure = %+v, want empty", held)
	}
}

func setupOR09Host1(t *testing.T, dataDir, fugueHome string, clk *clock.Fake) string {
	t.Helper()
	hp1 := startRecoveryHost(t, dataDir, fugueHome, clk)
	unlockCheapVault(t, hp1)
	wsID, repoDir := createTestRepoWorkspace(t, hp1)
	var headSHA string
	seedOR09SessionsAndWorktree(t, hp1, dataDir, wsID, repoDir, &headSHA)
	seedOR09TeamAndEvents(t, hp1, wsID)
	closeHostAdvanceClock(t, hp1, clk)
	return headSHA
}

func seedOR09SessionsAndWorktree(t *testing.T, hp1 *HostProcess, dataDir, wsID, repoDir string, headSHA *string) {
	t.Helper()
	ctx := t.Context()
	for _, s := range []struct{ id, role, taskID, handle string }{
		{recOR09LeadSess, string(vendorstore.RoleLead), recOR09LeadTask, "222e4567-e89b-12d3-a456-426614174000"},
		{"sess-rec-member", string(vendorstore.RoleMember), recOR09MemTask, "333e4567-e89b-12d3-a456-426614174000"},
	} {
		if err := hp1.vendorStore.CreateSession(ctx, &vendorstore.Session{
			ID: s.id, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
			Role: s.role, State: vendorstore.StateReady, ApprovalMode: vendorstore.ApprovalPlan,
			WorkspaceID: wsID, WorkspaceRoot: teamTestRootName, TeamID: recOR09TeamID, TaskID: s.taskID,
			ResumeHandle: s.handle,
		}); err != nil {
			t.Fatal(err)
		}
	}
	gm := hp1.rt.teamTasks.git
	sha, err := gm.WorktreeHead(ctx, repoDir)
	if err != nil || sha == "" {
		t.Fatalf("WorktreeHead: %q, %v", sha, err)
	}
	*headSHA = sha
	if branchErr := gm.CreateTeamBranch(ctx, repoDir, recOR09TeamID, sha); branchErr != nil {
		t.Fatalf("CreateTeamBranch: %v", branchErr)
	}
	teamBranch, err := gitops.TeamBranchName(recOR09TeamID)
	if err != nil {
		t.Fatal(err)
	}
	wtPath, err := gm.CreateTaskWorktree(ctx, dataDir, repoDir, repoIDFor(repoDir), recOR09MemTask, teamBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("uncommitted"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
}

func seedOR09TeamAndEvents(t *testing.T, hp1 *HostProcess, wsID string) {
	t.Helper()
	ctx := t.Context()
	propBytes := mustMarshalJSON(t, teams.ProposalInput{
		Title: "OR-E09 team", Brief: "b",
		Roles: []teams.ProposedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: teams.PermissionWrite}},
	})
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionWrite)}},
	})
	specBytes := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Inspect recovery."})
	rec, _, err := hp1.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: recOR09TeamID, WorkspaceID: wsID, SessionID: recOR09LeadSess,
		ProposedEventID: "ore09-prop", ProposedPayload: propBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: recOR09LeadTask, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "ore09-c1", ProtectedSpec: specBytes},
			{TaskID: recOR09MemTask, ParentTaskID: recOR09LeadTask, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "ore09-c2", ProtectedSpec: specBytes},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendTeamEventsSeq(t, hp1.vendorStore, recOR09TeamID, rec.LastSeq, []vendorstore.AppendTeamEventInput{
		{EventID: "ore09-start", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes},
		{EventID: "ore09-a1", TaskID: recOR09LeadTask, EventType: vendorstore.EventTaskAssigned, SessionID: recOR09LeadSess},
		{EventID: "ore09-s1", TaskID: recOR09LeadTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "ore09-a2", TaskID: recOR09MemTask, EventType: vendorstore.EventTaskAssigned, SessionID: "sess-rec-member"},
		{EventID: "ore09-s2", TaskID: recOR09MemTask, EventType: vendorstore.EventTaskStarted},
	})
}

func verifyOR09LockedState(t *testing.T, hp2 *HostProcess) {
	t.Helper()
	ctx := t.Context()
	_, tasks, err := hp2.vendorStore.TeamSnapshot(ctx, recOR09TeamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{recOR09LeadTask, recOR09MemTask} {
		if tk, ok := findTask(tasks, tid); !ok || tk.State != vendorstore.TaskStateUnknown {
			t.Fatalf("task %s state = %+v, want unknown", tid, tk)
		}
	}
	if pending, pendErr := hp2.vendorStore.PendingMailbox(ctx); pendErr != nil || len(pending) != 0 {
		t.Fatalf("pending mailbox while locked = (%+v, %v), want empty", pending, pendErr)
	}
}

func verifyOR09UnlockedNotice(t *testing.T, hp2 *HostProcess, headSHA string) {
	t.Helper()
	ctx := t.Context()
	unlockCheapVault(t, hp2)
	var markedSeq int64
	testutil.Spin(t, "member marked unknown event recorded", func() bool {
		evs, evErr := hp2.vendorStore.TeamEventsSince(ctx, recOR09TeamID, 0, 100)
		if evErr != nil {
			return false
		}
		for i := range evs {
			if evs[i].TaskID == recOR09MemTask && evs[i].EventType == vendorstore.EventTaskMarkedUnknown {
				markedSeq = evs[i].Sequence
				return true
			}
		}
		return false
	})
	msgID := fmt.Sprintf("%s:unknown-notice:%s:%d:%s", recOR09TeamID, recOR09MemTask, markedSeq, recOR09LeadTask)
	var rawMsg []byte
	testutil.Spin(t, "delegator recovery notice enqueued", func() bool {
		var openErr error
		rawMsg, openErr = hp2.vendorStore.OpenMailboxPayload(ctx, recOR09TeamID, msgID)
		return openErr == nil
	})
	assertOR09NoticeBytes(t, hp2, rawMsg, headSHA)
}

func assertOR09NoticeBytes(t *testing.T, hp2 *HostProcess, rawMsg []byte, headSHA string) {
	t.Helper()
	var notice mailboxDigestItem
	if unmErr := json.Unmarshal(rawMsg, &notice); unmErr != nil {
		t.Fatal(unmErr)
	}
	if notice.TaskID != recOR09MemTask || notice.State != string(vendorstore.TaskStateUnknown) ||
		!strings.Contains(notice.Summary, headSHA) || !strings.Contains(notice.Summary, "dirty (1 changed)") {
		t.Fatalf("unexpected recovery notice: %+v", notice)
	}
	if _, tasksAfter, snapErr := hp2.vendorStore.TeamSnapshot(t.Context(), recOR09TeamID); snapErr != nil {
		t.Fatal(snapErr)
	} else if mem, _ := findTask(tasksAfter, recOR09MemTask); mem.State != vendorstore.TaskStateUnknown {
		t.Fatalf("member task must never rerun automatically, got %s", mem.State)
	}
}

func verifyLeadResumeTransitionsUnknownToRunning(t *testing.T, hp2 *HostProcess) {
	t.Helper()
	ctx := t.Context()
	testutil.Spin(t, "wake resumes lead task and delivers notice", func() bool {
		snap, snapErr := hp2.vendorStore.Snapshot(ctx, recOR09LeadSess)
		if snapErr != nil || snap.State != vendorstore.StateReady {
			return false
		}
		_, tasksResumed, teamErr := hp2.vendorStore.TeamSnapshot(ctx, recOR09TeamID)
		lead, ok := findTask(tasksResumed, recOR09LeadTask)
		if teamErr != nil || !ok || lead.State != vendorstore.TaskStateRunning {
			return false
		}
		pending, pendErr := hp2.vendorStore.PendingMailbox(ctx)
		return pendErr == nil && len(pending) == 0
	})
	if held := hp2.rt.teamWake.HeldRecipients(); len(held) != 0 {
		t.Fatalf("HeldRecipients after wake resume = %+v, want empty", held)
	}
	evs, err := hp2.vendorStore.TeamEventsSince(ctx, recOR09TeamID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertLeadRecoveryStartEvents(t, evs)
}

func assertLeadRecoveryStartEvents(t *testing.T, evs []vendorstore.RecordedTeamEvent) {
	t.Helper()
	var sawRec, sawStart bool
	for i := range evs {
		ev := &evs[i]
		if ev.TaskID != recOR09LeadTask {
			continue
		}
		if ev.EventType == vendorstore.EventTaskRecoveryDecided && ev.Outcome == recoveryDecisionResume {
			sawRec = true
		}
		if sawRec && ev.EventType == vendorstore.EventTaskStarted {
			sawStart = true
		}
	}
	if !sawRec || !sawStart {
		t.Fatalf("missing EventTaskRecoveryDecided(resume) -> EventTaskStarted for lead: %+v", evs)
	}
}

type recoveryDecisionSetup struct {
	baseHead, wtKeep, wtRespawn             string
	tDetached, tRespawn, tKeepDiff, tCancel string
	sDetached, sRespawn, sCancel            string
}

// TestUnknownTaskRecoveryDecisionsResumeKeepDiffCancel exercises
// fugue_task_recover across all three decisions (resume detached, resume
// respawn in worktree, keep_diff commit to review, and cancel closing
// lingering sessions) and enforces delegator authorization in Go.
func TestUnknownTaskRecoveryDecisionsResumeKeepDiffCancel(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	setup := setupRecoveryDecisionsFixture(t, f)
	assertRecoverAuthorization(t, f, setup.tKeepDiff)
	assertRecoverDecisionsOutcomes(t, f, &setup)
}

func setupRecoveryDecisionsFixture(t *testing.T, f *activeTeamFixture) recoveryDecisionSetup {
	t.Helper()
	ctx := t.Context()
	initRepo(t, f.root)
	store := f.hp.vendorStore
	gm := f.rt.teamTasks.git

	baseHead, err := gm.WorktreeHead(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := gm.CreateTeamBranch(ctx, f.root, f.teamID, baseHead); branchErr != nil {
		t.Fatal(branchErr)
	}
	if sibSnap, snapErr := store.Snapshot(ctx, "sess-sib"); snapErr == nil {
		if _, appErr := store.Append(ctx, "sess-sib", sibSnap.LastSequence, &vendorstore.Event{
			Type: vendorstore.VendorSessionClosed, State: vendorstore.StateClosed, StopProof: vendorstore.StopProved,
		}); appErr != nil {
			t.Fatal(appErr)
		}
	}

	s := recoveryDecisionSetup{
		baseHead: baseHead, tDetached: "t-rec-det", tRespawn: f.siblingTaskID,
		tKeepDiff: "t-rec-keep", tCancel: "t-rec-cancel",
		sDetached: "sess-rec-det", sRespawn: "sess-rec-resp", sCancel: "sess-rec-cancel",
	}
	seedRecoveryDecisionSessions(t, f, &s)
	s.wtKeep, s.wtRespawn = createRecoveryDecisionWorktrees(t, f, gm, &s)
	amendAndMarkUnknownDecisions(t, f, &s)
	return s
}

func seedRecoveryDecisionSessions(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	for _, sess := range []struct{ id, tid, state, handle, stopProof string }{
		{s.sDetached, s.tDetached, vendorstore.StateDetached, "444e4567-e89b-12d3-a456-426614174000", ""},
		{s.sRespawn, s.tRespawn, vendorstore.StateClosed, "", vendorstore.StopProved},
		{s.sCancel, s.tCancel, vendorstore.StateDetached, "", ""},
	} {
		if sessErr := f.hp.vendorStore.CreateSession(ctx, &vendorstore.Session{
			ID: sess.id, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
			Role: string(vendorstore.RoleMember), State: sess.state, ApprovalMode: vendorstore.ApprovalAsk,
			WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName, TeamID: f.teamID, TaskID: sess.tid,
			ResumeHandle: sess.handle, StopProof: sess.stopProof,
		}); sessErr != nil {
			t.Fatal(sessErr)
		}
	}
}

func amendAndMarkUnknownDecisions(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	store := f.hp.vendorStore
	specBytes := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Recover work."})
	rec, _, err := store.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AmendTeam(ctx, f.teamID, &vendorstore.TeamAmendInput{
		ExpectedSeq: rec.LastSeq, AmendEventID: "am-rec", AmendPayload: []byte(`{"op":"add"}`),
		AddedTasks: []vendorstore.NewTaskInput{
			{TaskID: s.tDetached, ParentTaskID: f.leadTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "cr-det", ProtectedSpec: specBytes},
			{TaskID: s.tKeepDiff, ParentTaskID: f.leadTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "cr-keep", ProtectedSpec: specBytes},
			{TaskID: s.tCancel, ParentTaskID: f.leadTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "cr-can", ProtectedSpec: specBytes},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rec, _, err = store.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	appendTeamEventsSeq(t, store, f.teamID, rec.LastSeq, []vendorstore.AppendTeamEventInput{
		{EventID: "ev-a-det", TaskID: s.tDetached, EventType: vendorstore.EventTaskAssigned, SessionID: s.sDetached},
		{EventID: "ev-s-det", TaskID: s.tDetached, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-a-resp", TaskID: s.tRespawn, EventType: vendorstore.EventTaskAssigned, SessionID: s.sRespawn},
		{EventID: "ev-s-resp", TaskID: s.tRespawn, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-a-keep", TaskID: s.tKeepDiff, EventType: vendorstore.EventTaskAssigned},
		{EventID: "ev-s-keep", TaskID: s.tKeepDiff, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-a-can", TaskID: s.tCancel, EventType: vendorstore.EventTaskAssigned, SessionID: s.sCancel},
		{EventID: "ev-s-can", TaskID: s.tCancel, EventType: vendorstore.EventTaskStarted},
		{EventID: "ev-u-det", TaskID: s.tDetached, EventType: vendorstore.EventTaskMarkedUnknown},
		{EventID: "ev-u-resp", TaskID: s.tRespawn, EventType: vendorstore.EventTaskMarkedUnknown},
		{EventID: "ev-u-keep", TaskID: s.tKeepDiff, EventType: vendorstore.EventTaskMarkedUnknown},
		{EventID: "ev-u-can", TaskID: s.tCancel, EventType: vendorstore.EventTaskMarkedUnknown},
	})
}

func createRecoveryDecisionWorktrees(t *testing.T, f *activeTeamFixture, gm *gitops.Manager, s *recoveryDecisionSetup) (wtKeep, wtRespawn string) {
	t.Helper()
	ctx := t.Context()
	teamBranch, err := gitops.TeamBranchName(f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	canonRoot, err := f.rt.workspaces.domain.SelectSessionCwd(ctx, f.wsID, teamTestRootName)
	if err != nil {
		t.Fatal(err)
	}
	wtKeep, err = gm.CreateTaskWorktree(ctx, f.hp.rt.teamTasks.dataDir, canonRoot, repoIDFor(canonRoot), s.tKeepDiff, teamBranch)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(wtKeep, "kept.txt"), []byte("keep me\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	wtRespawn, err = gm.CreateTaskWorktree(ctx, f.hp.rt.teamTasks.dataDir, canonRoot, repoIDFor(canonRoot), s.tRespawn, teamBranch)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(wtRespawn, "resume.txt"), []byte("keep in worktree\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, wtErr := gm.CreateTaskWorktree(ctx, f.hp.rt.teamTasks.dataDir, canonRoot, repoIDFor(canonRoot), s.tDetached, teamBranch); wtErr != nil {
		t.Fatal(wtErr)
	}
	return wtKeep, wtRespawn
}

func assertRecoverAuthorization(t *testing.T, f *activeTeamFixture, tKeepDiff string) {
	t.Helper()
	ctx := t.Context()
	args := []byte(`{"task_id":"` + tKeepDiff + `","decision":"keep_diff"}`)
	if _, memToolErr := f.tools.Call(ctx, f.memberBinding, taskRecoverToolName, args); errcode.CodeOf(memToolErr) != errcode.NotFound {
		t.Fatalf("member tool call err = %v, want NotFound", memToolErr)
	}
	if _, memErr := f.rt.teamTasks.Recover(ctx, f.memberBinding.SessionID, tKeepDiff, recoveryDecisionKeepDiff, ""); errcode.CodeOf(memErr) != errcode.PermissionDenied {
		t.Fatalf("member service recover err = %v, want PermissionDenied", memErr)
	}
	if res, mgrToolErr := f.tools.Call(ctx, f.managerBinding, taskRecoverToolName, args); mgrToolErr != nil || !res.IsError {
		t.Fatalf("manager out-of-subtree tool call = (%+v, %v), want tool error", res, mgrToolErr)
	}
	if _, mgrErr := f.rt.teamTasks.Recover(ctx, f.managerBinding.SessionID, tKeepDiff, recoveryDecisionKeepDiff, ""); errcode.CodeOf(mgrErr) != errcode.PermissionDenied {
		t.Fatalf("manager out-of-subtree recover err = %v, want PermissionDenied", mgrErr)
	}
	if _, runErr := f.rt.teamTasks.Recover(ctx, f.leadBinding.SessionID, f.managerTaskID, recoveryDecisionCancel, ""); errcode.CodeOf(runErr) != errcode.FailedPrecondition {
		t.Fatalf("non-unknown task recover err = %v, want FailedPrecondition", runErr)
	}
}

func assertRecoverDecisionsOutcomes(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	for _, call := range []string{
		`{"task_id":"` + s.tKeepDiff + `","decision":"keep_diff","reason":"keep work"}`,
		`{"task_id":"` + s.tCancel + `","decision":"cancel","reason":"abort"}`,
		`{"task_id":"` + s.tDetached + `","decision":"resume","reason":"continue"}`,
		`{"task_id":"` + s.tRespawn + `","decision":"resume","reason":"fresh"}`,
	} {
		if res, callErr := f.tools.Call(ctx, f.leadBinding, taskRecoverToolName, []byte(call)); callErr != nil || res.IsError {
			t.Fatalf("recover call %s = (%+v, %v)", call, res, callErr)
		}
	}
	if stKeep, stErr := f.rt.teamTasks.git.WorktreeStatus(ctx, s.wtKeep); stErr != nil || stKeep.Dirty || stKeep.HeadSHA == s.baseHead {
		t.Fatalf("keep_diff worktree status = (%+v, %v), want committed clean head != %s", stKeep, stErr, s.baseHead)
	}
	assertRecoveredTaskStatesAndSessions(t, f, s)
}

func assertRecoveredTaskStatesAndSessions(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	_, finalTasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	wantStates := []struct {
		id    string
		state vendorstore.TaskState
	}{
		{s.tKeepDiff, vendorstore.TaskStateReview},
		{s.tCancel, vendorstore.TaskStateCancelled},
		{s.tDetached, vendorstore.TaskStateRunning},
	}
	for _, w := range wantStates {
		if tk, _ := findTask(finalTasks, w.id); tk.State != w.state {
			t.Fatalf("task %s state = %s, want %s", w.id, tk.State, w.state)
		}
	}
	if tk, _ := findTask(finalTasks, s.tRespawn); tk.State != vendorstore.TaskStateRunning || tk.SessionID == s.sRespawn || tk.SessionID == "" {
		t.Fatalf("resume(respawn) task = %+v, want running with new session_id", tk)
	}
	if _, statErr := os.Stat(filepath.Join(s.wtRespawn, "resume.txt")); statErr != nil {
		t.Fatalf("respawn must preserve existing worktree file: %v", statErr)
	}
	if snap, snapErr := f.hp.vendorStore.Snapshot(ctx, s.sDetached); snapErr != nil || (snap.State != vendorstore.StateReady && snap.State != vendorstore.StateRunning) {
		t.Fatalf("resumed detached session %s state = %+v, err=%v", s.sDetached, snap, snapErr)
	}
}

// TestHostStartRebuildsPendingProposalAndQuestionCards verifies Q-142 A &
// Q-143 A: unlocking after host restart rebuilds proposal cards for proposed
// teams whose proposing session is not closed and user question cards for
// unknown tasks marked from asking with latest TaskQuestionAsked to user; a
// second unlock creates no duplicates; answering the rebuilt question card
// moves the task unknown -> running and delivers the answer.
func TestHostStartRebuildsPendingProposalAndQuestionCards(t *testing.T) {
	t.Parallel()
	dataDir := cheapVaultDataDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC))

	setupRebuildCardsHost1(t, dataDir, fugueHome, clk)

	clk.Advance(time.Minute)
	hp2 := startRecoveryHost(t, dataDir, fugueHome, clk)
	defer closeHostAdvanceClock(t, hp2, clk)
	unlockCheapVault(t, hp2)

	qTicketID := assertRebuiltCardsPresent(t, hp2)
	qTicketID = assertSecondUnlockIdempotent(t, hp2, qTicketID)
	assertAnswerRebuiltQuestionWakesSession(t, hp2, qTicketID)
}

func setupRebuildCardsHost1(t *testing.T, dataDir, fugueHome string, clk *clock.Fake) {
	t.Helper()
	ctx := t.Context()
	hp1 := startRecoveryHost(t, dataDir, fugueHome, clk)
	unlockCheapVault(t, hp1)
	wsID, canonRoot := createTestRepoWorkspace(t, hp1)

	gm := hp1.rt.teamTasks.git
	headSHA, err := gm.WorktreeHead(ctx, canonRoot)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := gm.CreateTeamBranch(ctx, canonRoot, recQTeamID, headSHA); branchErr != nil {
		t.Fatal(branchErr)
	}
	qBranch, err := gitops.TeamBranchName(recQTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, wtErr := gm.CreateTaskWorktree(ctx, dataDir, canonRoot, repoIDFor(canonRoot), recQUserTask, qBranch); wtErr != nil {
		t.Fatal(wtErr)
	}

	for _, s := range []struct{ id, state, handle, stopProof, role, teamID, taskID string }{
		{recQOpenSess, vendorstore.StateReady, "444e4567-e89b-12d3-a456-426614174000", "", string(vendorstore.RoleLead), "", ""},
		{"sess-closed-prop", vendorstore.StateClosed, "", vendorstore.StopProved, string(vendorstore.RoleLead), "", ""},
		{recQAskUserSess, vendorstore.StateReady, "555e4567-e89b-12d3-a456-426614174000", "", string(vendorstore.RoleMember), recQTeamID, recQUserTask},
		{"sess-ask-parent", vendorstore.StateReady, "666e4567-e89b-12d3-a456-426614174000", "", string(vendorstore.RoleMember), recQTeamID, recQParentTask},
	} {
		if sessErr := hp1.vendorStore.CreateSession(ctx, &vendorstore.Session{
			ID: s.id, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
			Role: s.role, State: s.state, ApprovalMode: vendorstore.ApprovalPlan,
			WorkspaceID: wsID, WorkspaceRoot: teamTestRootName, TeamID: s.teamID, TaskID: s.taskID,
			ResumeHandle: s.handle, StopProof: s.stopProof,
		}); sessErr != nil {
			t.Fatal(sessErr)
		}
	}

	createRebuildSeedTeams(t, hp1, wsID)
	// In-memory amend card must not persist across restart (Q-143 A).
	hp1.rt.teamProposals.pendingAmends.put(recQTeamID, &pendingAmend{sessionID: recQOpenSess, amendID: "mem-only", operationID: "op-mem-only"})
	closeHostAdvanceClock(t, hp1, clk)
}

func createRebuildSeedTeams(t *testing.T, hp1 *HostProcess, wsID string) {
	t.Helper()
	ctx := t.Context()
	propPayload := mustMarshalJSON(t, teams.ProposalInput{
		Title: "Rebuild Proposal", Brief: "b",
		Roles: []teams.ProposedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: teams.PermissionRead}},
		Tasks: []teams.ProposedTask{
			{ID: "lead", TeamRole: teams.RoleLead, Role: teamTestRole, Deliverable: teams.DeliverableReport, WriteScope: []string{}, Goal: "Lead"},
			{ID: "w1", ParentTaskID: "lead", TeamRole: teams.RoleMember, Role: teamTestRole, Deliverable: teams.DeliverableReport, WriteScope: []string{}, Goal: "Work"},
		},
	})
	specBytes := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Ask"})
	for _, tp := range []struct{ tid, sid, ev, cr string }{
		{"team-prop-open", recQOpenSess, "po-ev", "po-c1"},
		{"team-prop-closed", "sess-closed-prop", "pc-ev", "pc-c1"},
	} {
		if _, _, createErr := hp1.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
			TeamID: tp.tid, WorkspaceID: wsID, SessionID: tp.sid,
			ProposedEventID: tp.ev, ProposedPayload: propPayload,
			Tasks: []vendorstore.NewTaskInput{{TaskID: tp.cr, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: tp.cr, ProtectedSpec: specBytes}},
		}); createErr != nil {
			t.Fatal(createErr)
		}
	}

	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionRead)}},
	})
	recQ, _, err := hp1.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: recQTeamID, WorkspaceID: wsID, SessionID: recQOpenSess,
		ProposedEventID: "tq-p", ProposedPayload: propPayload,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: recQLeadTask, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "tq-c1", ProtectedSpec: specBytes},
			{TaskID: recQUserTask, ParentTaskID: recQLeadTask, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "tq-c2", ProtectedSpec: specBytes},
			{TaskID: recQParentTask, ParentTaskID: recQLeadTask, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "tq-c3", ProtectedSpec: specBytes},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendTeamEventsSeq(t, hp1.vendorStore, recQTeamID, recQ.LastSeq, []vendorstore.AppendTeamEventInput{
		{EventID: "tq-s", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes},
		{EventID: "tq-a1", TaskID: recQLeadTask, EventType: vendorstore.EventTaskAssigned, SessionID: recQOpenSess},
		{EventID: "tq-s1", TaskID: recQLeadTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "tq-a2", TaskID: recQUserTask, EventType: vendorstore.EventTaskAssigned, SessionID: recQAskUserSess},
		{EventID: "tq-s2", TaskID: recQUserTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "tq-q2", TaskID: recQUserTask, EventType: vendorstore.EventTaskQuestionAsked, ProtectedPayload: mustMarshalJSON(t, taskQuestionPayload{Question: "Which port?", To: askTargetUser})},
		{EventID: "tq-a3", TaskID: recQParentTask, EventType: vendorstore.EventTaskAssigned, SessionID: "sess-ask-parent"},
		{EventID: "tq-s3", TaskID: recQParentTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "tq-q3", TaskID: recQParentTask, EventType: vendorstore.EventTaskQuestionAsked, ProtectedPayload: mustMarshalJSON(t, taskQuestionPayload{Question: "Parent only?", To: askTargetParent})},
	})
}

func assertRebuiltCardsPresent(t *testing.T, hp2 *HostProcess) string {
	t.Helper()
	ctx := t.Context()
	var views []PendingInteractionView
	testutil.Spin(t, "rebuilt proposal and question cards", func() bool {
		var listErr error
		views, _, listErr = hp2.rt.interactions.ListPending(ctx, testAppPrincipal)
		return listErr == nil && len(views) == 2
	})
	var propTicketID, qTicketID string
	for i := range views {
		switch views[i].Tool {
		case teamProposeToolName:
			if !strings.Contains(views[i].Summary, "Rebuild Proposal") {
				t.Fatalf("unexpected rebuilt proposal card: %+v", views[i])
			}
			propTicketID = views[i].RequestID
		case taskAskToolName:
			if !strings.Contains(views[i].Summary, recQUserTask) {
				t.Fatalf("unexpected rebuilt question card: %+v", views[i])
			}
			qTicketID = views[i].RequestID
		default:
			t.Fatalf("unexpected rebuilt card tool: %+v", views[i])
		}
	}
	if propTicketID == "" || qTicketID == "" {
		t.Fatalf("missing expected cards: %+v", views)
	}
	testutil.Spin(t, "unlock recovery mailbox notice enqueued", func() bool {
		pending, pendErr := hp2.vendorStore.PendingMailbox(ctx)
		return pendErr == nil && len(pending) >= 1
	})
	return qTicketID
}

func assertSecondUnlockIdempotent(t *testing.T, hp2 *HostProcess, qTicketID string) string {
	t.Helper()
	ctx := t.Context()
	hp2.vault.Lock()
	unlockCheapVault(t, hp2)
	testutil.Spin(t, "rebuilt cards after second unlock", func() bool {
		views2, _, listErr := hp2.rt.interactions.ListPending(ctx, testAppPrincipal)
		return listErr == nil && len(views2) == 2
	})
	if propErr := hp2.rt.teamProposals.RebuildPendingProposalCards(ctx); propErr != nil {
		t.Fatalf("idempotent RebuildPendingProposalCards: %v", propErr)
	}
	if recErr := hp2.rt.teamTasks.RecoverOnUnlock(ctx); recErr != nil {
		t.Fatalf("idempotent RecoverOnUnlock: %v", recErr)
	}
	views3, _, err := hp2.rt.interactions.ListPending(ctx, testAppPrincipal)
	if err != nil || len(views3) != 2 {
		t.Fatalf("cards after repeated unlock recovery = (%d, %v), want 2", len(views3), err)
	}
	for i := range views3 {
		if views3[i].Tool == taskAskToolName {
			return views3[i].RequestID
		}
	}
	return qTicketID
}

func assertAnswerRebuiltQuestionWakesSession(t *testing.T, hp2 *HostProcess, qTicketID string) {
	t.Helper()
	ctx := t.Context()
	if _, decErr := hp2.rt.interactions.Answer(ctx, &AnswerInteractionCommand{
		Principal:   testAppPrincipal,
		CommandID:   "ans-rebuilt-q",
		RequestID:   qTicketID,
		Incarnation: hp2.rt.interactions.Incarnation(),
		Outcome:     string(interaction.OutcomeAnswered),
		Answers:     []InteractionAnswer{{QuestionID: taskQuestionCardID, Text: "use 8080"}},
	}); decErr != nil {
		t.Fatalf("Answer rebuilt question card: %v", decErr)
	}
	_, tasksAfter, err := hp2.vendorStore.TeamSnapshot(ctx, recQTeamID)
	if err != nil {
		t.Fatal(err)
	}
	if qTask, _ := findTask(tasksAfter, recQUserTask); qTask.State != vendorstore.TaskStateRunning {
		t.Fatalf("task-q-user after answer = %s, want running", qTask.State)
	}
	testutil.Spin(t, "askUserSess resumed and became ready", func() bool {
		snap, snapErr := hp2.vendorStore.Snapshot(ctx, recQAskUserSess)
		return snapErr == nil && snap.State == vendorstore.StateReady
	})
}

// TestAgyIdleSendAndLoadResumeDeliverMailboxOrHoldOnFailure exercises both
// branches of the lifted agy gate using the fake agy vendor:
// (1) idle-send on a ready agy session and session/load resume on a detached
// agy session both deliver mailbox turns; (2) genuine resume failure holds
// the mailbox fail-closed with HeldRecipients and TopicTeamMailbox.
func TestAgyIdleSendAndLoadResumeDeliverMailboxOrHoldOnFailure(t *testing.T) {
	t.Parallel()
	assertAgyIdleAndLoadResumeDeliver(t)
	assertAgyResumeFailureHoldsFailClosed(t)
}

func assertAgyIdleAndLoadResumeDeliver(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	f := newWakeFixture(t)
	team := createAGYWakeTeam(t, f, vendorstore.StateDetached)
	if _, err := f.hp.vendorStore.Append(ctx, team.aSession, 1, &vendorstore.Event{
		Type: vendorstore.VendorSessionStateChanged, ResumeHandle: "777e4567-e89b-12d3-a456-426614174000",
	}); err != nil {
		t.Fatal(err)
	}
	for _, mail := range []struct{ task, key string }{
		{team.b, "agy-idle-b"},
		{team.a, "agy-resume-a"},
	} {
		if err := f.hp.vendorStore.EnqueueDirectMailbox(ctx, team.teamID, mail.task, vendorstore.MailboxKindSend, mail.key, digestItemPayload(t, mail.task)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatalf("Pump 1: %v", err)
	}
	if len(f.sentTexts(team.bSession)) != 1 || f.sessions.resumeCount() != 1 || len(f.sentTexts(team.aSession)) != 0 {
		t.Fatalf("after Pump 1: b=%v resumes=%d a=%v", f.sentTexts(team.bSession), f.sessions.resumeCount(), f.sentTexts(team.aSession))
	}
	if held := f.wake.HeldRecipients(); len(held) != 0 {
		t.Fatalf("HeldRecipients after successful agy resume = %+v, want empty", held)
	}
	f.setSessionState(t, team.aSession, vendorstore.StateReady)
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatalf("Pump 2: %v", err)
	}
	if len(f.sentTexts(team.aSession)) != 1 {
		t.Fatalf("resumed agy session texts = %v, want 1", f.sentTexts(team.aSession))
	}
}

func assertAgyResumeFailureHoldsFailClosed(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	fFail := newWakeFixture(t)
	teamFail := createAGYWakeTeam(t, fFail, vendorstore.StateDetached)
	fFail.sessions.resumeErr = errcode.New(errcode.Unavailable, "session/load failed")
	fFail.enqueueMailbox(t, teamFail, teamFail.a, digestItemPayload(t, teamFail.a))

	if err := fFail.wake.Pump(ctx); err != nil {
		t.Fatalf("Pump fail: %v", err)
	}
	if len(fFail.sentTexts(teamFail.aSession)) != 0 {
		t.Fatalf("failed agy resume must not deliver: %v", fFail.sentTexts(teamFail.aSession))
	}
	held := fFail.wake.HeldRecipients()
	if len(held) != 1 || held[0].SessionID != teamFail.aSession || held[0].Vendor != VendorAGY {
		t.Fatalf("HeldRecipients = %+v, want held agy session", held)
	}
	pending, err := fFail.hp.vendorStore.PendingMailbox(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingMailbox after resume failure = (%+v, %v), want 1 pending", pending, err)
	}
}
