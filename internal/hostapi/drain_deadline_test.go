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
		return b.Backend.Status(ctx)
	}
	b.enterOnce.Do(func() { close(b.entered) })
	<-ctx.Done()
	b.doneOnce.Do(func() { close(b.returned) })
	return app.HostStatusView{}, ctx.Err()
}

func TestDrainDeadlineForcesCloseBeforeStoreClose(t *testing.T) {
	t.Parallel()
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

	clientA, _ := newTestClient(t, sock.Path)
	clientB, _ := newTestClient(t, sock.Path)

	// Both connections say hello first; Status and Shutdown reject otherwise.
	for _, c := range []hostv1connect.HostServiceClient{clientA, clientB} {
		if _, err := c.Hello(t.Context(), &connect.Request[hostv1.HelloRequest]{
			Msg: &hostv1.HelloRequest{
				ClientKind:           hostv1.ClientKind_CLIENT_KIND_CLI,
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
		Msg: &hostv1.ShutdownRequest{CommandId: "deadline-1"},
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
