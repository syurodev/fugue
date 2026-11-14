package hostapi

import (
	"math"
	"net"
	"strconv"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// PeerCredentials is what the transport knows about the process on the other
// end of a unix connection. Only the UID decides access (host-api §7): being
// the same OS user is the accepted trust model — it proves the same user, not
// a particular app, which the security documentation records. The PID is
// recorded at accept time and decides one thing more: a process of a live
// vendor session never becomes a local principal (roadmap Q-41 A). Zero means
// the PID could not be read, and the local principal is then denied.
type PeerCredentials struct {
	UID uint32
	PID int
}

// peerCredentials reads the peer credentials of a connected unix socket; the
// platform files implement the getsockopt calls (SO_PEERCRED on Linux,
// LOCAL_PEERCRED and LOCAL_PEERPID on macOS) and unsupported platforms fail closed
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

// socketFD runs fn with the descriptor of the connected socket inside the
// RawConn callback: the descriptor is only valid while Control runs, so the
// credential getsockopt must happen there — outside it, a concurrent close
// could recycle the descriptor and the check would read another socket's UID.
func socketFD(c net.Conn, fn func(fd int) error) error {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return errcode.New(errcode.InvalidArgument, "peer credentials need a socket connection")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "raw peer socket", err)
	}
	var opErr error
	if cerr := raw.Control(func(value uintptr) {
		if value > uintptr(math.MaxInt) {
			opErr = errcode.New(errcode.Internal, "peer socket descriptor overflows int")
			return
		}
		opErr = fn(int(value))
	}); cerr != nil {
		return errcode.Wrap(errcode.Internal, "control peer socket", cerr)
	}
	return opErr
}

// readPeerCredentials reads the peer credentials of c through the platform's
// getsockopt call, inside the RawConn callback.
func readPeerCredentials(c net.Conn, getsockopt func(fd int) (PeerCredentials, error)) (PeerCredentials, error) {
	var creds PeerCredentials
	if err := socketFD(c, func(fd int) error {
		var err error
		creds, err = getsockopt(fd)
		return err
	}); err != nil {
		return PeerCredentials{}, err
	}
	return creds, nil
}
