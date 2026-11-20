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
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

// Constants shared across workspace client tests so goconst stays satisfied.
const (
	testWSExpectedHash  = "hash-ws-v1"
	testWSRootNewName   = "api-next"
	testWSRootPath      = "/repo/api"
	testWSCodeConflict  = "workspace.config_changed"
	testWSCodeInvalidID = "workspace.root_invalid"
)

// errWorkspaceScripted is the static cause behind scripted workspace errors.
var errWorkspaceScripted = errors.New("scripted workspace refusal")

// workspaceFake records every workspace mutation RPC sent by pkg/client and
// returns a scripted error when err is set.
type workspaceFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	workspacev1connect.UnimplementedWorkspaceServiceHandler

	mu      sync.Mutex
	err     error
	created []*workspacev1.CreateWorkspaceRequest
	added   []*workspacev1.AddRootRequest
	removed []*workspacev1.RemoveRootRequest
	updated []*workspacev1.UpdateRootRequest
	deleted []*workspacev1.DeleteWorkspaceRequest
	confirm []*workspacev1.ConfirmWorkspaceConfigRequest
	reject  []*workspacev1.RejectWorkspaceConfigRequest
}

func (f *workspaceFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{}), nil
}

func (f *workspaceFake) CreateWorkspace(_ context.Context, req *connect.Request[workspacev1.CreateWorkspaceRequest]) (*connect.Response[workspacev1.CreateWorkspaceResponse], error) {
	f.mu.Lock()
	f.created = append(f.created, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.CreateWorkspaceResponse{}), nil
}

func (f *workspaceFake) AddRoot(_ context.Context, req *connect.Request[workspacev1.AddRootRequest]) (*connect.Response[workspacev1.AddRootResponse], error) {
	f.mu.Lock()
	f.added = append(f.added, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.AddRootResponse{}), nil
}

func (f *workspaceFake) RemoveRoot(_ context.Context, req *connect.Request[workspacev1.RemoveRootRequest]) (*connect.Response[workspacev1.RemoveRootResponse], error) {
	f.mu.Lock()
	f.removed = append(f.removed, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.RemoveRootResponse{}), nil
}

func (f *workspaceFake) UpdateRoot(_ context.Context, req *connect.Request[workspacev1.UpdateRootRequest]) (*connect.Response[workspacev1.UpdateRootResponse], error) {
	f.mu.Lock()
	f.updated = append(f.updated, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.UpdateRootResponse{}), nil
}

func (f *workspaceFake) DeleteWorkspace(_ context.Context, req *connect.Request[workspacev1.DeleteWorkspaceRequest]) (*connect.Response[workspacev1.DeleteWorkspaceResponse], error) {
	f.mu.Lock()
	f.deleted = append(f.deleted, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.DeleteWorkspaceResponse{}), nil
}

func (f *workspaceFake) ConfirmWorkspaceConfig(_ context.Context, req *connect.Request[workspacev1.ConfirmWorkspaceConfigRequest]) (*connect.Response[workspacev1.ConfirmWorkspaceConfigResponse], error) {
	f.mu.Lock()
	f.confirm = append(f.confirm, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.ConfirmWorkspaceConfigResponse{}), nil
}

func (f *workspaceFake) RejectWorkspaceConfig(_ context.Context, req *connect.Request[workspacev1.RejectWorkspaceConfigRequest]) (*connect.Response[workspacev1.RejectWorkspaceConfigResponse], error) {
	f.mu.Lock()
	f.reject = append(f.reject, req.Msg)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workspacev1.RejectWorkspaceConfigResponse{}), nil
}

// serveWorkspaceFake serves the fake workspace RPCs over a unix socket on
// unencrypted HTTP/2 and dials a pkg/client at it.
func serveWorkspaceFake(t *testing.T, fake *workspaceFake) *client.Client {
	t.Helper()
	socket := filepath.Join(shortDir(t), "ws.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
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
		t.Fatalf("dial the scripted workspace host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// scriptedWorkspaceError builds a connect error carrying a host ErrorDetail code.
func scriptedWorkspaceError(t *testing.T, code string) error {
	t.Helper()
	cerr := connect.NewError(connect.CodeFailedPrecondition, errWorkspaceScripted)
	detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	cerr.AddDetail(detail)
	return cerr
}

// assertCodedRefusal verifies err wraps a FailedPrecondition error with wantCode.
func assertCodedRefusal(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("mutation succeeded, want error")
	}
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("connect code = %v, want FailedPrecondition", got)
	}
	code, ok := client.WorkspaceCodeOf(err)
	if !ok || code != wantCode {
		t.Fatalf("WorkspaceCodeOf = (%q, %v), want (%q, true)", code, ok, wantCode)
	}
}

// TestWorkspaceRootMutationsSendFieldsAndSurfaceErrors pins CreateWorkspace,
// AddRoot, RemoveRoot and UpdateRoot fields and error wrapping.
func TestWorkspaceRootMutationsSendFieldsAndSurfaceErrors(t *testing.T) {
	t.Parallel()
	fake := &workspaceFake{}
	c := serveWorkspaceFake(t, fake)
	ctx := testCtx(t)

	if err := c.CreateWorkspace(ctx, testWorkspaceName); err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoot(ctx, testWorkspaceID, testRoot, testWSRootPath, client.RootAccessRW, testWSExpectedHash); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveRoot(ctx, testWorkspaceID, testRoot, testWSExpectedHash); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRoot(ctx, testWorkspaceID, testRoot, testWSRootNewName, testWSRootPath, client.RootAccessRO, testWSExpectedHash); err != nil {
		t.Fatal(err)
	}

	assertRootRequests(t, fake)

	fake.mu.Lock()
	fake.err = scriptedWorkspaceError(t, testWSCodeInvalidID)
	fake.mu.Unlock()
	assertCodedRefusal(t, c.CreateWorkspace(ctx, testWorkspaceName), testWSCodeInvalidID)
	assertCodedRefusal(t, c.AddRoot(ctx, testWorkspaceID, testRoot, testWSRootPath, client.RootAccessRW, testWSExpectedHash), testWSCodeInvalidID)
	assertCodedRefusal(t, c.RemoveRoot(ctx, testWorkspaceID, testRoot, testWSExpectedHash), testWSCodeInvalidID)
	assertCodedRefusal(t, c.UpdateRoot(ctx, testWorkspaceID, testRoot, testWSRootNewName, testWSRootPath, client.RootAccessRO, testWSExpectedHash), testWSCodeInvalidID)
}

// assertRootRequests checks that every Create/Add/Remove/Update root RPC sent
// the exact proto fields without alteration.
func assertRootRequests(t *testing.T, fake *workspaceFake) {
	t.Helper()
	gotCreate := []string{fake.created[0].GetName()}
	if diff := cmp.Diff([]string{testWorkspaceName}, gotCreate); diff != "" {
		t.Fatalf("CreateWorkspace request mismatch (-want +got):\n%s", diff)
	}
	add := fake.added[0]
	gotAdd := []string{add.GetId(), add.GetName(), add.GetPath(), add.GetAccess(), add.GetExpectedHash()}
	wantAdd := []string{testWorkspaceID, testRoot, testWSRootPath, client.RootAccessRW, testWSExpectedHash}
	if diff := cmp.Diff(wantAdd, gotAdd); diff != "" {
		t.Fatalf("AddRoot request mismatch (-want +got):\n%s", diff)
	}
	rm := fake.removed[0]
	gotRm := []string{rm.GetId(), rm.GetName(), rm.GetExpectedHash()}
	if diff := cmp.Diff([]string{testWorkspaceID, testRoot, testWSExpectedHash}, gotRm); diff != "" {
		t.Fatalf("RemoveRoot request mismatch (-want +got):\n%s", diff)
	}
	upd := fake.updated[0]
	gotUpd := []string{upd.GetId(), upd.GetName(), upd.GetNewName(), upd.GetPath(), upd.GetAccess(), upd.GetExpectedHash()}
	wantUpd := []string{testWorkspaceID, testRoot, testWSRootNewName, testWSRootPath, client.RootAccessRO, testWSExpectedHash}
	if diff := cmp.Diff(wantUpd, gotUpd); diff != "" {
		t.Fatalf("UpdateRoot request mismatch (-want +got):\n%s", diff)
	}
}

// TestWorkspaceLifecycleAndPolicyMutationsSendFieldsAndSurfaceErrors pins
// DeleteWorkspace, ConfirmWorkspaceConfig and RejectWorkspaceConfig fields and
// coded error paths.
func TestWorkspaceLifecycleAndPolicyMutationsSendFieldsAndSurfaceErrors(t *testing.T) {
	t.Parallel()
	fake := &workspaceFake{}
	c := serveWorkspaceFake(t, fake)
	ctx := testCtx(t)

	if err := c.DeleteWorkspace(ctx, testWorkspaceID, testWSExpectedHash, testWorkspaceName); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmWorkspaceConfig(ctx, testWorkspaceID, testWSExpectedHash); err != nil {
		t.Fatal(err)
	}
	if err := c.RejectWorkspaceConfig(ctx, testWorkspaceID, testWSExpectedHash); err != nil {
		t.Fatal(err)
	}

	del := fake.deleted[0]
	if del.GetId() != testWorkspaceID || del.GetExpectedHash() != testWSExpectedHash || del.GetConfirmName() != testWorkspaceName {
		t.Fatalf("DeleteWorkspace request = %+v", del)
	}
	conf := fake.confirm[0]
	if conf.GetId() != testWorkspaceID || conf.GetExpectedHash() != testWSExpectedHash {
		t.Fatalf("ConfirmWorkspaceConfig request = %+v", conf)
	}
	rej := fake.reject[0]
	if rej.GetId() != testWorkspaceID || rej.GetExpectedHash() != testWSExpectedHash {
		t.Fatalf("RejectWorkspaceConfig request = %+v", rej)
	}

	fake.mu.Lock()
	fake.err = scriptedWorkspaceError(t, testWSCodeConflict)
	fake.mu.Unlock()
	assertCodedRefusal(t, c.DeleteWorkspace(ctx, testWorkspaceID, testWSExpectedHash, testWorkspaceName), testWSCodeConflict)
	assertCodedRefusal(t, c.ConfirmWorkspaceConfig(ctx, testWorkspaceID, testWSExpectedHash), testWSCodeConflict)
	assertCodedRefusal(t, c.RejectWorkspaceConfig(ctx, testWorkspaceID, testWSExpectedHash), testWSCodeConflict)
}

// TestWorkspaceCodeOfUnwrapsHostDetailAndIgnoresOthers pins WorkspaceCodeOf
// across a coded ErrorDetail, a non-connect error, a bare connect error and
// an unrelated connect error detail.
func TestWorkspaceCodeOfUnwrapsHostDetailAndIgnoresOthers(t *testing.T) {
	t.Parallel()
	if code, ok := client.WorkspaceCodeOf(scriptedWorkspaceError(t, testWSCodeConflict)); !ok || code != testWSCodeConflict {
		t.Fatalf("coded connect error = (%q, %v), want (%q, true)", code, ok, testWSCodeConflict)
	}
	if code, ok := client.WorkspaceCodeOf(errWorkspaceScripted); ok || code != "" {
		t.Fatalf("non-connect error = (%q, %v), want empty false", code, ok)
	}
	bare := connect.NewError(connect.CodeInvalidArgument, errWorkspaceScripted)
	if code, ok := client.WorkspaceCodeOf(bare); ok || code != "" {
		t.Fatalf("bare connect error = (%q, %v), want empty false", code, ok)
	}
	other := connect.NewError(connect.CodeInvalidArgument, errWorkspaceScripted)
	detail, err := connect.NewErrorDetail(wrapperspb.String("unrelated"))
	if err != nil {
		t.Fatal(err)
	}
	other.AddDetail(detail)
	if code, ok := client.WorkspaceCodeOf(other); ok || code != "" {
		t.Fatalf("unrelated detail connect error = (%q, %v), want empty false", code, ok)
	}
}
