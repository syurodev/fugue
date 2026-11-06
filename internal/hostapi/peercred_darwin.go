//go:build darwin

package hostapi

import (
	"net"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func peerCredentials(c net.Conn) (PeerCredentials, error) {
	return readPeerCredentials(c, func(fd int) (PeerCredentials, error) {
		cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			return PeerCredentials{}, errcode.Wrap(errcode.PermissionDenied, "read peer credentials (LOCAL_PEERCRED)", err)
		}
		return PeerCredentials{UID: cred.Uid}, nil
	})
}
