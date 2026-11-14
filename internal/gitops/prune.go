package gitops

import (
	"context"
	"os"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// DefaultRetention is how long a team's worktrees and branches stay after the
// team ended (orchestration §9: 24 hours).
const DefaultRetention = 24 * time.Hour

// PruneTask names one task whose team has ended, with the ended-at time the
// caller read from the store; gitops never reads the store itself.
type PruneTask struct {
	TaskID      string
	TeamEndedAt time.Time
}

// PruneSpec describes one pruning pass over one repository.
type PruneSpec struct {
	// Root is the data directory whose worktrees directory is cleaned.
	Root string
	// RepoID is the repository id segment under <root>/worktrees.
	RepoID string
	// RepoDir locates the repository (main worktree or bare) whose refs are
	// cleaned.
	RepoDir string
	// Retention is how long after TeamEndedAt a task's worktree and branch
	// are kept; zero uses DefaultRetention.
	Retention time.Duration
	// Tasks lists the ended team's tasks with their ended-at times.
	Tasks []PruneTask
}

// PruneResult reports what one pruning pass removed.
type PruneResult struct {
	// RemovedWorktrees are the task worktree paths removed.
	RemovedWorktrees []string
	// RemovedBranches are the task branches deleted.
	RemovedBranches []string
	// RemovedAdminEntries counts stale worktree admin entries git pruned.
	RemovedAdminEntries int
}

// Prune removes the task worktrees and branches whose team ended more than
// the retention duration ago and runs git worktree prune for stale admin
// entries (orchestration §9, the fugue task prune primitive). The caller
// supplies the ended-at times; the injected clock supplies now. A nil spec is
// a typed error; spec.Root must be absolute and is resolved once for both the
// existence check and the worktree removal.
func (m *Manager) Prune(ctx context.Context, spec *PruneSpec) (PruneResult, error) {
	if spec == nil {
		return PruneResult{}, errcode.New(errcode.InvalidArgument, "gitops: prune spec is nil")
	}
	root, rootErr := resolveRoot(spec.Root)
	if rootErr != nil {
		return PruneResult{}, rootErr
	}
	retention, err := pruneRetention(spec.Retention)
	if err != nil {
		return PruneResult{}, err
	}
	if idErr := validateTaskIDs(spec.Tasks); idErr != nil {
		return PruneResult{}, idErr
	}
	cutoff := m.clk.Now().Add(-retention)
	var result PruneResult
	err = m.withRepoLock(ctx, spec.RepoDir, func() error {
		// Stale admin entries go first: a vanished worktree directory still
		// holds its branch checked out as far as git is concerned, which
		// would refuse the branch deletion below.
		removed, pruneErr := m.pruneAdminEntries(ctx, spec.RepoDir)
		if pruneErr != nil {
			return pruneErr
		}
		result.RemovedAdminEntries = removed
		for _, task := range spec.Tasks {
			if task.TeamEndedAt.After(cutoff) {
				continue
			}
			if taskErr := m.pruneTask(ctx, spec.RepoDir, root, spec.RepoID, task, &result); taskErr != nil {
				return taskErr
			}
		}
		return nil
	})
	return result, err
}

// pruneRetention resolves the effective retention: zero means the default.
func pruneRetention(retention time.Duration) (time.Duration, error) {
	switch {
	case retention == 0:
		return DefaultRetention, nil
	case retention < 0:
		return 0, errcode.New(errcode.InvalidArgument, "gitops: retention is negative")
	default:
		return retention, nil
	}
}

// validateTaskIDs checks every task id before anything is removed.
func validateTaskIDs(tasks []PruneTask) error {
	for _, task := range tasks {
		if err := ValidateID("task", task.TaskID); err != nil {
			return err
		}
	}
	return nil
}

// pruneTask removes one expired task's worktree and branch. A worktree
// directory that is already gone is left to the admin-entry prune; a missing
// branch is not an error.
func (m *Manager) pruneTask(ctx context.Context, repoDir, root, repoID string, task PruneTask, result *PruneResult) error {
	branch, err := TaskBranchName(task.TaskID)
	if err != nil {
		return err
	}
	wtPath, err := TaskWorktreePath(root, repoID, task.TaskID)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(wtPath); statErr == nil {
		if _, rmErr := m.run(ctx, repoDir, "worktree", "remove", "--force", wtPath); rmErr != nil {
			return rmErr
		}
		result.RemovedWorktrees = append(result.RemovedWorktrees, wtPath)
	}
	if err := m.deleteBranch(ctx, repoDir, branch); err != nil {
		return err
	}
	result.RemovedBranches = append(result.RemovedBranches, branch)
	return nil
}

// pruneAdminEntries runs git worktree prune and reports how many stale admin
// entries it removed, so the caller can log the pass.
func (m *Manager) pruneAdminEntries(ctx context.Context, repoDir string) (int, error) {
	before, err := m.worktreeListRows(ctx, repoDir)
	if err != nil {
		return 0, err
	}
	if _, runErr := m.run(ctx, repoDir, "worktree", "prune", "--verbose"); runErr != nil {
		return 0, runErr
	}
	after, err := m.worktreeListRows(ctx, repoDir)
	if err != nil {
		return 0, err
	}
	return len(before) - len(after), nil
}
