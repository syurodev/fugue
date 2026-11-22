package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type pruneSeedSpec struct {
	teamID     string
	taskID     string
	advanceBy  time.Duration
	settleTeam bool
}

// TestPruneWorktreesRespectsRetentionAndProtectsActiveTeams pins F11.11
// (orchestration §9): TasksService.PruneWorktrees removes ended-team task
// worktrees and branches only once team end + retention (24h default) has
// elapsed, leaving within-retention ended teams and active teams intact.
func TestPruneWorktreesRespectsRetentionAndProtectsActiveTeams(t *testing.T) {
	t.Parallel()
	f := newTeamFixture(t, "", true)
	initRepo(t, f.root)
	repoDir, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	baseHead, err := f.rt.teamTasks.git.WorktreeHead(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	specs := []pruneSeedSpec{
		{teamID: "prune-old", taskID: "t-prune-old", advanceBy: 0, settleTeam: true},
		{teamID: "prune-bound", taskID: "t-prune-bound", advanceBy: time.Hour, settleTeam: true},
		{teamID: "prune-fresh", taskID: "t-prune-fresh", advanceBy: time.Hour, settleTeam: true},
		{teamID: "prune-active", taskID: "t-prune-active", advanceBy: 0, settleTeam: false},
	}
	worktrees := seedPruneTeams(t, f, repoDir, baseHead, specs)

	// Now is +25h from prune-old, +24h from prune-bound, and +23h from prune-fresh.
	f.clk.Advance(23 * time.Hour)

	svc := f.hp.rt.tasksService
	if _, negErr := svc.PruneWorktrees(ctx, -time.Second); errcode.CodeOf(negErr) != errcode.InvalidArgument {
		t.Fatalf("negative retention err = %v, want invalid_argument", negErr)
	}

	res, err := svc.PruneWorktrees(ctx, 0)
	if err != nil {
		t.Fatalf("PruneWorktrees(0) = %v", err)
	}
	assertPrunedTasks(t, res, worktrees, []string{"t-prune-old", "t-prune-bound"})
	assertKeptTasks(t, res, worktrees, []string{"t-prune-fresh", "t-prune-active"})
}

func seedPruneTeams(t *testing.T, f *teamProposalFixture, repoDir, baseHead string, specs []pruneSeedSpec) map[string]string {
	t.Helper()
	worktrees := make(map[string]string, len(specs))
	for _, sp := range specs {
		worktrees[sp.taskID] = seedOnePruneTeam(t, f, repoDir, baseHead, sp)
	}
	return worktrees
}

func seedOnePruneTeam(t *testing.T, f *teamProposalFixture, repoDir, baseHead string, sp pruneSeedSpec) string {
	t.Helper()
	ctx := t.Context()
	if sp.advanceBy > 0 {
		f.clk.Advance(sp.advanceBy)
	}
	gm := f.rt.teamTasks.git
	if err := gm.CreateTeamBranch(ctx, repoDir, sp.teamID, baseHead); err != nil {
		t.Fatal(err)
	}
	teamBr, err := gitops.TeamBranchName(sp.teamID)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := gm.CreateTaskWorktree(ctx, f.hp.rt.teamTasks.dataDir, repoDir, repoIDFor(repoDir), sp.taskID, teamBr)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: sp.teamID, WorkspaceID: f.wsID, SessionID: "sess-" + sp.teamID,
		ProposedEventID: "ev-p-" + sp.teamID, ProposedPayload: []byte("{}"),
		Tasks: []vendorstore.NewTaskInput{{
			TaskID: sp.taskID, HierarchyRole: vendorstore.RoleLead, CatalogRole: roleLead,
			Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "tc-" + sp.taskID, ProtectedSpec: []byte("{}"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sp.settleTeam {
		if _, err := f.hp.vendorStore.SettleTeam(ctx, sp.teamID, &vendorstore.TeamSettleInput{
			ExpectedSeq: rec.LastSeq, EndEventID: "ev-end-" + sp.teamID, EndPayload: []byte("{}"),
		}); err != nil {
			t.Fatal(err)
		}
		return wt
	}
	if _, err := f.hp.vendorStore.AppendTeamEvent(ctx, sp.teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "ev-start-" + sp.teamID, EventType: vendorstore.EventTeamStarted, ProtectedPayload: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	return wt
}

func assertPrunedTasks(t *testing.T, res PruneWorktreesResult, worktrees map[string]string, tasks []string) {
	t.Helper()
	for _, taskID := range tasks {
		wt := worktrees[taskID]
		if !slices.Contains(res.RemovedWorktrees, wt) {
			t.Fatalf("RemovedWorktrees = %v, want %q", res.RemovedWorktrees, wt)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("expired worktree %q still exists: %v", wt, err)
		}
		br, err := gitops.TaskBranchName(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(res.RemovedBranches, br) {
			t.Fatalf("RemovedBranches = %v, want %q", res.RemovedBranches, br)
		}
	}
}

func assertKeptTasks(t *testing.T, res PruneWorktreesResult, worktrees map[string]string, tasks []string) {
	t.Helper()
	for _, taskID := range tasks {
		wt := worktrees[taskID]
		if slices.Contains(res.RemovedWorktrees, wt) {
			t.Fatalf("RemovedWorktrees unexpectedly removed %q", wt)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Fatalf("kept worktree %q missing: %v", wt, err)
		}
	}
}
