package execstore

import (
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Sentinel errors returned by the execution store.
var (
	// ErrUnsafePragmas is returned when the database does not meet durability requirements.
	ErrUnsafePragmas = errcode.New(errcode.FailedPrecondition, "database pragmas do not satisfy durability requirements")

	// ErrStaleFence is returned when an admission token or queue revision does not match the active state.
	ErrStaleFence = errcode.New(errcode.FailedPrecondition, "stale fence: admission token or queue revision mismatch")

	// ErrStaleRevision is returned when a run or admission revision does not match expected state.
	ErrStaleRevision = errcode.New(errcode.FailedPrecondition, "stale revision: expected revision does not match stored state")

	// ErrReceiptConflict is returned when a command ID has conflicting content or metadata.
	ErrReceiptConflict = errcode.New(errcode.FailedPrecondition, "receipt conflict: command ID already exists with different binding or content")

	// ErrCodecVersionMismatch is returned when a receipt was produced under a different codec version.
	ErrCodecVersionMismatch = errcode.New(errcode.FailedPrecondition, "codec version mismatch")

	// ErrProtectedInputUnavailable is returned when protected input equality cannot be established.
	ErrProtectedInputUnavailable = errcode.New(errcode.FailedPrecondition, "protected input unavailable for deduplication")

	// ErrUnknownVersion is returned when an event has an unsupported schema or protocol version.
	ErrUnknownVersion = errcode.New(errcode.FailedPrecondition, "unsupported schema or protocol version")

	// ErrSequenceGap is returned when event sequences are not strictly contiguous.
	ErrSequenceGap = errcode.New(errcode.FailedPrecondition, "event sequence gap detected")

	// ErrCorruptHistory is returned when stored event records cannot be decoded or validated.
	ErrCorruptHistory = errcode.New(errcode.FailedPrecondition, "corrupt event history")

	// ErrStoreUnavailable is returned when the execution store or database cannot be accessed.
	ErrStoreUnavailable = errcode.New(errcode.Unavailable, "execution store is unavailable")

	// ErrNotCommitted is returned when an ambiguous commit is resolved as not committed.
	ErrNotCommitted = errcode.New(errcode.NotFound, "command was not committed")

	// ErrSessionPurging is returned when attempting to write to a session that is being purged.
	ErrSessionPurging = errcode.New(errcode.FailedPrecondition, "session is being purged")

	// ErrRunNotFound is returned when an expected run row does not exist.
	ErrRunNotFound = errcode.New(errcode.NotFound, "execution run not found")

	// ErrInvalidBatch is returned when a proposed event batch violates identity or sequence rules.
	ErrInvalidBatch = errcode.New(errcode.InvalidArgument, "invalid transition batch or sequence")
)
