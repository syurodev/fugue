package gitops

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// DiffForFiles returns the unified diff between parentRef and HEAD in the task
// worktree, restricted to the given repo-relative paths.
func (m *Manager) DiffForFiles(ctx context.Context, dataDir, repoID, taskID, parentRef string, files []string) (string, error) {
	worktreeDir, err := m.prepareDiffWorktree(dataDir, repoID, taskID, parentRef)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	args := make([]string, 0, 6+len(files))
	args = append(args, "diff", "--no-ext-diff", "--no-textconv", parentRef+"...HEAD", "--")
	args = append(args, files...)
	return m.run(ctx, worktreeDir, args...)
}

// BranchHead returns the commit SHA of branch in repoDir.
func (m *Manager) BranchHead(ctx context.Context, repoDir, branch string) (string, error) {
	if valErr := ValidateRev(branch); valErr != nil {
		return "", errcode.New(errcode.InvalidArgument, "gitops: invalid branch")
	}
	var sha string
	err := m.withRepoLock(ctx, repoDir, func() error {
		commit, resErr := m.resolveBranchCommit(ctx, repoDir, branch)
		if resErr != nil {
			return resErr
		}
		sha = commit
		return nil
	})
	return sha, err
}

// CurrentBranch returns the checked-out branch name of repoDir.
func (m *Manager) CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	var branch string
	err := m.withRepoLock(ctx, repoDir, func() error {
		out, runErr := m.run(ctx, repoDir, "symbolic-ref", "--short", "HEAD")
		if runErr != nil {
			return runErr
		}
		branch = strings.TrimSpace(out)
		return nil
	})
	if err != nil {
		return "", err
	}
	return branch, nil
}

// HasUnmergedCommits reports whether sourceBranch has commits not reachable from targetBranch.
func (m *Manager) HasUnmergedCommits(ctx context.Context, repoDir, targetBranch, sourceBranch string) (bool, error) {
	if err := ValidateRev(targetBranch); err != nil {
		return false, errcode.New(errcode.InvalidArgument, "gitops: invalid target branch")
	}
	if err := ValidateRev(sourceBranch); err != nil {
		return false, errcode.New(errcode.InvalidArgument, "gitops: invalid source branch")
	}
	var count int
	err := m.withRepoLock(ctx, repoDir, func() error {
		exists, existsErr := m.branchExists(ctx, repoDir, sourceBranch)
		if existsErr != nil || !exists {
			return existsErr
		}
		out, runErr := m.run(ctx, repoDir, "rev-list", "--count", targetBranch+".."+sourceBranch)
		if runErr != nil {
			return runErr
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(out))
		if convErr != nil {
			return errcode.Wrap(errcode.Internal, "gitops: parse rev-list count", convErr)
		}
		count = n
		return nil
	})
	return count > 0, err
}

// DiffBetween returns the unified diff from baseRef to headRef in repoDir, truncated to limit bytes.
func (m *Manager) DiffBetween(ctx context.Context, repoDir, baseRef, headRef string, limit int) (DiffResult, error) {
	if err := ValidateRev(baseRef); err != nil {
		return DiffResult{}, errcode.New(errcode.InvalidArgument, "gitops: invalid base ref")
	}
	if err := ValidateRev(headRef); err != nil {
		return DiffResult{}, errcode.New(errcode.InvalidArgument, "gitops: invalid head ref")
	}
	if limit <= 0 {
		limit = maxDiffPageBytes
	}
	var raw string
	err := m.withRepoLock(ctx, repoDir, func() error {
		out, runErr := m.run(ctx, repoDir, "diff", "--no-ext-diff", "--no-textconv", baseRef+".."+headRef)
		if runErr != nil {
			return runErr
		}
		raw = out
		return nil
	})
	if err != nil {
		return DiffResult{}, err
	}
	total := len(raw)
	if total > limit {
		return DiffResult{Text: raw[:limit], TotalBytes: total, HasMore: true}, nil
	}
	return DiffResult{Text: raw, TotalBytes: total}, nil
}

// TeamWorktreePath returns the temporary integration worktree path for a team branch.
func TeamWorktreePath(root, repoID, teamID string) (string, error) {
	return TaskWorktreePath(root, repoID, "team-"+teamID)
}

// AddTeamWorktree checks out an existing team branch into a temporary worktree
// for final rebase and gates 1-4 verification before fast-forward merge.
func (m *Manager) AddTeamWorktree(ctx context.Context, root, repoDir, repoID, teamID, branch string) (string, error) {
	wtPath, err := TeamWorktreePath(root, repoID, teamID)
	if err != nil {
		return "", err
	}
	if valErr := ValidateRev(branch); valErr != nil {
		return "", errcode.New(errcode.InvalidArgument, "gitops: invalid team branch")
	}
	err = m.withRepoLock(ctx, repoDir, func() error {
		if mkErr := os.MkdirAll(filepath.Dir(wtPath), 0o700); mkErr != nil {
			return errcode.Wrap(errcode.Internal, "gitops: create team worktree parent", mkErr)
		}
		_, runErr := m.run(ctx, repoDir, "worktree", "add", wtPath, branch)
		return runErr
	})
	if err != nil {
		return "", err
	}
	return wtPath, nil
}

// RemoveTeamWorktree removes the temporary team integration worktree without
// deleting the team branch itself.
func (m *Manager) RemoveTeamWorktree(ctx context.Context, repoDir, root, repoID, teamID string) error {
	wtPath, err := TeamWorktreePath(root, repoID, teamID)
	if err != nil {
		return err
	}
	return m.withRepoLock(ctx, repoDir, func() error {
		if _, statErr := os.Stat(wtPath); os.IsNotExist(statErr) {
			return nil
		}
		if _, runErr := m.run(ctx, repoDir, "worktree", "remove", "--force", wtPath); runErr != nil {
			return runErr
		}
		if _, pruneErr := m.run(ctx, repoDir, "worktree", "prune"); pruneErr != nil {
			return pruneErr
		}
		return nil
	})
}
