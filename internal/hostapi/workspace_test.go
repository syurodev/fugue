package hostapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

func workspaceSocketClients(t *testing.T, sockPath string) (hostv1connect.HostServiceClient, workspacev1connect.WorkspaceServiceClient) {
	t.Helper()
	host, httpClient := newTestClient(t, sockPath)
	return host, workspacev1connect.NewWorkspaceServiceClient(httpClient, "http://fugue")
}

func hostWithWorkspaceServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := shortDir(t)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Unix(0, 0))
	sock, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{Socket: sock, DataDir: dir, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("conn")), HostUID: int64(os.Getuid())})
	hp, err := app.StartHost(t.Context(), &app.HostDeps{
		DataDir:      dir,
		WorkspaceEnv: config.MapEnv{config.EnvFugueHome: fugueHome},
		Config:       app.HostConfig{HostVersion: testHostE2EVersion, APIVersions: []string{"v1"}},
		Clock:        clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("host")), Transport: srv,
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
	return srv, fugueHome
}

func TestWorkspaceServiceEndToEndOverSocket(t *testing.T) {
	t.Parallel()
	srv, fugueHome := hostWithWorkspaceServer(t)
	host, workspaces := workspaceSocketClients(t, srv.cfg.Socket.Path)
	if _, callErr := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); callErr != nil {
		t.Fatal(callErr)
	}
	stream, err := host.Subscribe(streamCtx(t), subscribeRequest(app.TopicWorkspaces))
	if err != nil {
		t.Fatal(err)
	}
	initial := recv(t, stream).GetSnapshot()
	if initial == nil || string(initial.State) != "[]" || initial.Sequence != 0 {
		t.Fatalf("workspace snapshot = %+v, want empty snapshot at sequence zero", initial)
	}

	ws := createWorkspaceForTest(t, workspaces, stream)
	assertEffectiveConfig(t, workspaces, ws.Id)
	updateWorkspaceWithCAS(t, workspaces, stream, ws)
	assertInvalidConfig(t, workspaces, ws.Id)
	confirmed := confirmWorkspacePolicy(t, workspaces, stream, ws.Id, fugueHome)
	rejected := rejectWorkspacePolicy(t, workspaces, stream, ws.Id, fugueHome, confirmed.ConfigHash)
	assertWorkspaceMissingRoot(t, workspaces, stream, ws.Id, rejected.ConfigHash)
	assertWorkspaceSnapshot(t, host, ws.Id)
}

func assertWorkspaceSnapshot(t *testing.T, host hostv1connect.HostServiceClient, id string) {
	t.Helper()
	stream, err := host.Subscribe(streamCtx(t), subscribeRequest(app.TopicWorkspaces))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recv(t, stream).GetSnapshot()
	if snapshot == nil {
		t.Fatal("workspaces subscription did not start with a snapshot")
	}
	var summaries []app.WorkspaceSummary
	if err := json.Unmarshal(snapshot.State, &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != id || summaries[0].ConfigRevision != 4 || summaries[0].PendingPolicyConfirmation {
		t.Fatalf("workspace snapshot summaries = %+v", summaries)
	}
	if strings.Contains(string(snapshot.State), "raw_toml") || strings.Contains(string(snapshot.State), "[policy]") {
		t.Fatal("workspace snapshot leaked raw TOML")
	}
}

func createWorkspaceForTest(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse]) *workspacev1.Workspace {
	t.Helper()
	created, err := api.CreateWorkspace(t.Context(), connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: "api-workspace"}))
	if err != nil {
		t.Fatal(err)
	}
	ws := created.Msg.Workspace
	if ws.Id == "" || ws.Name != "api-workspace" || ws.ConfigHash == "" || len(ws.RawToml) == 0 {
		t.Fatalf("created workspace = %+v", ws)
	}
	assertWorkspaceEvent(t, stream, 1, app.EventWorkspaceCreated)
	return ws
}

func assertEffectiveConfig(t *testing.T, api workspacev1connect.WorkspaceServiceClient, id string) {
	t.Helper()
	effective, err := api.GetEffectiveConfig(t.Context(), connect.NewRequest(&workspacev1.GetEffectiveConfigRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	if effective.Msg.Values == nil || len(effective.Msg.Provenance) == 0 {
		t.Fatalf("effective config lacks values or provenance: %+v", effective.Msg)
	}
}

func updateWorkspaceWithCAS(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], ws *workspacev1.Workspace) *workspacev1.Workspace {
	t.Helper()
	updatedRaw := []byte("name = \"api-updated\"\n")
	updated, err := api.UpdateWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{Id: ws.Id, RawToml: updatedRaw, ExpectedHash: ws.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Msg.Workspace.Name != "api-updated" || updated.Msg.Workspace.ConfigRevision != 2 {
		t.Fatalf("updated workspace = %+v", updated.Msg.Workspace)
	}
	assertWorkspaceEvent(t, stream, 2, app.EventWorkspaceConfigChanged)
	_, err = api.UpdateWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{Id: ws.Id, RawToml: []byte("name = \"lost-update\"\n"), ExpectedHash: ws.ConfigHash}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.WorkspaceConfigChanged)
	current, err := api.GetWorkspace(t.Context(), connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: ws.Id}))
	if err != nil || string(current.Msg.Workspace.RawToml) != string(updatedRaw) {
		t.Fatalf("stale update changed config: %+v, err %v", current, err)
	}
	return updated.Msg.Workspace
}

func assertInvalidConfig(t *testing.T, api workspacev1connect.WorkspaceServiceClient, id string) {
	t.Helper()
	marker := "must-not-leak-configuration-value"
	validated, err := api.ValidateConfig(t.Context(), connect.NewRequest(&workspacev1.ValidateConfigRequest{Id: id, RawToml: []byte("name = \"safe\"\nunknown_key = \"" + marker + "\"\n")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Msg.Issues) != 1 || validated.Msg.Issues[0].Key != "unknown_key" || validated.Msg.Issues[0].Line == 0 || validated.Msg.Issues[0].Column == 0 {
		t.Fatalf("validation issue = %+v", validated.Msg.Issues)
	}
	if strings.Contains(validated.Msg.Issues[0].Message, marker) {
		t.Fatal("validation response leaked the config value")
	}
}

func confirmWorkspacePolicy(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], id, home string) *workspacev1.Workspace {
	t.Helper()
	disk := filepath.Join(home, "workspaces", id, "fugue.toml")
	pendingRaw := []byte("name = \"api-updated\"\n\n[policy]\napproval_mode = \"full\"\n")
	if err := os.WriteFile(disk, pendingRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	pending, err := api.GetWorkspace(t.Context(), connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Msg.Workspace.PendingPolicyConfirmation || len(pending.Msg.Workspace.PolicyDiff) == 0 {
		t.Fatalf("workspace did not report pending policy diff: %+v", pending.Msg.Workspace)
	}
	assertWorkspaceEvent(t, stream, 3, app.EventWorkspacePendingConfirm)
	_, err = api.ConfirmWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.ConfirmWorkspaceConfigRequest{Id: id, ExpectedHash: "wrong-hash"}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.WorkspaceConfigChanged)
	confirmed, err := api.ConfirmWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.ConfirmWorkspaceConfigRequest{Id: id, ExpectedHash: pending.Msg.Workspace.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Msg.Workspace.PolicyRevision != 2 || confirmed.Msg.Workspace.PendingPolicyConfirmation {
		t.Fatalf("confirmed workspace = %+v", confirmed.Msg.Workspace)
	}
	assertWorkspaceEvent(t, stream, 4, app.EventWorkspaceConfirmed)
	return confirmed.Msg.Workspace
}

func rejectWorkspacePolicy(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], id, home, currentHash string) *workspacev1.Workspace {
	t.Helper()
	disk := filepath.Join(home, "workspaces", id, "fugue.toml")
	secondPending := []byte("name = \"api-updated\"\n\n[policy]\napproval_mode = \"plan\"\n")
	if err := os.WriteFile(disk, secondPending, 0o600); err != nil {
		t.Fatal(err)
	}
	pending, err := api.GetWorkspace(t.Context(), connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceEvent(t, stream, 5, app.EventWorkspacePendingConfirm)
	rejected, err := api.RejectWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.RejectWorkspaceConfigRequest{Id: id, ExpectedHash: pending.Msg.Workspace.ConfigHash}))
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Msg.Workspace.PolicyRevision != 2 || rejected.Msg.Workspace.PendingPolicyConfirmation || rejected.Msg.Workspace.ConfigHash != currentHash {
		t.Fatalf("rejected workspace = %+v", rejected.Msg.Workspace)
	}
	assertWorkspaceEvent(t, stream, 6, app.EventWorkspaceRejected)
	return rejected.Msg.Workspace
}

func assertWorkspaceMissingRoot(t *testing.T, api workspacev1connect.WorkspaceServiceClient, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], id, expectedHash string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	rootConfig := []byte("name = \"api-updated\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(root) + "\"\naccess = \"rw\"\n")
	rootUpdate, err := api.UpdateWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{Id: id, RawToml: rootConfig, ExpectedHash: expectedHash}))
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceEvent(t, stream, 7, app.EventWorkspaceConfigChanged)
	removeErr := os.RemoveAll(root)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	listed, err := api.ListWorkspaces(t.Context(), connect.NewRequest(&workspacev1.ListWorkspacesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.Workspaces) != 1 || len(listed.Msg.Workspaces[0].MissingRoots) != 1 {
		t.Fatalf("workspace list did not report missing root: %+v (revision %d)", listed.Msg.Workspaces, rootUpdate.Msg.Workspace.ConfigRevision)
	}
}

func TestWorkspaceNonLocalPrincipalIsDeniedOverSocket(t *testing.T) {
	t.Parallel()
	srv, _ := hostWithWorkspaceServer(t)
	host, workspaces := workspaceSocketClients(t, srv.cfg.Socket.Path)
	_, helloErr := host.Hello(t.Context(), helloRequest(hostv1.ClientKind(99), "v1"))
	if connect.CodeOf(helloErr) != connect.CodeInvalidArgument {
		t.Fatalf("unknown principal hello = %v", helloErr)
	}
	_, rpcErr := workspaces.ListWorkspaces(t.Context(), connect.NewRequest(&workspacev1.ListWorkspacesRequest{}))
	if connect.CodeOf(rpcErr) != connect.CodePermissionDenied {
		t.Fatalf("workspace RPC without a local principal = %v", rpcErr)
	}
	stream, err := host.Subscribe(streamCtx(t), subscribeRequest(app.TopicWorkspaces))
	if err != nil {
		t.Fatal(err)
	}
	if stream.Receive() || connect.CodeOf(stream.Err()) != connect.CodePermissionDenied {
		t.Fatalf("workspaces subscription error = %v", stream.Err())
	}
}

func TestWorkspaceRPCRejectsSessionPrincipal(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	state := &connState{id: "test-connection", kind: app.ClientKind("session"), phase: phaseLeased}
	ctx := context.WithValue(t.Context(), connStateKey{}, state)
	_, err := (&workspaceHandler{backend: backend}).ListWorkspaces(ctx, connect.NewRequest(&workspacev1.ListWorkspacesRequest{}))
	assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
}

func assertWorkspaceEvent(t *testing.T, stream *connect.ServerStreamForClient[hostv1.SubscribeResponse], sequence uint64, eventType string) {
	t.Helper()
	msg := recv(t, stream).GetEvent()
	if msg == nil || msg.Sequence != sequence || msg.Type != eventType {
		t.Fatalf("workspace event = %+v, want sequence %d type %s", msg, sequence, eventType)
	}
	var summary map[string]any
	if err := json.Unmarshal(msg.Payload, &summary); err != nil {
		t.Fatal(err)
	}
	if _, hasRaw := summary["raw_toml"]; hasRaw {
		t.Fatal("workspace event leaked raw TOML")
	}
}
