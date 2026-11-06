package hostapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"
	"github.com/syurodev/fugue/internal/app"
	hostv1 "github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1"
	"github.com/syurodev/fugue/internal/hostapi/internal/pb/fugue/host/v1/hostv1connect"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// fakeBackend is a scripted app.Backend for tests that exercise the transport
// without the real runtime.
type fakeBackend struct {
	mu        sync.Mutex
	hellos    int
	leased    []string
	released  chan string
	statuses  int
	status    app.HostStatusView
	helloRes  app.HelloResult
	helloErr  error
	shutdowns []app.ShutdownCommand
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{released: make(chan string, 8)}
}

func (f *fakeBackend) Hello(_ context.Context, _ app.HelloParams) (app.HelloResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hellos++
	if f.helloErr != nil {
		return app.HelloResult{}, f.helloErr
	}
	return f.helloRes, nil
}

func (f *fakeBackend) LeaseAcquired(_ context.Context, connID string, _ app.ClientKind) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leased = append(f.leased, connID)
	return nil
}

func (f *fakeBackend) LeaseReleased(_ context.Context, connID string) {
	select {
	case f.released <- connID:
	default:
	}
}

func (f *fakeBackend) Status(_ context.Context) (app.HostStatusView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses++
	return f.status, nil
}

func (f *fakeBackend) Shutdown(_ context.Context, cmd app.ShutdownCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns = append(f.shutdowns, cmd)
	return nil
}

func (f *fakeBackend) helloCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hellos
}

func (f *fakeBackend) statusCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses
}

func (f *fakeBackend) leasedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.leased...)
}

func fakeStatus() app.HostStatusView {
	return app.HostStatusView{
		HostVersion:          "host-fake",
		SupportedAPIVersions: []string{"v1", "v2"},
		StartedAt:            time.Unix(42, 0),
	}
}

// startTestServer binds a socket in dir and serves backend; cleanup drains
// the server so no test leaves a serve goroutine behind.
func startTestServer(t *testing.T, dir string, backend app.Backend) *Server {
	t.Helper()
	sock, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(0, 0))
	srv := NewServer(ServerConfig{
		Socket:  sock,
		DataDir: dir,
		IDs:     ids.NewUUIDv7(clk, ids.NewDeterministic("conn")),
		HostUID: int64(os.Getuid()),
	})
	if err := srv.Start(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Drain(context.WithoutCancel(t.Context()), true)
	})
	return srv
}

// newTestClient connects over the unix socket with unencrypted HTTP/2, the
// way F7.1's client will.
func newTestClient(t *testing.T, sockPath string) (hostv1connect.HostServiceClient, *http.Client) {
	t.Helper()
	tr := &http.Transport{}
	var protos http.Protocols
	protos.SetUnencryptedHTTP2(true)
	tr.Protocols = &protos
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	httpClient := &http.Client{Transport: tr}
	t.Cleanup(httpClient.CloseIdleConnections)
	return hostv1connect.NewHostServiceClient(httpClient, "http://fugue"), httpClient
}

// hostWithServer wires the real runtime and the real server on a temp data
// directory. tr replaces the server as the runtime's transport when non-nil.
func hostWithServer(t *testing.T, cfg app.HostConfig, tr app.HostTransport) (*app.HostProcess, *Server, string, *clock.Fake) {
	t.Helper()
	dir := shortDir(t)
	clk := clock.NewFake(time.Unix(0, 0))
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
	if tr == nil {
		tr = srv
	}
	hp, err := app.StartHost(t.Context(), app.HostDeps{
		DataDir:   dir,
		Config:    cfg,
		Clock:     clk,
		IDs:       ids.NewUUIDv7(clk, ids.NewDeterministic("host")),
		Transport: tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(t.Context(), hp.Runtime()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Drain(context.WithoutCancel(t.Context()), true)
		_ = hp.Close()
	})
	return hp, srv, dir, clk
}

// releaseSignalBackend wraps a backend and signals every lease release, so a
// test waits for the real runtime's release without sleeping.
type releaseSignalBackend struct {
	app.Backend
	released chan struct{}
}

func (b *releaseSignalBackend) LeaseReleased(ctx context.Context, connID string) {
	b.Backend.LeaseReleased(ctx, connID)
	select {
	case b.released <- struct{}{}:
	default:
	}
}

func helloRequest(kind hostv1.ClientKind, versions ...string) *connect.Request[hostv1.HelloRequest] {
	return connect.NewRequest(&hostv1.HelloRequest{
		ClientKind:           kind,
		ClientName:           "fugue-test",
		ClientVersion:        "1.0",
		SupportedApiVersions: versions,
	})
}

func TestServerServesHelloOverTheSocket(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.helloRes = app.HelloResult{APIVersion: "v2", HostVersion: "host-fake"}
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	res, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1", "v2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.ApiVersion != "v2" || res.Msg.HostVersion != "host-fake" {
		t.Fatalf("Hello = %+v, want v2 from host-fake", res.Msg)
	}
	leased := backend.leasedIDs()
	if len(leased) != 1 || leased[0] == "" {
		t.Fatalf("LeaseAcquired calls = %v, want one non-empty connection id", leased)
	}
}

func TestSecondHelloOnTheSameConnectionIsRejected(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.helloRes = app.HelloResult{APIVersion: "v1", HostVersion: "host-fake"}
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	req := helloRequest(hostv1.ClientKind_CLIENT_KIND_TUI, "v1")
	if _, err := client.Hello(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	_, err := client.Hello(t.Context(), req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second Hello = %v, want failed_precondition", err)
	}
	if got := backend.helloCount(); got != 1 {
		t.Fatalf("backend called %d times, want 1", got)
	}
}

func TestRPCBeforeHelloIsRejected(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	statusesBefore := backend.statusCount() // Start reads the status for host.json
	_, err := client.HostStatus(t.Context(), connect.NewRequest(&hostv1.HostStatusRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("HostStatus before Hello = %v, want permission_denied", err)
	}
	if backend.statusCount() != statusesBefore || backend.helloCount() != 0 {
		t.Fatalf("the backend served the rpc before hello (statuses %d, hellos %d)", backend.statusCount(), backend.helloCount())
	}
}

func TestClientKindUnspecifiedIsRejected(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	_, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_UNSPECIFIED, "v1"))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Hello without client_kind = %v, want invalid_argument", err)
	}
	if got := backend.helloCount(); got != 0 {
		t.Fatalf("backend called %d times, want 0", got)
	}
}

func TestConnectionCloseReleasesTheLease(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, httpClient := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	leased := backend.leasedIDs()
	if len(leased) != 1 {
		t.Fatalf("leased = %v, want one connection", leased)
	}
	// Closing the client's pooled connection must release the same lease.
	httpClient.CloseIdleConnections()
	select {
	case got := <-backend.released:
		if got != leased[0] {
			t.Fatalf("released connection %q, want %q", got, leased[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lease was not released after the connection closed")
	}
}

// blockingDrainTransport blocks the runtime's drain until the test releases
// it, then delegates to the inner transport.
type blockingDrainTransport struct {
	block   chan struct{}
	started chan struct{}
	once    sync.Once
	inner   app.HostTransport
}

func (b *blockingDrainTransport) Drain(ctx context.Context, force bool) error {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.block:
	case <-ctx.Done():
		return ctx.Err()
	}
	if b.inner != nil {
		return b.inner.Drain(ctx, force)
	}
	return nil
}

func TestShutdownRPCRepliesBeforeDrain(t *testing.T) {
	t.Parallel()
	tr := &blockingDrainTransport{block: make(chan struct{}), started: make(chan struct{})}
	hp, srv, dir, _ := hostWithServer(t, app.HostConfig{HostVersion: "host-e2e", APIVersions: []string{"v1"}}, tr)
	tr.inner = srv
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	// The reply arrives while the drain is still blocked on the transport.
	if _, err := client.Shutdown(t.Context(), connect.NewRequest(&hostv1.ShutdownRequest{CommandId: "cmd-1"})); err != nil {
		t.Fatal(err)
	}
	<-tr.started
	// The discovery file is untouched while the drain waits: the RPC replies
	// first, the drain runs asynchronously.
	if _, err := os.Stat(HostFilePath(dir)); err != nil {
		t.Fatalf("host.json disappeared before the drain ran: %v", err)
	}
	close(tr.block)
	<-hp.DrainDone()
	if _, err := os.Stat(HostFilePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host.json survived the drain: %v", err)
	}
}

func TestHostJSONRemovedOnCleanDrain(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	srv := startTestServer(t, dir, newFakeBackend())
	if _, err := os.Stat(HostFilePath(dir)); err != nil {
		t.Fatalf("host.json missing after start: %v", err)
	}
	if err := srv.Drain(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(HostFilePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host.json survived the clean drain: %v", err)
	}
	// A second drain is the same no-op reply.
	if err := srv.Drain(t.Context(), false); err != nil {
		t.Fatalf("second Drain = %v, want nil", err)
	}
}

func TestDrainStopsServing(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	if err := srv.Drain(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(srv.cfg.Socket.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file survived the drain: %v", err)
	}
	conn, err := net.DialTimeout("unix", srv.cfg.Socket.Path, time.Second)
	if err == nil {
		closeQuietly(conn)
		t.Fatal("a new dial succeeded after the drain")
	}
	select {
	case <-backend.released:
	case <-time.After(5 * time.Second):
		t.Fatal("the drained connection's lease was not released")
	}
}

func TestShutdownRPCEndToEndWithCleanTeardown(t *testing.T) {
	t.Parallel()
	hp, srv, dir, _ := hostWithServer(t, app.HostConfig{HostVersion: "host-e2e", APIVersions: []string{"v1"}}, nil)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Shutdown(t.Context(), connect.NewRequest(&hostv1.ShutdownRequest{CommandId: "cmd-1"})); err != nil {
		t.Fatal(err)
	}
	<-hp.DrainDone()
	if _, err := os.Stat(HostFilePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host.json survived the shutdown: %v", err)
	}
	// The store closed last: the lock is released.
	assertDataDirLockReleased(t, dir)
}

func TestLeaseGraceEndToEnd(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	clk := clock.NewFake(time.Unix(0, 0))
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
	hp, err := app.StartHost(t.Context(), app.HostDeps{
		DataDir:   dir,
		Config:    app.HostConfig{HostVersion: "host-e2e", APIVersions: []string{"v1"}, IdleGrace: 10 * time.Second},
		Clock:     clk,
		IDs:       ids.NewUUIDv7(clk, ids.NewDeterministic("host")),
		Transport: srv,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Drain(context.WithoutCancel(t.Context()), true)
		_ = hp.Close()
	})
	backend := &releaseSignalBackend{Backend: hp.Runtime(), released: make(chan struct{}, 1)}
	if err := srv.Start(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	client, httpClient := newTestClient(t, srv.cfg.Socket.Path)
	if _, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_TUI, "v1")); err != nil {
		t.Fatal(err)
	}
	// Closing the client's connection drops the last lease; the grace fires
	// through the fake clock and the drain removes the discovery files.
	httpClient.CloseIdleConnections()
	<-backend.released
	if clk.Pending() != 1 {
		t.Fatalf("grace timer not armed after the last lease dropped: %d pending", clk.Pending())
	}
	clk.Advance(10 * time.Second)
	<-hp.DrainDone()
	if _, err := os.Stat(HostFilePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host.json survived the idle drain: %v", err)
	}
	if _, err := os.Lstat(srv.cfg.Socket.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file survived the idle drain: %v", err)
	}
}

func TestServerSetsNoConnectionTimeouts(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, shortDir(t), newFakeBackend())
	if srv.httpSrv.ReadTimeout != 0 || srv.httpSrv.ReadHeaderTimeout != 0 || srv.httpSrv.IdleTimeout != 0 {
		t.Fatalf("the http server sets connection timeouts: %+v", srv.httpSrv)
	}
}

func TestHostJSONVersionsComeFromTheBackend(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	backend := newFakeBackend()
	backend.status = app.HostStatusView{
		HostVersion:          "host-distinctive",
		SupportedAPIVersions: []string{"v7", "v9"},
		StartedAt:            time.Unix(7, 0),
	}
	startTestServer(t, dir, backend)
	data, err := os.ReadFile(HostFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		`"pid":` + strconv.Itoa(os.Getpid()),
		`"socket_path":"`,
		`"api_versions":["v7","v9"]`,
		`"host_version":"host-distinctive"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("host.json = %s, want it to contain %s", content, want)
		}
	}
}

func TestErrorDetailCarriesCodeAndVersions(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.status = fakeStatus()
	backend.helloErr = errcode.Wrap(errcode.UnsupportedVersion, "the host supports v1, v2", app.ErrNoCommonAPIVersion)
	srv := startTestServer(t, shortDir(t), backend)
	client, _ := newTestClient(t, srv.cfg.Socket.Path)
	_, err := client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v3"))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Hello without a common version = %v, want failed_precondition", err)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatal("the error is not a connect error")
	}
	if len(cerr.Details()) != 1 {
		t.Fatalf("the error carries %d details, want 1", len(cerr.Details()))
	}
	value, verr := cerr.Details()[0].Value()
	if verr != nil {
		t.Fatal(verr)
	}
	detail, ok := value.(*hostv1.ErrorDetail)
	if !ok {
		t.Fatalf("detail of type %T, want *hostv1.ErrorDetail", value)
	}
	if detail.Code != "unsupported_version" {
		t.Fatalf("detail code = %q, want unsupported_version", detail.Code)
	}
	if diff := cmp.Diff([]string{"v1", "v2"}, detail.SupportedApiVersions); diff != "" {
		t.Fatalf("detail versions mismatch (-want +got):\n%s", diff)
	}
}

func TestErrorMappingExposesOnlyPublicCodes(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	backend.status = fakeStatus()
	h := &serviceHandler{backend: backend}
	connectCodes := map[errcode.Code]connect.Code{
		errcode.InvalidArgument:    connect.CodeInvalidArgument,
		errcode.NotFound:           connect.CodeNotFound,
		errcode.AlreadyExists:      connect.CodeAlreadyExists,
		errcode.FailedPrecondition: connect.CodeFailedPrecondition,
		errcode.PermissionDenied:   connect.CodePermissionDenied,
		errcode.Unavailable:        connect.CodeUnavailable,
		errcode.Canceled:           connect.CodeCanceled,
		errcode.DeadlineExceeded:   connect.CodeDeadlineExceeded,
		errcode.UnsupportedVersion: connect.CodeFailedPrecondition,
		errcode.Internal:           connect.CodeInternal,
	}
	for code, wantConnect := range connectCodes {
		err := h.connectError(t.Context(), errcode.New(code, "public message for "+string(code)))
		if connect.CodeOf(err) != wantConnect {
			t.Fatalf("code %s mapped to %v, want %v", code, connect.CodeOf(err), wantConnect)
		}
		cerr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("code %s produced %T, want *connect.Error", code, err)
		}
		if !code.IsPublic() {
			if len(cerr.Details()) != 0 {
				t.Fatalf("hidden code %s carries a detail", code)
			}
			if cerr.Message() != "internal error" {
				t.Fatalf("hidden code %s leaked the message %q", code, cerr.Message())
			}
			continue
		}
		if len(cerr.Details()) != 1 {
			t.Fatalf("public code %s carries %d details, want 1", code, len(cerr.Details()))
		}
		value, verr := cerr.Details()[0].Value()
		if verr != nil {
			t.Fatal(verr)
		}
		detail := value.(*hostv1.ErrorDetail)
		if detail.Code != string(code) {
			t.Fatalf("detail code = %q, want %q", detail.Code, string(code))
		}
		if code != errcode.UnsupportedVersion && len(detail.SupportedApiVersions) != 0 {
			t.Fatalf("code %s unexpectedly carries versions", code)
		}
		if code == errcode.UnsupportedVersion {
			if diff := cmp.Diff([]string{"v1", "v2"}, detail.SupportedApiVersions); diff != "" {
				t.Fatalf("detail versions mismatch (-want +got):\n%s", diff)
			}
		}
		if !strings.Contains(cerr.Message(), "public message") {
			t.Fatalf("public code %s lost its message: %q", code, cerr.Message())
		}
	}
}
