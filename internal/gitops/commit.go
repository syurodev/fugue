package gitops

import (
	"context"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// CommitResult reports what CommitWorktree did.
type CommitResult struct {
	// Committed is false when the worktree had no changes to capture; the
	// task branch head then stays at the parent's commit.
	Committed bool
	// SHA is the resulting commit; empty when Committed is false.
	SHA string
}

// maxSubjectBytes bounds the result commit subject.
const maxSubjectBytes = 200

// CommitWorktree stages everything in the task worktree (tracked changes and
// untracked files, respecting .gitignore) and creates one commit on the task
// branch (orchestration §9). With no changes it reports Committed=false and
// does not fail. The user's identity and signing configuration are used as
// configured; a commit they refuse is a typed error.
func (m *Manager) CommitWorktree(ctx context.Context, worktreeDir, subject string) (CommitResult, error) {
	subject, err := commitSubject(subject)
	if err != nil {
		return CommitResult{}, err
	}
	var result CommitResult
	err = m.withRepoLock(ctx, worktreeDir, func() error {
		if _, addErr := m.run(ctx, worktreeDir, "add", "-A"); addErr != nil {
			return addErr
		}
		staged, stagedErr := m.hasStagedChanges(ctx, worktreeDir)
		if stagedErr != nil {
			return stagedErr
		}
		if !staged {
			return nil
		}
		if _, commitErr := m.run(ctx, worktreeDir, "commit", "-m", subject); commitErr != nil {
			return commitErr
		}
		sha, headErr := m.run(ctx, worktreeDir, "rev-parse", "HEAD")
		if headErr != nil {
			return headErr
		}
		result = CommitResult{Committed: true, SHA: strings.TrimSpace(sha)}
		return nil
	})
	return result, err
}

// hasStagedChanges reports whether the index differs from HEAD. Exit 1 of
// diff --quiet means differences; anything above is a failure.
func (m *Manager) hasStagedChanges(ctx context.Context, worktreeDir string) (bool, error) {
	res, err := m.exec(ctx, worktreeDir, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, m.gitFailure("diff", res)
	}
}

// commitSubject validates the commit subject: one trimmed, non-empty line
// within the size bound. A subject is host-generated text, so a malformed one
// is a caller bug, not a git problem.
func commitSubject(subject string) (string, error) {
	trimmed := strings.TrimSpace(subject)
	switch {
	case trimmed == "":
		return "", errcode.New(errcode.InvalidArgument, "gitops: commit subject is empty")
	case len(trimmed) > maxSubjectBytes:
		return "", errcode.New(errcode.InvalidArgument, "gitops: commit subject exceeds the size limit")
	case strings.ContainsAny(trimmed, "\n\r"):
		return "", errcode.New(errcode.InvalidArgument, "gitops: commit subject must be one line")
	}
	return trimmed, nil
}
