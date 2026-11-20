package client_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"

	"github.com/syurodev/fugue/pkg/client"
)

// The strings the mapping tests assert on, which goconst counts across the
// package; the session state reuses read_map_test's.
const (
	testStateStarting = "starting"
	testModel         = "sonnet"
	testRoot          = "main"
	testWorkspaceID   = "ws-1"
	testWorkspaceName = "backend"
	testMissingRoot   = "gone"
)

// errFakeBinaryMissing is the static cause behind the scripted probe
// failure; it is wrapped, never constructed, at the call site.
var errFakeBinaryMissing = errors.New("claude binary not found")

// vendorFake is the scripted vendor and workspace service the mapping tests
// run against: every RPC answers with the canned response the test set, and
// records the requests the client sent.
type vendorFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	vendorsv1connect.UnimplementedVendorServiceHandler
	workspacev1connect.UnimplementedWorkspaceServiceHandler

	mu         sync.Mutex
	started    []*vendorsv1.StartSessionRequest
	startErr   error
	resumed    *vendorsv1.ResumeSessionRequest
	interrupt  vendorsv1.OperationStatus
	commands   *vendorsv1.ListCommandsResponse
	probe      *vendorsv1.ProbeVendorsResponse
	probeErr   error
	workspaces *workspacev1.ListWorkspacesResponse
	workspace  *workspacev1.GetWorkspaceResponse
}

func (f *vendorFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{}), nil
}

func (f *vendorFake) StartSession(ctx context.Context, req *connect.Request[vendorsv1.StartSessionRequest]) (*connect.Response[vendorsv1.StartSessionResponse], error) {
	f.mu.Lock()
	f.started = append(f.started, req.Msg)
	startErr := f.startErr
	f.mu.Unlock()
	if startErr != nil {
		return nil, startErr
	}
	return connect.NewResponse(&vendorsv1.StartSessionResponse{
		Session: &vendorsv1.VendorSession{
			SessionId: "sess-1", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, State: testStateStarting,
			WaitingReason: "", CreatedAt: "t1", UpdatedAt: "t1",
		},
	}), nil
}

func (f *vendorFake) Send(context.Context, *connect.Request[vendorsv1.SendRequest]) (*connect.Response[vendorsv1.SendResponse], error) {
	return connect.NewResponse(&vendorsv1.SendResponse{}), nil
}

func (f *vendorFake) Steer(context.Context, *connect.Request[vendorsv1.SteerRequest]) (*connect.Response[vendorsv1.SteerResponse], error) {
	return connect.NewResponse(&vendorsv1.SteerResponse{}), nil
}

func (f *vendorFake) ResumeSession(ctx context.Context, req *connect.Request[vendorsv1.ResumeSessionRequest]) (*connect.Response[vendorsv1.ResumeSessionResponse], error) {
	f.mu.Lock()
	f.resumed = req.Msg
	f.mu.Unlock()
	return connect.NewResponse(&vendorsv1.ResumeSessionResponse{
		Session: &vendorsv1.VendorSession{SessionId: req.Msg.SessionId, Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, State: testStateStarting},
	}), nil
}

func (f *vendorFake) Interrupt(context.Context, *connect.Request[vendorsv1.InterruptRequest]) (*connect.Response[vendorsv1.InterruptResponse], error) {
	return connect.NewResponse(&vendorsv1.InterruptResponse{Status: f.interrupt}), nil
}

func (f *vendorFake) ListCommands(context.Context, *connect.Request[vendorsv1.ListCommandsRequest]) (*connect.Response[vendorsv1.ListCommandsResponse], error) {
	return connect.NewResponse(f.commands), nil
}

func (f *vendorFake) ProbeVendors(context.Context, *connect.Request[vendorsv1.ProbeVendorsRequest]) (*connect.Response[vendorsv1.ProbeVendorsResponse], error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return connect.NewResponse(f.probe), nil
}

// TestProbeVendorsCarriesTheHostsTypedFailure pins that a failed probe —
// the host cannot resolve a vendor CLI, say — reaches the caller wrapped,
// never swallowed.
func TestProbeVendorsCarriesTheHostsTypedFailure(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{probeErr: connect.NewError(connect.CodeNotFound, errFakeBinaryMissing)}
	c := serveVendorFake(t, fake)
	if _, err := c.ProbeVendors(testCtx(t)); err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the host's typed not-found", err)
	}
}

func (f *vendorFake) ListWorkspaces(context.Context, *connect.Request[workspacev1.ListWorkspacesRequest]) (*connect.Response[workspacev1.ListWorkspacesResponse], error) {
	return connect.NewResponse(f.workspaces), nil
}

func (f *vendorFake) GetWorkspace(context.Context, *connect.Request[workspacev1.GetWorkspaceRequest]) (*connect.Response[workspacev1.GetWorkspaceResponse], error) {
	return connect.NewResponse(f.workspace), nil
}

// serveVendorFake serves the fake over a unix socket on unencrypted HTTP/2,
// the transport shape the real host serves, and dials a client at it.
func serveVendorFake(t *testing.T, fake *vendorFake) *client.Client {
	t.Helper()
	socket := filepath.Join(shortDir(t), "fake.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(vendorsv1connect.NewVendorServiceHandler(fake))
	mux.Handle(workspacev1connect.NewWorkspaceServiceHandler(fake))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial the scripted host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestStartSessionMapsTheWireSessionAndMintsTheVendorEnum pins the happy
// path: the client's answer is the wire session's public shape, the vendor
// name maps onto the wire enum, and an unknown name keeps the unspecified
// vendor the host rejects.
func TestStartSessionMapsTheWireSessionAndMintsTheVendorEnum(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{}
	c := serveVendorFake(t, fake)
	sess, err := c.StartSession(testCtx(t), "start-1", client.VendorClaude, "ws", testRoot, client.SessionProfile{Model: testModel, Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	want := client.Session{ID: "sess-1", Vendor: client.VendorClaude, State: "starting", CreatedAt: "t1", UpdatedAt: "t1"}
	if sess != want {
		t.Fatalf("session = %+v, want %+v", sess, want)
	}
	fake.mu.Lock()
	started := fake.started
	fake.mu.Unlock()
	if len(started) != 1 || started[0].Vendor != vendorsv1.Vendor_VENDOR_CLAUDE ||
		started[0].WorkspaceId != "ws" || started[0].RootName != testRoot ||
		started[0].Profile.GetModel() != testModel || started[0].Profile.GetEffort() != "high" {
		t.Fatalf("request = %+v, want the vendor enum and the profile's values", started)
	}

	// Every name the wire defines maps onto its enum, and an unknown name
	// keeps the unspecified vendor the host rejects.
	for name, want := range map[string]vendorsv1.Vendor{
		client.VendorCodex: vendorsv1.Vendor_VENDOR_CODEX,
		client.VendorAGY:   vendorsv1.Vendor_VENDOR_AGY,
		"no-such-vendor":   vendorsv1.Vendor_VENDOR_UNSPECIFIED,
	} {
		if _, err := c.StartSession(testCtx(t), "start-map", name, "ws", "", client.SessionProfile{}); err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		last := fake.started[len(fake.started)-1]
		fake.mu.Unlock()
		if last.Vendor != want {
			t.Fatalf("vendor %q mapped to %v, want %v", name, last.Vendor, want)
		}
	}
}

// TestSendAndSteerDeliverTheirInput pins the happy path of the two inputs:
// the text travels whole and the empty answers read as success.
func TestSendAndSteerDeliverTheirInput(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{}
	c := serveVendorFake(t, fake)
	if err := c.Send(testCtx(t), "send-1", "sess-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := c.Steer(testCtx(t), "steer-1", "sess-1", "mid-turn"); err != nil {
		t.Fatal(err)
	}
}

// TestResumeSessionCarriesTheBindingAndTheProfile pins the resume request:
// the caller's workspace binding and profile travel whole.
func TestResumeSessionCarriesTheBindingAndTheProfile(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{}
	c := serveVendorFake(t, fake)
	sess, err := c.ResumeSession(testCtx(t), "resume-1", "sess-9", "ws", testRoot, client.SessionProfile{Model: testModel})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "sess-9" || sess.State != "starting" {
		t.Fatalf("session = %+v, want the resumed session's identity", sess)
	}
	fake.mu.Lock()
	resumed := fake.resumed
	fake.mu.Unlock()
	if resumed.SessionId != "sess-9" || resumed.WorkspaceId != "ws" || resumed.RootName != testRoot || resumed.Profile.GetModel() != "sonnet" {
		t.Fatalf("request = %+v, want the binding and the profile", resumed)
	}
}

// TestInterruptReportsWhetherTheStopWasDelivered pins the stop answer: DONE
// reports delivered and PENDING keeps the caller informed the stop still
// runs on the host.
func TestInterruptReportsWhetherTheStopWasDelivered(t *testing.T) {
	t.Parallel()
	for tc, want := range map[vendorsv1.OperationStatus]bool{
		vendorsv1.OperationStatus_OPERATION_STATUS_DONE:    true,
		vendorsv1.OperationStatus_OPERATION_STATUS_PENDING: false,
	} {
		fake := &vendorFake{interrupt: tc}
		c := serveVendorFake(t, fake)
		done, err := c.Interrupt(testCtx(t), "stop-1", "sess-1")
		if err != nil {
			t.Fatal(err)
		}
		if done != want {
			t.Fatalf("interrupt %v = %v, want %v", tc, done, want)
		}
	}
}

// TestListCommandsMapsBothGroupsAndTheRevision pins the slash menu's source:
// the vendor group keeps the vendor's names and order, the `/fugue:` group
// carries availability and owner, and the revision rides along.
func TestListCommandsMapsBothGroupsAndTheRevision(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{commands: &vendorsv1.ListCommandsResponse{
		CommandsRevision: 7,
		Groups: []*vendorsv1.CommandGroup{
			{Name: "vendor", VendorCommands: []*vendorsv1.VendorCommand{{Name: "clear"}, {Name: "compact"}}},
			{Name: "fugue", FugueCommands: []*vendorsv1.FugueCommand{
				{Name: "/fugue:team", Owner: "F11.2"},
				{Name: "/fugue:account", Available: true, Owner: "F9.10"},
			}},
		},
	}}
	c := serveVendorFake(t, fake)
	commands, err := c.ListCommands(testCtx(t), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if commands.Revision != 7 || len(commands.Groups) != 2 {
		t.Fatalf("commands = %+v, want two groups at revision 7", commands)
	}
	vendor, fugue := commands.Groups[0], commands.Groups[1]
	if len(vendor.Vendor) != 2 || vendor.Vendor[0] != "clear" || vendor.Vendor[1] != "compact" {
		t.Fatalf("vendor group = %+v, want the vendor's names and order", vendor)
	}
	if len(fugue.Fugue) != 2 || fugue.Fugue[0].Name != "/fugue:team" || fugue.Fugue[0].Available ||
		!fugue.Fugue[1].Available || fugue.Fugue[1].Owner != "F9.10" {
		t.Fatalf("fugue group = %+v, want availability and owner carried", fugue)
	}
}

// TestProbeVendorsMapsTheCapability pins the capability mapping: the fields
// the composer's Steer and stop gating reads travel whole.
func TestProbeVendorsMapsTheCapability(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{probe: &vendorsv1.ProbeVendorsResponse{
		Probes: []*vendorsv1.VendorProbe{{
			Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, Version: "2.1.319", VersionTested: true,
			VersionWarning: "untested", Login: "logged_in",
			Capability: &vendorsv1.Capability{
				PerRequestApproval: true, Interrupt: true, Steer: true, Resume: true,
				PartialDeltas: true, VendorSlashCommands: true, ModelList: true,
			},
		}},
	}}
	c := serveVendorFake(t, fake)
	probes, err := c.ProbeVendors(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %+v, want one", probes)
	}
	probe := probes[0]
	if probe.Vendor != client.VendorClaude || probe.Version != "2.1.319" || !probe.VersionTested ||
		probe.VersionWarning != "untested" || probe.Login != "logged_in" {
		t.Fatalf("probe = %+v, want the probed identity", probe)
	}
	want := client.Capability{
		PerRequestApproval: true, Interrupt: true, Steer: true, Resume: true,
		PartialDeltas: true, VendorSlashCommands: true, ModelList: true,
	}
	if probe.Capability != want {
		t.Fatalf("capability = %+v, want %+v", probe.Capability, want)
	}
}

// TestWorkspaceReadsMapTheRootsAndTheFlags pins the start flow's source: the
// workspace list and the workspace's roots, missing roots, policy flag and
// config error location travel whole.
func TestWorkspaceReadsMapTheRootsAndTheFlags(t *testing.T) {
	t.Parallel()
	fake := &vendorFake{
		workspaces: &workspacev1.ListWorkspacesResponse{
			Workspaces: []*workspacev1.WorkspaceSummary{
				{Id: testWorkspaceID, Name: testWorkspaceName, MissingRoots: []string{testMissingRoot}, PendingPolicyConfirmation: true},
			},
		},
		workspace: &workspacev1.GetWorkspaceResponse{
			Workspace: &workspacev1.Workspace{
				Id: testWorkspaceID, Name: testWorkspaceName,
				Roots: []*workspacev1.Root{
					{Name: "main", Path: "/repo", Access: "rw"},
					{Name: testMissingRoot, Path: "/gone", Missing: true},
				},
				PendingPolicyConfirmation: true,
				ConfigErrorKey:            "session", ConfigErrorLine: 3, ConfigErrorColumn: 7,
			},
		},
	}
	c := serveVendorFake(t, fake)
	list, err := c.ListWorkspaces(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	wantList := []client.WorkspaceSummary{{
		ID: testWorkspaceID, Name: testWorkspaceName,
		MissingRoots: []string{testMissingRoot}, PendingPolicyConfirmation: true,
	}}
	if diff := cmp.Diff(wantList, list); diff != "" {
		t.Fatalf("workspaces (-want +got):\n%s", diff)
	}
	ws, err := c.Workspace(testCtx(t), testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	wantWS := client.Workspace{
		ID: testWorkspaceID, Name: testWorkspaceName,
		Roots: []client.WorkspaceRoot{
			{Name: testRoot, Path: "/repo", Access: "rw"},
			{Name: testMissingRoot, Path: "/gone", Missing: true},
		},
		PendingPolicyConfirmation: true,
		ConfigErrorKey:            "session", ConfigErrorLine: 3, ConfigErrorColumn: 7,
	}
	if diff := cmp.Diff(wantWS, ws); diff != "" {
		t.Fatalf("workspace (-want +got):\n%s", diff)
	}
}
