package client

import (
	"errors"
	"strings"
)

// The typed failures a caller can branch on. They carry no detail beyond what
// the host already showed on the wire: no internal cause and no secret ever
// reaches this package's errors.
var (
	// ErrHostNotRunning means no usable host answered: host.json is absent or
	// nothing listens on the socket. The caller may start a host or retry
	// (auto-start is F10.2b).
	ErrHostNotRunning = errors.New("host is not running")
	// ErrResyncRequired means the subscription fell too far behind and the
	// host ended the stream (host-api §6). The caller must discard its state
	// for the topic and subscribe again from a fresh snapshot.
	ErrResyncRequired = errors.New("the event stream fell behind and needs a resync")
	// ErrStreamEnded means the stream ended cleanly: the host is draining or
	// ended the subscription. The caller may resubscribe with its cursor.
	ErrStreamEnded = errors.New("the event stream ended")
	// ErrStreamMalformed means the host sent a subscription message this
	// client cannot interpret: a contract violation of the topic's owner.
	ErrStreamMalformed = errors.New("the host sent a subscription message with no known kind")
	// ErrDisconnected means this Client's one connection is gone: the
	// transport will never dial again (one Client is one connection is one
	// lease), so the caller redials — a fresh Dial, which says a new Hello.
	ErrDisconnected = errors.New("the leased connection is gone; dial again")
	// ErrDataDirNotAbsolute is the static cause behind a SYNX_FUGUE_DATA_DIR
	// override that is not an absolute path; it is wrapped, never
	// constructed, at the call site.
	ErrDataDirNotAbsolute = errors.New("the data directory override must be an absolute path")
)

// VersionMismatchError is the typed shape of the host's unsupported_version
// rejection (Q-26 B): Hello found no common API version. HostAPIVersions are
// the host's supported versions, ascending, most preferred last, so a caller
// can tell "host older" from "host newer" (cli §2.3). The comparison against
// the client's own requirement is the caller's: it is what the F10.2b version
// handling implements over cli §2.3.
type VersionMismatchError struct {
	HostAPIVersions []string
}

// Error implements error. The message names the versions, never a secret.
func (e *VersionMismatchError) Error() string {
	if len(e.HostAPIVersions) == 0 {
		return "no Host API version in common with the host"
	}
	return "no Host API version in common with the host, which supports " +
		strings.Join(e.HostAPIVersions, ", ")
}
