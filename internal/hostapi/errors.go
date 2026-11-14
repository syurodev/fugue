package hostapi

import "github.com/syurodev/fugue/internal/platform/errcode"

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
	// ErrPeerCredentialDenied means the connection's peer uid differs from the
	// host process user or its credentials could not be read; the connection
	// is rejected before any request byte is read.
	ErrPeerCredentialDenied = errcode.New(errcode.PermissionDenied, "the connection is not from this process user")
	// ErrAlreadyHelloed means the connection already negotiated a hello; a
	// second hello on a leased connection is a typed rejection.
	ErrAlreadyHelloed = errcode.New(errcode.FailedPrecondition, "this connection already negotiated a hello")
	// ErrHelloFirst means the connection called an rpc before hello.
	ErrHelloFirst = errcode.New(errcode.PermissionDenied, "call hello before any other rpc")
	// ErrSessionPrincipalDenied means a connection that presented a session
	// token called an rpc outside the session tool service (host-api §7).
	ErrSessionPrincipalDenied = errcode.New(errcode.PermissionDenied, "the session principal may not call this rpc")
	// ErrSessionAttachDenied means a connection that already negotiated a
	// hello tried to attach a session token.
	ErrSessionAttachDenied = errcode.New(errcode.PermissionDenied, "this connection cannot attach a session")
	// ErrSessionAttachFirst means a session tool rpc arrived on a connection
	// that is not a session principal.
	ErrSessionAttachFirst = errcode.New(errcode.PermissionDenied, "only an attached session may call this rpc")
	// ErrVendorPeerDenied means the peer process belongs to a live vendor
	// session, or its process could not be identified; it never becomes a
	// local principal (roadmap Q-41 A).
	ErrVendorPeerDenied = errcode.New(errcode.PermissionDenied, "this process may not act as a local client")
	// ErrConnectionStateMissing means the request carries no per-connection
	// state; the server attached it at accept time, so this is an internal
	// failure and reaches the client only as the generic internal error.
	ErrConnectionStateMissing = errcode.New(errcode.Internal, "the connection state is missing")
)
