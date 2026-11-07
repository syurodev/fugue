package hostapi

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	hostv1 "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1"
	hostv1connect "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1/hostv1connect"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// blockingStatusBackend serves the real runtime; its Status passes the
// Start-time call through and blocks every later one until the request
// context ends — the shape of a handler still running when the drain deadline
// fires.
type blockingStatusBackend struct {
	app.Backend
	started    atomic.Bool
	enterOnce  sync.Once
	entered    chan struct{}
	doneOnce   sync.Once
	returned   chan struct{}
	proceed    chan struct{}
	proceedOne sync.Once
}

func newBlockingStatusBackend(rt app.Backend) *blockingStatusBackend {
	return &blockingStatusBackend{
		Backend:  rt,
		entered:  make(chan struct{}),
		returned: make(chan struct{}),
		proceed:  make(chan struct{}),
	}
}

// release lets the blocked handler finish; safe to call more than once.
func (b *blockingStatusBackend) release() { b.proceedOne.Do(func() { close(b.proceed) }) }

func (b *blockingStatusBackend) Status(ctx context.Context) (app.HostStatusView, error) {
	if !b.started.Swap(true) {
		view, err := b.Backend.Status(ctx)
		if err != nil {
			return app.HostStatusView{}, errcode.Wrap(errcode.Internal, "the runtime status failed", err)
		}
		return view, nil
	}
	// A request replayed onto a fresh connection after the force close is
	// rejected by the server's admission gate (drained is already set), so it
	// never reaches this backend and cannot hold the drain's in-flight wait.
	b.enterOnce.Do(func() { close(b.entered) })
	<-ctx.Done()
	b.doneOnce.Do(func() { close(b.returned) })
	// Hold the handler open until the test releases it: the drain waits for
	// in-flight handlers, so the store cannot close while this gate is shut.
	<-b.proceed
	return app.HostStatusView{}, errcode.Wrap(errcode.Canceled, "the blocked status request ended", ctx.Err())
}

// drainStartedTransport signals when the drain reached the transport, after
// the deadline timer is armed.
type drainStartedTransport struct {
	inner   app.HostTransport
	started chan struct{}
	once    sync.Once
}

func (t *drainStartedTransport) Drain(ctx context.Context, force bool) error {
	t.once.Do(func() { close(t.started) })
	if err := t.inner.Drain(ctx, force); err != nil {
		return errcode.Wrap(errcode.Internal, "the inner transport drain failed", err)
	}
	return nil
}

// startDeadlineServer wires a real runtime with a drain deadline on the fake
// clock behind a real server, and a backend whose Status blocks every call
// after the Start-time one.
func startDeadlineServer(t *testing.T) (*app.HostProcess, *Server, *blockingStatusBackend, string, *clock.Fake, <-chan struct{}) {
	t.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	dir := shortDir(t)
	sock, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{
		Socket:  sock,
		DataDir: dir,
		IDs:     ids.NewUUIDv7(clk, ids.NewDeterministic("conn")),
		HostUID: int64(os.Getuid()),
	})
	started := make(chan struct{})
	tr := &drainStartedTransport{inner: srv, started: started}
	hp, err := app.StartHost(t.Context(), &app.HostDeps{
		DataDir: dir,
		Config: app.HostConfig{
			HostVersion:  "test-drain",
			APIVersions:  []string{"v1"},
			IdleGrace:    0,
			DrainTimeout: 5 * time.Second,
		},
		Clock:     clk,
		IDs:       ids.NewUUIDv7(clk, ids.NewDeterministic("host")),
		Transport: tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := newBlockingStatusBackend(hp.Runtime())
	if err := srv.Start(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend.release()
		if err := srv.Drain(context.WithoutCancel(t.Context()), true); err != nil {
			t.Errorf("cleanup drain: %v", err)
		}
		// The deadline drain ends in the escalation error by design; its
		// chain must carry the canceled drain context.
		if err := hp.Close(); !errors.Is(err, context.Canceled) {
			t.Errorf("close the host = %v, want the deadline escalation error", err)
		}
	})
	return hp, srv, backend, sock.Path, clk, started
}

func TestDrainDeadlineForcesCloseBeforeStoreClose(t *testing.T) {
	t.Parallel()
	hp, _, backend, sockPath, clk, drainStarted := startDeadlineServer(t)

	clientA, _ := newTestClient(t, sockPath)
	clientB, _ := newTestClient(t, sockPath)

	// Both connections say hello first; Status and Shutdown reject otherwise.
	for _, c := range []hostv1connect.HostServiceClient{clientA, clientB} {
		if _, err := c.Hello(t.Context(), &connect.Request[hostv1.HelloRequest]{
			Msg: &hostv1.HelloRequest{
				ClientKind:           hostv1.ClientKind_CLIENT_KIND_CLI,
				ClientName:           "fugue-test",
				ClientVersion:        "1.0",
				SupportedApiVersions: []string{"v1"},
			},
		}); err != nil {
			t.Fatalf("hello = %v", err)
		}
	}

	// An in-flight request holds one active connection.
	resCh := make(chan error, 1)
	supervise.Go(t.Context(), "test.status", func(ctx context.Context) error {
		_, err := clientA.HostStatus(ctx, &connect.Request[hostv1.HostStatusRequest]{})
		if err != nil {
			err = errcode.Wrap(errcode.Internal, "the status rpc failed", err)
		}
		resCh <- err
		return nil
	})
	<-backend.entered

	// The Shutdown RPC replies at once and starts the drain against the
	// fake-clock deadline.
	if _, err := clientB.Shutdown(t.Context(), &connect.Request[hostv1.ShutdownRequest]{
		Msg: &hostv1.ShutdownRequest{CommandId: "deadline-1", Force: false},
	}); err != nil {
		t.Fatalf("Shutdown rpc = %v", err)
	}
	// The deadline timer is armed before the drain enters the transport, so
	// waiting for the drain to get there makes the advance deterministic.
	<-drainStarted
	clk.Advance(5 * time.Second)

	// Close must end the blocked handler before the drain can return, and the
	// store closes only after the transport drain returned: the drain is
	// waiting for this handler until the test releases it, so the store must
	// still be open here — deterministically.
	<-backend.returned
	if hp.StoreClosed() {
		t.Fatal("the store closed while the blocked handler was still held")
	}
	backend.release()
	res := <-resCh
	if res == nil {
		t.Fatal("the forced-closed request reported success")
	}
	<-hp.DrainDone()
	if !hp.StoreClosed() {
		t.Fatal("the drain finished without closing the store")
	}
}
