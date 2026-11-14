package gitops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The failure kinds behind a GitError. Each maps to one public error code and
// one message that never repeats git's raw stderr, which can contain
// repository paths.
type failureKind int

const (
	// failureGit is a git invocation that failed for any unclassified reason.
	failureGit failureKind = iota
	// failureIdentity means the commit failed because the user's identity or
	// signing configuration is missing or refused the commit.
	failureIdentity
	// failureSigning means the commit failed because signature creation
	// failed under the user's signing configuration.
	failureSigning
	// failureRefLock means the ref (or index) lock stayed busy after the
	// configured retries.
	failureRefLock
	// failureMissingRef means the named branch or commit does not exist.
	failureMissingRef
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrRefLockExhausted is returned when a ref-lock error survives every retry.
	ErrRefLockExhausted = errors.New("gitops: ref lock stayed busy after retries")
	// ErrNoCommit is returned by CommitWorktree when the worktree had nothing to commit.
	ErrNoCommit = errors.New("gitops: no changes to commit")
)

// GitError reports a failed git operation. Error names only the operation and
// the failure kind: git stderr is kept in Detail for the host's logs, because
// it can contain repository paths and is untrusted output.
type GitError struct {
	Op     string
	Kind   failureKind
	Detail string
}

// Error implements error without git's raw stderr.
func (e *GitError) Error() string {
	switch e.Kind {
	case failureGit:
		return fmt.Sprintf("gitops: %s failed", e.Op)
	case failureIdentity:
		return fmt.Sprintf("gitops: %s failed: no usable git identity or signing key", e.Op)
	case failureSigning:
		return fmt.Sprintf("gitops: %s failed: signature creation failed", e.Op)
	case failureRefLock:
		return fmt.Sprintf("gitops: %s failed: %v", e.Op, ErrRefLockExhausted)
	case failureMissingRef:
		return fmt.Sprintf("gitops: %s failed: unknown branch or commit", e.Op)
	default:
		return fmt.Sprintf("gitops: %s failed", e.Op)
	}
}

// Unwrap reports the sentinel behind the failure, if any.
func (e *GitError) Unwrap() error {
	if e.Kind == failureRefLock {
		return ErrRefLockExhausted
	}
	return nil
}

// Code returns the public error code for the failure.
func (e *GitError) Code() errcode.Code {
	switch e.Kind {
	case failureGit, failureRefLock:
		return errcode.Unavailable
	case failureIdentity, failureSigning:
		return errcode.FailedPrecondition
	case failureMissingRef:
		return errcode.NotFound
	default:
		return errcode.Unavailable
	}
}

// gitFailure turns a non-zero git exit into a typed GitError, classifying
// identity and signing problems on the way.
func (m *Manager) gitFailure(op string, res gitResult) *GitError {
	return &GitError{
		Op:     op,
		Kind:   classify(res.Stderr),
		Detail: trimDetail(res.Stderr),
	}
}

// classify maps git's stderr (LC_ALL=C keeps the wording stable) to a failure
// kind. Everything unrecognised stays failureGit.
func classify(stderr string) failureKind {
	lower := strings.ToLower(stderr)
	switch {
	case containsAny(lower, "please tell me who you are", "committer identity unknown", "author identity unknown", "unable to auto-detect email address", "empty ident name"):
		return failureIdentity
	case containsAny(lower, "gpg failed to sign", "error: gpg", "cannot sign the commit", "ssh-keygen"):
		return failureSigning
	case containsAny(lower, "not a valid object name", "not a valid commit name", "bad revision", "malformed object name", "unknown revision", "not found"):
		return failureMissingRef
	default:
		return failureGit
	}
}

// containsAny reports whether s contains any of the markers.
func containsAny(s string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// trimDetail collapses git stderr to one bounded line for the log Detail.
func trimDetail(stderr string) string {
	detail := strings.Join(strings.Fields(stderr), " ")
	if len(detail) > maxErrorDetail {
		return detail[:maxErrorDetail]
	}
	return detail
}
