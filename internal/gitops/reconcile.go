package gitops

import (
	"context"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// BranchContainsCommit reports whether commit is an ancestor of branch, the
// reconciliation query behind the restart note on F11.4 (OR-15): the F11.5
// merge queue uses it after a host restart to confirm an integrating outcome
// instead of re-merging. The query reads refs only, so it takes no repo lock.
// A missing branch or commit is a typed not-found error.
func (m *Manager) BranchContainsCommit(ctx context.Context, repoDir, branch, commit string) (bool, error) {
	if err := ValidateRefName(branch); err != nil {
		return false, err
	}
	if err := ValidateRev(commit); err != nil {
		return false, err
	}
	res, err := m.exec(ctx, repoDir, "merge-base", "--is-ancestor", commit, branch)
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, m.gitFailure("merge-base", res)
	}
}

// WorktreeHead returns the head commit of the branch checked out in the
// worktree behind worktreeDir, or an empty string when the branch has no
// commits yet (an unborn HEAD exits 1 under --verify --quiet).
func (m *Manager) WorktreeHead(ctx context.Context, worktreeDir string) (string, error) {
	res, err := m.exec(ctx, worktreeDir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case 0:
		sha := strings.TrimSpace(res.Stdout)
		if sha == "" {
			return "", errcode.New(errcode.Internal, "gitops: rev-parse returned an empty head hash")
		}
		return sha, nil
	case 1, 128:
		return "", nil
	default:
		return "", m.gitFailure("rev-parse", res)
	}
}
