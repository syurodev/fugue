package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const testRecoverCmdResume = "rec-resume-1"

func gitExec(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", cmd.Args, err, out)
	}
}

func setupTeamReadGitRepo(t *testing.T, f *teamProposalFixture) (*gitops.Manager, string) {
	t.Helper()
	ctx := t.Context()
	wsDir := f.root
	gm, err := gitops.NewManager(gitBinary(t), f.hp.deps.DataDir, f.clk)
	if err != nil {
		t.Fatal(err)
	}
	gitExec(t, wsDir, exec.CommandContext(ctx, "git", "init"))
	gitExec(t, wsDir, exec.CommandContext(ctx, "git", "config", "user.email", "test@example.com"))
	gitExec(t, wsDir, exec.CommandContext(ctx, "git", "config", "user.name", "Test"))
	if writeErr := os.WriteFile(filepath.Join(wsDir, "README.md"), []byte("init\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	gitExec(t, wsDir, exec.CommandContext(ctx, "git", "add", "README.md"))
	gitExec(t, wsDir, exec.CommandContext(ctx, "git", "commit", "-m", "init"))
	baseBranch, err := gm.CurrentBranch(ctx, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	return gm, baseBranch
}

func seedTeamReadDiffBranches(t *testing.T, f *teamProposalFixture, gm *gitops.Manager, teamID, baseBranch string) string {
	t.Helper()
	ctx := t.Context()
	wsDir := f.root
	teamBranch, err := gitops.TeamBranchName(teamID)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := gm.CreateTeamBranch(ctx, wsDir, teamID, baseBranch); branchErr != nil {
		t.Fatalf("CreateTeamBranch: %v", branchErr)
	}
	teamWT, err := gm.AddTeamWorktree(ctx, f.hp.deps.DataDir, wsDir, repoIDFor(wsDir), teamID, teamBranch)
	if err != nil {
		t.Fatalf("AddTeamWorktree: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(teamWT, "team.txt"), []byte("team change\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, commitErr := gm.CommitWorktree(ctx, teamWT, "team diff"); commitErr != nil {
		t.Fatalf("CommitWorktree(team): %v", commitErr)
	}
	taskAID := durableTaskID(teamID, teamTestTaskA)
	taskWT, err := gm.CreateTaskWorktree(ctx, f.hp.deps.DataDir, wsDir, repoIDFor(wsDir), gitTaskID(taskAID), teamBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	bigPayload := strings.Repeat("x", (260<<10)) + "\n"
	if writeErr := os.WriteFile(filepath.Join(taskWT, "big.txt"), []byte(bigPayload), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, commitErr := gm.CommitWorktree(ctx, taskWT, "big diff"); commitErr != nil {
		t.Fatalf("CommitWorktree(task): %v", commitErr)
	}
	return taskAID
}

func TestListTeamsAndGetTeamTreeAndLockedVault(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTeamProposalFixture(t, "")
	gm, baseBranch := setupTeamReadGitRepo(t, f)

	if _, propErr := f.rt.teamProposals.Propose(ctx, f.session.ID, validTeamProposalArgs()); propErr != nil {
		t.Fatalf("Propose: %v", propErr)
	}
	pending, ok, err := f.hp.vendorStore.PendingTeamForSession(ctx, f.session.ID)
	if err != nil || !ok {
		t.Fatalf("PendingTeamForSession: ok=%v err=%v", ok, err)
	}
	f.rt.tasksService.SetWorktreePruner(f.hp.rt.workspaces.domain, gm, f.hp.deps.DataDir)
	taskAID := seedTeamReadDiffBranches(t, f, gm, pending.TeamID, baseBranch)

	assertTeamReadUnlocked(t, f, pending.TeamID, taskAID)
	assertTeamReadEditedRoleModel(t, f, pending.TeamID, taskAID)
	f.hp.vault.Lock()
	assertTeamReadLocked(t, f, pending.TeamID)
}

func assertTeamReadEditedRoleModel(t *testing.T, f *teamProposalFixture, teamID, taskAID string) {
	t.Helper()
	edit, err := json.Marshal(teams.EditSet{
		Version: teams.EditSetVersion,
		Roles:   []teams.RoleEdit{{Name: teamTestRole, Model: teamTestModelEdited}},
	})
	if err != nil {
		t.Fatalf("marshal edit set: %v", err)
	}
	if ansErr := f.answerCard(t, "decide-read-edited", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(edit)); ansErr != nil {
		t.Fatalf("approve with edits: %v", ansErr)
	}
	view, err := f.rt.tasksService.GetTeam(t.Context(), teamID, "")
	if err != nil {
		t.Fatalf("GetTeam after approve_edited: %v", err)
	}
	for i := range view.Tasks {
		if view.Tasks[i].TaskID == taskAID {
			if view.Tasks[i].Model != teamTestModelEdited {
				t.Fatalf("task %s model = %q, want %q", taskAID, view.Tasks[i].Model, teamTestModelEdited)
			}
			return
		}
	}
	t.Fatalf("task %s not found after approve_edited", taskAID)
}

func assertTeamReadUnlocked(t *testing.T, f *teamProposalFixture, teamID, taskAID string) {
	t.Helper()
	ctx := t.Context()
	svc := f.rt.tasksService
	summaries, err := svc.ListTeams(ctx, f.wsID, f.session.ID)
	if err != nil || len(summaries) != 1 || summaries[0].TeamID != teamID {
		t.Fatalf("ListTeams = (%+v, %v), want [%s]", summaries, err, teamID)
	}
	view, err := svc.GetTeam(ctx, teamID, teamDiffTargetAll)
	if err != nil {
		t.Fatalf("GetTeam(@team): %v", err)
	}
	if view.VaultLocked || view.Title == "" || view.Brief == "" || view.PendingRequestID == "" {
		t.Fatalf("unexpected unlocked GetTeam header: locked=%v title=%q brief=%q req=%q",
			view.VaultLocked, view.Title, view.Brief, view.PendingRequestID)
	}
	if !strings.Contains(view.DiffText, "team change") || view.DiffTruncated {
		t.Fatalf("unexpected @team diff: truncated=%v len=%d", view.DiffTruncated, len(view.DiffText))
	}
	assertTaskAHasSealedFields(t, view.Tasks, taskAID)
	assertTeamTaskDiffTruncated(t, svc, teamID, taskAID)
}

func assertTeamTaskDiffTruncated(t *testing.T, svc *TasksService, teamID, taskAID string) {
	t.Helper()
	taskView, err := svc.GetTeam(t.Context(), teamID, taskAID)
	if err != nil {
		t.Fatalf("GetTeam(taskA): %v", err)
	}
	if !taskView.DiffTruncated || len(taskView.DiffText) != maxTeamDiffBytes {
		t.Fatalf("expected truncated 256 KiB diff, got truncated=%v len=%d", taskView.DiffTruncated, len(taskView.DiffText))
	}
}

func assertTaskAHasSealedFields(t *testing.T, tasks []TaskNodeView, taskAID string) {
	t.Helper()
	for i := range tasks {
		if tasks[i].TaskID == taskAID {
			if tasks[i].Goal == "" || len(tasks[i].WriteScope) == 0 {
				t.Fatalf("expected sealed Goal and WriteScope when unlocked: %+v", tasks[i])
			}
			return
		}
	}
	t.Fatalf("task %s not found in GetTeam", taskAID)
}

func assertTeamReadLocked(t *testing.T, f *teamProposalFixture, teamID string) {
	t.Helper()
	ctx := t.Context()
	svc := f.rt.tasksService
	lockedList, err := svc.ListTeams(ctx, "", "")
	if err != nil || len(lockedList) != 1 {
		t.Fatalf("ListTeams while locked = (%d, %v), want 1", len(lockedList), err)
	}
	lockedView, err := svc.GetTeam(ctx, teamID, teamDiffTargetAll)
	if err != nil {
		t.Fatalf("GetTeam while locked: %v", err)
	}
	if !lockedView.VaultLocked || lockedView.Title != "" || lockedView.Brief != "" || lockedView.Budget != nil || lockedView.DiffText != "" {
		t.Fatalf("sealed fields leaked while vault locked: locked=%v title=%q brief=%q diff=%d",
			lockedView.VaultLocked, lockedView.Title, lockedView.Brief, len(lockedView.DiffText))
	}
	assertTasksLockedMetadataOnly(t, lockedView.Tasks)
}

func assertTasksLockedMetadataOnly(t *testing.T, tasks []TaskNodeView) {
	t.Helper()
	if len(tasks) == 0 {
		t.Fatal("expected metadata TaskNodes while vault locked")
	}
	for i := range tasks {
		if tasks[i].Goal != "" || len(tasks[i].WriteScope) != 0 {
			t.Fatalf("task %s leaked sealed fields while locked: %+v", tasks[i].TaskID, tasks[i])
		}
	}
}

func TestRecoverTaskDecisionsDedupAndLockedVault(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	s := setupRecoveryDecisionsFixture(t, f)

	assertRecoverNotUnknownAndResumeDedup(t, f, &s)
	assertRecoverKeepDiffCancelAndLocked(t, f, &s)
}

func assertRecoverNotUnknownAndResumeDedup(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	svc := f.rt.tasksService
	_, err := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: "rec-not-unknown", TeamID: f.teamID, TaskID: f.managerTaskID, Decision: recoveryDecisionCancel,
	})
	if errcode.CodeOf(err) != errcode.TasksTaskNotUnknown {
		t.Fatalf("RecoverTask on running task code = %q, want %q (err=%v)", errcode.CodeOf(err), errcode.TasksTaskNotUnknown, err)
	}
	resumed, err := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: testRecoverCmdResume, TeamID: f.teamID, TaskID: s.tDetached, Decision: recoveryDecisionResume, Reason: "continue",
	})
	if err != nil || resumed.State != string(vendorstore.TaskStateRunning) {
		t.Fatalf("RecoverTask(resume) = (%+v, %v)", resumed, err)
	}
	replayed, err := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: testRecoverCmdResume, TeamID: f.teamID, TaskID: s.tDetached, Decision: recoveryDecisionResume, Reason: "continue",
	})
	if err != nil || replayed.TaskID != resumed.TaskID || replayed.State != resumed.State {
		t.Fatalf("replayed RecoverTask = (%+v, %v), want (%+v, nil)", replayed, err, resumed)
	}
	if _, conflictErr := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: testRecoverCmdResume, TeamID: f.teamID, TaskID: s.tCancel, Decision: recoveryDecisionCancel,
	}); errcode.CodeOf(conflictErr) != errcode.InvalidArgument {
		t.Fatalf("conflicting command_id code = %q, want invalid_argument", errcode.CodeOf(conflictErr))
	}
}

func assertRecoverKeepDiffCancelAndLocked(t *testing.T, f *activeTeamFixture, s *recoveryDecisionSetup) {
	t.Helper()
	ctx := t.Context()
	svc := f.rt.tasksService
	kept, err := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: "rec-keep-1", TeamID: f.teamID, TaskID: s.tKeepDiff, Decision: recoveryDecisionKeepDiff,
	})
	if err != nil || kept.State != string(vendorstore.TaskStateReview) {
		t.Fatalf("RecoverTask(keep_diff) = (%+v, %v)", kept, err)
	}
	cancelled, err := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: "rec-cancel-1", TeamID: f.teamID, TaskID: s.tCancel, Decision: recoveryDecisionCancel, Reason: "abort",
	})
	if err != nil || cancelled.State != string(vendorstore.TaskStateCancelled) {
		t.Fatalf("RecoverTask(cancel) = (%+v, %v)", cancelled, err)
	}
	f.hp.vault.Lock()
	if _, lockedErr := svc.RecoverTask(ctx, &RecoverTaskCommand{
		CommandID: "rec-locked", TeamID: f.teamID, TaskID: s.tRespawn, Decision: recoveryDecisionCancel,
	}); errcode.CodeOf(lockedErr) != errcode.Unavailable {
		t.Fatalf("RecoverTask while locked code = %q, want unavailable", errcode.CodeOf(lockedErr))
	}
}
