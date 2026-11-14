package gitops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The failure kinds behind a GitError. Each maps to one public error code,
// one public message that never repeats git's raw stderr (which can contain
// repository paths), and an optional sentinel callers can match.
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

// code returns the public error code for the kind.
func (k failureKind) code() errcode.Code {
	switch k {
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

// message returns the public text for the kind; it names only the operation.
func (k failureKind) message(op string) string {
	switch k {
	case failureGit:
		return fmt.Sprintf("gitops: %s failed", op)
	case failureIdentity:
		return fmt.Sprintf("gitops: %s failed: no usable git identity or signing key", op)
	case failureSigning:
		return fmt.Sprintf("gitops: %s failed: signature creation failed", op)
	case failureRefLock:
		return fmt.Sprintf("gitops: %s failed: %v", op, ErrRefLockExhausted)
	case failureMissingRef:
		return fmt.Sprintf("gitops: %s failed: unknown branch or commit", op)
	default:
		return fmt.Sprintf("gitops: %s failed", op)
	}
}

// sentinel returns the error kind callers can match with errors.Is.
func (k failureKind) sentinel() error {
	if k == failureRefLock {
		return ErrRefLockExhausted
	}
	return nil
}

// Sentinel errors callers can match with errors.Is.
var (
	// ErrRefLockExhausted is returned when a ref-lock error survives every retry.
	ErrRefLockExhausted = errors.New("gitops: ref lock stayed busy after retries")
	// ErrNoCommit is returned by CommitWorktree when the worktree had nothing to commit.
	ErrNoCommit = errors.New("gitops: no changes to commit")
)

// GitError reports a failed git operation. Error names only the operation and
// the failure kind: git stderr is kept in Detail for the host's logs, because
// it can contain repository paths and is untrusted output. Unwrap reaches an
// *errcode.Error carrying the stable code, so errcode.CodeOf and Expose see
// the typed code, and the kind's sentinel behind it.
type GitError struct {
	Op     string
	Kind   failureKind
	Detail string

	coded *errcode.Error
}

// newGitError builds the typed error for one failure kind.
func newGitError(op string, kind failureKind, detail string) *GitError {
	return &GitError{
		Op:     op,
		Kind:   kind,
		Detail: detail,
		coded:  errcode.Wrap(kind.code(), kind.message(op), kind.sentinel()),
	}
}

// Error implements error without git's raw stderr.
func (e *GitError) Error() string { return e.Kind.message(e.Op) }

// Unwrap reaches the coded error (and, through it, the kind's sentinel), so
// errcode.CodeOf, errcode.Expose and errors.Is all see the stable code.
func (e *GitError) Unwrap() error { return e.coded }

// Code returns the public error code for the failure.
func (e *GitError) Code() errcode.Code { return e.Kind.code() }

// gitFailure turns a non-zero git exit into a typed GitError, classifying
// identity and signing problems on the way.
func (m *Manager) gitFailure(op string, res gitResult) *GitError {
	return newGitError(op, classify(res.Stderr), trimDetail(res.Stderr))
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
