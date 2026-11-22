package hostapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

const (
	testPatchCmdNonPolicy = "patch-non-policy-1"
	testPatchKeyIdle      = "teams.idle_detach_seconds"
)

func TestPatchWorkspaceConfigPreservesCommentsAndEnforcesCASAndPolicy(t *testing.T) {
	t.Parallel()
	srv, fugueHome := hostWithWorkspaceServer(t)
	host, workspaces := workspaceSocketClients(t, srv.cfg.Socket.Path)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); err != nil {
		t.Fatal(err)
	}
	ws := seedPatchWorkspace(t, workspaces)
	patched := assertNonPolicyPatchAndDedup(t, workspaces, ws)
	polPatched := assertPatchCASAndPolicy(t, workspaces, ws.Id, ws.ConfigHash, patched)
	assertPatchBoundsAndCorrupt(t, workspaces, fugueHome, ws.Id, polPatched.ConfigHash)
}

func seedPatchWorkspace(t *testing.T, workspaces workspacev1connect.WorkspaceServiceClient) *workspacev1.Workspace {
	t.Helper()
	created, err := workspaces.CreateWorkspace(t.Context(), connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: "patch-e2e"}))
	if err != nil {
		t.Fatal(err)
	}
	initial := strings.Join([]string{
		"# Top comment",
		"name = \"patch-e2e\"",
		"",
		"[policy]",
		"# Policy comment",
		"sandbox_profile = \"workspace-write\"",
		"approval_mode = \"auto\" # keep inline",
		"",
		"[teams]",
		"team_max_agents = 10",
		"idle_detach_seconds = 300",
		"",
	}, "\n")
	seeded, err := workspaces.UpdateWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{
		Id: created.Msg.Workspace.Id, RawToml: []byte(initial), ExpectedHash: created.Msg.Workspace.ConfigHash,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return seeded.Msg.Workspace
}

func assertNonPolicyPatchAndDedup(t *testing.T, workspaces workspacev1connect.WorkspaceServiceClient, ws *workspacev1.Workspace) *workspacev1.Workspace {
	t.Helper()
	patches := []*workspacev1.ConfigPatch{
		{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(90)},
		{DottedKey: "teams.team_max_agents", Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_DELETE},
	}
	patched, err := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: testPatchCmdNonPolicy, WorkspaceId: ws.Id, ExpectedHash: ws.ConfigHash, Patches: patches,
	}))
	if err != nil {
		t.Fatalf("PatchWorkspaceConfig non-policy: %v", err)
	}
	got := string(patched.Msg.Workspace.RawToml)
	for _, want := range []string{"# Top comment", "# Policy comment", "approval_mode = \"auto\" # keep inline", "idle_detach_seconds = 90"} {
		if !strings.Contains(got, want) {
			t.Fatalf("patched TOML missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "team_max_agents") {
		t.Fatalf("deleted key still present:\n%s", got)
	}
	if patched.Msg.Workspace.PolicyRevision != ws.PolicyRevision {
		t.Fatalf("PolicyRevision = %d, want %d", patched.Msg.Workspace.PolicyRevision, ws.PolicyRevision)
	}
	replayed, err := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: testPatchCmdNonPolicy, WorkspaceId: ws.Id, ExpectedHash: ws.ConfigHash, Patches: patches,
	}))
	if err != nil || replayed.Msg.Workspace.ConfigHash != patched.Msg.Workspace.ConfigHash {
		t.Fatalf("command_id replay = (%+v, %v)", replayed, err)
	}
	_, mismatchErr := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: testPatchCmdNonPolicy, WorkspaceId: ws.Id, ExpectedHash: ws.ConfigHash,
		Patches: []*workspacev1.ConfigPatch{{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(45)}},
	}))
	if connect.CodeOf(mismatchErr) != connect.CodeInvalidArgument {
		t.Fatalf("command_id reuse mismatch code = %v, want InvalidArgument", connect.CodeOf(mismatchErr))
	}
	return patched.Msg.Workspace
}

func assertPatchCASAndPolicy(t *testing.T, workspaces workspacev1connect.WorkspaceServiceClient, wsID, staleHash string, patched *workspacev1.Workspace) *workspacev1.Workspace {
	t.Helper()
	_, staleErr := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "patch-stale-1", WorkspaceId: wsID, ExpectedHash: staleHash,
		Patches: []*workspacev1.ConfigPatch{{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(60)}},
	}))
	assertConnectCode(t, staleErr, connect.CodeFailedPrecondition, errcode.WorkspaceConfigChanged)

	polPatched, err := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "patch-policy-1", WorkspaceId: wsID, ExpectedHash: patched.ConfigHash,
		Patches: []*workspacev1.ConfigPatch{{DottedKey: "policy.approval_mode", Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewStringValue("ask")}},
	}))
	if err != nil {
		t.Fatalf("PatchWorkspaceConfig policy: %v", err)
	}
	if polPatched.Msg.Workspace.PolicyRevision != patched.PolicyRevision+1 {
		t.Fatalf("PolicyRevision = %d, want %d", polPatched.Msg.Workspace.PolicyRevision, patched.PolicyRevision+1)
	}
	if !strings.Contains(string(polPatched.Msg.Workspace.RawToml), "approval_mode = \"ask\" # keep inline") {
		t.Fatalf("inline comment lost on policy patch:\n%s", polPatched.Msg.Workspace.RawToml)
	}
	return polPatched.Msg.Workspace
}

func assertPatchBoundsAndCorrupt(t *testing.T, workspaces workspacev1connect.WorkspaceServiceClient, fugueHome, wsID, curHash string) {
	t.Helper()
	_, emptyErr := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "patch-empty-1", WorkspaceId: wsID, ExpectedHash: curHash,
	}))
	if connect.CodeOf(emptyErr) != connect.CodeInvalidArgument {
		t.Fatalf("empty patches code = %v, want InvalidArgument", connect.CodeOf(emptyErr))
	}
	oversized := make([]*workspacev1.ConfigPatch, 65)
	for i := range oversized {
		oversized[i] = &workspacev1.ConfigPatch{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(60)}
	}
	_, overErr := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "patch-over-1", WorkspaceId: wsID, ExpectedHash: curHash, Patches: oversized,
	}))
	if connect.CodeOf(overErr) != connect.CodeInvalidArgument {
		t.Fatalf("oversized patches code = %v, want InvalidArgument", connect.CodeOf(overErr))
	}

	diskPath := filepath.Join(fugueHome, "workspaces", wsID, "fugue.toml")
	if writeErr := os.WriteFile(diskPath, []byte("not = [valid toml\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	corruptView, err := workspaces.GetWorkspace(t.Context(), connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: wsID}))
	if err != nil {
		t.Fatal(err)
	}
	_, corruptErr := workspaces.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "patch-corrupt-1", WorkspaceId: wsID, ExpectedHash: corruptView.Msg.Workspace.ConfigHash,
		Patches: []*workspacev1.ConfigPatch{{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(60)}},
	}))
	assertConnectCode(t, corruptErr, connect.CodeInvalidArgument, errcode.WorkspaceConfigInvalid)
}

func TestPatchWorkspaceConfigDeniesNonAppAndSessionPrincipals(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	v.readySession(t, "cmd-lead-patch", "", t.TempDir())

	_, attachHTTP, attachErr := attachSession(t, v.sockPath, sessionTokens(t, 1)[0])
	if attachErr != nil {
		t.Fatal(attachErr)
	}
	sessionWS := workspacev1connect.NewWorkspaceServiceClient(attachHTTP, "http://fugue")
	_, sessErr := sessionWS.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "sess-patch-1", WorkspaceId: "ws-1", ExpectedHash: "h1",
		Patches: []*workspacev1.ConfigPatch{{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(60)}},
	}))
	if connect.CodeOf(sessErr) != connect.CodePermissionDenied {
		t.Fatalf("session principal PatchWorkspaceConfig code = %v, want PermissionDenied", connect.CodeOf(sessErr))
	}

	cliHost, cliWS := workspaceSocketClients(t, v.sockPath)
	if _, err := cliHost.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatal(err)
	}
	_, cliErr := cliWS.PatchWorkspaceConfig(t.Context(), connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
		CommandId: "cli-patch-1", WorkspaceId: "ws-1", ExpectedHash: "h1",
		Patches: []*workspacev1.ConfigPatch{{DottedKey: testPatchKeyIdle, Op: workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET, Value: structpb.NewNumberValue(60)}},
	}))
	if connect.CodeOf(cliErr) != connect.CodePermissionDenied {
		t.Fatalf("cli principal PatchWorkspaceConfig code = %v, want PermissionDenied", connect.CodeOf(cliErr))
	}
}
