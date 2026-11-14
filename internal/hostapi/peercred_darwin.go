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
		return PeerCredentials{UID: cred.Uid, PID: peerPID(fd)}, nil
	})
}

// peerPID reads LOCAL_PEERPID. An unreadable PID keeps the connection — the
// session principal does not need it — and is zero, which denies the local
// principal.
func peerPID(fd int) int {
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
