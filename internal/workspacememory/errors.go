package workspacememory

import "github.com/syurodev/fugue/internal/platform/errcode"

// RevisionConflictError reports a CAS mismatch and returns the current entry
// so callers can inspect it before retrying (§3).
type RevisionConflictError struct {
	err     error
	current Entry
}

func newRevisionConflict(current Entry) *RevisionConflictError {
	return &RevisionConflictError{
		err:     errcode.New(errcode.MemoryRevisionConflict, "memory entry revision conflict"),
		current: current,
	}
}

// Error implements error.
func (e *RevisionConflictError) Error() string { return e.err.Error() }

// Unwrap returns the coded error so errcode.CodeOf sees MemoryRevisionConflict.
func (e *RevisionConflictError) Unwrap() error { return e.err }

// CurrentEntry returns the latest persisted entry with freshly evaluated status.
func (e *RevisionConflictError) CurrentEntry() Entry { return e.current }

// CurrentRevision returns the entry's current revision for transport error details.
func (e *RevisionConflictError) CurrentRevision() uint64 { return e.current.Revision }

func errInvalidArg(msg string) error {
	return errcode.New(errcode.InvalidArgument, msg)
}

func errNotFound(msg string) error {
	return errcode.New(errcode.NotFound, msg)
}

func errContainsSecret(kind string) error {
	return errcode.New(errcode.MemoryContainsSecret, "memory entry contains a detected secret ("+kind+")")
}

func errTooLarge(msg string) error {
	return errcode.New(errcode.MemoryTooLarge, msg)
}

func errLanguage(msg string) error {
	return errcode.New(errcode.MemoryLanguage, msg)
}

func errLabelTaken() error {
	return errcode.New(errcode.MemoryLabelTaken, "a valid memory entry with this label already exists in the workspace")
}

func errRateLimited() error {
	return errcode.New(errcode.MemoryRateLimited, "workspace memory write rate limit exceeded for this session")
}

func errLimitReached() error {
	return errcode.New(errcode.MemoryLimitReached, "workspace memory valid entry limit reached")
}
