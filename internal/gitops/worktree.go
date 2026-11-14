package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// CreateTeamBranch creates the team branch fugue/team/<team-id> at base in the
// repository behind repoDir (orchestration §9). The branch is created without
// checking it out. An existing team branch is a typed error.
func (m *Manager) CreateTeamBranch(ctx context.Context, repoDir, teamID, base string) error {
	branch, err := TeamBranchName(teamID)
	if err != nil {
		return err
	}
	if err := ValidateRev(base); err != nil {
		return errcode.New(errcode.InvalidArgument, "gitops: invalid base commit")
	}
	return m.withRepoLock(ctx, repoDir, func() error {
		exists, existsErr := m.branchExists(ctx, repoDir, branch)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			return errcode.New(errcode.AlreadyExists, "gitops: team branch already exists")
		}
		if _, runErr := m.run(ctx, repoDir, "branch", branch, base); runErr != nil {
			return runErr
		}
		return nil
	})
}

// CreateTaskWorktree creates the task branch fugue/task/<task-id> from
// parentBranch and checks it out in a worktree at
// <root>/worktrees/<repo-id>/<task-id> (orchestration §9). It returns the
// worktree path. The caller passes the data directory, which must be absolute
// and is resolved once for both the existence check and the worktree add. When
// the add fails after git created the branch, what this call made is removed
// so a retry starts clean; a pre-existing branch is never deleted.
func (m *Manager) CreateTaskWorktree(ctx context.Context, root, repoDir, repoID, taskID, parentBranch string) (string, error) {
	branch, err := TaskBranchName(taskID)
	if err != nil {
		return "", err
	}
	if err := ValidateRefName(parentBranch); err != nil {
		return "", errcode.New(errcode.InvalidArgument, "gitops: invalid parent branch")
	}
	root, rootErr := resolveRoot(root)
	if rootErr != nil {
		return "", rootErr
	}
	wtPath, pathErr := TaskWorktreePath(root, repoID, taskID)
	if pathErr != nil {
		return "", pathErr
	}
	lockErr := m.withRepoLock(ctx, repoDir, func() error {
		return m.addTaskWorktree(ctx, repoDir, wtPath, branch, parentBranch)
	})
	if lockErr != nil {
		return "", lockErr
	}
	return wtPath, nil
}

// addTaskWorktree is the lock-held body of CreateTaskWorktree.
func (m *Manager) addTaskWorktree(ctx context.Context, repoDir, wtPath, branch, parentBranch string) error {
	exists, existsErr := m.branchExists(ctx, repoDir, branch)
	if existsErr != nil {
		return existsErr
	}
	if exists {
		return errcode.New(errcode.AlreadyExists, "gitops: task branch already exists")
	}
	if _, statErr := os.Lstat(wtPath); statErr == nil {
		return errcode.New(errcode.AlreadyExists, "gitops: task worktree path already exists")
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(wtPath), 0o700); mkdirErr != nil {
		return fmt.Errorf("gitops: create worktree parent directory: %w", mkdirErr)
	}
	if _, addErr := m.run(ctx, repoDir, "worktree", "add", "-b", branch, wtPath, parentBranch); addErr != nil {
		if undoErr := m.undoFailedWorktreeAdd(ctx, repoDir, wtPath, branch); undoErr != nil {
			return errors.Join(addErr, undoErr)
		}
		return addErr
	}
	return nil
}

// undoFailedWorktreeAdd removes the worktree and branch a failed worktree add
// may have left behind. The branch was verified absent under the repo lock
// before the add, so deleting it never touches a pre-existing one. Every step
// is best effort: the caller reports the original add failure alongside.
func (m *Manager) undoFailedWorktreeAdd(ctx context.Context, repoDir, wtPath, branch string) error {
	var undoErrs []error
	if _, removeErr := m.run(ctx, repoDir, "worktree", "remove", "--force", wtPath); removeErr != nil {
		// The worktree directory may be missing or half-built; the admin
		// entry prune is what clears it either way.
		if _, pruneErr := m.run(ctx, repoDir, "worktree", "prune"); pruneErr != nil {
			undoErrs = append(undoErrs, pruneErr)
		}
	}
	if deleteErr := m.deleteBranch(ctx, repoDir, branch); deleteErr != nil {
		undoErrs = append(undoErrs, deleteErr)
	}
	return errors.Join(undoErrs...)
}

// RemoveTaskWorktree removes the worktree of a task and deletes its branch
// fugue/task/<task-id>. A worktree directory that is already gone is treated
// as stale (its admin entry is pruned) and a missing branch is not an error,
// so removal is idempotent for cleanup paths.
func (m *Manager) RemoveTaskWorktree(ctx context.Context, repoDir, root, repoID, taskID string) error {
	branch, err := TaskBranchName(taskID)
	if err != nil {
		return err
	}
	root, rootErr := resolveRoot(root)
	if rootErr != nil {
		return rootErr
	}
	wtPath, pathErr := TaskWorktreePath(root, repoID, taskID)
	if pathErr != nil {
		return pathErr
	}
	return m.withRepoLock(ctx, repoDir, func() error {
		if _, statErr := os.Lstat(wtPath); statErr == nil {
			if _, rmErr := m.run(ctx, repoDir, "worktree", "remove", "--force", wtPath); rmErr != nil {
				return rmErr
			}
		}
		if _, pruneErr := m.run(ctx, repoDir, "worktree", "prune"); pruneErr != nil {
			return pruneErr
		}
		return m.deleteBranch(ctx, repoDir, branch)
	})
}

// branchExists reports whether a local branch exists in repoDir.
func (m *Manager) branchExists(ctx context.Context, repoDir, branch string) (bool, error) {
	res, err := m.exec(ctx, repoDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1, 128:
		return false, nil
	default:
		return false, m.gitFailure("rev-parse", res)
	}
}

// deleteBranch deletes a local branch. A missing branch is not an error so
// cleanup paths stay idempotent.
func (m *Manager) deleteBranch(ctx context.Context, repoDir, branch string) error {
	res, err := m.exec(ctx, repoDir, "branch", "-D", branch)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && classify(res.Stderr) != failureMissingRef {
		return m.gitFailure("branch", res)
	}
	return nil
}

// worktreeListRows returns the worktree paths git knows about.
func (m *Manager) worktreeListRows(ctx context.Context, repoDir string) ([]string, error) {
	out, err := m.run(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var rows []string
	for line := range strings.SplitSeq(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			rows = append(rows, strings.TrimSpace(path))
		}
	}
	return rows, nil
}
