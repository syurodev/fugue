package hostapi

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// errControlFailed is scripted by the control-ordering test.
var errControlFailed = errors.New("getsockopt failed")

// fakeRawConn records the order of the callback relative to the control call,
// so the test can pin that the credential getsockopt runs inside the callback
// while the descriptor is valid.
type fakeRawConn struct {
	order *[]string
}

func (f *fakeRawConn) Control(fn func(fd uintptr)) error {
	*f.order = append(*f.order, "control-enter")
	fn(7)
	*f.order = append(*f.order, "control-exit")
	return nil
}

func (f *fakeRawConn) Read(func(uintptr) bool) error  { return nil }
func (f *fakeRawConn) Write(func(uintptr) bool) error { return nil }

// fakeSysConn is a net.Conn whose SyscallConn returns the scripted raw conn.
type fakeSysConn struct {
	net.Conn
	raw syscall.RawConn
}

func (c *fakeSysConn) SyscallConn() (syscall.RawConn, error) { return c.raw, nil }

func TestPeerCredentialsReadInsideTheControlCallback(t *testing.T) {
	t.Parallel()
	pipe, _ := net.Pipe()
	t.Cleanup(func() { closeQuietly(pipe) })
	var order []string
	conn := &fakeSysConn{Conn: pipe, raw: &fakeRawConn{order: &order}}
	creds, err := readPeerCredentials(conn, func(fd int) (PeerCredentials, error) {
		order = append(order, "getsockopt-fd-7")
		return PeerCredentials{UID: 1000}, nil
	})
	if err != nil {
		t.Fatalf("readPeerCredentials = %v", err)
	}
	if creds.UID != 1000 {
		t.Fatalf("UID = %d, want 1000", creds.UID)
	}
	if len(order) != 3 || order[0] != "control-enter" || order[1] != "getsockopt-fd-7" || order[2] != "control-exit" {
		t.Fatalf("call order = %v, want getsockopt between control-enter and control-exit", order)
	}
}

func TestPeerCredentialsPropagateTheGetsockoptError(t *testing.T) {
	t.Parallel()
	pipe, _ := net.Pipe()
	t.Cleanup(func() { closeQuietly(pipe) })
	conn := &fakeSysConn{Conn: pipe, raw: &fakeRawConn{order: &[]string{}}}
	_, err := readPeerCredentials(conn, func(int) (PeerCredentials, error) {
		return PeerCredentials{}, errcode.Wrap(errcode.PermissionDenied, "read peer credentials", errControlFailed)
	})
	if !errors.Is(err, errControlFailed) {
		t.Fatalf("readPeerCredentials = %v, want the getsockopt failure", err)
	}
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("code = %s, want permission_denied", errcode.CodeOf(err))
	}
}
