package app

import "github.com/syurodev/fugue/internal/platform/errcode"

// Errors reported by the host runtime. They wrap public errcode codes so the
// transport can show them to a client.
var (
	// ErrNoCommonAPIVersion means the client and the host share no Host API
	// version; the transport attaches the host's supported versions as the
	// typed detail (Q-26 B).
	ErrNoCommonAPIVersion = errcode.New(errcode.UnsupportedVersion, "the client and the host share no Host API version")
	// ErrHostStopping means the host is draining and takes no new clients.
	ErrHostStopping = errcode.New(errcode.Unavailable, "the host is shutting down")
)
