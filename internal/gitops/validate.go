package gitops

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// isSafeID reports whether id matches the id rule: it starts with a letter or
// digit, continues with letters, digits, dots, underscores and hyphens, and
// stays under idMaxLength bytes. Everything else (slashes, spaces, unicode,
// leading dots and hyphens) is rejected so an id can appear both in a ref
// name, fugue/<kind>/<id>, and in a worktree path without escaping.
func isSafeID(id string) bool {
	if len(id) == 0 || len(id) > idMaxLength {
		return false
	}
	for i := range len(id) {
		if !isIDChar(id[i], i == 0) {
			return false
		}
	}
	return true
}

// isIDChar reports whether c may appear at position first of an id.
func isIDChar(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
		return true
	case !first && (c == '.' || c == '_' || c == '-'):
		return true
	default:
		return false
	}
}

const (
	idMaxLength  = 100
	revMaxLength = 64
	revMinLength = 7
)

// ErrInvalidID is returned when a team, task or repository id fails
// validation; the callers own the ids and can correct them.
var ErrInvalidID = errors.New("gitops: id is not a safe ref or path component")

// ValidateID reports whether id can be used as a ref and path component.
// Beyond the shape rules it rejects ".." (dot-dot is invalid in a ref
// component and a path traversal) and a trailing ".lock" (a ref lock file).
func ValidateID(kind, id string) error {
	if !isSafeID(id) || strings.Contains(id, "..") ||
		strings.HasSuffix(id, ".") || strings.HasSuffix(id, ".lock") {
		return errcode.New(errcode.InvalidArgument, "gitops: invalid "+kind+" id")
	}
	return nil
}

// ValidateRefName reports whether name is safe to pass as a git ref argument:
// without it, a caller-supplied name such as "-C" or "--upload-pack" would be
// parsed as a flag. It implements the relevant rules of git-check-ref-format.
func ValidateRefName(name string) error {
	if strings.Contains(name, "/") {
		return slashComponentsProblem(name)
	}
	return simpleRefNameProblem(name)
}

// simpleRefNameProblem returns the violation of a slash-free ref name, if any.
func simpleRefNameProblem(name string) error {
	switch {
	case name == "":
		return errcode.New(errcode.InvalidArgument, "gitops: ref name is empty")
	case strings.HasPrefix(name, "-"):
		return errcode.New(errcode.InvalidArgument, "gitops: ref name starts with a dash")
	case strings.HasPrefix(name, ".") || strings.HasSuffix(name, "."):
		return errcode.New(errcode.InvalidArgument, "gitops: ref name begins or ends with a dot")
	case strings.Contains(name, ".."):
		return errcode.New(errcode.InvalidArgument, "gitops: ref name contains dot-dot")
	case strings.HasSuffix(name, ".lock"):
		return errcode.New(errcode.InvalidArgument, "gitops: ref name ends with .lock")
	case strings.Contains(name, "@{") || name == "@":
		return errcode.New(errcode.InvalidArgument, "gitops: ref name uses an at-expansion")
	case !isSafeID(name):
		return errcode.New(errcode.InvalidArgument, "gitops: ref name is invalid")
	default:
		return nil
	}
}

// slashComponentsProblem validates a slash-separated ref name componentwise.
func slashComponentsProblem(name string) error {
	for part := range strings.SplitSeq(name, "/") {
		if part == "" {
			return errcode.New(errcode.InvalidArgument, "gitops: ref name has an empty component")
		}
		if simpleRefNameProblem(part) != nil {
			return errcode.New(errcode.InvalidArgument, "gitops: ref name has an invalid component")
		}
	}
	return nil
}

// ValidateRev reports whether rev is safe to pass as a commit argument: a
// hexadecimal object name (short or full SHA-1 or SHA-256) or a validated ref
// name. A leading dash or whitespace would otherwise change the command.
func ValidateRev(rev string) error {
	if len(rev) >= revMinLength && len(rev) <= revMaxLength && isHex(rev) {
		return nil
	}
	return ValidateRefName(rev)
}

func isHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		digit := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		if !digit {
			return false
		}
	}
	return true
}

// TeamBranchName returns fugue/team/<team-id>.
func TeamBranchName(teamID string) (string, error) {
	if err := ValidateID("team", teamID); err != nil {
		return "", err
	}
	return TeamBranchPrefix + teamID, nil
}

// TaskBranchName returns fugue/task/<task-id>.
func TaskBranchName(taskID string) (string, error) {
	if err := ValidateID("task", taskID); err != nil {
		return "", err
	}
	return TaskBranchPrefix + taskID, nil
}

// TaskWorktreePath returns <root>/worktrees/<repo-id>/<task-id>, the worktree
// location of a task under the data directory (orchestration §9).
func TaskWorktreePath(root, repoID, taskID string) (string, error) {
	if err := ValidateID("repo", repoID); err != nil {
		return "", err
	}
	if err := ValidateID("task", taskID); err != nil {
		return "", err
	}
	if root == "" {
		return "", errcode.New(errcode.InvalidArgument, "gitops: data directory is empty")
	}
	return filepath.Join(root, worktreesDirName, repoID, taskID), nil
}
