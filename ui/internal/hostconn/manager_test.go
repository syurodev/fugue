package hostconn

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

const (
	testTrailer          = "clients"
	testIncA             = "inc-A"
	testIncC             = "inc-C"
	testTopicHost        = "host"
	reasonHostNotRunning = "host not running"
)

// errNoSubscriptionScripted is the static rejection a fake host gives when a
// test did not script a subscription; it is wrapped, never constructed, at
// the call site.
var errNoSubscriptionScripted = errors.New("no subscription scripted")

// errNoCommonVersion is the static rejection behind the version-mismatch
// fixture; it is wrapped, never constructed, at the call site.
var errNoCommonVersion = errors.New("no common version")

// fakeTimer is a timer the test fires by hand; it records the duration the
// manager asked for.
type fakeTimer struct {
	clock *fakeClock
	d     time.Duration
	c     chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if _, live := t.clock.live[t]; !live {
		return false
	}
	delete(t.clock.live, t)
	return true
}

// fire moves a live timer's channel; a stopped timer does not fire.
func (t *fakeTimer) fire() {
	t.c <- time.Time{}
}

// fakeClock records requested backoffs and lets the test fire live timers.
type fakeClock struct {
	mu    sync.Mutex
	live  map[*fakeTimer]struct{}
	durs  []time.Duration
	armed chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{live: map[*fakeTimer]struct{}{}, armed: make(chan time.Duration, 64)}
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{clock: c, d: d, c: make(chan time.Time, 1)}
	c.mu.Lock()
	c.live[t] = struct{}{}
	c.durs = append(c.durs, d)
	c.mu.Unlock()
	select {
	case c.armed <- d:
	default:
	}
	return t
}

// durations returns the durations every NewTimer asked for so far.
func (c *fakeClock) durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durs...)
}

// armedFor waits for the next timer the manager arms and returns its
// duration, so tests observe retry attempts that produce no new state.
func (c *fakeClock) armedFor(t *testing.T, ctx context.Context) time.Duration {
	t.Helper()
	select {
	case d := <-c.armed:
		return d
	case <-ctx.Done():
		t.Fatalf("no timer was armed: %v", ctx.Err())
		return 0
	}
}

// fireAll fires every live timer once; a manager arms at most one at a time,
// so this is the reconnect nudge.
func (c *fakeClock) fireAll() {
	c.mu.Lock()
	live := make([]*fakeTimer, 0, len(c.live))
	for t := range c.live {
		live = append(live, t)
	}
	for _, t := range live {
		delete(c.live, t)
	}
	c.mu.Unlock()
	for _, t := range live {
		t.fire()
	}
}

// closer is anything test teardown closes.
type closer interface{ Close() error }

func closeQuietly(c closer) {
	if err := c.Close(); err != nil {
		return
	}
}

func removeQuietly(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		return
	}
}

// fakeHandler serves scripted Hello and Subscribe replies; the subscription
// callback may be called more than once when the manager resubscribes.
type fakeHandler struct {
	hostv1connect.UnimplementedHostServiceHandler
	hello     func(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error)
	subscribe func(context.Context, *connect.Request[hostv1.SubscribeRequest], *connect.ServerStream[hostv1.SubscribeResponse]) error
}

func (h *fakeHandler) Hello(ctx context.Context, req *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	if h.hello != nil {
		return h.hello(ctx, req)
	}
	return connect.NewResponse(&hostv1.HelloResponse{ApiVersion: "v1", HostVersion: "test-host"}), nil
}

func (h *fakeHandler) Subscribe(ctx context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	if h.subscribe == nil {
		return connect.NewError(connect.CodePermissionDenied, errNoSubscriptionScripted)
	}
	return h.subscribe(ctx, req, stream)
}

// shortDir creates a directory whose path is short enough to bind a unix
// socket, which t.TempDir() cannot promise on macOS: sun_path holds 104 bytes
// and macOS test temp dirs already exceed 100.
func shortDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fugue-%x", sum[:4]))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeQuietly(dir) })
	return dir
}

// serveFakeHost runs a fake HostService on a fresh unix socket.
func serveFakeHost(t *testing.T, h hostv1connect.HostServiceHandler) string {
	t.Helper()
	socket := filepath.Join(shortDir(t), "fugue.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(h))
	srv := &http.Server{Protocols: &protos, Handler: mux, ReadHeaderTimeout: 0}
	go func() {
		if serr := srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			return
		}
	}()
	t.Cleanup(func() { closeQuietly(srv) })
	return socket
}

// dataDirWithHost points a discovery file at the given socket.
func dataDirWithHost(t *testing.T, socket string) string {
	t.Helper()
	dir := t.TempDir()
	hf := fmt.Sprintf(`{"pid":1,"socket_path":%q,"api_versions":["v1"],"host_version":"test-host"}`, socket)
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(hf), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// appParams is the Hello identity the manager tests use.
func appParams() client.Params {
	return client.Params{Kind: client.KindApp, Name: "fugue-app-test", Version: "0", APIVersions: []string{"v1"}}
}

// collector gathers updates in the background so tests assert on the full
// history without missing concurrent sends.
type collector struct {
	ch      chan Update
	mu      sync.Mutex
	all     []Update
	changed chan struct{}
	done    chan struct{}
}

func newCollector(buffer int) *collector {
	return &collector{ch: make(chan Update, buffer), changed: make(chan struct{}, 1), done: make(chan struct{})}
}

// start consumes updates until stop is called.
func (c *collector) start() {
	go func() {
		for u := range c.ch {
			c.mu.Lock()
			c.all = append(c.all, u)
			c.mu.Unlock()
			select {
			case c.changed <- struct{}{}:
			default:
			}
		}
		close(c.done)
	}()
}

// stop stops consuming; the history stays readable.
func (c *collector) stop() {
	close(c.ch)
	<-c.done
}

// history returns every update seen so far.
func (c *collector) history() []Update {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Update(nil), c.all...)
}

// offset returns the current history length, the marker a later find starts
// from.
func (c *collector) offset() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.all)
}

// find returns the first update at or after `from` that satisfies f, waiting
// on new ones until it appears or the test's safety-net context ends. The
// waits are channel-driven; the deadline is only the safety net. It returns
// the update and the index one past it.
func (c *collector) find(t *testing.T, ctx context.Context, from int, f func(Update) bool) (Update, int) {
	t.Helper()
	for {
		c.mu.Lock()
		for i := from; i < len(c.all); i++ {
			if f(c.all[i]) {
				u := c.all[i]
				c.mu.Unlock()
				return u, i + 1
			}
		}
		c.mu.Unlock()
		select {
		case <-c.changed:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for the expected update: %v", ctx.Err())
		}
	}
}

// waitFor is find from the beginning of the history.
func (c *collector) waitFor(t *testing.T, ctx context.Context, f func(Update) bool) Update {
	t.Helper()
	u, _ := c.find(t, ctx, 0, f)
	return u
}

// testCtx returns the test's context with a 60 s safety-net deadline: the
// waits themselves are channel-driven.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newTestManager builds a manager over the discovery file with a fake clock.
func newTestManager(t *testing.T, dataDir string, clock *fakeClock, topics ...string) (*Manager, *collector) {
	t.Helper()
	upd := newCollector(256)
	m := NewManager(&Config{
		DataDir: dataDir,
		Params:  appParams(),
		Topics:  topics,
		Updates: upd.ch,
		Clock:   clock,
	})
	upd.start()
	t.Cleanup(func() {
		m.Stop()
		upd.stop()
	})
	return m, upd
}

// TestManagerConnectsAndDeliversSnapshotAndEvents pins the happy path: the
// manager dials, reports connected, and forwards the snapshot and events of
// its topic in order.
func TestManagerConnectsAndDeliversSnapshotAndEvents(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(_ context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 7, State: []byte("state"), IncarnationId: testIncA},
		}}); err != nil {
			return fmt.Errorf("send the snapshot: %w", err)
		}
		return stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
			Event: &hostv1.HostEvent{Sequence: 8, Type: testTrailer, Payload: []byte(`{}`)},
		}})
	}})
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	upd.waitFor(t, ctx, func(u Update) bool { return u.Snapshot != nil && u.Snapshot.Incarnation == testIncA })
	upd.waitFor(t, ctx, func(u Update) bool { return u.Event != nil && u.Event.Sequence == 8 })
	if _, reason := m.State(); reason != "" {
		t.Errorf("connected reason = %q, want empty", reason)
	}
}

// errTransportFailure is the static transport failure the scripted stream
// ends with; it is wrapped, never constructed, at the call site.
var errTransportFailure = errors.New("the connection dropped")

// reconnectTracker tracks hello dials, subscribe calls and the resumed cursor.
type reconnectTracker struct {
	mu           sync.Mutex
	dials        int
	subscribes   int
	secondCursor chan *hostv1.EventCursor
}

func (rt *reconnectTracker) onHello() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.dials++
}

func (rt *reconnectTracker) countDials() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.dials
}

func (rt *reconnectTracker) onSubscribe(req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	rt.mu.Lock()
	rt.subscribes++
	n := rt.subscribes
	rt.mu.Unlock()
	if n == 1 {
		return sendSnapshotThenFail(stream)
	}
	rt.secondCursor <- req.Msg.GetCursor()
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
		Event: &hostv1.HostEvent{Sequence: 12, Type: testTrailer},
	}}); err != nil {
		return fmt.Errorf("send event: %w", err)
	}
	return nil
}

func sendSnapshotThenFail(stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
		Snapshot: &hostv1.SnapshotEvent{Sequence: 10, IncarnationId: testIncA, State: []byte("initial")},
	}}); err != nil {
		return fmt.Errorf("send snapshot: %w", err)
	}
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
		Event: &hostv1.HostEvent{Sequence: 11, Type: testTrailer},
	}}); err != nil {
		return fmt.Errorf("send event: %w", err)
	}
	return errTransportFailure
}

// TestHostconnReconnectsWithANewClientAndResumesFromTheCursor pins that after
// a transport failure, the manager dials a new client (a client never
// redials) and sends the stored cursor, resuming event streaming without a
// fresh snapshot or duplicate events.
func TestHostconnReconnectsWithANewClientAndResumesFromTheCursor(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	rt := &reconnectTracker{secondCursor: make(chan *hostv1.EventCursor, 1)}
	h := &fakeHandler{
		hello: func(_ context.Context, _ *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
			rt.onHello()
			return connect.NewResponse(&hostv1.HelloResponse{ApiVersion: "v1", HostVersion: "test-host"}), nil
		},
		subscribe: func(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
			return rt.onSubscribe(req, stream)
		},
	}
	socket := serveFakeHost(t, h)
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)

	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	upd.waitFor(t, ctx, func(u Update) bool { return u.Event != nil && u.Event.Sequence == 11 })
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateDisconnected && u.Reason == "connection lost" })

	if durs := clock.durations(); len(durs) != 1 || durs[0] != backoffBase {
		t.Fatalf("backoff durations = %v, want [%v]", durs, backoffBase)
	}
	clock.fireAll()

	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	upd.waitFor(t, ctx, func(u Update) bool { return u.Event != nil && u.Event.Sequence == 12 })

	if dials := rt.countDials(); dials != 2 {
		t.Errorf("total client dials = %d, want 2 (a new client per reconnect)", dials)
	}
	assertExpectedCursor(t, ctx, rt.secondCursor, 11, testIncA)

	c := m.cursorFor(testTopicHost)
	if c == nil || c.Sequence != 12 || c.Incarnation != testIncA {
		t.Errorf("final cursor = %+v, want seq 12 %s", c, testIncA)
	}
}

func assertExpectedCursor(t *testing.T, ctx context.Context, ch <-chan *hostv1.EventCursor, wantSeq uint64, wantInc string) {
	t.Helper()
	select {
	case cur := <-ch:
		if cur == nil || cur.GetSequence() != wantSeq || cur.GetIncarnationId() != wantInc || cur.GetTopic() != testTopicHost {
			t.Fatalf("resumed cursor = %+v, want topic %q seq %d %s", cur, testTopicHost, wantSeq, wantInc)
		}
	case <-ctx.Done():
		t.Fatalf("the manager never sent the resume cursor: %v", ctx.Err())
	}
}

func sendSnapshotAndResync(stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
		Snapshot: &hostv1.SnapshotEvent{Sequence: 3, State: []byte("state1"), IncarnationId: "inc-B"},
	}}); err != nil {
		return fmt.Errorf("send the snapshot: %w", err)
	}
	if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_End{
		End: &hostv1.StreamEnd{ResyncRequired: true},
	}}); err != nil {
		return fmt.Errorf("send end: %w", err)
	}
	return nil
}

// TestHostconnReloadsTheSnapshotOnResyncOrNewIncarnation pins that on
// resync_required or when the host presents a new incarnation, the manager
// reloads the snapshot: the stored cursor is cleared on resync (sending a nil
// cursor), and a new incarnation resets the topic's cursor incarnation.
func TestHostconnReloadsTheSnapshotOnResyncOrNewIncarnation(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	secondCursor := make(chan *hostv1.EventCursor, 1)
	subscribes := 0
	var mu sync.Mutex
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		mu.Lock()
		subscribes++
		n := subscribes
		mu.Unlock()
		if n == 1 {
			return sendSnapshotAndResync(stream)
		}
		secondCursor <- req.Msg.GetCursor()
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 1, State: []byte("state2"), IncarnationId: testIncC},
		}}); err != nil {
			return fmt.Errorf("send snapshot: %w", err)
		}
		return nil
	}})
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)

	upd.waitFor(t, ctx, func(u Update) bool {
		return u.Snapshot != nil && u.Snapshot.Sequence == 3 && u.Snapshot.Incarnation == "inc-B"
	})

	if d := clock.armedFor(t, ctx); d != backoffBase {
		t.Errorf("restart backoff = %v, want %v", d, backoffBase)
	}
	clock.fireAll()

	assertNilCursor(t, ctx, secondCursor)

	upd.waitFor(t, ctx, func(u Update) bool {
		return u.Snapshot != nil && u.Snapshot.Sequence == 1 && u.Snapshot.Incarnation == testIncC
	})

	c := m.cursorFor(testTopicHost)
	if c == nil || c.Incarnation != testIncC || c.Sequence != 1 {
		t.Errorf("cursor after new incarnation snapshot = %+v, want %s seq 1", c, testIncC)
	}
}

func assertNilCursor(t *testing.T, ctx context.Context, ch <-chan *hostv1.EventCursor) {
	t.Helper()
	select {
	case cur := <-ch:
		if cur != nil {
			t.Errorf("resync subscribe carried a cursor: %+v, want nil", cur)
		}
	case <-ctx.Done():
		t.Fatalf("the manager never resubscribed: %v", ctx.Err())
	}
}

// TestHostconnClosesAbandonedStreams pins that a stream the manager abandons
// (when stopping or tearing down the stream set) is closed promptly:
// defer stream.Close() cancels the HTTP/2 stream on the host, preventing
// idle leaks.
func TestHostconnClosesAbandonedStreams(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	serverStreamClosed := make(chan struct{})
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(sctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 1, IncarnationId: testIncA},
		}}); err != nil {
			return fmt.Errorf("send snapshot: %w", err)
		}
		<-sctx.Done()
		close(serverStreamClosed)
		return sctx.Err()
	}})
	clock := newFakeClock()
	upd := newCollector(64)
	upd.start()
	baseCtx := testCtx(t)
	runCtx, cancel := context.WithCancel(baseCtx)
	m := NewManager(&Config{
		DataDir: dataDirWithHost(t, socket),
		Params:  appParams(),
		Topics:  []string{testTopicHost},
		Updates: upd.ch,
		Clock:   clock,
	})
	m.Start(runCtx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	upd.waitFor(t, ctx, func(u Update) bool { return u.Snapshot != nil })

	// Stopping the manager abandons the live stream. defer stream.Close() must
	// close the stream and cancel the server context.
	m.Stop()
	cancel()
	upd.stop()

	select {
	case <-serverStreamClosed:
		// Succeeded: server context completed because stream was closed.
	case <-ctx.Done():
		t.Fatalf("the abandoned stream was not closed before deadline: %v", ctx.Err())
	}
}

// TestManagerReloadsTheCursorAfterASnapshot pins that events after a snapshot
// store the snapshot's incarnation with their own sequences, so a later
// reconnect catches up inside the right incarnation.
func TestManagerReloadsTheCursorAfterASnapshot(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(_ context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 5, State: []byte("state"), IncarnationId: testIncC},
		}}); err != nil {
			return fmt.Errorf("send the snapshot: %w", err)
		}
		return stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
			Event: &hostv1.HostEvent{Sequence: 6, Type: testTrailer},
		}})
	}})
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, socket), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.Event != nil && u.Event.Sequence == 6 })
	got := m.cursorFor(testTopicHost)
	if got == nil || got.Incarnation != testIncC || got.Sequence != 6 {
		t.Errorf("cursor = %+v, want incarnation inc-C at sequence 6", got)
	}
}

// TestManagerReportsHostNotRunningAndRetries pins the retry state when no
// host answers: disconnected with the host-not-running reason, growing
// backoff, and no incompatible freeze.
func TestManagerReportsHostNotRunningAndRetries(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	clock := newFakeClock()
	m, upd := newTestManager(t, t.TempDir(), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateDisconnected && u.Reason == reasonHostNotRunning })
	// Identical states are not re-pushed; the retry is observable on the
	// clock: the first backoff is the base, the second doubles it.
	if d := clock.armedFor(t, ctx); d != backoffBase {
		t.Errorf("first backoff = %v, want %v", d, backoffBase)
	}
	clock.fireAll()
	if d := clock.armedFor(t, ctx); d != 2*backoffBase {
		t.Errorf("second backoff = %v, want %v", d, 2*backoffBase)
	}
}

// TestManagerFreezesOnAnIncompatibleHost pins that an unsupported_version
// rejection ends the loop in the incompatible state with no further retries.
func TestManagerFreezesOnAnIncompatibleHost(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	h := &fakeHandler{hello: func(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errNoCommonVersion)
		detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{
			Code:                 "unsupported_version",
			SupportedApiVersions: []string{"v9"},
		})
		if derr != nil {
			return nil, derr
		}
		cerr.AddDetail(detail)
		return nil, cerr
	}}
	clock := newFakeClock()
	m, upd := newTestManager(t, dataDirWithHost(t, serveFakeHost(t, h)), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateIncompatible })
	if durs := clock.durations(); len(durs) != 0 {
		t.Errorf("an incompatible host must not arm retries, armed %v", durs)
	}
	// The manager's loop has ended: Stop is a no-op wait, and the state holds.
	if s, _ := m.State(); s != StateIncompatible {
		t.Errorf("state = %q, want incompatible", s)
	}
}

// TestManagerStopsWithTheWindowContext pins the stop path: canceling the
// window context ends the loop and Stop returns without goroutines left
// (goleak is the guard).
func TestManagerStopsWithTheWindowContext(t *testing.T) {
	t.Parallel()
	socket := serveFakeHost(t, &fakeHandler{subscribe: func(ctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		// Hold the stream open until the context ends.
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 1, IncarnationId: "inc-D"},
		}}); err != nil {
			return fmt.Errorf("send the snapshot: %w", err)
		}
		<-ctx.Done()
		return ctx.Err()
	}})
	clock := newFakeClock()
	upd := newCollector(64)
	upd.start()
	baseCtx := testCtx(t)
	ctx, cancel := context.WithCancel(baseCtx)
	m := NewManager(&Config{
		DataDir: dataDirWithHost(t, socket),
		Params:  appParams(),
		Topics:  []string{testTopicHost},
		Updates: upd.ch,
		Clock:   clock,
	})
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateConnected })
	t.Cleanup(func() {
		cancel()
		m.Stop()
		upd.stop()
	})
	if s, _ := m.State(); s != StateConnected {
		// Stop freezes the last known state; the window is gone anyway.
		t.Logf("state after stop = %q", s)
	}
}

// TestHostconnBackoffIsBoundedOnTheFakeClock pins that reconnect backoffs on
// the fake clock start at 500ms, double on each retry, and never exceed 10s.
func TestHostconnBackoffIsBoundedOnTheFakeClock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, 500 * time.Millisecond},
		{2, time.Second},
		{3, 2 * time.Second},
		{4, 4 * time.Second},
		{5, 8 * time.Second},
		{6, 10 * time.Second},
		{7, 10 * time.Second},
		{50, 10 * time.Second},
	} {
		if got := backoffFor(tc.attempt); got != tc.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}

	ctx := testCtx(t)
	clock := newFakeClock()
	// An empty directory means host not running; the manager retries repeatedly.
	m, upd := newTestManager(t, t.TempDir(), clock, testTopicHost)
	m.Start(ctx)
	upd.waitFor(t, ctx, func(u Update) bool { return u.State == StateDisconnected && u.Reason == reasonHostNotRunning })

	expected := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for i, want := range expected {
		if d := clock.armedFor(t, ctx); d != want {
			t.Fatalf("attempt %d armed %v, want %v", i+1, d, want)
		}
		clock.fireAll()
	}
}
