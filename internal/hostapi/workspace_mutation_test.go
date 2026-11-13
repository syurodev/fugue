package hostapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	hostv1connect "github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	workspacev1connect "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

const (
	workspaceTestMainRoot    = "main"
	workspaceTestRenamedRoot = "renamed"
)

func TestWorkspaceRootMutationsOverSocket_WSE08(t *testing.T) {
	t.Parallel()
	srv, home := hostWithWorkspaceServer(t)
	host, api := connectedWorkspaceClient(t, srv)
	stream := workspaceEventStream(t, host)
	ws := createWorkspaceMutationFixture(t, api, stream, "roots-api", 1)
	first := mkdir(t, filepath.Join(t.TempDir(), "first"))
	second := mkdir(t, filepath.Join(t.TempDir(), "second"))
	ws = addWorkspaceRoot(t, api, stream, ws, first, 2)
	assertWorkspaceFile(t, home, ws.Id, workspaceTestMainRoot, first, "rw")
	assertStaleRootUpdateLeavesConfig(t, api, ws, second)
	ws = changeWorkspaceRootPath(t, api, stream, ws, second, 3, 3)
	assertWorkspaceFile(t, home, ws.Id, workspaceTestMainRoot, second, "rw")
	ws = renameWorkspaceRoot(t, api, stream, ws, 4, 4)
	assertWorkspaceFile(t, home, ws.Id, workspaceTestRenamedRoot, second, "rw")
	ws = setWorkspaceRootReadOnly(t, api, stream, ws, 5, 5)
	assertWorkspaceFile(t, home, ws.Id, workspaceTestRenamedRoot, second, "ro")
	ws = removeWorkspaceRoot(t, api, stream, ws, 6)
	if ws.ConfigRevision != 6 || ws.PolicyRevision != 6 || len(ws.Roots) != 0 {
		t.Fatalf("removed workspace = %+v", ws)
	}
}

func TestWorkspaceRootValidationOverSocket(t *testing.T) {
	t.Parallel()
	srv, home := hostWithWorkspaceServer(t)
	_, api := connectedWorkspaceClient(t, srv)
	ws, err := api.CreateWorkspace(t.Context(), connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: "root-validation"}))
	if err != nil {
		t.Fatal(err)
	}
	base := mkdir(t, filepath.Join(t.TempDir(), "root"))
	nested := mkdir(t, filepath.Join(base, "nested"))
	wsView := addWorkspaceRoot(t, api, nil, ws.Msg.Workspace, base, 0)
	assertInvalidRoot(t, api, wsView, "other", home, connect.CodePermissionDenied, errcode.WorkspacePathDenied)
	assertInvalidRoot(t, api, wsView, "other", srv.cfg.DataDir, connect.CodePermissionDenied, errcode.WorkspacePathDenied)
	assertInvalidRoot(t, api, wsView, "other", filepath.Join(t.TempDir(), "missing"), connect.CodeNotFound, errcode.WorkspaceRootMissing)
	assertInvalidRoot(t, api, wsView, workspaceTestMainRoot, base, connect.CodeInvalidArgument, errcode.WorkspaceRootInvalid)
	assertInvalidRoot(t, api, wsView, "child", nested, connect.CodeInvalidArgument, errcode.WorkspaceRootInvalid)
	if !strings.Contains(string(wsView.RawToml), base) {
		t.Fatalf("valid root was not stored in TOML: %s", wsView.RawToml)
	}
}

func TestDeleteWorkspaceOverSocketIsConfinedAndConfirmed(t *testing.T) {
	t.Parallel()
	srv, home := hostWithWorkspaceServer(t)
	host, api := connectedWorkspaceClient(t, srv)
	stream := workspaceEventStream(t, host)
	root := mkdir(t, filepath.Join(t.TempDir(), "real-root"))
	content := filepath.Join(root, "keep.txt")
	if writeErr := os.WriteFile(content, []byte("preserve"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	ws := createWorkspaceMutationFixture(t, api, stream, "delete-me", 1)
	ws = addWorkspaceRoot(t, api, stream, ws, root, 2)
	assertInvalidWorkspaceIDs(t, api, ws)
	assertDeleteConfirmationRequired(t, api, ws, home)
	assertWorkspaceDeletePreservesRoot(t, api, stream, ws, home, srv.cfg.DataDir, content)
	if listed, listErr := api.ListWorkspaces(t.Context(), connect.NewRequest(&workspacev1.ListWorkspacesRequest{})); listErr != nil || len(listed.Msg.Workspaces) != 0 {
		t.Fatalf("deleted workspace is still listed: %+v, err %v", listed, listErr)
	}
}

func TestDeleteWorkspaceRejectsWorkspaceSymlinkOverSocket(t *testing.T) {
	t.Parallel()
	srv, home := hostWithWorkspaceServer(t)
	_, api := connectedWorkspaceClient(t, srv)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	ws := createWorkspaceMutationFixture(t, api, nil, "symlink", 0)
	workspaceDir := filepath.Join(home, "workspaces", ws.Id)
	if removeErr := os.RemoveAll(workspaceDir); removeErr != nil {
		t.Fatal(removeErr)
	}
	if linkErr := os.Symlink(root, workspaceDir); linkErr != nil {
		t.Fatal(linkErr)
	}
	_, callErr := api.DeleteWorkspace(t.Context(), connect.NewRequest(&workspacev1.DeleteWorkspaceRequest{Id: ws.Id, ExpectedHash: ws.ConfigHash, ConfirmName: ws.Name}))
	assertConnectCode(t, callErr, connect.CodePermissionDenied, errcode.WorkspacePathDenied)
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		t.Fatalf("root was touched: info=%v, err=%v", info, statErr)
	}
	acceptedPath := filepath.Join(srv.cfg.DataDir, "workspaces", ws.Id, "accepted.toml")
	if _, statErr := os.Lstat(acceptedPath); statErr != nil {
		t.Fatalf("rejected deletion removed accepted copy: %v", statErr)
	}
}

func connectedWorkspaceClient(t *testing.T, srv *Server) (hostv1connect.HostServiceClient, workspacev1connect.WorkspaceServiceClient) {
	t.Helper()
	host, api := workspaceSocketClients(t, srv.cfg.Socket.Path)
	if _, helloErr := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); helloErr != nil {
		t.Fatal(helloErr)
	}
	return host, api
}

func workspaceEventStream(t *testing.T, host hostv1connect.HostServiceClient) *connect.ServerStreamForClient[hostv1.SubscribeResponse] {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	stream, err := host.Subscribe(ctx, subscribeRequest(app.TopicWorkspaces))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, stream)
	return stream
}

func createWorkspaceMutationFixture(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], name string, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	created, err := api.CreateWorkspace(t.Context(), connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: name}))
	if err != nil {
		t.Fatal(err)
	}
	if stream != nil {
		assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceCreated)
	}
	return created.Msg.Workspace
}

func addWorkspaceRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, path string, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	rootName := workspaceTestMainRoot
	added, err := api.AddRoot(t.Context(), connect.NewRequest(&workspacev1.AddRootRequest{Id: ws.Id, Name: rootName, Path: path, Access: "rw", ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	ws = added.Msg.Workspace
	assertMutationState(t, ws, 2, 2, rootName, path, "rw")
	if stream != nil {
		assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceRootAdded)
	}
	return ws
}

func assertStaleRootUpdateLeavesConfig(t *testing.T, api workspacev1connect.WorkspaceServiceClient, ws *workspacev1.Workspace, newPath string) {
	t.Helper()
	oldTOML := string(ws.RawToml)
	_, callErr := api.UpdateRoot(t.Context(), connect.NewRequest(&workspacev1.UpdateRootRequest{Id: ws.Id, Name: workspaceTestMainRoot, Path: newPath, ExpectedHash: "old-hash"}))
	assertConnectCode(t, callErr, connect.CodeFailedPrecondition, errcode.WorkspaceConfigChanged)
	current, getErr := api.GetWorkspace(t.Context(), connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: ws.Id}))
	if getErr != nil || string(current.Msg.Workspace.RawToml) != oldTOML {
		t.Fatalf("stale root update changed config: err=%v", getErr)
	}
}

func changeWorkspaceRootPath(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, path string, revision int64, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	changed, err := api.UpdateRoot(t.Context(), connect.NewRequest(&workspacev1.UpdateRootRequest{Id: ws.Id, Name: workspaceTestMainRoot, Path: path, ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	ws = changed.Msg.Workspace
	assertMutationState(t, ws, revision, revision, workspaceTestMainRoot, path, "rw")
	assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceRootUpdated)
	return ws
}

func renameWorkspaceRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, revision int64, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	renamed, err := api.UpdateRoot(t.Context(), connect.NewRequest(&workspacev1.UpdateRootRequest{Id: ws.Id, Name: workspaceTestMainRoot, NewName: workspaceTestRenamedRoot, ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	ws = renamed.Msg.Workspace
	assertMutationState(t, ws, revision, revision, workspaceTestRenamedRoot, ws.Roots[0].Path, "rw")
	assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceRootUpdated)
	return ws
}

func setWorkspaceRootReadOnly(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, revision int64, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	updated, err := api.UpdateRoot(t.Context(), connect.NewRequest(&workspacev1.UpdateRootRequest{Id: ws.Id, Name: workspaceTestRenamedRoot, Access: "ro", ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	ws = updated.Msg.Workspace
	assertMutationState(t, ws, revision, revision, workspaceTestRenamedRoot, ws.Roots[0].Path, "ro")
	assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceRootUpdated)
	return ws
}

func removeWorkspaceRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, sequence uint64) *workspacev1.Workspace {
	t.Helper()
	removed, err := api.RemoveRoot(t.Context(), connect.NewRequest(&workspacev1.RemoveRootRequest{Id: ws.Id, Name: workspaceTestRenamedRoot, ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceEvent(t, stream, sequence, app.EventWorkspaceRootRemoved)
	return removed.Msg.Workspace
}

func assertInvalidRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, ws *workspacev1.Workspace, name, path string, status connect.Code, code errcode.Code) {
	t.Helper()
	_, callErr := api.AddRoot(t.Context(), connect.NewRequest(&workspacev1.AddRootRequest{Id: ws.Id, Name: name, Path: path, Access: "rw", ExpectedHash: ws.ConfigHash}))
	assertConnectCode(t, callErr, status, code)
}

func assertInvalidWorkspaceIDs(t *testing.T, api workspacev1connect.WorkspaceServiceClient, ws *workspacev1.Workspace) {
	t.Helper()
	for _, id := range []string{"../x", filepath.Join(string(filepath.Separator), "tmp", "x")} {
		_, callErr := api.DeleteWorkspace(t.Context(), connect.NewRequest(&workspacev1.DeleteWorkspaceRequest{Id: id, ExpectedHash: ws.ConfigHash, ConfirmName: ws.Name}))
		assertConnectCode(t, callErr, connect.CodeNotFound, errcode.WorkspaceNotFound)
	}
}

func assertDeleteConfirmationRequired(t *testing.T, api workspacev1connect.WorkspaceServiceClient, ws *workspacev1.Workspace, home string) {
	t.Helper()
	requests := []*workspacev1.DeleteWorkspaceRequest{
		{Id: ws.Id, ConfirmName: ws.Name},
		{Id: ws.Id, ExpectedHash: ws.ConfigHash},
		{Id: ws.Id, ExpectedHash: ws.ConfigHash, ConfirmName: "wrong-name"},
	}
	for _, req := range requests {
		_, callErr := api.DeleteWorkspace(t.Context(), connect.NewRequest(req))
		assertConnectCode(t, callErr, connect.CodeFailedPrecondition, errcode.WorkspaceConfigUnconfirmed)
		if _, statErr := os.Stat(filepath.Join(home, "workspaces", ws.Id)); statErr != nil {
			t.Fatalf("failed confirmation removed workspace: %v", statErr)
		}
		assertWorkspaceStillListed(t, api, ws.Id)
	}
}

func assertWorkspaceStillListed(t *testing.T, api workspacev1connect.WorkspaceServiceClient, id string) {
	t.Helper()
	listed, err := api.ListWorkspaces(t.Context(), connect.NewRequest(&workspacev1.ListWorkspacesRequest{}))
	if err != nil || len(listed.Msg.Workspaces) != 1 || listed.Msg.Workspaces[0].Id != id {
		t.Fatalf("workspace changed after rejected deletion: %+v, err %v", listed, err)
	}
}

func assertWorkspaceDeletePreservesRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace, home, dataDir, content string) {
	t.Helper()
	workspaceDir := filepath.Join(home, "workspaces", ws.Id)
	if err := os.Symlink(filepath.Dir(content), filepath.Join(workspaceDir, "linked-root")); err != nil {
		t.Fatal(err)
	}
	deleted, err := api.DeleteWorkspace(t.Context(), connect.NewRequest(&workspacev1.DeleteWorkspaceRequest{Id: ws.Id, ExpectedHash: ws.ConfigHash, ConfirmName: ws.Name}))
	if err != nil || deleted.Msg.Id != ws.Id {
		t.Fatalf("DeleteWorkspace = %+v, err %v", deleted, err)
	}
	assertWorkspaceEvent(t, stream, 3, app.EventWorkspaceDeleted)
	assertWorkspacePathsRemoved(t, ws.Id, workspaceDir, dataDir)
	data, readErr := os.ReadFile(filepath.Clean(content))
	if readErr != nil || string(data) != "preserve" {
		t.Fatalf("root content changed: %q, err %v", data, readErr)
	}
}

func assertWorkspacePathsRemoved(t *testing.T, id, workspaceDir, dataDir string) {
	t.Helper()
	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("workspace directory still exists: %v", err)
	}
	acceptedPath := filepath.Join(dataDir, "workspaces", id, "accepted.toml")
	if _, err := os.Lstat(acceptedPath); !os.IsNotExist(err) {
		t.Fatalf("accepted copy still exists: %v", err)
	}
}

func assertMutationState(t *testing.T, ws *workspacev1.Workspace, configRevision, policyRevision int64, name, path, access string) {
	t.Helper()
	path = canonicalWorkspaceTestPath(t, path)
	if ws.ConfigRevision != configRevision || ws.PolicyRevision != policyRevision || len(ws.Roots) != 1 ||
		ws.Roots[0].Name != name || ws.Roots[0].Path != path || ws.Roots[0].Access != access {
		t.Fatalf("workspace mutation result = %+v", ws)
	}
}

func canonicalWorkspaceTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertWorkspaceFile(t *testing.T, home, id string, roots ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(filepath.Join(home, "workspaces", id, "WORKSPACE.md")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range roots {
		if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil {
			want = resolved
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("WORKSPACE.md does not contain %q: %s", want, data)
		}
	}
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
