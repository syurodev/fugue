package client_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	testTopic   = "host"
	testTrailer = "clients"
	testVer     = "1.2.3"
)

// errNoSubscriptionScripted is the static rejection a fake host gives when a
// test did not script a subscription; it is wrapped, never constructed, at the
// call site.
var errNoSubscriptionScripted = errors.New("no subscription scripted")

// errNoCommonVersion is the static rejection behind the version-mismatch
// fixture; it is wrapped, never constructed, at the call site.
var errNoCommonVersion = errors.New("no common version")

// errNoConfigDir is the static failure behind a user-config-dir resolver
// that finds nothing; it is wrapped, never constructed, at the call site.
var errNoConfigDir = errors.New("no config dir")

// closer is anything test teardown closes.
type closer interface{ Close() error }

// removeQuietly removes dir during test teardown, where nothing can act on
// the error (the same teardown rule internal tests follow).
func removeQuietly(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		return
	}
}

// closeQuietly closes c during test teardown, where nothing can act on the
// error (the same teardown rule internal tests follow).
func closeQuietly(c closer) {
	if err := c.Close(); err != nil {
		return
	}
}

// shortDir creates a directory whose path is short enough to bind a unix
// socket, which t.TempDir() cannot promise on macOS: sun_path holds 104 bytes
// and macOS test temp dirs already exceed 100 (the same helper internal/hostapi
// needs). Each call gets a unique directory.
func shortDir(t *testing.T) string {
	t.Helper()
	// The test name and the process ID separate parallel tests and
	// concurrent test binaries; the index separates the calls of one test,
	// such as a host restarted on a new directory. Mkdir fails on an existing
	// path, so a later call, a hash collision or a leftover from a crashed
	// run moves on to the next index instead of sharing a directory.
	sum := sha256.Sum256([]byte(t.Name()))
	var dir string
	for index := 0; ; index++ {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("fg-%x-%d-%d", sum[:4], os.Getpid(), index))
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || index == 100 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { removeQuietly(dir) })
	return dir
}

// testCtx bounds a test's waits: the waits themselves are channel- and
// deadline-driven; the 60 s deadline is only the safety net.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// fakeHandler serves scripted Hello and Subscribe replies for one test.
type fakeHandler struct {
	hostv1connect.UnimplementedHostServiceHandler
	hello     func(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error)
	status    func(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error)
	subscribe func(context.Context, *connect.Request[hostv1.SubscribeRequest], *connect.ServerStream[hostv1.SubscribeResponse]) error
	helloed   chan<- *hostv1.HelloRequest
}

func (h *fakeHandler) HostStatus(ctx context.Context, req *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
	if h.status != nil {
		return h.status(ctx, req)
	}
	return nil, connect.NewError(connect.CodePermissionDenied, errNoSubscriptionScripted)
}

func (h *fakeHandler) Hello(ctx context.Context, req *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	if h.helloed != nil {
		h.helloed <- req.Msg
	}
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

// sendEvent sends one scripted event on the stream.
func sendEvent(stream *connect.ServerStream[hostv1.SubscribeResponse], seq uint64, typ string) error {
	err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Event{
		Event: &hostv1.HostEvent{Sequence: seq, Type: typ},
	}})
	if err != nil {
		return fmt.Errorf("send the event %d: %w", seq, err)
	}
	return nil
}

// serveHost runs a fake HostService on a fresh unix socket over unencrypted
// HTTP/2, exactly the transport shape the real host serves (F5.1). The
// listener is served by httptest, so no goroutine is started here.
func serveHost(t *testing.T, h hostv1connect.HostServiceHandler) string {
	t.Helper()
	socket, _ := serveHostCounting(t, h)
	return socket
}

func appParams() client.Params {
	return client.Params{
		Kind:        client.KindApp,
		Name:        "fugue-app-test",
		Version:     "0",
		APIVersions: []string{"v1"},
	}
}

// dialApp dials the scripted host and registers the client for teardown.
func dialApp(t *testing.T, socket string) *client.Client {
	t.Helper()
	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestDialSaysHelloWithTheClientKind pins that Dial opens the connection and
// completes the hello with the caller's kind and versions.
func TestDialSaysHelloWithTheClientKind(t *testing.T) {
	t.Parallel()
	helloed := make(chan *hostv1.HelloRequest, 1)
	socket := serveHost(t, &fakeHandler{helloed: helloed})
	dialApp(t, socket)
	select {
	case got := <-helloed:
		if got.GetClientKind() != hostv1.ClientKind_CLIENT_KIND_APP {
			t.Errorf("hello kind = %v, want APP", got.GetClientKind())
		}
		if got.GetClientName() != "fugue-app-test" {
			t.Errorf("hello name = %q, want fugue-app-test", got.GetClientName())
		}
		if len(got.GetSupportedApiVersions()) != 1 || got.GetSupportedApiVersions()[0] != "v1" {
			t.Errorf("hello versions = %v, want [v1]", got.GetSupportedApiVersions())
		}
	default:
		t.Error("the hello never reached the host")
	}
}

// TestDialUnknownKindFails pins that an unknown kind fails before anything
// touches the socket.
func TestDialUnknownKindFails(t *testing.T) {
	t.Parallel()
	socket := serveHost(t, &fakeHandler{})
	if _, err := client.Dial(t.Context(), socket, client.Params{Kind: "ghost"}); err == nil {
		t.Fatal("an unknown kind dialed anyway")
	}
}

// TestDialVersionMismatchIsTyped pins Q-26 B on the client side: the host's
// unsupported_version rejection arrives as a VersionMismatchError carrying the
// host's supported versions.
func TestDialVersionMismatchIsTyped(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{hello: func(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errNoCommonVersion)
		detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{
			Code:                 "unsupported_version",
			SupportedApiVersions: []string{"v1", "v2"},
		})
		if derr != nil {
			return nil, fmt.Errorf("build the detail: %w", derr)
		}
		cerr.AddDetail(detail)
		return nil, cerr
	}}
	socket := serveHost(t, h)
	_, err := client.Dial(t.Context(), socket, appParams())
	var mismatch *client.VersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("dial err = %v, want *client.VersionMismatchError", err)
	}
	if got, want := mismatch.HostAPIVersions, []string{"v1", "v2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("host versions = %v, want %v", got, want)
	}
}

// TestReadHostFileMissingOrMalformed pins which discovery-file shapes read as
// "no host" and which read as a broken file.
func TestReadHostFileMissingOrMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := client.ReadHostFile(dir); !errors.Is(err, client.ErrHostNotRunning) {
		t.Errorf("absent file: err = %v, want ErrHostNotRunning", err)
	}
	if _, err := client.ReadHostFile(filepath.Join(dir, "no-such-dir")); !errors.Is(err, client.ErrHostNotRunning) {
		t.Errorf("absent directory: err = %v, want ErrHostNotRunning", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadHostFile(dir); err == nil || errors.Is(err, client.ErrHostNotRunning) {
		t.Errorf("malformed file: err = %v, want a parse error that is not ErrHostNotRunning", err)
	}
}

// TestReadHostFileShape covers a written discovery file and the parsed but
// unusable shape without a socket path.
func TestReadHostFileShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"pid":12,"started_at":"2026-11-08T10:00:00Z","socket_path":"/run/fugue/fugue.sock","api_versions":["v1"],"host_version":"1.2.3"}`
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	hf, err := client.ReadHostFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hf.PID != 12 || hf.SocketPath != "/run/fugue/fugue.sock" || hf.HostVersion != testVer ||
		len(hf.APIVersions) != 1 || hf.APIVersions[0] != "v1" || hf.StartedAt.IsZero() {
		t.Errorf("host file = %+v, want the written shape", hf)
	}
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "host.json"), []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadHostFile(empty); err == nil || errors.Is(err, client.ErrHostNotRunning) {
		t.Errorf("socket-less file: err = %v, want a parse error that is not ErrHostNotRunning", err)
	}
}

// recvSnapshot consumes the scripted snapshot and pins its shape.
func recvSnapshot(t *testing.T, stream *client.Stream) *client.Snapshot {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv the snapshot: %v", err)
	}
	if msg.Snapshot == nil {
		t.Fatalf("first message is no snapshot: %+v", msg)
	}
	return msg.Snapshot
}

// recvEvent consumes one scripted event and pins its shape.
func recvEvent(t *testing.T, stream *client.Stream, want uint64) {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv the event %d: %v", want, err)
	}
	if msg.Event == nil {
		t.Fatalf("message %d is no event: %+v", want, msg)
	}
	if msg.Event.Sequence != want || msg.Event.Type != testTrailer {
		t.Errorf("event = %+v, want sequence %d of type %s", msg.Event, want, testTrailer)
	}
}

// TestSubscribeStreamsSnapshotAndEventsAndTypedEnds covers the stream types:
// a snapshot, events in order, a clean end and a resync end.
func TestSubscribeStreamsSnapshotAndEventsAndTypedEnds(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{subscribe: func(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		if got := req.Msg.GetTopic(); got != testTopic {
			t.Errorf("topic = %q, want %s", got, testTopic)
		}
		if req.Msg.GetCursor() != nil {
			t.Error("a nil cursor must reach the host as no cursor")
		}
		if err := stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_Snapshot{
			Snapshot: &hostv1.SnapshotEvent{Sequence: 4, State: []byte("state"), IncarnationId: "inc-1"},
		}}); err != nil {
			return fmt.Errorf("send the snapshot: %w", err)
		}
		for _, seq := range []uint64{5, 6} {
			if err := sendEvent(stream, seq, testTrailer); err != nil {
				return err
			}
		}
		return stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_End{
			End: &hostv1.StreamEnd{ResyncRequired: false},
		}})
	}}
	socket := serveHost(t, h)
	c := dialApp(t, socket)
	stream, err := c.Subscribe(t.Context(), testTopic, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(stream) })

	snap := recvSnapshot(t, stream)
	if snap.Sequence != 4 || string(snap.State) != "state" || snap.Incarnation != "inc-1" {
		t.Errorf("snapshot = %+v, want the scripted one", snap)
	}
	recvEvent(t, stream, 5)
	recvEvent(t, stream, 6)
	if _, err := stream.Recv(); !errors.Is(err, client.ErrStreamEnded) {
		t.Errorf("clean end: err = %v, want ErrStreamEnded", err)
	}
}

// TestSubscribeCarriesTheCursor pins that a cursor survives the round trip,
// which is what a reconnect's catch-up depends on, and that a resync end is
// typed.
func TestSubscribeCarriesTheCursor(t *testing.T) {
	t.Parallel()
	got := make(chan *hostv1.EventCursor, 1)
	h := &fakeHandler{subscribe: func(_ context.Context, req *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		got <- req.Msg.GetCursor()
		return stream.Send(&hostv1.SubscribeResponse{Kind: &hostv1.SubscribeResponse_End{
			End: &hostv1.StreamEnd{ResyncRequired: true},
		}})
	}}
	socket := serveHost(t, h)
	c := dialApp(t, socket)
	cursor := &client.Cursor{Topic: testTopic, Incarnation: "inc-9", Sequence: 41}
	stream, err := c.Subscribe(t.Context(), testTopic, cursor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(stream) })
	if _, err := stream.Recv(); !errors.Is(err, client.ErrResyncRequired) {
		t.Errorf("resync end: err = %v, want ErrResyncRequired", err)
	}
	// The handler sent before it ended the stream, so the cursor is here.
	wc := <-got
	if wc.GetTopic() != testTopic || wc.GetIncarnationId() != "inc-9" || wc.GetSequence() != 41 {
		t.Errorf("cursor = %+v, want %+v", wc, cursor)
	}
}

// TestStreamCloseEndsWithoutTheHostEndingIt pins the F5.2 review behavior: a
// stream abandoned before its end is closed without error and without
// goroutines left behind (goleak is the guard).
func TestStreamCloseEndsWithoutTheHostEndingIt(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{subscribe: func(ctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		// Send one event so Subscribe's headers arrive, then hold the stream
		// open until the client's context ends.
		if err := sendEvent(stream, 1, testTrailer); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	socket := serveHost(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, err := client.Dial(ctx, socket, appParams())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	stream, err := c.Subscribe(ctx, testTopic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestDialCarriesEveryClientKind pins the kind mapping of host-api §8: every
// kind the client exports reaches the host, and the host sees it verbatim.
func TestDialCarriesEveryClientKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind client.Kind
		want hostv1.ClientKind
	}{
		{client.KindTUI, hostv1.ClientKind_CLIENT_KIND_TUI},
		{client.KindCLI, hostv1.ClientKind_CLIENT_KIND_CLI},
		{client.KindApp, hostv1.ClientKind_CLIENT_KIND_APP},
		{client.KindAppBackground, hostv1.ClientKind_CLIENT_KIND_APP_BACKGROUND},
	}
	for _, tc := range cases {
		helloed := make(chan *hostv1.HelloRequest, 1)
		socket := serveHost(t, &fakeHandler{helloed: helloed})
		p := appParams()
		p.Kind = tc.kind
		if _, err := client.Dial(t.Context(), socket, p); err != nil {
			t.Fatalf("dial as %s: %v", tc.kind, err)
		}
		if got := (<-helloed).GetClientKind(); got != tc.want {
			t.Errorf("hello kind for %s = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestVersionMismatchMessageNamesTheVersions pins the typed error's message:
// it shows the host's versions so a caller can tell "host older" from "host
// newer" (cli §2.3).
func TestVersionMismatchMessageNamesTheVersions(t *testing.T) {
	t.Parallel()
	m := &client.VersionMismatchError{HostAPIVersions: []string{"v1", "v2"}}
	if got := m.Error(); got == "" || !strings.Contains(got, "v1") || !strings.Contains(got, "v2") {
		t.Errorf("message = %q, want it to name the versions", got)
	}
	empty := &client.VersionMismatchError{}
	if got := empty.Error(); got == "" {
		t.Error("the versionless message is empty")
	}
}

// TestRecvRejectsAMessageWithoutKind pins the malformed-stream guard.
func TestRecvRejectsAMessageWithoutKind(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{subscribe: func(_ context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		return stream.Send(&hostv1.SubscribeResponse{})
	}}
	socket := serveHost(t, h)
	c := dialApp(t, socket)
	stream, err := c.Subscribe(t.Context(), testTopic, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(stream) })
	if _, err := stream.Recv(); !errors.Is(err, client.ErrStreamMalformed) {
		t.Errorf("recv a kindless message: err = %v, want ErrStreamMalformed", err)
	}
}

// TestRecvSurfacesAContextGone pins that a canceled context ends Recv with the
// transport error, not with a typed stream end.
func TestRecvSurfacesAContextGone(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{subscribe: func(ctx context.Context, _ *connect.Request[hostv1.SubscribeRequest], stream *connect.ServerStream[hostv1.SubscribeResponse]) error {
		if err := sendEvent(stream, 1, testTrailer); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	socket := serveHost(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	c, err := client.Dial(ctx, socket, appParams())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stream, err := c.Subscribe(ctx, testTopic, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	msg, rerr := stream.Recv()
	if rerr != nil || msg.Event == nil {
		cancel()
		t.Fatalf("first recv: %+v, %v", msg, rerr)
	}
	cancel()
	if _, err := stream.Recv(); err == nil || errors.Is(err, client.ErrStreamEnded) || errors.Is(err, client.ErrResyncRequired) {
		cancel()
		t.Errorf("recv after the context is gone: err = %v, want a transport error", err)
	}
	closeQuietly(stream)
	c.Close()
}

// TestDialRejectsAnUnrelatedPrecondition pins that a failed_precondition
// without an unsupported_version detail is not misread as a version mismatch.
func TestDialRejectsAnUnrelatedPrecondition(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{hello: func(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errNoCommonVersion)
	}}
	socket := serveHost(t, h)
	_, err := client.Dial(t.Context(), socket, appParams())
	var mismatch *client.VersionMismatchError
	if errors.As(err, &mismatch) {
		t.Fatalf("dial err = %v, want a plain connect error", err)
	}
	if err == nil {
		t.Fatal("a rejected hello dialed anyway")
	}
}

// TestHostStatusCarriesTheHostIdentity pins the status read the version
// handling (cli §2.3) builds on: versions, incarnation, and the typed wrap on
// a host that rejects it.
func TestHostStatusCarriesTheHostIdentity(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{status: func(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
		return connect.NewResponse(&hostv1.HostStatusResponse{
			HostVersion:          "1.2.3",
			SupportedApiVersions: []string{"v1"},
			IncarnationId:        "inc-status",
		}), nil
	}}
	socket := serveHost(t, h)
	c := dialApp(t, socket)
	status, err := c.HostStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.HostVersion != testVer || status.Incarnation != "inc-status" ||
		len(status.SupportedAPIVersions) != 1 || status.SupportedAPIVersions[0] != "v1" {
		t.Errorf("status = %+v, want the scripted shape", status)
	}
	plain := &fakeHandler{}
	plainSocket := serveHost(t, plain)
	plainClient := dialApp(t, plainSocket)
	if _, err := plainClient.HostStatus(t.Context()); err == nil {
		t.Error("a host that rejects the status read succeeded anyway")
	}
}

func TestHostStatusReportsClientCounts(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{status: func(context.Context, *connect.Request[hostv1.HostStatusRequest]) (*connect.Response[hostv1.HostStatusResponse], error) {
		return connect.NewResponse(&hostv1.HostStatusResponse{Clients: &hostv1.ClientCounts{App: 1, Cli: 2, Total: 3}}), nil
	}}
	c := dialApp(t, serveHost(t, h))
	status, err := c.HostStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.Clients.App != 1 || status.Clients.CLI != 2 || status.Clients.Total != 3 {
		t.Fatalf("client counts = %+v", status.Clients)
	}
}

type shutdownHandler struct {
	fakeHandler
	commands chan *hostv1.ShutdownRequest
}

func (h *shutdownHandler) Shutdown(_ context.Context, req *connect.Request[hostv1.ShutdownRequest]) (*connect.Response[hostv1.ShutdownResponse], error) {
	h.commands <- req.Msg
	return connect.NewResponse(&hostv1.ShutdownResponse{}), nil
}

func TestShutdownUsesHostAPI(t *testing.T) {
	t.Parallel()
	h := &shutdownHandler{commands: make(chan *hostv1.ShutdownRequest, 1)}
	c := dialApp(t, serveHost(t, h))
	if err := c.Shutdown(t.Context(), "upgrade-1"); err != nil {
		t.Fatal(err)
	}
	cmd := <-h.commands
	if cmd.GetCommandId() != "upgrade-1" || cmd.GetForce() {
		t.Fatalf("unexpected shutdown request: %+v", cmd)
	}
}

func TestShutdownReportsHostRejection(t *testing.T) {
	t.Parallel()
	c := dialApp(t, serveHost(t, &fakeHandler{}))
	if err := c.Shutdown(t.Context(), "upgrade-rejected"); err == nil {
		t.Fatal("host rejection was ignored")
	}
}
