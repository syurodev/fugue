package hostapi

import (
	"io"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Errors reported by the socket and the transport. They wrap public errcode
// codes so errcode.Expose can show them to a client.
var (
	// ErrSocketInUse means a live process answers the socket path; nothing was
	// unlinked. The data-directory lock decides who is alive, so this state is
	// a contradiction and fails closed.
	ErrSocketInUse = errcode.New(errcode.FailedPrecondition, "the socket path is already served by a live process")
	// ErrSocketPathNotSocket means the socket path exists and is not a socket;
	// it is never removed.
	ErrSocketPathNotSocket = errcode.New(errcode.FailedPrecondition, "the socket path exists and is not a socket")
	// ErrSocketDirectoryUntrusted means a socket directory is a symlink,
	// foreign-owned, has an unreportable owner or an unacceptable mode, or is
	// a missing directory fugue does not create.
	ErrSocketDirectoryUntrusted = errcode.New(errcode.PermissionDenied, "the socket directory is not trusted")
)

// closeQuietly closes c and drops the error. Read paths and teardown use it;
// a Close error on a write path must be handled (quality-gates §2).
func closeQuietly(c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		return
	}
}
