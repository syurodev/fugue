package client_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

const (
	testCfgWSName     = "alpha"
	testCfgModeAuto   = "auto"
	testCfgModeAsk    = "ask"
	testCfgShellAllow = "git status"
	testCfgTagProd    = "prod"
	testCfgBadKey     = "bad_key"
	testCfgProvKey    = "policy.approval_mode"
)

var (
	errScriptedConfigChanged = errors.New("workspace configuration changed")
	errScriptedConfigInvalid = errors.New("workspace configuration is invalid at key bad_key (line 4, column 2)")
)

type workspaceConfigFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	workspacev1connect.UnimplementedWorkspaceServiceHandler

	mu         sync.Mutex
	rawTOML    []byte
	hash       string
	revision   int64
	policyRev  int64
	values     map[string]any
	provenance map[string]int64
	issues     []*workspacev1.ConfigIssue
	updateErr  error
	lastPatch  *workspacev1.PatchWorkspaceConfigRequest
}

func newWorkspaceConfigFake() *workspaceConfigFake {
	return &workspaceConfigFake{
		rawTOML:  []byte("name = \"" + testCfgWSName + "\"\n"),
		hash:     "hash-v1",
		revision: 1,
		values: map[string]any{
			"Name": testCfgWSName,
			"Policy": map[string]any{
				"ApprovalMode":   testCfgModeAuto,
				"SandboxProfile": "workspace-write",
				"ShellAllow":     []any{testCfgShellAllow},
				"EnvTags": []any{
					map[string]any{"Match": "*." + testCfgTagProd, "Tag": testCfgTagProd},
				},
			},
			"Session": map[string]any{
				"Teams":            true,
				"TeamsAutoApprove": false,
			},
			"Teams": map[string]any{
				"IdleDetachSeconds":     float64(300),
				"TaskDeadlineSeconds":   float64(3600),
				"TaskMaxTurns":          float64(50),
				"SilenceTimeoutSeconds": float64(600),
				"TeamDeadlineSeconds":   float64(14400),
				"TeamMaxAgents":         float64(12),
			},
		},
		provenance: map[string]int64{
			testCfgProvKey:              client.ConfigTierDefault,
			"policy.sandbox_profile":    client.ConfigTierDefault,
			"teams.idle_detach_seconds": client.ConfigTierGlobal,
		},
	}
}

func (f *workspaceConfigFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *workspaceConfigFake) GetWorkspace(_ context.Context, req *connect.Request[workspacev1.GetWorkspaceRequest]) (*connect.Response[workspacev1.GetWorkspaceResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&workspacev1.GetWorkspaceResponse{
		Workspace: &workspacev1.Workspace{
			Id:             req.Msg.GetId(),
			Name:           testCfgWSName,
			RawToml:        f.rawTOML,
			ConfigHash:     f.hash,
			ConfigRevision: f.revision,
			PolicyRevision: f.policyRev,
		},
	}), nil
}

func (f *workspaceConfigFake) GetEffectiveConfig(context.Context, *connect.Request[workspacev1.GetEffectiveConfigRequest]) (*connect.Response[workspacev1.GetEffectiveConfigResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	val, err := structpb.NewStruct(f.values)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&workspacev1.GetEffectiveConfigResponse{
		Values:     val,
		Provenance: f.provenance,
	}), nil
}

func (f *workspaceConfigFake) ValidateConfig(_ context.Context, req *connect.Request[workspacev1.ValidateConfigRequest]) (*connect.Response[workspacev1.ValidateConfigResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if strings.Contains(string(req.Msg.GetRawToml()), testCfgBadKey) {
		return connect.NewResponse(&workspacev1.ValidateConfigResponse{
			Issues: []*workspacev1.ConfigIssue{
				{Key: testCfgBadKey, Line: 3, Column: 5, Message: "invalid configuration"},
			},
		}), nil
	}
	return connect.NewResponse(&workspacev1.ValidateConfigResponse{Issues: f.issues}), nil
}

func (f *workspaceConfigFake) PatchWorkspaceConfig(_ context.Context, req *connect.Request[workspacev1.PatchWorkspaceConfigRequest]) (*connect.Response[workspacev1.PatchWorkspaceConfigResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if req.Msg.GetExpectedHash() != f.hash {
		return nil, scriptedConfigError(connect.CodeFailedPrecondition, errScriptedConfigChanged, client.WorkspaceCodeConfigChanged)
	}
	for _, p := range req.Msg.GetPatches() {
		if p.GetDottedKey() == testCfgBadKey {
			return nil, scriptedConfigError(connect.CodeInvalidArgument, errScriptedConfigInvalid, client.WorkspaceCodeConfigInvalid)
		}
	}
	f.lastPatch = req.Msg
	f.hash = "hash-patched"
	f.revision++
	return connect.NewResponse(&workspacev1.PatchWorkspaceConfigResponse{
		Workspace: &workspacev1.Workspace{
			Id:             req.Msg.GetWorkspaceId(),
			Name:           testCfgWSName,
			RawToml:        f.rawTOML,
			ConfigHash:     f.hash,
			ConfigRevision: f.revision,
			PolicyRevision: f.policyRev,
		},
	}), nil
}

func scriptedConfigError(connectCode connect.Code, cause error, publicCode string) error {
	cerr := connect.NewError(connectCode, cause)
	detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: publicCode})
	if err == nil {
		cerr.AddDetail(detail)
	}
	return cerr
}

func (f *workspaceConfigFake) UpdateWorkspaceConfig(_ context.Context, req *connect.Request[workspacev1.UpdateWorkspaceConfigRequest]) (*connect.Response[workspacev1.UpdateWorkspaceConfigResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Msg.GetId() == "missing-ws" {
		return nil, connect.NewError(connect.CodeNotFound, errScriptedConfigChanged)
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if req.Msg.GetId() == "empty" {
		return connect.NewResponse(&workspacev1.UpdateWorkspaceConfigResponse{}), nil
	}
	if req.Msg.GetExpectedHash() != f.hash {
		return nil, scriptedConfigError(connect.CodeFailedPrecondition, errScriptedConfigChanged, client.WorkspaceCodeConfigChanged)
	}
	if strings.Contains(string(req.Msg.GetRawToml()), testCfgBadKey) {
		return nil, scriptedConfigError(connect.CodeInvalidArgument, errScriptedConfigInvalid, client.WorkspaceCodeConfigInvalid)
	}
	f.rawTOML = append([]byte(nil), req.Msg.GetRawToml()...)
	f.revision++
	f.hash = "hash-v2"
	return connect.NewResponse(&workspacev1.UpdateWorkspaceConfigResponse{
		Workspace: &workspacev1.Workspace{
			Id:             req.Msg.GetId(),
			Name:           testCfgWSName,
			RawToml:        f.rawTOML,
			ConfigHash:     f.hash,
			ConfigRevision: f.revision,
			PolicyRevision: f.policyRev,
		},
	}), nil
}

func serveWorkspaceConfigFake(t *testing.T, fake *workspaceConfigFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(workspacev1connect.NewWorkspaceServiceHandler(fake))
	return serveFakeOnUnixSocket(t, mux)
}

func TestWorkspaceConfigCASAndValidateOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := testCtx(t)
	c := serveWorkspaceConfigFake(t, newWorkspaceConfigFake())

	ws := assertWorkspaceReadAndEffective(t, ctx, c)
	assertValidateConfigIssues(t, ctx, c)
	assertUpdateCASAndTypedErrors(t, ctx, c, &ws)
}

func assertWorkspaceReadAndEffective(t *testing.T, ctx context.Context, c *client.Client) client.Workspace {
	t.Helper()
	ws, err := c.Workspace(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if string(ws.RawTOML) != "name = \""+testCfgWSName+"\"\n" || ws.ConfigHash != "hash-v1" {
		t.Fatalf("unexpected workspace: %+v", ws)
	}
	eff, err := c.GetEffectiveConfig(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("GetEffectiveConfig: %v", err)
	}
	wantSettings := client.WorkspaceSettings{
		ApprovalMode:          testCfgModeAuto,
		SandboxProfile:        "workspace-write",
		ShellAllow:            []string{testCfgShellAllow},
		EnvTags:               []client.EnvTagRule{{Match: "*." + testCfgTagProd, Tag: testCfgTagProd}},
		SessionTeams:          true,
		TeamsAutoApprove:      false,
		IdleDetachSeconds:     300,
		TaskDeadlineSeconds:   3600,
		TaskMaxTurns:          50,
		SilenceTimeoutSeconds: 600,
		TeamDeadlineSeconds:   14400,
		TeamMaxAgents:         12,
	}
	if diff := cmp.Diff(wantSettings, eff.Settings); diff != "" {
		t.Fatalf("effective settings mismatch (-want +got):\n%s", diff)
	}
	if eff.Provenance["teams.idle_detach_seconds"] != client.ConfigTierGlobal {
		t.Fatalf("provenance = %+v", eff.Provenance)
	}
	return ws
}

func assertValidateConfigIssues(t *testing.T, ctx context.Context, c *client.Client) {
	t.Helper()
	issues, err := c.ValidateConfig(ctx, testWorkspaceID, []byte("name = \""+testCfgWSName+"\"\n\n"+testCfgBadKey+" = 1\n"))
	if err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	wantIssues := []client.WorkspaceConfigIssue{{Key: testCfgBadKey, Line: 3, Column: 5, Message: "invalid configuration"}}
	if diff := cmp.Diff(wantIssues, issues); diff != "" {
		t.Fatalf("issues mismatch (-want +got):\n%s", diff)
	}
	okIssues, err := c.ValidateConfig(ctx, testWorkspaceID, []byte("name = \""+testCfgWSName+"\"\n"))
	if err != nil || len(okIssues) != 0 {
		t.Fatalf("valid ValidateConfig = (%+v, %v), want empty nil", okIssues, err)
	}
}

func assertUpdateCASAndTypedErrors(t *testing.T, ctx context.Context, c *client.Client, ws *client.Workspace) {
	t.Helper()
	updated, err := c.UpdateWorkspaceConfig(ctx, testWorkspaceID, []byte("name = \""+testCfgWSName+"\"\n[teams]\nidle_detach_seconds = 120\n"), ws.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig: %v", err)
	}
	if updated.ConfigHash != "hash-v2" || updated.ConfigRevision != 2 {
		t.Fatalf("updated = %+v", updated)
	}

	_, err = c.UpdateWorkspaceConfig(ctx, testWorkspaceID, []byte("name = \""+testCfgWSName+"\"\n"), ws.ConfigHash)
	if !errors.Is(err, client.ErrWorkspaceConfigChanged) {
		t.Fatalf("stale CAS err = %v, want ErrWorkspaceConfigChanged", err)
	}
	if code, ok := client.WorkspaceCodeOf(err); !ok || code != client.WorkspaceCodeConfigChanged {
		t.Fatalf("WorkspaceCodeOf = (%q, %v)", code, ok)
	}
	assertUpdateValidationErr(t, ctx, c, updated.ConfigHash)
}

func assertUpdateValidationErr(t *testing.T, ctx context.Context, c *client.Client, expectedHash string) {
	t.Helper()
	_, err := c.UpdateWorkspaceConfig(ctx, testWorkspaceID, []byte(testCfgBadKey+" = 1\n"), expectedHash)
	if !errors.Is(err, client.ErrWorkspaceConfigInvalid) {
		t.Fatalf("invalid err = %v, want ErrWorkspaceConfigInvalid", err)
	}
	var verr *client.WorkspaceConfigValidationError
	if !errors.As(err, &verr) || verr.Key != testCfgBadKey || verr.Line != 4 || verr.Column != 2 {
		t.Fatalf("validation error = %+v, want %s 4:2", verr, testCfgBadKey)
	}
}

func TestPatchWorkspaceConfigClientRPC(t *testing.T) {
	t.Parallel()
	fake := newWorkspaceConfigFake()
	c := serveWorkspaceConfigFake(t, fake)
	ctx := t.Context()

	approval := testCfgModeAsk
	sandbox := "read-only"
	allow := []string{testCfgShellAllow, "go test"}
	tags := []client.EnvTagRule{{Match: "prod/*", Tag: testCfgTagProd}}
	teams := false
	autoApprove := true
	updated, err := c.PatchWorkspaceConfig(ctx, &client.PatchWorkspaceConfigInput{
		CommandID:    "cmd-patch-1",
		WorkspaceID:  testWorkspaceID,
		ExpectedHash: fake.hash,
		Patch: &client.WorkspaceSettingsPatch{
			ApprovalMode:          &approval,
			SandboxProfile:        &sandbox,
			ShellAllow:            &allow,
			EnvTags:               &tags,
			SessionTeams:          &teams,
			TeamsAutoApprove:      &autoApprove,
			IdleDetachSeconds:     intPtr(90),
			TaskDeadlineSeconds:   intPtr(1800),
			TaskMaxTurns:          intPtr(25),
			SilenceTimeoutSeconds: intPtr(300),
			TeamDeadlineSeconds:   intPtr(7200),
			TeamMaxAgents:         intPtr(6),
		},
	})
	if err != nil {
		t.Fatalf("PatchWorkspaceConfig: %v", err)
	}
	if updated.ConfigHash != "hash-patched" || updated.ConfigRevision != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	if fake.lastPatch == nil || fake.lastPatch.GetCommandId() != "cmd-patch-1" || len(fake.lastPatch.GetPatches()) != 13 {
		t.Fatalf("lastPatch = %+v, want 13 patches (12 settings + delete shell_allow_add)", fake.lastPatch)
	}
	if _, err := c.PatchWorkspaceConfig(ctx, &client.PatchWorkspaceConfigInput{
		CommandID:    "cmd-patch-stale",
		WorkspaceID:  testWorkspaceID,
		ExpectedHash: "hash-v1",
		Patch:        &client.WorkspaceSettingsPatch{IdleDetachSeconds: intPtr(60)},
	}); !errors.Is(err, client.ErrWorkspaceConfigChanged) {
		t.Fatalf("stale CAS err = %v, want ErrWorkspaceConfigChanged", err)
	}
	if _, err := c.PatchWorkspaceConfig(ctx, &client.PatchWorkspaceConfigInput{
		CommandID:    "cmd-patch-bad",
		WorkspaceID:  testWorkspaceID,
		ExpectedHash: updated.ConfigHash,
		Patches:      []client.ConfigPatch{{DottedKey: testCfgBadKey, Op: client.ConfigPatchSet, Value: 1}},
	}); !errors.Is(err, client.ErrWorkspaceConfigInvalid) {
		t.Fatalf("invalid key err = %v, want ErrWorkspaceConfigInvalid", err)
	}
}

func TestWorkspaceConfigErrorsAndEmptyResponses(t *testing.T) {
	t.Parallel()
	fake := newWorkspaceConfigFake()
	fake.values = nil
	fake.provenance = nil
	c := serveWorkspaceConfigFake(t, fake)
	ctx := t.Context()

	eff, err := c.GetEffectiveConfig(ctx, testWorkspaceID)
	if err != nil || len(eff.Provenance) != 0 {
		t.Fatalf("empty GetEffectiveConfig = (%+v, %v)", eff, err)
	}
	emptyWS, err := c.UpdateWorkspaceConfig(ctx, "empty", fake.rawTOML, fake.hash)
	if err != nil || emptyWS.ID != "" {
		t.Fatalf("empty UpdateWorkspaceConfig = (%+v, %v)", emptyWS, err)
	}
	if _, err := c.UpdateWorkspaceConfig(ctx, "missing-ws", nil, ""); err == nil || errors.Is(err, client.ErrWorkspaceConfigChanged) {
		t.Fatalf("missing err = %v", err)
	}

	fake.mu.Lock()
	fake.updateErr = scriptedConfigError(connect.CodeInvalidArgument, errScriptedConfigChanged, client.WorkspaceCodeConfigInvalid)
	fake.mu.Unlock()
	if _, err := c.GetEffectiveConfig(ctx, testWorkspaceID); err == nil {
		t.Fatal("GetEffectiveConfig error expected")
	}
	if _, err := c.ValidateConfig(ctx, testWorkspaceID, nil); err == nil {
		t.Fatal("ValidateConfig error expected")
	}
	_, verrRaw := c.UpdateWorkspaceConfig(ctx, testWorkspaceID, nil, "")
	var verr *client.WorkspaceConfigValidationError
	if !errors.As(verrRaw, &verr) || verr.Line != 0 || errors.Unwrap(verr) == nil {
		t.Fatalf("malformed validation error = %+v (%v)", verr, verrRaw)
	}
}

func intPtr(v int) *int { return &v }
