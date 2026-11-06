//go:build !unix

package hostapi

import (
	"net"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// errPeerCredUnsupported is returned when the platform has no supported peer
// credential check. Stage A supports Linux and macOS only; this fails closed
// so the Windows nightly build compiles and never serves an unchecked
// connection.
var errPeerCredUnsupported = errcode.New(errcode.PermissionDenied, "this platform has no supported peer credential check")

func peerCredentials(net.Conn) (PeerCredentials, error) {
	return PeerCredentials{}, errPeerCredUnsupported
}
