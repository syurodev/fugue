//go:build darwin

package hostapi

import (
	"net"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func peerCredentials(c net.Conn) (PeerCredentials, error) {
	fd, err := socketFD(c)
	if err != nil {
		return PeerCredentials{}, err
	}
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return PeerCredentials{}, errcode.Wrap(errcode.PermissionDenied, "read peer credentials (LOCAL_PEERCRED)", err)
	}
	return PeerCredentials{UID: cred.Uid}, nil
}
