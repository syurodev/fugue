package hostapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

// vendorHost is a real host on a real socket whose Claude adapter runs this
// test binary as the vendor CLI.
type vendorHost struct {
	hp         *app.HostProcess
	srv        *Server
	fugueHome  string
	host       hostv1connect.HostServiceClient
	vendors    vendorsv1connect.VendorServiceClient
	workspaces workspacev1connect.WorkspaceServiceClient
}

func startVendorHost(t *testing.T, unlock bool) *vendorHost {
	t.Helper()
	dir := shortDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Real{}
	sock, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{Socket: sock, DataDir: dir, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("conn")), HostUID: int64(os.Getuid())})
	hp, err := app.StartHost(t.Context(), &app.HostDeps{
		DataDir: dir, WorkspaceEnv: config.MapEnv{config.EnvFugueHome: fugueHome},
		Config: app.HostConfig{HostVersion: testHostE2EVersion, APIVersions: []string{"v1"}},
		Clock:  clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("host")), Transport: srv, VendorBinary: exe,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(t.Context(), hp.Runtime()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Drain(context.WithoutCancel(t.Context()), true); err != nil {
			t.Errorf("cleanup drain: %v", err)
		}
		if err := hp.Close(); err != nil {
			t.Errorf("close host: %v", err)
		}
	})
	if unlock {
		if _, err := hp.Vault().Setup(t.Context(), redact.NewSecret("vendor-test-master-password")); err != nil {
			t.Fatal(err)
		}
	}
	host, httpClient := newTestClient(t, sock.Path)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	return &vendorHost{
		hp: hp, srv: srv, fugueHome: fugueHome, host: host,
		vendors:    vendorsv1connect.NewVendorServiceClient(httpClient, "http://fugue"),
		workspaces: workspacev1connect.NewWorkspaceServiceClient(httpClient, "http://fugue"),
	}
}

// workspaceWithRoot creates a workspace whose single root "main" is root.
func (v *vendorHost) workspaceWithRoot(t *testing.T, root string) string {
	t.Helper()
	created, err := v.workspaces.CreateWorkspace(t.Context(), connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: "vendor-ws"}))
	if err != nil {
		t.Fatal(err)
	}
	ws := created.Msg.Workspace
	raw := []byte("name = \"vendor-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(root) + "\"\naccess = \"rw\"\n")
	if _, err := v.workspaces.UpdateWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{Id: ws.Id, RawToml: raw, ExpectedHash: ws.ConfigHash})); err != nil {
		t.Fatal(err)
	}
	return ws.Id
}

func (v *vendorHost) start(t *testing.T, commandID, workspaceID string) *vendorsv1.VendorSession {
	t.Helper()
	res, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: commandID, Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: workspaceID,
		Profile: &vendorsv1.SessionProfile{Model: "fake-model", Effort: "high"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.Session
}

func (v *vendorHost) send(t *testing.T, commandID, sessionID, text string) error {
	t.Helper()
	if _, err := v.vendors.Send(t.Context(), connect.NewRequest(&vendorsv1.SendRequest{CommandId: commandID, SessionId: sessionID, Text: text})); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

func subscribeVendorSessions(t *testing.T, host hostv1connect.HostServiceClient, cursor *hostv1.EventCursor) *connect.ServerStreamForClient[hostv1.SubscribeResponse] {
	t.Helper()
	stream, err := host.Subscribe(streamCtx(t), connect.NewRequest(&hostv1.SubscribeRequest{Topic: app.TopicVendorSessions, Cursor: cursor}))
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

type vendorChange struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// awaitVendorState reads the vendor-sessions stream until the session reaches
// state and returns that event.
func awaitVendorState(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], sessionID, state string) *hostv1.HostEvent {
	t.Helper()
	for {
		event := recv(t, stream).GetEvent()
		if event == nil {
			continue
		}
		var change vendorChange
		if err := json.Unmarshal(event.Payload, &change); err != nil {
			t.Fatal(err)
		}
		if change.SessionID == sessionID && change.State == state {
			return event
		}
	}
}

func TestVendorRPCEndToEndWithFakeVendor(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	stream := subscribeVendorSessions(t, v.host, nil)
	snapshot := recv(t, stream).GetSnapshot()
	if snapshot == nil || snapshot.Sequence != 0 || string(snapshot.State) != `{"sessions":[]}` {
		t.Fatalf("initial vendor-sessions snapshot = %+v", snapshot)
	}
	assertFakeVendorProbe(t, v)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	session := v.start(t, "start-1", wsID)
	created := awaitVendorState(t, stream, session.SessionId, "starting")
	if created.Type != app.EventVendorSessionCreated {
		t.Fatalf("first session event = %q, want created", created.Type)
	}
	awaitVendorState(t, stream, session.SessionId, "ready")
	runTurns(t, v, stream, session.SessionId)
	closeAndList(t, v, stream, session.SessionId, wsID, created.Sequence)
	assertVendorCatchUp(t, v.host, snapshot.IncarnationId, created.Sequence, session.SessionId)
}

func assertFakeVendorProbe(t *testing.T, v *vendorHost) {
	t.Helper()
	probe, err := v.vendors.ProbeVendors(t.Context(), connect.NewRequest(&vendorsv1.ProbeVendorsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := probe.Msg.Probes; len(got) != 1 || got[0].Version != fakeVendorVersion || !got[0].VersionTested || !got[0].Capability.Steer {
		t.Fatalf("probe = %+v", got)
	}
}

// closeAndList closes the session, checks that a closed session is not
// resumable and that the list is ahead of the created event.
func closeAndList(t *testing.T, v *vendorHost, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], sessionID, wsID string, createdSeq uint64) {
	t.Helper()
	closed, err := v.vendors.CloseSession(t.Context(), connect.NewRequest(&vendorsv1.CloseSessionRequest{CommandId: "close-1", SessionId: sessionID}))
	if err != nil || closed.Msg.Status != vendorsv1.OperationStatus_OPERATION_STATUS_DONE {
		t.Fatalf("close = %v, %v", closed, err)
	}
	awaitVendorState(t, stream, sessionID, "closed")
	_, err = v.vendors.ResumeSession(t.Context(), connect.NewRequest(&vendorsv1.ResumeSessionRequest{CommandId: "resume-1", SessionId: sessionID, WorkspaceId: wsID}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.VendorSessionState)
	listed, err := v.vendors.ListSessions(t.Context(), connect.NewRequest(&vendorsv1.ListSessionsRequest{}))
	if err != nil || len(listed.Msg.Sessions) != 1 || listed.Msg.Sessions[0].State != "closed" || listed.Msg.Sequence <= createdSeq {
		t.Fatalf("list = %+v, %v", listed, err)
	}
}

// runTurns drives send, steer, interrupt, dedup and the /fugue: rule through
// the fake vendor.
func runTurns(t *testing.T, v *vendorHost, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], sessionID string) {
	t.Helper()
	if err := v.send(t, "send-1", sessionID, fakeHoldTurn); err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, sessionID, "running")
	assertConnectCode(t, v.send(t, "send-1", sessionID, "a different prompt"), connect.CodeInvalidArgument, errcode.VendorCommandConflict)
	if err := v.send(t, "send-1", sessionID, fakeHoldTurn); err != nil {
		t.Fatalf("retry of the same send = %v, want the original result", err)
	}
	if _, err := v.vendors.Steer(t.Context(), connect.NewRequest(&vendorsv1.SteerRequest{CommandId: "steer-1", SessionId: sessionID, Text: "steer now"})); err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, sessionID, "ready")
	assertConnectCode(t, v.send(t, "fugue-1", sessionID, "/fugue:team build it"), connect.CodeInvalidArgument, errcode.VendorUnknownCommand)
	assertConnectCode(t, v.send(t, "fugue-2", sessionID, "\n /fugue:team"), connect.CodeInvalidArgument, errcode.VendorUnknownCommand)
	if err := v.send(t, "send-2", sessionID, fakeHoldTurn); err != nil {
		t.Fatal(err)
	}
	awaitVendorState(t, stream, sessionID, "running")
	interrupted, err := v.vendors.Interrupt(t.Context(), connect.NewRequest(&vendorsv1.InterruptRequest{CommandId: "interrupt-1", SessionId: sessionID}))
	if err != nil || interrupted.Msg.Status != vendorsv1.OperationStatus_OPERATION_STATUS_DONE {
		t.Fatalf("interrupt = %v, %v", interrupted, err)
	}
	awaitVendorState(t, stream, sessionID, "ready")
	_, err = v.vendors.Interrupt(t.Context(), connect.NewRequest(&vendorsv1.InterruptRequest{CommandId: "interrupt-2", SessionId: sessionID}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.VendorTurnNotActive)
}

// assertVendorCatchUp reconnects from the created event's cursor: the stream
// starts with the next event, not a snapshot, and misses no change.
func assertVendorCatchUp(t *testing.T, host hostv1connect.HostServiceClient, incarnation string, after uint64, sessionID string) {
	t.Helper()
	stream := subscribeVendorSessions(t, host, &hostv1.EventCursor{Topic: app.TopicVendorSessions, IncarnationId: incarnation, Sequence: after})
	first := recv(t, stream)
	event := first.GetEvent()
	if event == nil || event.Sequence != after+1 || event.Type != app.EventVendorSessionStateChanged {
		t.Fatalf("catch-up started with %+v, want the event after %d", first, after)
	}
	awaitVendorState(t, stream, sessionID, "closed")
}

func TestVendorRetrySameCommandIDReturnsTheSameSession(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	first := v.start(t, "start-dedup", wsID)
	retried := v.start(t, "start-dedup", wsID)
	if retried.SessionId != first.SessionId {
		t.Fatalf("retried start created session %q, want %q", retried.SessionId, first.SessionId)
	}
	_, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
		CommandId: "start-dedup", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: wsID, Profile: &vendorsv1.SessionProfile{Effort: "low"},
	}))
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.VendorCommandConflict)
	listed, err := v.vendors.ListSessions(t.Context(), connect.NewRequest(&vendorsv1.ListSessionsRequest{}))
	if err != nil || len(listed.Msg.Sessions) != 1 {
		t.Fatalf("list after retries = %+v, %v; want exactly one session", listed, err)
	}
}

func TestVendorCapacityRejectsThirdStart(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	v.start(t, "cap-1", wsID)
	v.start(t, "cap-2", wsID)
	_, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{CommandId: "cap-3", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: wsID}))
	assertConnectCode(t, err, connect.CodeResourceExhausted, errcode.VendorCapacity)
}

func TestVendorTypedErrorsCarryNoClientValues(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	startWith := func(vendor vendorsv1.Vendor, root string, profile *vendorsv1.SessionProfile) error {
		_, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{
			CommandId: "typed-" + root + profile.GetEffort() + profile.GetModel(), Vendor: vendor, WorkspaceId: wsID, RootName: root, Profile: profile,
		}))
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
		return nil
	}
	assertConnectCode(t, startWith(vendorsv1.Vendor_VENDOR_CODEX, "", nil), connect.CodeFailedPrecondition, errcode.VendorUnsupported)
	_, err := v.vendors.ProbeVendors(t.Context(), connect.NewRequest(&vendorsv1.ProbeVendorsRequest{Vendors: []vendorsv1.Vendor{vendorsv1.Vendor_VENDOR_AGY}}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.VendorUnsupported)
	for _, profile := range []*vendorsv1.SessionProfile{{Effort: "turbo"}, {Model: "--dangerously-skip-permissions"}, {ResumeSessionId: "not-a-uuid"}} {
		startErr := startWith(vendorsv1.Vendor_VENDOR_CLAUDE, "", profile)
		assertConnectCode(t, startErr, connect.CodeInvalidArgument, errcode.VendorInvalidProfile)
		for _, value := range []string{profile.Effort, profile.Model, profile.ResumeSessionId} {
			if value != "" && strings.Contains(startErr.Error(), value) {
				t.Fatalf("error %q echoes the profile value", startErr)
			}
		}
	}
	err = startWith(vendorsv1.Vendor_VENDOR_CLAUDE, "elsewhere", nil)
	assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.WorkspaceRootInvalid)
	if strings.Contains(err.Error(), "elsewhere") || strings.Contains(err.Error(), string(filepath.Separator)) {
		t.Fatalf("root error %q names the root or a path", err)
	}
	assertConnectCode(t, v.send(t, "missing-1", "no-such-session", "hi"), connect.CodeNotFound, errcode.VendorSessionNotFound)
	assertConnectCode(t, v.send(t, "", "no-such-session", "hi"), connect.CodeInvalidArgument, errcode.InvalidArgument)
}

func TestVendorCwdInsideFugueHomeIsDenied(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	wsID := v.workspaceWithRoot(t, root)
	// The root is swapped for a link into the Fugue home after it was
	// accepted: the root is re-checked before spawn and the vendor never runs.
	inside := filepath.Join(v.fugueHome, "inside")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, root); err != nil {
		t.Fatal(err)
	}
	_, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{CommandId: "cwd-1", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: wsID}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.WorkspacePathDenied)
	if strings.Contains(err.Error(), v.fugueHome) {
		t.Fatalf("cwd error %q names a host path", err)
	}
}

func TestVendorStartWhileVaultLockedIsTyped(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, false)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	_, err := v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{CommandId: "locked-1", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, WorkspaceId: wsID}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.VendorVaultLocked)
}

func TestVendorNonLocalPrincipalIsDeniedOnRPCsAndTopic(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	v.srv.mu.Lock()
	for _, st := range v.srv.conns {
		st.mu.Lock()
		st.kind = app.ClientKind("session")
		st.mu.Unlock()
	}
	v.srv.mu.Unlock()
	_, err := v.vendors.ListSessions(t.Context(), connect.NewRequest(&vendorsv1.ListSessionsRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	_, err = v.vendors.StartSession(t.Context(), connect.NewRequest(&vendorsv1.StartSessionRequest{CommandId: "denied-1", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	stream := subscribeVendorSessions(t, v.host, nil)
	if stream.Receive() {
		t.Fatal("a non-local principal subscribed to vendor-sessions")
	}
	if connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
		t.Fatalf("vendor-sessions subscribe = %v, want permission_denied", stream.Err())
	}
}
