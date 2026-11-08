package client_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

// serveHostCounting is serveHost with the accepted-connection counter, for
// the tests that pin the one-connection lease.
func serveHostCounting(t *testing.T, h hostv1connect.HostServiceHandler) (string, *countingListener) {
	t.Helper()
	dir := shortDir(t)
	socket := filepath.Join(dir, "fugue.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingListener{Listener: ln}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(h))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = counting
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)
	return socket, counting
}

// countingListener wraps a net.Listener and counts the connections it
// accepted, so a test can pin how many connections the client made.
type countingListener struct {
	net.Listener
	mu       sync.Mutex
	accepted int
	conns    []net.Conn
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, fmt.Errorf("accept: %w", err)
	}
	l.mu.Lock()
	l.accepted++
	l.conns = append(l.conns, conn)
	l.mu.Unlock()
	return conn, nil
}

// acceptedCount reports how many connections were accepted so far.
func (l *countingListener) acceptedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.accepted
}

// dropServerConns closes every connection the listener accepted, as a host
// restart or a network drop would from the server side.
func (l *countingListener) dropServerConns() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, conn := range l.conns {
		if cerr := conn.Close(); cerr != nil {
			// The connection the host already closed stays closed; keep
			// dropping the rest.
			continue
		}
	}
	l.conns = nil
}

// TestDialToAMissingSocketIsNotRunning pins that a socket file that is gone
// reads as ErrHostNotRunning — the retryable state the status bar shows as
// "host not running".
func TestDialToAMissingSocketIsNotRunning(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	socket := filepath.Join(dir, "missing.sock")
	_, err := client.Dial(t.Context(), socket, appParams())
	if !errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("dial a missing socket: err = %v, want ErrHostNotRunning", err)
	}
}

// TestDialRespectsTheCallerDeadline pins that a dial against a socket nobody
// serves returns when the caller's context ends instead of hanging: connecting
// to a bound but never accepted socket succeeds on some platforms and the
// request then waits forever, so the deadline is the caller's guard (the
// reconnect loop applies it on its injected clock).
func TestDialRespectsTheCallerDeadline(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	var lc net.ListenConfig
	ln, lerr := lc.Listen(t.Context(), "unix", filepath.Join(dir, "silent.sock"))
	if lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := client.Dial(ctx, ln.Addr().String(), appParams()); err == nil {
		t.Fatal("a dial against a silent socket returned no error")
	}
}

// TestDialContextGoneFails pins that a canceled dial does not hang: the caller
// owns lifetimes, and it may take them back.
func TestDialContextGoneFails(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", filepath.Join(dir, "slow.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Dial(ctx, ln.Addr().String(), appParams()); err == nil {
		t.Fatal("a canceled dial succeeded")
	}
}

// TestDroppedLeaseNeverDialsAgain pins one Client is one connection is one
// lease: after the host drops the connection, the client's next calls fail
// with ErrDisconnected and the fake host never sees a second connection — the
// client cannot silently become a connection the host has not helloed.
//
// The first call after the drop may surface the raw connection error while
// the transport learns its pooled connection is dead; from the next call on,
// the dial guard answers, so the test accepts at most one such call before
// demanding the typed one.
func TestDroppedLeaseNeverDialsAgain(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	socket, listener := serveHostCounting(t, &fakeHandler{status: func(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
		return connect.NewResponse(&hostv1.HostStatusResponse{HostVersion: testVer}), nil
	}})
	c, err := client.Dial(ctx, socket, appParams())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if _, serr := c.HostStatus(ctx); serr != nil {
		t.Fatalf("the first status read: %v", serr)
	}

	// The host drops the connection from its side.
	listener.dropServerConns()

	// The next calls surface the gone lease: at most one raw connection
	// error first, then every call wraps ErrDisconnected, and the fake host
	// accepted exactly one connection throughout.
	var sawDisconnected bool
	for range 3 {
		_, err = c.HostStatus(ctx)
		if err == nil {
			t.Fatal("a call after the drop succeeded")
		}
		if errors.Is(err, client.ErrDisconnected) {
			sawDisconnected = true
			break
		}
	}
	if !sawDisconnected {
		t.Fatalf("no call within three surfaced ErrDisconnected; last: %v", err)
	}
	if _, err := c.HostStatus(ctx); !errors.Is(err, client.ErrDisconnected) {
		t.Errorf("a later status read: err = %v, want ErrDisconnected", err)
	}
	// A stream reports the gone lease either when it is opened or at its
	// first Recv: the request's write can succeed against the dead socket
	// before the read learns the truth.
	serr := errDisconnectedFromStream(ctx, c)
	if !errors.Is(serr, client.ErrDisconnected) {
		t.Errorf("a later subscribe: err = %v, want ErrDisconnected", serr)
	}
	if got := listener.acceptedCount(); got != 1 {
		t.Errorf("the fake host accepted %d connections, want exactly 1", got)
	}
}

// errDisconnectedFromStream opens a subscription and returns its first
// failure, from the open or the first Recv, closing the stream either way.
func errDisconnectedFromStream(ctx context.Context, c *client.Client) error {
	stream, err := c.Subscribe(ctx, "host", nil)
	if err != nil {
		return fmt.Errorf("open the subscription: %w", err)
	}
	defer closeQuietly(stream)
	if _, err := stream.Recv(); err != nil {
		return fmt.Errorf("receive from the subscription: %w", err)
	}
	return nil
}
