package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
)

// fakeHost answers the Hello a Dial says; the vendor calls are what the
// poster delivers.
type fakeHost struct {
	hostv1connect.UnimplementedHostServiceHandler
}

func (fakeHost) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{}), nil
}

// shortDir is the socket's directory: short enough for the unix socket path,
// which t.TempDir() cannot promise on macOS — sun_path holds 104 bytes and
// macOS test temp dirs already exceed 100.
func shortDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fg-%x-%d", sum[:4], os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			return // nothing the test could do; the temp dir is disposable
		}
	})
	return dir
}

// sessionStarted is the state the fake service answers with; one literal
// here, so the package's goconst count stays quiet.
const sessionStarted = "starting"

// capturedCall is one vendor RPC the fake service recorded.
type capturedCall struct {
	method, commandID, session, text string
}

// fakeVendors records the state-changing calls the poster delivers and
// answers them with the session the request named.
type fakeVendors struct {
	vendorsv1connect.UnimplementedVendorServiceHandler
	mu    sync.Mutex
	calls []capturedCall
}

func (f *fakeVendors) record(method string, req *connect.Request[vendorsv1.SendRequest]) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: method, commandID: req.Msg.CommandId, session: req.Msg.SessionId, text: req.Msg.Text})
}

func (f *fakeVendors) Send(ctx context.Context, req *connect.Request[vendorsv1.SendRequest]) (*connect.Response[vendorsv1.SendResponse], error) {
	f.record("send", req)
	return connect.NewResponse(&vendorsv1.SendResponse{}), nil
}

func (f *fakeVendors) Steer(ctx context.Context, req *connect.Request[vendorsv1.SteerRequest]) (*connect.Response[vendorsv1.SteerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: "steer", commandID: req.Msg.CommandId, session: req.Msg.SessionId, text: req.Msg.Text})
	return connect.NewResponse(&vendorsv1.SteerResponse{}), nil
}

func (f *fakeVendors) Interrupt(ctx context.Context, req *connect.Request[vendorsv1.InterruptRequest]) (*connect.Response[vendorsv1.InterruptResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: "interrupt", commandID: req.Msg.CommandId, session: req.Msg.SessionId})
	return connect.NewResponse(&vendorsv1.InterruptResponse{Status: vendorsv1.OperationStatus_OPERATION_STATUS_DONE}), nil
}

func (f *fakeVendors) CloseSession(ctx context.Context, req *connect.Request[vendorsv1.CloseSessionRequest]) (*connect.Response[vendorsv1.CloseSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: "close", commandID: req.Msg.CommandId, session: req.Msg.SessionId})
	return connect.NewResponse(&vendorsv1.CloseSessionResponse{Status: vendorsv1.OperationStatus_OPERATION_STATUS_DONE}), nil
}

func (f *fakeVendors) StartSession(ctx context.Context, req *connect.Request[vendorsv1.StartSessionRequest]) (*connect.Response[vendorsv1.StartSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: "start", commandID: req.Msg.CommandId, text: req.Msg.Profile.GetModel()})
	session := &vendorsv1.VendorSession{SessionId: "started-" + req.Msg.CommandId, Vendor: req.Msg.Vendor, State: sessionStarted}
	return connect.NewResponse(&vendorsv1.StartSessionResponse{Session: session}), nil
}

func (f *fakeVendors) ResumeSession(ctx context.Context, req *connect.Request[vendorsv1.ResumeSessionRequest]) (*connect.Response[vendorsv1.ResumeSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{method: "resume", commandID: req.Msg.CommandId, session: req.Msg.SessionId})
	session := &vendorsv1.VendorSession{SessionId: req.Msg.SessionId, State: sessionStarted}
	return connect.NewResponse(&vendorsv1.ResumeSessionResponse{Session: session}), nil
}

// posterHost serves the fake vendor service over a unix socket on
// unencrypted HTTP/2, the transport shape the real host serves, and dials a
// real client at it. The listener is served by httptest, so no goroutine of
// the test's own is started here.
func posterHost(t *testing.T) (*client.Client, *fakeVendors) {
	t.Helper()
	fake := &fakeVendors{}
	socket := filepath.Join(shortDir(t), "host.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fakeHost{}))
	mux.Handle(vendorsv1connect.NewVendorServiceHandler(fake))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, client.Params{Kind: client.KindApp, Name: "test", Version: "test", APIVersions: []string{"v1"}})
	if err != nil {
		t.Fatalf("dial the fake host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fake
}

// recorded returns the calls the fake service has taken so far.
func recorded(f *fakeVendors) []capturedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestPosterDeliversEveryWriteWithItsOwnCommandID posts all five writes and
// pins the contract: each runs against the current connection as its own
// command — a unique, non-empty command id per post, so the host never
// refuses two posts as one retried command — and every result names the
// write it finished.
func TestPosterDeliversEveryWriteWithItsOwnCommandID(t *testing.T) {
	t.Parallel()
	c, fake := posterHost(t)
	p := NewPoster(func() *client.Client { return c })
	p.Start(t.Context())
	defer p.Stop()

	p.Send("s1", "hello")
	p.Steer("s1", "mid-turn")
	p.Interrupt("s1")
	p.StartSession("claude", "ws", "", client.SessionProfile{Model: "sonnet"})
	p.ResumeSession("s2", client.SessionProfile{})
	p.CloseSession("s3")

	seen := map[PostKind]bool{}
	for range []int{0, 1, 2, 3, 4, 5} {
		res := <-p.Results()
		if res.Err != nil {
			t.Fatalf("post %d failed: %v", res.Kind, res.Err)
		}
		if seen[res.Kind] {
			t.Fatalf("post kind %d delivered twice", res.Kind)
		}
		seen[res.Kind] = true
	}
	if !seen[PostClose] {
		t.Fatal("the close post was never delivered")
	}
	calls := recorded(fake)
	if len(calls) != 6 {
		t.Fatalf("calls = %+v, want the six writes", calls)
	}
	ids := map[string]bool{}
	for _, call := range calls {
		if call.commandID == "" {
			t.Fatalf("%s carried no command id", call.method)
		}
		if ids[call.commandID] {
			t.Fatalf("command id %q was minted twice", call.commandID)
		}
		ids[call.commandID] = true
	}
}

// TestPosterFailsWhileDisconnectedAndKeepsTheText pins the disconnected
// rule: a post while the host is down fails at once instead of vanishing,
// and a failed send hands the user's words back for the draft restore.
func TestPosterFailsWhileDisconnectedAndKeepsTheText(t *testing.T) {
	t.Parallel()
	p := NewPoster(func() *client.Client { return nil })
	p.Start(t.Context())
	defer p.Stop()

	p.Send("s1", "the user's words")
	res := <-p.Results()
	if res.Err == nil {
		t.Fatal("a send while disconnected succeeded")
	}
	if res.Text != "the user's words" {
		t.Fatalf("failed send text = %q, want the draft back", res.Text)
	}
	if res.Session != "s1" || res.Kind != PostSend {
		t.Fatalf("result = %+v, want the send's session and kind", res)
	}
}
