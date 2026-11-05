package coordinator

import (
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Sentinel errors returned by the coordinator.
var (
	// ErrCommitUnknown is returned when the outcome of a commit cannot be determined.
	ErrCommitUnknown = errcode.New(errcode.Unavailable, "commit outcome unknown; read-back required")

	// ErrCommitUnresolved is returned when an unknown commit cannot be resolved after retries.
	ErrCommitUnresolved = errcode.New(errcode.Unavailable, "commit outcome unresolved; dispatch halted")

	// ErrNoCapacity is returned when worker capacity is exhausted.
	ErrNoCapacity = errcode.New(errcode.Unavailable, "no worker capacity available")

	// ErrStaleLease is returned when a worker observation carries a stale fence, token, or incarnation.
	ErrStaleLease = errcode.New(errcode.FailedPrecondition, "stale worker lease: fence, token, or incarnation mismatch")

	// ErrRecoveryRequired is returned when an active run was inherited from an earlier incarnation.
	ErrRecoveryRequired = errcode.New(errcode.FailedPrecondition, "recovery required for active run from earlier incarnation")

	// ErrSessionStopped is returned when an operation is attempted on a stopped session.
	ErrSessionStopped = errcode.New(errcode.FailedPrecondition, "session coordinator is stopped")

	// ErrHandoffFailed is returned when worker slot handoff or release fails due to an invalid or stale reference.
	ErrHandoffFailed = errcode.New(errcode.FailedPrecondition, "slot handoff failed: invalid or already used reservation reference")

	// ErrOriginRequired is returned when a request carries no host-set origin.
	ErrOriginRequired = errcode.New(errcode.InvalidArgument, "request origin is required")

	// ErrInvalidAccept is returned when an AcceptRun lacks data that only the caller can supply.
	ErrInvalidAccept = errcode.New(errcode.InvalidArgument, "AcceptRun request is incomplete or invalid")

	// ErrObservationNotAllowed is returned when a worker observation carries a command that is not an observation.
	ErrObservationNotAllowed = errcode.New(errcode.InvalidArgument, "command is not an allowed worker observation")
)
