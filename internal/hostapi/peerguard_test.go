package hostapi

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

var errTreeUnreadable = errcode.New(errcode.Internal, "the process tree cannot be read")

// peerBackend is a backend with vendor sessions whose process trees answer
// what the test says.
type peerBackend struct {
	*fakeBackend
	owned bool
	err   error

	askMu sync.Mutex
	asked []int
}

func (b *peerBackend) VendorOwnsPeer(_ context.Context, pid int) (bool, error) {
	b.askMu.Lock()
	defer b.askMu.Unlock()
	b.asked = append(b.asked, pid)
	return b.owned, b.err
}

func (b *peerBackend) askedPIDs() []int {
	b.askMu.Lock()
	defer b.askMu.Unlock()
	return slices.Clone(b.asked)
}

// helloWith serves backend with the given peer credentials and returns the
// result of a local hello.
func helloWith(t *testing.T, backend *peerBackend, creds func(net.Conn) (PeerCredentials, error)) error {
	t.Helper()
	dir := shortDir(t)
	sock, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{
		Socket: sock, DataDir: dir, HostUID: int64(os.Getuid()), PeerCredentials: creds,
		IDs: ids.NewUUIDv7(clock.NewFake(time.Unix(0, 0)), ids.NewDeterministic("peer-conn")),
	})
	if startErr := srv.Start(t.Context(), backend); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if drainErr := srv.Drain(context.WithoutCancel(t.Context()), true); drainErr != nil {
			t.Errorf("cleanup drain: %v", drainErr)
		}
	})
	client, _ := newTestClient(t, sock.Path)
	if _, err = client.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		return fmt.Errorf("local hello: %w", err)
	}
	return nil
}

func fixedPeer(pid int) func(net.Conn) (PeerCredentials, error) {
	return func(net.Conn) (PeerCredentials, error) {
		uid, err := strconv.ParseUint(strconv.Itoa(os.Getuid()), 10, 32)
		if err != nil {
			return PeerCredentials{}, errTreeUnreadable
		}
		return PeerCredentials{UID: uint32(uid), PID: pid}, nil
	}
}

// assertLocalHelloFailsClosed covers every unknown of Q-41 A: no peer PID, a
// process tree that cannot be read, and a peer inside a vendor session.
func assertLocalHelloFailsClosed(t *testing.T) {
	t.Helper()
	cases := map[string]struct {
		backend *peerBackend
		pid     int
		denied  bool
	}{
		"a peer outside every session is served":   {&peerBackend{fakeBackend: newFakeBackend()}, 4242, false},
		"a peer inside a vendor session is denied": {&peerBackend{fakeBackend: newFakeBackend(), owned: true}, 4242, true},
		"an unreadable process tree is denied":     {&peerBackend{fakeBackend: newFakeBackend(), err: errTreeUnreadable}, 4242, true},
		"an unknown peer pid is denied":            {&peerBackend{fakeBackend: newFakeBackend()}, 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := helloWith(t, tc.backend, fixedPeer(tc.pid))
			if !tc.denied {
				if asked := tc.backend.askedPIDs(); err != nil || len(asked) != 1 || asked[0] != tc.pid {
					t.Fatalf("hello = %v after asking %v", err, asked)
				}
				return
			}
			assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
			if tc.backend.helloCount() != 0 {
				t.Fatal("a denied peer reached the backend hello")
			}
		})
	}
}

func TestLocalHelloFailsClosedOnAnUnknownPeer(t *testing.T) {
	t.Parallel()
	assertLocalHelloFailsClosed(t)
}

// The platform call reads the real PID of a real peer: this process.
func TestPeerCredentialsCarryThePeerPID(t *testing.T) {
	t.Parallel()
	backend := &peerBackend{fakeBackend: newFakeBackend()}
	if err := helloWith(t, backend, nil); err != nil {
		t.Fatal(err)
	}
	if asked := backend.askedPIDs(); len(asked) != 1 || asked[0] != os.Getpid() {
		t.Fatalf("peer pids asked = %v, want this process %d", asked, os.Getpid())
	}
}

// A backend without the session tool service, and a connection that never
// attached, get typed refusals from the session tool RPCs.
func TestSessionToolRPCsWithoutAService(t *testing.T) {
	t.Parallel()
	srv := startTestServer(t, shortDir(t), newFakeBackend())
	_, httpClient := newTestClient(t, srv.cfg.Socket.Path)
	client := sessiontoolsv1connect.NewSessionToolServiceClient(httpClient, "http://fugue")
	_, err := client.Attach(t.Context(), connect.NewRequest(&sessiontoolsv1.AttachRequest{SessionToken: "any"}))
	assertConnectCode(t, err, connect.CodeUnavailable, errcode.Unavailable)
	_, err = client.ListTools(t.Context(), connect.NewRequest(&sessiontoolsv1.ListToolsRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	_, err = client.CallTool(t.Context(), connect.NewRequest(&sessiontoolsv1.CallToolRequest{Name: "any"}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
}

// TestLocalPeerCheckWaitsForInFlightSpawnRegistration proves that a Hello sent
// after cmd.Start() and before OnSpawned has registered the owner waits for the
// in-flight spawn to register and is denied (never racing past Q-41), and that
// timeout or cancellation while waiting also denies typed.
func TestLocalPeerCheckWaitsForInFlightSpawnRegistration(t *testing.T) {
	t.Parallel()
	t.Run("child_hello_before_onspawned", func(t *testing.T) {
		t.Parallel()
		assertPreOnSpawnedChildHelloDenied(t)
	})
	t.Run("timeout_and_cancel_while_waiting", func(t *testing.T) {
		t.Parallel()
		assertInflightWaitTimeoutAndCancelDenied(t)
	})
}

func assertPreOnSpawnedChildHelloDenied(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	beforeOnSpawned := make(chan struct{})
	releaseOnSpawned := make(chan struct{})
	waitStarted := make(chan struct{})
	var beforeOnce, waitOnce sync.Once
	v := startVendorHostWith(t, true, func(deps *app.HostDeps, sockPath string) {
		deps.Random = ids.NewDeterministic(sessionTokenSeed)
		deps.Bridge = app.VendorBridge{Binary: exe, Socket: sockPath}
		deps.TestDelaySpawnRegistration = func(ctx context.Context) {
			beforeOnce.Do(func() { close(beforeOnSpawned) })
			select {
			case <-releaseOnSpawned:
			case <-ctx.Done():
			}
		}
		deps.TestOnWaitInflightSpawn = func() {
			waitOnce.Do(func() { close(waitStarted) })
		}
	})
	root := t.TempDir()
	wsID := v.workspaceWithRoot(t, root)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(shortDir(t), "pre-onspawned.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	if writeErr := os.WriteFile(filepath.Join(root, fakePreInitHelloFile), []byte(ln.Addr().String()), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	startErr := make(chan error, 1)
	supervise.Go(t.Context(), "test.pre-onspawned-start", func(ctx context.Context) error {
		_, callErr := v.vendors.StartSession(ctx, newPreInitSessionReq("pre-onspawned-start", wsID))
		startErr <- callErr
		return nil
	})
	<-beforeOnSpawned
	<-waitStarted
	close(releaseOnSpawned)
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	line, readErr := bufio.NewReader(conn).ReadString('\n')
	closeQuietly(conn)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.TrimSpace(line); !strings.HasPrefix(got, connect.CodePermissionDenied.String()+":") {
		t.Fatalf("child hello before OnSpawned = %q, want permission_denied", got)
	}
	if sErr := <-startErr; sErr != nil {
		t.Fatalf("StartSession after releasing OnSpawned = %v", sErr)
	}
}

func assertInflightWaitTimeoutAndCancelDenied(t *testing.T) {
	t.Helper()
	clk := clock.NewFake(time.Unix(0, 0))
	beforeOnSpawned := make(chan struct{})
	releaseOnSpawned := make(chan struct{})
	waitStarted := make(chan struct{}, 4)
	var beforeOnce sync.Once
	v := startVendorHostWith(t, true, func(deps *app.HostDeps, _ string) {
		deps.Clock = clk
		deps.TestDelaySpawnRegistration = func(ctx context.Context) {
			beforeOnce.Do(func() { close(beforeOnSpawned) })
			select {
			case <-releaseOnSpawned:
			case <-ctx.Done():
			}
		}
		deps.TestOnWaitInflightSpawn = func() {
			waitStarted <- struct{}{}
		}
	})
	wsID := v.workspaceWithRoot(t, t.TempDir())
	startErr := make(chan error, 1)
	supervise.Go(t.Context(), "test.inflight-timeout-start", func(ctx context.Context) error {
		_, callErr := v.vendors.StartSession(ctx, newPreInitSessionReq("inflight-timeout-start", wsID))
		startErr <- callErr
		return nil
	})
	<-beforeOnSpawned

	timeoutErr := make(chan error, 1)
	hostTimeout, _ := newTestClient(t, v.sockPath)
	supervise.Go(t.Context(), "test.inflight-hello-timeout", func(ctx context.Context) error {
		_, callErr := hostTimeout.Hello(ctx, helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1"))
		timeoutErr <- callErr
		return nil
	})
	<-waitStarted
	clk.Advance(10 * time.Second)
	assertConnectCode(t, <-timeoutErr, connect.CodePermissionDenied, errcode.PermissionDenied)

	cancelErr := make(chan error, 1)
	helloCtx, cancel := context.WithCancel(t.Context())
	hostCancel, _ := newTestClient(t, v.sockPath)
	supervise.Go(t.Context(), "test.inflight-hello-cancel", func(context.Context) error {
		_, callErr := hostCancel.Hello(helloCtx, helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1"))
		cancelErr <- callErr
		return nil
	})
	<-waitStarted
	cancel()
	gotCancel := <-cancelErr
	if code := connect.CodeOf(gotCancel); code != connect.CodeCanceled && code != connect.CodePermissionDenied {
		t.Fatalf("canceled hello while spawn in flight = %v (code %v), want canceled or permission_denied", gotCancel, code)
	}

	close(releaseOnSpawned)
	if sErr := <-startErr; sErr != nil {
		t.Fatalf("StartSession after releasing OnSpawned = %v", sErr)
	}
}
