package hostapi

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// errQueueEmpty is returned when a test asks for more connections than it
// queued.
var errQueueEmpty = errors.New("queueListener has no connection left")

// queueListener hands out pre-made connections, so the credential tests can
// feed rejected and accepted connections without a real socket.
type queueListener struct {
	mu    sync.Mutex
	queue []net.Conn
}

func (l *queueListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return nil, errQueueEmpty
	}
	c := l.queue[0]
	l.queue = l.queue[1:]
	return c, nil
}

func (l *queueListener) Close() error   { return nil }
func (l *queueListener) Addr() net.Addr { return unixAddr{} }

type unixAddr struct{}

func (unixAddr) Network() string { return "unix" }
func (unixAddr) String() string  { return "fugue-test.sock" }

// credsFor scripts the peer uid of each connection; the named connections
// report an unreadable credential instead, so one listener can serve
// mismatching, unreadable and matching peers in the same test.
func credsFor(uidOf map[net.Conn]uint32, failFor ...net.Conn) func(net.Conn) (PeerCredentials, error) {
	fail := make(map[net.Conn]bool, len(failFor))
	for _, c := range failFor {
		fail[c] = true
	}
	return func(c net.Conn) (PeerCredentials, error) {
		if fail[c] {
			return PeerCredentials{}, errcode.New(errcode.Internal, "unreadable credential")
		}
		return PeerCredentials{UID: uidOf[c]}, nil
	}
}

// wrapped returns the innermost connection Accept handed back, so tests can
// compare identity with the queued connection.
func wrapped(c net.Conn) net.Conn {
	for {
		tc, ok := c.(*trackedConn)
		if !ok {
			return c
		}
		c = tc.Conn
	}
}

func acceptOne(t *testing.T, l *AcceptListener) net.Conn {
	t.Helper()
	c, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept = %v, want a connection", err)
	}
	return c
}

func TestPeerCredentialMismatchIsRejected(t *testing.T) {
	t.Parallel()
	bad1, _ := net.Pipe()
	bad2, _ := net.Pipe()
	good, _ := net.Pipe()
	t.Cleanup(func() {
		for _, c := range []net.Conn{bad1, bad2, good} {
			closeQuietly(c)
		}
	})
	inner := &queueListener{queue: []net.Conn{bad1, bad2, good}}
	l := NewAcceptListener(ListenerOptions{
		Inner:           inner,
		HostUID:         1000,
		PeerCredentials: credsFor(map[net.Conn]uint32{bad1: 4242, bad2: 4242, good: 1000}),
	})
	got := acceptOne(t, l)
	if wrapped(got) != good {
		t.Fatal("Accept returned a connection whose peer UID does not match the host user")
	}
}

func TestPeerCredentialUnavailableIsRejected(t *testing.T) {
	t.Parallel()
	bad, _ := net.Pipe()
	good, _ := net.Pipe()
	t.Cleanup(func() {
		closeQuietly(bad)
		closeQuietly(good)
	})
	inner := &queueListener{queue: []net.Conn{bad, good}}
	l := NewAcceptListener(ListenerOptions{
		Inner:           inner,
		HostUID:         1000,
		PeerCredentials: credsFor(map[net.Conn]uint32{bad: 1000, good: 1000}, bad),
	})
	got := acceptOne(t, l)
	if wrapped(got) != good {
		t.Fatal("Accept served a connection whose credentials could not be read")
	}
}

func TestListenerCountsRejections(t *testing.T) {
	t.Parallel()
	bad1, _ := net.Pipe()
	bad2, _ := net.Pipe()
	good, _ := net.Pipe()
	t.Cleanup(func() {
		for _, c := range []net.Conn{bad1, bad2, good} {
			closeQuietly(c)
		}
	})
	inner := &queueListener{queue: []net.Conn{bad1, bad2, good}}
	var counts []uint64
	l := NewAcceptListener(ListenerOptions{
		Inner:           inner,
		HostUID:         1000,
		PeerCredentials: credsFor(map[net.Conn]uint32{bad1: 4242, bad2: 4242, good: 1000}),
		OnReject:        func(count uint64) { counts = append(counts, count) },
	})
	acceptOne(t, l)
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 {
		t.Fatalf("OnReject counts = %v, want [1 2]", counts)
	}
}

func TestTrackedConnNotifiesExactlyOnce(t *testing.T) {
	t.Parallel()
	good, _ := net.Pipe()
	t.Cleanup(func() { closeQuietly(good) })
	inner := &queueListener{queue: []net.Conn{good}}
	var closed []net.Conn
	l := NewAcceptListener(ListenerOptions{
		Inner:           inner,
		HostUID:         1000,
		PeerCredentials: credsFor(map[net.Conn]uint32{good: 1000}),
		OnClosed:        func(c net.Conn) { closed = append(closed, c) },
	})
	c := acceptOne(t, l)
	if err := c.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if len(closed) != 1 || closed[0] != c {
		t.Fatalf("close notifications = %d, want exactly one for the wrapped conn", len(closed))
	}
	if wrapped(closed[0]) != good {
		t.Fatal("the close notification carried a different connection")
	}
}

func TestAcceptListenerDelegatesCloseAndAddr(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	inner := listenUnix(t, filepath.Join(dir, SocketFileName), true)
	l := NewAcceptListener(ListenerOptions{Inner: inner, HostUID: int64(os.Getuid())})
	addr := l.Addr()
	if addr.Network() != inner.Addr().Network() || addr.String() != inner.Addr().String() {
		t.Fatalf("Addr = %v, want the inner listener's %v", addr, inner.Addr())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if _, err := inner.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
}

func TestRealUnixConnectionCarriesThisProcessUID(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	path := filepath.Join(dir, SocketFileName)
	ln := listenUnix(t, path, true)
	dialUnix(t, path)
	server, aerr := ln.Accept()
	if aerr != nil {
		t.Fatal(aerr)
	}
	t.Cleanup(func() { closeQuietly(server) })
	got, cerr := peerCredentials(server)
	if cerr != nil {
		t.Fatalf("peerCredentials = %v", cerr)
	}
	if int64(got.UID) != int64(os.Getuid()) {
		t.Fatalf("peer UID = %d, want this process uid %d", got.UID, os.Getuid())
	}
	if uerr := uidAllowed(got.UID, int64(os.Getuid())); uerr != nil {
		t.Fatalf("uidAllowed(matching) = %v, want nil", uerr)
	}
	mismatch := uidAllowed(got.UID+1, int64(os.Getuid()))
	if !errors.Is(mismatch, ErrPeerCredentialDenied) {
		t.Fatalf("uidAllowed(mismatch) = %v, want the peer credential denial", mismatch)
	}
	if errcode.CodeOf(mismatch) != errcode.PermissionDenied {
		t.Fatalf("uidAllowed(mismatch) code = %s, want permission_denied", errcode.CodeOf(mismatch))
	}
}
