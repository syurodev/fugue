package app

import (
	"context"
	"sort"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

// PruneWorktreesResult reports the task worktrees, task branches, and stale
// git worktree admin entries removed by one prune pass (orchestration §9).
type PruneWorktreesResult struct {
	RemovedWorktrees    []string `json:"removed_worktrees,omitempty"`
	RemovedBranches     []string `json:"removed_branches,omitempty"`
	RemovedAdminEntries int      `json:"removed_admin_entries"`
}

// SetWorktreePruner configures the workspace, git, and data directory ports
// needed by PruneWorktrees.
func (s *TasksService) SetWorktreePruner(workspaces *workspace.Service, git *gitops.Manager, dataDir string) {
	s.workspaces = workspaces
	s.git = git
	s.dataDir = dataDir
}

type pruneRepoKey struct {
	repoDir string
	repoID  string
}

// PruneWorktrees removes ended-team task worktrees and branches whose team
// ended more than retention ago (zero uses gitops.DefaultRetention = 24h),
// leaving active, non-terminal, and within-retention teams untouched.
func (s *TasksService) PruneWorktrees(ctx context.Context, retention time.Duration) (PruneWorktreesResult, error) {
	if s.git == nil || s.store == nil || s.clk == nil {
		return PruneWorktreesResult{}, errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	if retention < 0 {
		return PruneWorktreesResult{}, errcode.New(errcode.InvalidArgument, "prune retention must not be negative")
	}
	effRetention := retention
	if effRetention == 0 {
		effRetention = gitops.DefaultRetention
	}
	cutoff := s.clk.Now().Add(-effRetention)
	grouped, keys, err := s.collectEndedTeamPruneTasks(ctx, cutoff)
	if err != nil {
		return PruneWorktreesResult{}, err
	}
	var out PruneWorktreesResult
	for _, key := range keys {
		res, pruneErr := s.git.Prune(ctx, &gitops.PruneSpec{
			Root:      s.dataDir,
			RepoID:    key.repoID,
			RepoDir:   key.repoDir,
			Retention: retention,
			Tasks:     grouped[key],
		})
		if pruneErr != nil {
			return out, boundaryError("prune team worktrees", pruneErr)
		}
		out.RemovedWorktrees = append(out.RemovedWorktrees, res.RemovedWorktrees...)
		out.RemovedBranches = append(out.RemovedBranches, res.RemovedBranches...)
		out.RemovedAdminEntries += res.RemovedAdminEntries
	}
	return out, nil
}

func (s *TasksService) collectEndedTeamPruneTasks(ctx context.Context, cutoff time.Time) (map[pruneRepoKey][]gitops.PruneTask, []pruneRepoKey, error) {
	teams, err := s.store.EndedTeams(ctx)
	if err != nil {
		return nil, nil, boundaryError("list ended teams for prune", err)
	}
	grouped := make(map[pruneRepoKey][]gitops.PruneTask)
	for i := range teams {
		rec := &teams[i]
		if rec.EndedAt <= 0 {
			continue
		}
		endedAt := time.UnixMilli(rec.EndedAt).UTC()
		if endedAt.After(cutoff) {
			continue
		}
		repoDir, repoID := resolveTeamRepo(ctx, s.workspaces, rec)
		if repoDir == "" {
			continue
		}
		_, tasks, snapErr := s.store.TeamSnapshot(ctx, rec.TeamID)
		if snapErr != nil {
			return nil, nil, boundaryError("read ended team tasks for prune", snapErr)
		}
		key := pruneRepoKey{repoDir: repoDir, repoID: repoID}
		for j := range tasks {
			grouped[key] = append(grouped[key], gitops.PruneTask{
				TaskID:      gitTaskID(tasks[j].TaskID),
				TeamEndedAt: endedAt,
			})
		}
	}
	keys := make([]pruneRepoKey, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].repoDir < keys[j].repoDir
	})
	return grouped, keys, nil
}
