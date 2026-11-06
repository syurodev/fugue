package hostapi

import (
	"net"
	"strconv"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// PeerCredentials is what the transport knows about the process on the other
// end of a unix connection. Only the UID decides access (host-api §7): being
// the same OS user is the accepted trust model — it proves the same user, not
// a particular app, which the security documentation records.
type PeerCredentials struct {
	UID uint32
}

// peerCredentials reads the peer credentials of a connected unix socket; the
// platform files implement the getsockopt calls (SO_PEERCRED on Linux,
// LOCAL_PEERCRED on macOS) and unsupported platforms fail closed
// (peercred_other.go).

// uidAllowed reports whether a connection from uid may talk to a host run by
// want. An unreadable credential never reaches this check; the listener
// rejects the connection already.
func uidAllowed(got uint32, want int64) error {
	if int64(got) == want {
		return nil
	}
	return errcode.Wrap(errcode.PermissionDenied,
		"connection from uid "+strconv.FormatUint(uint64(got), 10), ErrPeerCredentialDenied)
}

// socketFD extracts the descriptor of a connected socket for the credential
// getsockopt calls.
func socketFD(c net.Conn) (int, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return 0, errcode.New(errcode.InvalidArgument, "peer credentials need a socket connection")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, errcode.Wrap(errcode.Internal, "raw peer socket", err)
	}
	var fd int
	if opErr := raw.Control(func(value uintptr) { fd = int(value) }); opErr != nil {
		return 0, errcode.Wrap(errcode.Internal, "control peer socket", opErr)
	}
	return fd, nil
}
