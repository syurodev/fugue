//go:build !unix

package hostapi

import (
	"errors"
	"net"
)

// errPeerCredUnsupported is returned when the platform has no supported peer
// credential check. Stage A supports Linux and macOS only; this fails closed
// so the Windows nightly build compiles and never serves an unchecked
// connection.
var errPeerCredUnsupported = errors.New("this platform has no supported peer credential check")

func peerCredentials(net.Conn) (PeerCredentials, error) {
	return PeerCredentials{}, errPeerCredUnsupported
}
