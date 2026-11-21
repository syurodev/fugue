package appstate

import "github.com/syurodev/fugue/internal/platform/errcode"

// The coded failures a caller can branch on. Document bytes never appear in
// a message: the envelope is described, not quoted.
var (
	// ErrInvalidDocument means the JSON document does not parse or its
	// schema_version is missing, not an integer, or below 1.
	ErrInvalidDocument = errcode.New(errcode.AppStateInvalidDocument, "the document is not valid JSON with an integer schema_version >= 1")
	// ErrLayoutTooLarge means a layout document exceeds MaxLayoutBytes.
	ErrLayoutTooLarge = errcode.New(errcode.AppStateTooLarge, "the layout exceeds the size limit")
	// ErrPreferencesTooLarge means a preferences document exceeds MaxPreferencesBytes.
	ErrPreferencesTooLarge = errcode.New(errcode.AppStateTooLarge, "the preferences exceed the size limit")
	// ErrTooManyWindows means the store already holds MaxWindows windows.
	ErrTooManyWindows = errcode.New(errcode.AppStateTooManyWindows, "at most 16 windows can be saved")
	// ErrWindowIDInvalid means a window_id is not a UUID (host-api §5.2).
	ErrWindowIDInvalid = errcode.New(errcode.InvalidArgument, "window_id must be a UUID")
	// ErrChangeCursorTooOld reports a catch-up from a cursor older than the
	// oldest retained journal row. The topic layer maps it to the hidden
	// resync signal, so no client sees this error raw.
	ErrChangeCursorTooOld = errcode.New(errcode.FailedPrecondition, "the app-state change cursor is older than the retained journal")
)

// ConflictError reports a CAS mismatch: expected did not match the stored
// revision. Current is the revision the document carries now (0 when the
// window or the preferences are absent), so the caller can retry against it;
// the transport attaches it to the typed detail (host-api §5.2).
type ConflictError struct {
	err     error
	current uint64
}

func newConflict(current uint64) *ConflictError {
	return &ConflictError{
		err:     errcode.New(errcode.AppStateRevisionConflict, "the document was changed by another writer"),
		current: current,
	}
}

// Error implements error.
func (e *ConflictError) Error() string { return e.err.Error() }

// Unwrap returns the coded error.
func (e *ConflictError) Unwrap() error { return e.err }

// CurrentRevision returns the revision the document carries now.
func (e *ConflictError) CurrentRevision() uint64 { return e.current }
