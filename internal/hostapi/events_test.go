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
)

// streamCtx bounds a subscription's lifetime so a regression fails the test in
// seconds instead of hanging until the package timeout.
func streamCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func subscribeRequest(topic string) *connect.Request[hostv1.SubscribeRequest] {
	return connect.NewRequest(&hostv1.SubscribeRequest{Topic: topic})
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
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest("things"))
	if err != nil {
		t.Fatal(err)
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
		ev := recv(t, stream).GetEvent()
		if ev == nil || ev.Sequence != uint64(i)+1 || string(ev.Payload) != want {
			t.Fatalf("event %d = %+v, want seq %d with payload %q", i, ev, i+1, want)
		}
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
	var snapState hostStateShape
	if err := json.Unmarshal(recv(t, stream).GetSnapshot().GetState(), &snapState); err != nil {
		t.Fatal(err)
	}
	if snapState.Clients.Total != 1 || snapState.Draining {
		t.Fatalf("host snapshot = %+v, want one client and no drain", snapState)
	}
	// A second client's hello and its departure are both published.
	second, secondHTTP := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := second.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	msg := recv(t, stream).GetEvent()
	if msg == nil || msg.Type != app.EventHostClients {
		t.Fatalf("message = %+v, want a %s event", msg, app.EventHostClients)
	}
	var evState hostStateShape
	if err := json.Unmarshal(msg.Payload, &evState); err != nil {
		t.Fatal(err)
	}
	if evState.Clients.Total != 2 {
		t.Fatalf("clients event = %+v, want two clients", evState)
	}
	secondHTTP.CloseIdleConnections()
	if err := json.Unmarshal(recv(t, stream).GetEvent().Payload, &evState); err != nil {
		t.Fatal(err)
	}
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
	if _, err := client.Shutdown(streamCtx(t), connect.NewRequest(&hostv1.ShutdownRequest{CommandId: "cmd-1"})); err != nil {
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
	// A client whose raw connection the test holds: closing it is what a
	// crashed client's process death looks like to the host.
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
		c, err := d.DialContext(ctx, "unix", srv.cfg.Socket.Path)
		if err != nil {
			return nil, err
		}
		connMu.Lock()
		conn = c
		connMu.Unlock()
		return c, nil
	}
	httpClient := &http.Client{Transport: tr}
	t.Cleanup(httpClient.CloseIdleConnections)
	client := hostv1connect.NewHostServiceClient(httpClient, "http://fugue")
	if _, err := client.Hello(streamCtx(t), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Subscribe(streamCtx(t), subscribeRequest("things"))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, stream)
	// The connection dies: the stream's context is canceled, the handler
	// unsubscribes and returns, and the lease is released.
	connMu.Lock()
	c := conn
	connMu.Unlock()
	if c == nil {
		t.Fatal("no connection was dialed")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.released:
	case <-t.Context().Done():
		t.Fatal("the lease was not released after the connection died")
	}
	// A burst of publishes afterwards never blocks on the gone subscriber.
	for i := 0; i < 3000; i++ {
		if _, err := backend.hub.Publish("things", app.Event{Type: "e", Payload: []byte("p")}); err != nil {
			t.Fatal(err)
		}
	}
}
