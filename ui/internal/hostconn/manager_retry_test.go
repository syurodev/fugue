package hostconn

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

var errStatusUnavailable = errors.New("status unavailable")

// statusHost answers HostStatus through a scripted function and keeps every
// subscription open, so a connected manager stays connected.
type statusHost struct {
	fakeHandler
	calls  atomic.Int32
	status func(ctx context.Context, call int32) (*connect.Response[hostv1.HostStatusResponse], error)
}

func (h *statusHost) HostStatus(ctx context.Context, _ *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
	return h.status(ctx, h.calls.Add(1))
}

func newStatusHost(status func(ctx context.Context, call int32) (*connect.Response[hostv1.HostStatusResponse], error)) *statusHost {
	h := &statusHost{status: status}
	h.subscribe = func(ctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], _ *connect.ServerStream[hostv1.SubscribeResponse]) error {
		<-ctx.Done()
		return ctx.Err()
	}
	return h
}

// compatibleStatus matches appParams' version, so no version advice applies.
func compatibleStatus() *connect.Response[hostv1.HostStatusResponse] {
	return connect.NewResponse(&hostv1.HostStatusResponse{
		HostVersion: appParams().Version,
		Clients:     &hostv1.ClientCounts{App: 1, Total: 1},
	})
}

// newAutoStartManager runs an auto-starting manager against a fake host that
// is already running, so it never launches a process.
func newAutoStartManager(t *testing.T, h *statusHost) (*Manager, *fakeClock, *collector) {
	t.Helper()
	socket := serveFakeHost(t, h)
	testSetMode(t, socket, 0o600)
	dir := dataDirWithHost(t, socket)
	testSetMode(t, dir, 0o700)
	clock := newFakeClock()
	upd := newCollector(256)
	m := NewManager(&Config{
		DataDir:   dir,
		Params:    appParams(),
		AutoStart: true,
		Topics:    []string{testTopicHost},
		Updates:   upd.ch,
		Clock:     clock,
	})
	upd.start()
	t.Cleanup(func() {
		m.Stop()
		upd.stop()
	})
	return m, clock, upd
}

// fireRetryBackoff waits for the manager to arm its retry backoff and fires
// only that timer. The startup deadlines are left alone: firing them while a
// dial is in flight would time the dial out instead of retrying it.
func fireRetryBackoff(t *testing.T, ctx context.Context, clock *fakeClock) {
	t.Helper()
	for clock.armedFor(t, ctx) == startupDeadline {
		// A startup deadline armed before the backoff; skip it.
	}
	clock.mu.Lock()
	var due []*fakeTimer
	for timer := range clock.live {
		if timer.d != startupDeadline {
			due = append(due, timer)
			delete(clock.live, timer)
		}
	}
	clock.mu.Unlock()
	for _, timer := range due {
		timer.fire()
	}
}

// An auto-starting manager whose HostStatus fails once backs off and
// connects on the next attempt instead of staying disconnected for good.
func TestAutoStartRetriesAFailedHostStatus(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	h := newStatusHost(func(_ context.Context, call int32) (*connect.Response[hostv1.HostStatusResponse], error) {
		if call == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, errStatusUnavailable)
		}
		return compatibleStatus(), nil
	})
	m, clock, upd := newAutoStartManager(t, h)
	m.Start(ctx)

	upd.waitFor(t, ctx, func(u Update) bool {
		return u.State == StateDisconnected && u.Reason == "host status unavailable"
	})
	fireRetryBackoff(t, ctx, clock)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	if got := h.calls.Load(); got != 2 {
		t.Fatalf("HostStatus calls = %d, want 2", got)
	}
}

// A host that answers Hello but stalls on HostStatus hits the startup
// deadline; the manager reports it and retries rather than hanging in
// "connecting" forever.
func TestAutoStartHostStatusStallHitsTheStartupDeadline(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	entered := make(chan struct{}, 1)
	h := newStatusHost(func(callCtx context.Context, call int32) (*connect.Response[hostv1.HostStatusResponse], error) {
		if call == 1 {
			entered <- struct{}{}
			<-callCtx.Done()
			return nil, connect.NewError(connect.CodeCanceled, callCtx.Err())
		}
		return compatibleStatus(), nil
	})
	m, clock, upd := newAutoStartManager(t, h)
	m.Start(ctx)

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("HostStatus was never called")
	}
	// The dial's deadline is stopped by now; the only live startup deadline
	// is the one bounding HostStatus.
	clock.fireAll()
	upd.waitFor(t, ctx, func(u Update) bool {
		return u.State == StateDisconnected && u.Reason == "host status unavailable"
	})
	fireRetryBackoff(t, ctx, clock)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
}
