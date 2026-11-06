//go:build linux

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
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return PeerCredentials{}, errcode.Wrap(errcode.PermissionDenied, "read peer credentials (SO_PEERCRED)", err)
	}
	return PeerCredentials{UID: cred.Uid}, nil
}
