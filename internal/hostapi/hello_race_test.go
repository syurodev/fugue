package hostapi

import (
	"net"
	"testing"

	hostv1 "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1"
)

// TestConcurrentHelloAcquiresOneLease pins the HTTP/2 multiplexing race:
// several Hello requests on one connection must yield exactly one lease, and
// the losers must be rejected as repeated hellos, never lease twice.
func TestConcurrentHelloAcquiresOneLease(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)

	const n = 8
	start := make(chan struct{})
	errs := make(chan error, n)
	for range n {
		go func() {
			<-start
			_, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1"))
			errs <- err
		}()
	}
	close(start)
	oks := 0
	for range n {
		if err := <-errs; err == nil {
			oks++
		}
	}
	if oks != 1 {
		t.Fatalf("successful hellos = %d, want exactly 1", oks)
	}
	if got := backend.leasedIDs(); len(got) != 1 {
		t.Fatalf("leases = %v, want exactly one", got)
	}
	if got := len(backend.released); got != 0 {
		t.Fatalf("lease releases = %d, want 0", got)
	}
}

// TestConnectionClosedDuringHelloReleasesTheLease pins the close-during-hello
// race: the connection closes while LeaseAcquired is still running, so the
// close hook sees no lease yet — the hello must release the lease itself once
// it lands, and the lease count must return to zero.
func TestConnectionClosedDuringHelloReleasesTheLease(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.holdLease = make(chan struct{})
	backend.leaseEntered = make(chan struct{}, 1)
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)

	resCh := make(chan error, 1)
	go func() {
		_, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1"))
		resCh <- err
	}()
	<-backend.leaseEntered

	// Closing the server-side conn runs the close hook while the hello is
	// still acquiring: no lease is held, so the hook must not release.
	srv.mu.Lock()
	conns := make([]net.Conn, 0, len(srv.conns))
	for c := range srv.conns {
		conns = append(conns, c)
	}
	srv.mu.Unlock()
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Errorf("close the server-side conn: %v", err)
		}
	}
	close(backend.holdLease)

	// The hello lands on a closed connection and must release its lease
	// itself; the close hook saw the connection still acquiring and released
	// nothing. A blocked receive is the deterministic wait — a regression
	// hangs and fails the package timeout instead of passing silently.
	<-backend.released
	if got, want := backend.leasedIDs(), 1; len(got) != want {
		t.Fatalf("lease acquisitions = %d, want %d", len(got), want)
	}
	if err := <-resCh; err == nil {
		t.Fatal("the hello on a closed connection reported success")
	}
}
