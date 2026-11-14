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
// The target is resolved strictly as a local branch, so a tag sharing the
// branch's name never satisfies the query, and a missing branch is a typed
// not-found error rather than "not contained".
func (m *Manager) BranchContainsCommit(ctx context.Context, repoDir, branch, commit string) (bool, error) {
	if err := ValidateRefName(branch); err != nil {
		return false, err
	}
	if err := ValidateRev(commit); err != nil {
		return false, err
	}
	target, err := m.resolveBranchCommit(ctx, repoDir, branch)
	if err != nil {
		return false, err
	}
	res, err := m.exec(ctx, repoDir, "merge-base", "--is-ancestor", commit, target)
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

// resolveBranchCommit resolves branch strictly as a local branch and returns
// its head commit. --end-of-options plus the refs/heads/ prefix and the
// ^{commit} peel keep a same-named tag or remote ref from satisfying the
// lookup; a missing branch is a typed not-found error.
func (m *Manager) resolveBranchCommit(ctx context.Context, repoDir, branch string) (string, error) {
	res, err := m.exec(ctx, repoDir, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case 0:
		sha := strings.TrimSpace(res.Stdout)
		if sha == "" {
			return "", errcode.New(errcode.Internal, "gitops: rev-parse returned an empty branch hash")
		}
		return sha, nil
	default:
		// A branch that does not resolve is missing, whatever git's wording.
		missing := newGitError("rev-parse", failureMissingRef, trimDetail(res.Stderr))
		return "", missing
	}
}

// WorktreeHead returns the head commit of the branch checked out in the
// worktree behind worktreeDir. An unborn HEAD — the branch has no commits yet
// — reports an empty string; any other failure, such as a path that is not a
// repository, is a typed error.
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
	case 1:
		// The documented case: an unborn HEAD under --verify --quiet exits 1
		// without output, and nothing else does.
		return "", nil
	default:
		return "", m.gitFailure("rev-parse", res)
	}
}
