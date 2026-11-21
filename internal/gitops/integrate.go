package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// RebaseResult reports the outcome of rebasing a task branch onto a target.
type RebaseResult struct {
	// Conflicted is true when the rebase hit merge conflicts and was cleanly
	// aborted; ConflictedFiles lists the unmerged paths.
	Conflicted      bool
	ConflictedFiles []string
	// HeadSHA is the resulting HEAD commit when Conflicted is false.
	HeadSHA string
}

// Rebase rebases the branch checked out in worktreeDir onto targetBranch
// through the per-repo mutex. Any conflict or failure aborts the rebase
// cleanly so no half-done rebase is left behind.
func (m *Manager) Rebase(ctx context.Context, worktreeDir, targetBranch string) (RebaseResult, error) {
	if err := ValidateRefName(targetBranch); err != nil {
		return RebaseResult{}, err
	}
	var out RebaseResult
	err := m.withRepoLock(ctx, worktreeDir, func() error {
		res, rebaseErr := m.rebaseWorktree(ctx, worktreeDir, "refs/heads/"+targetBranch)
		out = res
		return rebaseErr
	})
	return out, err
}

// RebaseOnto rebases commits after upstreamRev in worktreeDir onto
// newBaseBranch (orchestration §10.2 for re-parented tasks) through the
// per-repo mutex. Any conflict or failure aborts the rebase cleanly.
func (m *Manager) RebaseOnto(ctx context.Context, worktreeDir, newBaseBranch, upstreamRev string) (RebaseResult, error) {
	if err := ValidateRefName(newBaseBranch); err != nil {
		return RebaseResult{}, err
	}
	if err := ValidateRev(upstreamRev); err != nil {
		return RebaseResult{}, err
	}
	var out RebaseResult
	err := m.withRepoLock(ctx, worktreeDir, func() error {
		res, rebaseErr := m.rebaseWorktree(ctx, worktreeDir, "--onto", "refs/heads/"+newBaseBranch, upstreamRev)
		out = res
		return rebaseErr
	})
	return out, err
}

func (m *Manager) rebaseWorktree(ctx context.Context, worktreeDir string, args ...string) (RebaseResult, error) {
	cmdArgs := make([]string, 0, 1+len(args))
	cmdArgs = append(cmdArgs, "rebase")
	cmdArgs = append(cmdArgs, args...)
	res, err := m.exec(ctx, worktreeDir, cmdArgs...)
	if err != nil {
		if abortErr := m.abortRebaseIfActive(ctx, worktreeDir); abortErr != nil {
			return RebaseResult{}, errors.Join(err, abortErr)
		}
		return RebaseResult{}, err
	}
	if res.ExitCode != 0 {
		return m.handleRebaseFailure(ctx, worktreeDir, res)
	}
	sha, headErr := m.WorktreeHead(ctx, worktreeDir)
	if headErr != nil {
		return RebaseResult{}, headErr
	}
	return RebaseResult{HeadSHA: sha}, nil
}

func (m *Manager) handleRebaseFailure(ctx context.Context, worktreeDir string, res gitResult) (RebaseResult, error) {
	files, listErr := m.conflictedFiles(ctx, worktreeDir)
	abortErr := m.abortRebaseIfActive(ctx, worktreeDir)
	if listErr == nil && len(files) > 0 {
		if abortErr != nil {
			return RebaseResult{}, abortErr
		}
		return RebaseResult{Conflicted: true, ConflictedFiles: files}, nil
	}
	gitErr := m.gitFailure("rebase", res)
	if abortErr != nil {
		return RebaseResult{}, errors.Join(gitErr, abortErr)
	}
	return RebaseResult{}, gitErr
}

func (m *Manager) conflictedFiles(ctx context.Context, worktreeDir string) ([]string, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoWorktreeTimeout)
	defer cancel()
	out, err := m.run(cleanupCtx, worktreeDir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for line := range strings.SplitSeq(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

func (m *Manager) abortRebaseIfActive(ctx context.Context, worktreeDir string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), undoWorktreeTimeout)
	defer cancel()
	active, err := m.hasActiveRebase(cleanupCtx, worktreeDir)
	if err != nil || !active {
		return err
	}
	_, abortErr := m.run(cleanupCtx, worktreeDir, "rebase", "--abort")
	return abortErr
}

func (m *Manager) hasActiveRebase(ctx context.Context, worktreeDir string) (bool, error) {
	for _, marker := range []string{"rebase-merge", "rebase-apply"} {
		out, err := m.run(ctx, worktreeDir, "rev-parse", "--git-path", marker)
		if err != nil {
			return false, err
		}
		p := strings.TrimSpace(out)
		if !filepath.IsAbs(p) {
			p = filepath.Join(worktreeDir, p)
		}
		if _, statErr := os.Stat(p); statErr == nil {
			return true, nil
		}
	}
	return false, nil
}

// MergeBase returns the best common ancestor commit SHA of revA and revB.
func (m *Manager) MergeBase(ctx context.Context, repoDir, revA, revB string) (string, error) {
	if err := ValidateRev(revA); err != nil {
		return "", err
	}
	if err := ValidateRev(revB); err != nil {
		return "", err
	}
	out, err := m.run(ctx, repoDir, "merge-base", revA, revB)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", errcode.New(errcode.Internal, "gitops: merge-base returned an empty hash")
	}
	return sha, nil
}

// FastForwardBranch advances targetBranch to sourceBranch when targetBranch
// is an ancestor of sourceBranch, returning the resulting commit SHA. If
// targetBranch is checked out in a worktree (such as a manager's worktree),
// the fast-forward runs there so the index and working tree stay in sync;
// otherwise the branch ref is updated atomically with CAS update-ref.
func (m *Manager) FastForwardBranch(ctx context.Context, repoDir, targetBranch, sourceBranch string) (string, error) {
	if err := ValidateRefName(targetBranch); err != nil {
		return "", err
	}
	if err := ValidateRefName(sourceBranch); err != nil {
		return "", err
	}
	var mergedSHA string
	err := m.withRepoLock(ctx, repoDir, func() error {
		sha, ffErr := m.fastForwardUnderLock(ctx, repoDir, targetBranch, sourceBranch)
		if ffErr != nil {
			return ffErr
		}
		mergedSHA = sha
		return nil
	})
	return mergedSHA, err
}

func (m *Manager) fastForwardUnderLock(ctx context.Context, repoDir, targetBranch, sourceBranch string) (string, error) {
	targetSHA, err := m.resolveBranchCommit(ctx, repoDir, targetBranch)
	if err != nil {
		return "", err
	}
	sourceSHA, err := m.resolveBranchCommit(ctx, repoDir, sourceBranch)
	if err != nil {
		return "", err
	}
	res, err := m.exec(ctx, repoDir, "merge-base", "--is-ancestor", targetSHA, sourceSHA)
	if err != nil {
		return "", err
	}
	if res.ExitCode == 1 {
		return "", errcode.New(errcode.FailedPrecondition, "gitops: fast-forward requires rebase")
	}
	if res.ExitCode != 0 {
		return "", m.gitFailure("merge-base", res)
	}
	wtDir, err := m.worktreeForBranch(ctx, repoDir, targetBranch)
	if err != nil {
		return "", err
	}
	if wtDir != "" {
		if _, ffErr := m.run(ctx, wtDir, "merge", "--ff-only", "refs/heads/"+sourceBranch); ffErr != nil {
			return "", ffErr
		}
		return sourceSHA, nil
	}
	if _, upErr := m.run(ctx, repoDir, "update-ref", "refs/heads/"+targetBranch, sourceSHA, targetSHA); upErr != nil {
		return "", upErr
	}
	return sourceSHA, nil
}

func (m *Manager) worktreeForBranch(ctx context.Context, repoDir, branch string) (string, error) {
	out, err := m.run(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	wantRef := "refs/heads/" + branch
	var curPath string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			curPath = ""
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			if strings.TrimPrefix(line, "branch ") == wantRef {
				return curPath, nil
			}
		}
	}
	return "", nil
}
