package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	hostv1 "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1"
	"github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1/hostv1connect"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// streamCtx bounds a subscription's lifetime so a regression fails the test in
// seconds instead of hanging until the package timeout.
func streamCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newCapturingClient returns a client whose single raw connection the test
// can close directly — what a crashed client's process death looks like to
// the host.
func newCapturingClient(t *testing.T, sockPath string) (hostv1connect.HostServiceClient, func()) {
	t.Helper()
	var (
		connMu sync.Mutex
		conn   net.Conn
	)
	tr := &http.Transport{}
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	tr.Protocols = &protos
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", sockPath)
		if err != nil {
			return nil, errcode.Wrap(errcode.Internal, "dial the test socket", err)
		}
		connMu.Lock()
		conn = c
		connMu.Unlock()
		return c, nil
	}
	httpClient := &http.Client{Transport: tr}
	t.Cleanup(httpClient.CloseIdleConnections)
	closeConn := func() {
		connMu.Lock()
		defer connMu.Unlock()
		if conn == nil {
			t.Error("no connection was dialed")
			return
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}
	return hostv1connect.NewHostServiceClient(httpClient, "http://fugue"), closeConn
}

func subscribeRequest(topic string) *connect.Request[hostv1.SubscribeRequest] {
	return connect.NewRequest(&hostv1.SubscribeRequest{Topic: topic, Cursor: nil})
}

// recv advances a client stream one message, failing the test on an error.
func recv(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse]) *hostv1.SubscribeResponse {
	t.Helper()
	if !stream.Receive() {
		t.Fatalf("receive failed: %v", stream.Err())
	}
	return stream.Msg()
}

func TestSubscribeBeforeHelloIsRejected(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest("host"))
	if err != nil {
		t.Fatal(err)
	}
	if stream.Receive() {
		t.Fatal("Subscribe before Hello succeeded")
	}
	if connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
		t.Fatalf("Subscribe before Hello = %v, want permission_denied", stream.Err())
	}
}

func TestUnauthorizedTopicIsDenied(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest("not-registered"))
	if err != nil {
		t.Fatal(err)
	}
	if stream.Receive() {
		t.Fatal("an unregistered topic subscribed")
	}
	if connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
		t.Fatalf("unregistered topic = %v, want permission_denied", stream.Err())
	}
}

func TestSubscribeStreamsSnapshotAndEventsOverTheSocket(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	err := backend.hub.RegisterEphemeral("things", func(context.Context) ([]byte, error) {
		return []byte(`"state"`), nil
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_TUI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, serr := client.Subscribe(streamCtx(t), subscribeRequest("things"))
	if serr != nil {
		t.Fatal(serr)
	}
	// The snapshot arrives first; publishing after it is observed cannot race
	// the subscriber's registration.
	snap := recv(t, stream).GetSnapshot()
	if snap == nil || snap.Sequence != 0 || string(snap.State) != `"state"` {
		t.Fatalf("first message = %+v, want the snapshot at 0", snap)
	}
	if _, err := backend.hub.Publish("things", app.Event{Type: "e", Payload: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.hub.Publish("things", app.Event{Type: "e", Payload: []byte("two")}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"one", "two"} {
		expectEvent(t, stream, uint64(i)+1, want)
	}
}

// expectEvent reads one event message and checks its sequence and payload.
func expectEvent(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], seq uint64, payload string) {
	t.Helper()
	ev := recv(t, stream).GetEvent()
	if ev == nil || ev.Sequence != seq || string(ev.Payload) != payload {
		t.Fatalf("event = %+v, want seq %d with payload %q", ev, seq, payload)
	}
}

func TestHostTopicLeaseEventsOverTheSocket(t *testing.T) {
	t.Parallel()
	hp, srv, _ := hostWithServer(t, app.HostConfig{HostVersion: testHostE2EVersion, APIVersions: []string{"v1"}}, nil)
	_ = hp
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest(app.TopicHost))
	if err != nil {
		t.Fatal(err)
	}
	snapState := hostStateOf(t, recv(t, stream))
	if snapState.Clients.Total != 1 || snapState.Draining {
		t.Fatalf("host snapshot = %+v, want one client and no drain", snapState)
	}
	// A second client's hello and its departure are both published.
	second, secondHTTP := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := second.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	msg := recv(t, stream)
	if msg.GetEvent() == nil || msg.GetEvent().Type != app.EventHostClients {
		t.Fatalf("message = %+v, want a %s event", msg, app.EventHostClients)
	}
	evState := hostStateOf(t, msg)
	if evState.Clients.Total != 2 {
		t.Fatalf("clients event = %+v, want two clients", evState)
	}
	secondHTTP.CloseIdleConnections()
	evState = hostStateOf(t, recv(t, stream))
	if evState.Clients.Total != 1 {
		t.Fatalf("clients event after departure = %+v, want one client", evState)
	}
}

// hostStateShape mirrors the app package's host topic payload.
type hostStateShape struct {
	Clients struct {
		Total uint32 `json:"total"`
	} `json:"clients"`
	Draining bool `json:"draining"`
}

// hostStateOf parses one message's host-topic payload (snapshot state or
// event payload).
func hostStateOf(t *testing.T, msg *hostv1.SubscribeResponse) hostStateShape {
	t.Helper()
	var state hostStateShape
	payload := msg.GetSnapshot().GetState()
	if payload == nil {
		payload = msg.GetEvent().GetPayload()
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestDrainEndsStreamsCleanly(t *testing.T) {
	t.Parallel()
	hp, srv, _ := hostWithServer(t, app.HostConfig{HostVersion: testHostE2EVersion, APIVersions: []string{"v1"}}, nil)
	_ = hp
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest(app.TopicHost))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, stream) // the snapshot
	// The Shutdown RPC starts the drain: the draining event is published, the
	// stream is then ended cleanly, and the drain completes while it is open.
	if _, err := client.Shutdown(streamCtx(t), connect.NewRequest(&hostv1.ShutdownRequest{CommandId: testCommandID, Force: false})); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, stream).GetEvent(); got == nil || got.Type != app.EventHostDraining {
		t.Fatalf("message = %+v, want the %s event", got, app.EventHostDraining)
	}
	end := recv(t, stream).GetEnd()
	if end == nil || end.ResyncRequired {
		t.Fatalf("message = %+v, want a clean stream end", end)
	}
	if stream.Receive() {
		t.Fatal("the stream continued after its end")
	}
	if stream.Err() != nil && !errors.Is(stream.Err(), io.EOF) {
		t.Fatalf("receive after the end = %v, want io.EOF", stream.Err())
	}
	<-hp.DrainDone()
}

func TestSubscribeStreamsEndWhenTheClientDisconnects(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	err := backend.hub.RegisterEphemeral("things", func(context.Context) ([]byte, error) {
		return []byte(`"state"`), nil
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, shortDir(t), backend)
	client, closeConn := newCapturingClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, serr := client.Subscribe(streamCtx(t), subscribeRequest("things"))
	if serr != nil {
		t.Fatal(serr)
	}
	recv(t, stream)
	// The connection dies: the stream's context is canceled, the handler
	// unsubscribes and returns, and the lease is released.
	closeConn()
	select {
	case <-backend.released:
	case <-t.Context().Done():
		t.Fatal("the lease was not released after the connection died")
	}
	// A burst of publishes afterwards never blocks on the gone subscriber.
	for range 3000 {
		if _, err := backend.hub.Publish("things", app.Event{Type: "e", Payload: []byte("p")}); err != nil {
			t.Fatal(err)
		}
	}
}
