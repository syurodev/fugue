package hostconn

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

type backgroundHost struct {
	fakeHandler
	hello chan hostv1.ClientKind
}

func (h *backgroundHost) Hello(_ context.Context, r *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	h.hello <- r.Msg.GetClientKind()
	return connect.NewResponse(&hostv1.HelloResponse{ApiVersion: "v1", HostVersion: testDevVersion}), nil
}

func (*backgroundHost) HostStatus(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
	return connect.NewResponse(&hostv1.HostStatusResponse{HostVersion: testDevVersion}), nil
}

func (*backgroundHost) Subscribe(ctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], _ *connect.ServerStream[hostv1.SubscribeResponse]) error {
	<-ctx.Done()
	return fmt.Errorf("stream ended: %w", ctx.Err())
}

func TestBackgroundLeaseStopsWhenPreferenceDisabled(t *testing.T) {
	t.Parallel()
	h := &backgroundHost{hello: make(chan hostv1.ClientKind, 2)}
	socket := serveFakeHost(t, h)
	testSetMode(t, socket, 0o600)
	dir := dataDirWithHost(t, socket)
	testSetMode(t, dir, 0o700)
	if err := client.SetBackgroundEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b, err := StartBackground(ctx, dir, lifecycleParams(), "")
	if err != nil || b == nil {
		t.Fatalf("background: %v", err)
	}
	defer b.Close()
	select {
	case kind := <-h.hello:
		if kind != hostv1.ClientKind_CLIENT_KIND_APP_BACKGROUND {
			t.Fatalf("kind = %v", kind)
		}
	case <-ctx.Done():
		t.Fatal("no background lease")
	}
	if err = client.SetBackgroundEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-ctx.Done():
		t.Fatal("background owner did not stop")
	}
}

func TestBackgroundKeepsRealHostAliveUntilDisabled(t *testing.T) {
	t.Parallel()
	appPath, data := lifecycleFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	m := lifecycleManager(ctx, data, appPath)
	defer m.Stop()
	waitConnectedForTest(t, ctx, m)
	if err := client.SetBackgroundEnabled(data, true); err != nil {
		t.Fatal(err)
	}
	bg, err := StartBackground(ctx, data, lifecycleParams(), appPath)
	if err != nil || bg == nil {
		t.Fatalf("start background: %v", err)
	}
	defer bg.Close()
	waitConnectedForTest(t, ctx, bg.manager)
	m.Stop()
	bg.manager.mu.Lock()
	conn := bg.manager.conn
	bg.manager.mu.Unlock()
	waitBackgroundOnly(t, ctx, conn)
	if err = client.SetBackgroundEnabled(data, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bg.Done():
	case <-ctx.Done():
		t.Fatal("background did not stop")
	}
	if successes := waitLifecycleChildren(t, ctx, []*Manager{m}); successes != 1 {
		t.Fatalf("successful hosts = %d", successes)
	}
}

func waitConnectedForTest(t *testing.T, ctx context.Context, m *Manager) {
	t.Helper()
	for {
		state, reason := m.State()
		if state == StateConnected {
			return
		}
		if state == StateIncompatible || state == StateDisconnected {
			t.Fatalf("connect: %s", reason)
		}
		timer := RealClock{}.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C():
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			t.Fatal("connection timeout")
		}
	}
}

func waitBackgroundOnly(t *testing.T, ctx context.Context, conn *client.Client) {
	t.Helper()
	for {
		status, err := conn.HostStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Clients.AppBackground == 1 && status.Clients.Total == 1 {
			return
		}
		timer := RealClock{}.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C():
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			t.Fatal("background lease did not become sole client")
		}
	}
}
