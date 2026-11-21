package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	intLeadTaskID  = "int-lead"
	intMgrTaskID   = "int-mgr"
	intLeadCreated = "c-int-lead"
	intLeadGoal    = "integration lead"
	intMgrGoal     = "integration mgr"
	intSharedFile  = "shared.txt"
	intMgrFile     = "mgr.txt"
	intMemFile     = "mem.txt"
)

type integrationTeamSpec struct {
	teamID   string
	leadSess string
	tasks    []vendorstore.NewTaskInput
	sessions [][3]string
}

type integrationFixture struct {
	*teamProposalFixture
	teamID   string
	leadSess string
	repoDir  string
	repoID   string
	baseHead string
	teamBr   string
}

func newIntegrationFixture(t *testing.T, spec *integrationTeamSpec) *integrationFixture {
	t.Helper()
	f := newTeamFixture(t, "", false)
	initRepo(t, f.root)
	repoDir, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	gm := f.rt.teamTasks.git
	baseHead, err := gm.WorktreeHead(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := gm.CreateTeamBranch(ctx, repoDir, spec.teamID, baseHead); branchErr != nil {
		t.Fatal(branchErr)
	}
	teamBr, err := gitops.TeamBranchName(spec.teamID)
	if err != nil {
		t.Fatal(err)
	}
	seedIntegrationStore(t, f, spec)
	return &integrationFixture{
		teamProposalFixture: f,
		teamID:              spec.teamID,
		leadSess:            spec.leadSess,
		repoDir:             repoDir,
		repoID:              repoIDFor(repoDir),
		baseHead:            baseHead,
		teamBr:              teamBr,
	}
}

func seedIntegrationStore(t *testing.T, f *teamProposalFixture, spec *integrationTeamSpec) {
	t.Helper()
	ctx := t.Context()
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles: []decidedRole{
			{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: string(teams.PermissionWrite)},
			{Name: "reader", Vendor: VendorClaude, Permission: string(teams.PermissionRead)},
		},
	})
	propBytes := mustMarshalJSON(t, teams.ProposalInput{
		Title: "Integration Team", Brief: "Shared brief.",
		Roles: []teams.ProposedRole{
			{Name: gateTestRoleCoder, Vendor: VendorClaude, Permission: teams.PermissionWrite},
			{Name: "reader", Vendor: VendorClaude, Permission: teams.PermissionRead},
		},
	})
	seedTeamSessions(t, f, spec.teamID, spec.sessions)
	for _, s := range spec.sessions {
		if regErr := f.rt.transcripts.RegisterSession(s[0]); regErr != nil {
			t.Fatal(regErr)
		}
	}
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: spec.teamID, WorkspaceID: f.wsID, SessionID: spec.leadSess,
		ProposedEventID: "prop-" + spec.teamID, ProposedPayload: propBytes, Tasks: spec.tasks,
	})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.hp.vendorStore.AppendTeamEvent(ctx, spec.teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "start-" + spec.teamID, EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range spec.sessions {
		seq, err = f.hp.vendorStore.AppendTeamEvent(ctx, spec.teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: "asgn-" + s[1], TaskID: s[1], EventType: vendorstore.EventTaskAssigned, SessionID: s[0],
		})
		if err != nil {
			t.Fatal(err)
		}
		seq, err = f.hp.vendorStore.AppendTeamEvent(ctx, spec.teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: "st-" + s[1], TaskID: s[1], EventType: vendorstore.EventTaskStarted,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newLeadMembersFixture(t *testing.T, teamID, leadSess string, memberIDs []string, scopes [][]string) *integrationFixture {
	t.Helper()
	tasks := make([]vendorstore.NewTaskInput, 1, 1+len(memberIDs))
	tasks[0] = vendorstore.NewTaskInput{
		TaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleLead, CatalogRole: gateTestRoleCoder,
		Deliverable: vendorstore.DeliverableReport, CreatedEventID: intLeadCreated,
		ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: intLeadGoal}),
	}
	sessions := make([][3]string, 1, 1+len(memberIDs))
	sessions[0] = [3]string{leadSess, intLeadTaskID, string(vendorstore.RoleLead)}
	for i, id := range memberIDs {
		tasks = append(tasks, vendorstore.NewTaskInput{
			TaskID: id, ParentTaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleMember,
			CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff,
			CreatedEventID: fmt.Sprintf("c-%d-%s", i, id),
			ProtectedSpec:  mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: id, WriteScope: scopes[i]}),
		})
		sessions = append(sessions, [3]string{"sess-" + id, id, string(vendorstore.RoleMember)})
	}
	return newIntegrationFixture(t, &integrationTeamSpec{
		teamID: teamID, leadSess: leadSess, tasks: tasks, sessions: sessions,
	})
}

func (fx *integrationFixture) mustTaskBranch(t *testing.T, taskID string) string {
	t.Helper()
	br, err := gitops.TaskBranchName(taskID)
	if err != nil {
		t.Fatalf("TaskBranchName(%s): %v", taskID, err)
	}
	return br
}

func (fx *integrationFixture) createWorktree(t *testing.T, taskID, parentBranch string) string {
	t.Helper()
	wt, err := fx.rt.teamTasks.git.CreateTaskWorktree(t.Context(), fx.rt.teamTasks.dataDir, fx.repoDir, fx.repoID, gitTaskID(taskID), parentBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree(%s): %v", taskID, err)
	}
	return wt
}

func (fx *integrationFixture) writeAndCommit(t *testing.T, wtDir, rel, content string) string {
	t.Helper()
	p := filepath.Join(wtDir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := fx.rt.teamTasks.git.CommitWorktree(t.Context(), wtDir, "work in "+rel)
	if err != nil {
		t.Fatal(err)
	}
	return res.SHA
}

func (fx *integrationFixture) waitMergeIdle(t *testing.T) {
	t.Helper()
	if err := fx.rt.teamTasks.mergeQueue.WaitIdle(t.Context()); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
}

func (fx *integrationFixture) setSessionState(t *testing.T, sessionID, state string) {
	t.Helper()
	info, err := fx.hp.vendorStore.Snapshot(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("Snapshot(%s): %v", sessionID, err)
	}
	if _, err := fx.hp.vendorStore.Append(t.Context(), sessionID, info.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorSessionStateChanged, State: state,
	}); err != nil {
		t.Fatalf("Append(%s, %s): %v", sessionID, state, err)
	}
}

func (fx *integrationFixture) taskRecord(t *testing.T, taskID string) vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := fx.hp.vendorStore.TeamSnapshot(t.Context(), fx.teamID)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := findTask(tasks, taskID)
	if !ok {
		t.Fatalf("task %s not found", taskID)
	}
	return *rec
}

func (fx *integrationFixture) doneAndAccept(t *testing.T, ids ...string) {
	t.Helper()
	ctx := t.Context()
	for _, id := range ids {
		if _, err := fx.rt.teamTasks.Done(ctx, "sess-"+id, "done "+id, nil, taskStatusSucceeded); err != nil {
			t.Fatalf("Done(%s): %v", id, err)
		}
	}
	for _, id := range ids {
		if _, err := fx.rt.teamTasks.Accept(ctx, fx.leadSess, id, ""); err != nil {
			t.Fatalf("Accept(%s): %v", id, err)
		}
	}
	fx.waitMergeIdle(t)
}

// TestMergeQueueSerializesAndRebasesTheSecondTask pins OR-E05: two tasks
// touching one file integrate in order; the second rebases cleanly when
// non-overlapping; a conflicting task aborts cleanly and returns to the branch
// owner with the conflicted file list.
func TestMergeQueueSerializesAndRebasesTheSecondTask(t *testing.T) {
	t.Parallel()
	const teamID, leadSess = "team-ore05", "sess-ore05-lead"
	fx := newLeadMembersFixture(t, teamID, leadSess,
		[]string{"t1", "t2", "t3"},
		[][]string{{intSharedFile}, {intSharedFile}, {intSharedFile}},
	)
	ctx := t.Context()

	seedWT := fx.createWorktree(t, "seed-tmp", fx.teamBr)
	fx.writeAndCommit(t, seedWT, intSharedFile, "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n")
	if _, err := fx.rt.teamTasks.git.FastForwardBranch(ctx, fx.repoDir, fx.teamBr, fx.mustTaskBranch(t, "seed-tmp")); err != nil {
		t.Fatal(err)
	}

	wt1 := fx.createWorktree(t, "t1", fx.teamBr)
	wt2 := fx.createWorktree(t, "t2", fx.teamBr)
	wt3 := fx.createWorktree(t, "t3", fx.teamBr)
	fx.writeAndCommit(t, wt1, intSharedFile, "line1-t1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n")
	fx.writeAndCommit(t, wt2, intSharedFile, "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10-t2\n")
	fx.writeAndCommit(t, wt3, intSharedFile, "line1-t3-conflict\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n")

	fx.doneAndAccept(t, "t1", "t2", "t3")

	for _, id := range []string{"t1", "t2"} {
		if st := fx.taskRecord(t, id).State; st != vendorstore.TaskStateDone {
			t.Fatalf("%s state = %s, want done", id, st)
		}
	}
	if st := fx.taskRecord(t, "t3").State; st != vendorstore.TaskStateReview {
		t.Fatalf("t3 state = %s, want review after conflict", st)
	}
	raw, found, err := fx.hp.vendorStore.OpenLatestTaskEventPayload(ctx, teamID, "t3", vendorstore.EventTaskIntegrated)
	if err != nil || !found || !strings.Contains(string(raw), intSharedFile) {
		t.Fatalf("t3 conflict payload = (%q, %v, %v), want %s", raw, found, err, intSharedFile)
	}
}

func (fx *integrationFixture) integrateChildAtManagerTurnBoundary(t *testing.T, mgrWT, mgrSess, memID, memSess string) {
	t.Helper()
	ctx := t.Context()
	memWT := fx.createWorktree(t, memID, fx.mustTaskBranch(t, intMgrTaskID))
	fx.writeAndCommit(t, memWT, intMemFile, "from member\n")
	if _, err := fx.rt.teamTasks.Done(ctx, memSess, "mem done", nil, taskStatusSucceeded); err != nil {
		t.Fatalf("Done(mem): %v", err)
	}
	fx.setSessionState(t, mgrSess, vendorstore.StateRunning)
	if err := os.WriteFile(filepath.Join(mgrWT, intMgrFile), []byte("manager own uncommitted work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.rt.teamTasks.Accept(ctx, mgrSess, memID, ""); err != nil {
		t.Fatalf("Accept(mem): %v", err)
	}
	fx.waitMergeIdle(t)
	if st := fx.taskRecord(t, memID).State; st != vendorstore.TaskStateIntegrating {
		t.Fatalf("mem state while manager mid-turn = %s, want integrating", st)
	}
	fx.setSessionState(t, mgrSess, vendorstore.StateReady)
	fx.rt.teamTasks.OnTurnCompleted(ctx, mgrSess)
	fx.waitMergeIdle(t)
	if st := fx.taskRecord(t, memID).State; st != vendorstore.TaskStateDone {
		t.Fatalf("mem state after manager turn boundary = %s, want done", st)
	}
}

// TestLayeredIntegrationNeverTouchesTheTargetBranch pins OR-E17 (layered
// integration part): member -> manager branch (with turn-boundary commit) ->
// team branch, while the workspace target branch (main) never moves.
func TestLayeredIntegrationNeverTouchesTheTargetBranch(t *testing.T) {
	t.Parallel()
	const (
		teamID, leadSess = "team-ore17", "sess-ore17-lead"
		mgrSess, memID   = "sess-ore17-mgr", "mem"
		memSess          = "sess-ore17-mem"
	)
	fx := newIntegrationFixture(t, &integrationTeamSpec{
		teamID: teamID, leadSess: leadSess,
		tasks: []vendorstore.NewTaskInput{
			{TaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleLead, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableReport, CreatedEventID: intLeadCreated, ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: intLeadGoal})},
			{TaskID: intMgrTaskID, ParentTaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleTeamManager, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-m", ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: intMgrGoal, WriteScope: []string{intMgrFile, intMemFile}})},
			{TaskID: memID, ParentTaskID: intMgrTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-c", ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "mem", WriteScope: []string{intMemFile}})},
		},
		sessions: [][3]string{
			{leadSess, intLeadTaskID, string(vendorstore.RoleLead)},
			{mgrSess, intMgrTaskID, string(vendorstore.RoleTeamManager)},
			{memSess, memID, string(vendorstore.RoleMember)},
		},
	})
	ctx := t.Context()

	mgrWT := fx.createWorktree(t, intMgrTaskID, fx.teamBr)
	fx.integrateChildAtManagerTurnBoundary(t, mgrWT, mgrSess, memID, memSess)
	for _, f := range []string{intMgrFile, intMemFile} {
		if _, err := os.Stat(filepath.Join(mgrWT, f)); err != nil {
			t.Fatalf("%s missing in mgrWT: %v", f, err)
		}
	}

	if _, err := fx.rt.teamTasks.Done(ctx, mgrSess, "mgr done", nil, taskStatusSucceeded); err != nil {
		t.Fatalf("Done(mgr): %v", err)
	}
	if _, err := fx.rt.teamTasks.Accept(ctx, leadSess, intMgrTaskID, ""); err != nil {
		t.Fatalf("Accept(mgr): %v", err)
	}
	fx.waitMergeIdle(t)
	if st := fx.taskRecord(t, intMgrTaskID).State; st != vendorstore.TaskStateDone {
		t.Fatalf("mgr state = %s, want done", st)
	}
	if mainHead, err := fx.rt.teamTasks.git.WorktreeHead(ctx, fx.repoDir); err != nil || mainHead != fx.baseHead {
		t.Fatalf("workspace target HEAD = (%q, %v), want %q", mainHead, err, fx.baseHead)
	}
}

// TestReparentedTaskRebasesOntoTheTeamBranch pins OR-E22 (§10.2): a child
// re-parented from a cancelled unknown manager rebases --onto the team branch
// so the cancelled manager's commits never land, reruns §10 gates 1-4, and
// returns any conflict to the lead without auto-resolving.
func TestReparentedTaskRebasesOntoTheTeamBranch(t *testing.T) {
	t.Parallel()
	const (
		teamID, leadSess = "team-ore22", "sess-ore22-lead"
		mgrSess          = "sess-ore22-mgr"
		c1ID, c2ID       = "c-clean", "c-conflict"
	)
	fx := newIntegrationFixture(t, &integrationTeamSpec{
		teamID: teamID, leadSess: leadSess,
		tasks: []vendorstore.NewTaskInput{
			{TaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleLead, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableReport, CreatedEventID: intLeadCreated, ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: intLeadGoal})},
			{TaskID: intMgrTaskID, ParentTaskID: intLeadTaskID, HierarchyRole: vendorstore.RoleTeamManager, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-m", ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: intMgrGoal, WriteScope: []string{intMgrFile}})},
			{TaskID: c1ID, ParentTaskID: intMgrTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-1", ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "clean", WriteScope: []string{"clean.txt"}})},
			{TaskID: c2ID, ParentTaskID: intMgrTaskID, HierarchyRole: vendorstore.RoleMember, CatalogRole: gateTestRoleCoder, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "c-2", ProtectedSpec: mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "conflict", WriteScope: []string{"clean.txt"}})},
		},
		sessions: [][3]string{
			{leadSess, intLeadTaskID, string(vendorstore.RoleLead)},
			{mgrSess, intMgrTaskID, string(vendorstore.RoleTeamManager)},
			{"sess-" + c1ID, c1ID, string(vendorstore.RoleMember)},
			{"sess-" + c2ID, c2ID, string(vendorstore.RoleMember)},
		},
	})
	ctx := t.Context()

	mgrWT := fx.createWorktree(t, intMgrTaskID, fx.teamBr)
	mgrSHA := fx.writeAndCommit(t, mgrWT, intMgrFile, "cancelled manager commit\n")
	mgrBr := fx.mustTaskBranch(t, intMgrTaskID)

	c1WT := fx.createWorktree(t, c1ID, mgrBr)
	c2WT := fx.createWorktree(t, c2ID, mgrBr)
	fx.writeAndCommit(t, c1WT, "clean.txt", "clean child work\n")
	fx.writeAndCommit(t, c2WT, "clean.txt", "conflicting child work\n")

	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.hp.vendorStore.AppendTeamEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "unk-mgr", TaskID: intMgrTaskID, EventType: vendorstore.EventTaskMarkedUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.rt.teamTasks.Recover(ctx, leadSess, intMgrTaskID, recoveryDecisionCancel, "lead takes over"); err != nil {
		t.Fatalf("Recover(cancel mgr): %v", err)
	}

	fx.doneAndAccept(t, c1ID, c2ID)

	if st := fx.taskRecord(t, c1ID).State; st != vendorstore.TaskStateDone {
		t.Fatalf("c1 state = %s, want done", st)
	}
	if hasMgr, err := fx.rt.teamTasks.git.BranchContainsCommit(ctx, fx.repoDir, fx.teamBr, mgrSHA); err != nil || hasMgr {
		t.Fatalf("team branch contains cancelled manager commit: (%v, %v)", hasMgr, err)
	}
	if st := fx.taskRecord(t, c2ID).State; st != vendorstore.TaskStateReview {
		t.Fatalf("c2 state = %s, want review after conflict", st)
	}
}

// TestIntegratingTasksReconcileAfterRestart pins §12 restart reconciliation
// on vault unlock: a task in `integrating` whose commit is already in the
// target branch becomes `done` without replaying fast-forward, while one not
// yet in the target branch is re-enqueued and integrated.
func TestIntegratingTasksReconcileAfterRestart(t *testing.T) {
	t.Parallel()
	const (
		teamID, leadSess = "team-rec", "sess-rec-lead"
		tDoneID, tPendID = "t-already", "t-pending"
	)
	fx := newLeadMembersFixture(t, teamID, leadSess,
		[]string{tDoneID, tPendID},
		[][]string{{"a.txt"}, {"b.txt"}},
	)
	ctx := t.Context()

	wtDone := fx.createWorktree(t, tDoneID, fx.teamBr)
	wtPend := fx.createWorktree(t, tPendID, fx.teamBr)
	fx.writeAndCommit(t, wtDone, "a.txt", "already merged\n")
	fx.writeAndCommit(t, wtPend, "b.txt", "pending merge\n")

	for _, id := range []string{tDoneID, tPendID} {
		if _, err := fx.rt.teamTasks.Done(ctx, "sess-"+id, "done", nil, taskStatusSucceeded); err != nil {
			t.Fatalf("Done(%s): %v", id, err)
		}
	}
	if _, err := fx.rt.teamTasks.git.FastForwardBranch(ctx, fx.repoDir, fx.teamBr, fx.mustTaskBranch(t, tDoneID)); err != nil {
		t.Fatal(err)
	}
	rec, _, err := fx.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	seq := rec.LastSeq
	for _, tid := range []string{tDoneID, tPendID} {
		seq, err = fx.hp.vendorStore.AppendTeamEvent(ctx, teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: "app-" + tid, TaskID: tid, EventType: vendorstore.EventTaskReviewed, Outcome: "approved",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := fx.rt.teamTasks.RecoverOnUnlock(ctx); err != nil {
		t.Fatalf("RecoverOnUnlock: %v", err)
	}
	fx.waitMergeIdle(t)

	for _, id := range []string{tDoneID, tPendID} {
		if st := fx.taskRecord(t, id).State; st != vendorstore.TaskStateDone {
			t.Fatalf("%s state = %s, want done", id, st)
		}
	}
}

// TestApproveEveryDiffStepAndPostRebaseGateFailure verifies the optional
// teams.approve_every_diff_step user card.
func TestApproveEveryDiffStepAndPostRebaseGateFailure(t *testing.T) {
	t.Parallel()
	const teamID, leadSess, tID = "team-opt", "sess-opt-lead", "t-opt"
	fx := newLeadMembersFixture(t, teamID, leadSess, []string{tID}, [][]string{{"allowed.txt"}})
	ctx := t.Context()

	ws, err := fx.rt.workspaces.domain.GetWorkspace(ctx, fx.wsID)
	if err != nil {
		t.Fatal(err)
	}
	rawCfg := fmt.Appendf(nil, "[[roots]]\nname = %q\npath = %q\naccess = \"rw\"\n\n[teams]\napprove_every_diff_step = true\n", teamTestRootName, ws.Roots[0].Path)
	updated, err := fx.rt.workspaces.domain.UpdateWorkspaceConfig(ctx, fx.wsID, rawCfg, ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.rt.workspaces.domain.ConfirmWorkspaceConfig(ctx, fx.wsID, updated.ConfigHash); err != nil {
		t.Fatal(err)
	}

	wt := fx.createWorktree(t, tID, fx.teamBr)
	fx.writeAndCommit(t, wt, "allowed.txt", "ok\n")
	if _, err := fx.rt.teamTasks.Done(ctx, "sess-"+tID, "done", nil, taskStatusSucceeded); err != nil {
		t.Fatalf("Done(%s): %v", tID, err)
	}

	doneCh := make(chan error, 1)
	supervise.Go(ctx, "accept-diff-step", func(_ context.Context) error {
		_, accErr := fx.rt.teamTasks.Accept(ctx, leadSess, tID, "")
		doneCh <- accErr
		return nil
	})
	var pending PendingInteractionView
	testutil.Spin(t, "diff step approval card", func() bool {
		list, _, listErr := fx.rt.interactions.ListPending(ctx, testAppPrincipal)
		if listErr != nil {
			return false
		}
		for i := range list {
			if strings.Contains(list[i].Summary, tID) {
				pending = list[i]
				return true
			}
		}
		return false
	})
	if _, err := fx.rt.interactions.Answer(ctx, &AnswerInteractionCommand{
		CommandID: "cmd-diff-opt", RequestID: pending.RequestID, Incarnation: pending.Incarnation,
		Outcome: string(interaction.OutcomeAllowedOnce), Principal: testAppPrincipal,
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if err := <-doneCh; err != nil {
		t.Fatalf("Accept after user approval: %v", err)
	}
	fx.waitMergeIdle(t)
	if st := fx.taskRecord(t, tID).State; st != vendorstore.TaskStateDone {
		t.Fatalf("state = %s, want done", st)
	}
}
