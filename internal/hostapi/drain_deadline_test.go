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
	"github.com/syurodev/fugue/internal/config"
	hostv1 "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1"
	hostv1connect "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1/hostv1connect"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

// blockingStatusBackend serves the real runtime; its Status passes the
// Start-time call through and blocks every later one until the request
// context ends — the shape of a handler still running when the drain deadline
// fires.
type blockingStatusBackend struct {
	app.Backend
	started   atomic.Bool
	enterOnce sync.Once
	entered   chan struct{}
	doneOnce  sync.Once
	returned  chan struct{}
}

func newBlockingStatusBackend(rt app.Backend) *blockingStatusBackend {
	return &blockingStatusBackend{
		Backend:  rt,
		entered:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

func (b *blockingStatusBackend) Status(ctx context.Context) (app.HostStatusView, error) {
	if !b.started.Swap(true) {
		view, err := b.Backend.Status(ctx)
		if err != nil {
			return app.HostStatusView{}, errcode.Wrap(errcode.Internal, "the runtime status failed", err)
		}
		return view, nil
	}
	b.enterOnce.Do(func() { close(b.entered) })
	<-ctx.Done()
	b.doneOnce.Do(func() { close(b.returned) })
	return app.HostStatusView{}, errcode.Wrap(errcode.Canceled, "the blocked status request ended", ctx.Err())
}

// startDeadlineServer wires a real runtime with a drain deadline on the fake
// clock behind a real server, and a backend whose Status blocks every call
// after the Start-time one.
func startDeadlineServer(t *testing.T) (*app.HostProcess, *Server, *blockingStatusBackend, string, *clock.Fake, string) {
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
		Transport: srv,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := newBlockingStatusBackend(hp.Runtime())
	if err := srv.Start(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Drain(context.WithoutCancel(t.Context()), true); err != nil {
			t.Errorf("cleanup drain: %v", err)
		}
		// The deadline drain ends in the escalation error by design; its
		// chain must carry the canceled drain context.
		if err := hp.Close(); !errors.Is(err, context.Canceled) {
			t.Errorf("close the host = %v, want the deadline escalation error", err)
		}
	})
	return hp, srv, backend, sock.Path, clk, dir
}

func TestDrainDeadlineForcesCloseBeforeStoreClose(t *testing.T) {
	t.Parallel()
	hp, _, backend, sockPath, clk, dir := startDeadlineServer(t)

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
	go func() {
		_, err := clientA.HostStatus(t.Context(), &connect.Request[hostv1.HostStatusRequest]{})
		resCh <- err
	}()
	<-backend.entered

	// The Shutdown RPC replies at once and starts the drain against the
	// fake-clock deadline.
	if _, err := clientB.Shutdown(t.Context(), &connect.Request[hostv1.ShutdownRequest]{
		Msg: &hostv1.ShutdownRequest{CommandId: "deadline-1", Force: false},
	}); err != nil {
		t.Fatalf("Shutdown rpc = %v", err)
	}
	clk.Advance(5 * time.Second)

	// Close must end the blocked handler before the drain can return, and the
	// store closes only after the transport drain returned.
	<-backend.returned
	res := <-resCh
	if res == nil {
		t.Fatal("the forced-closed request reported success")
	}
	<-hp.DrainDone()
	reopened, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk, ids.NewUUIDv7(clk, ids.NewDeterministic("reopen")))
	if err != nil {
		t.Fatalf("the store did not close with the drain: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Errorf("close the reopened store: %v", err)
	}
}
