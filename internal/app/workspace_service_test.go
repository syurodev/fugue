package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

const workspaceRuntimeTestRoot = "runtime-root"

func TestWorkspaceRuntimeAndTopic(t *testing.T) {
	t.Parallel()
	rt, stream, home := workspaceRuntimeFixture(t)
	created := workspaceCreateAndRead(t, rt, stream)
	workspaceValidateAndUpdate(t, rt, stream, created)
	workspaceConfirmAndReject(t, rt, stream, home, created.ID)
}

func TestWorkspaceRootRuntimeAndDeleteTopic(t *testing.T) {
	t.Parallel()
	rt, stream, _ := workspaceRuntimeFixture(t)
	created := createRootRuntimeWorkspace(t, rt, stream)
	updated := exerciseWorkspaceRootRuntime(t, rt, stream, created)
	exerciseWorkspaceDeleteRuntime(t, rt, stream, updated, created.Name)
}

func createRootRuntimeWorkspace(t *testing.T, rt *HostRuntime, stream *Subscription) *WorkspaceView {
	t.Helper()
	created, err := rt.CreateWorkspace(t.Context(), string(KindApp), "root-api")
	if err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 1, EventWorkspaceCreated)
	first := filepath.Join(t.TempDir(), "first")
	if err = os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.AddWorkspaceRoot(t.Context(), "session", created.ID, workspaceRuntimeTestRoot, first, "rw", created.ConfigHash); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("non-local AddRoot error = %v", err)
	}
	_, err = rt.AddWorkspaceRoot(t.Context(), string(KindApp), created.ID, workspaceRuntimeTestRoot, first, "rw", created.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 2, EventWorkspaceRootAdded)
	return created
}

func exerciseWorkspaceRootRuntime(t *testing.T, rt *HostRuntime, stream *Subscription, created *WorkspaceView) *WorkspaceView {
	t.Helper()
	second := filepath.Join(t.TempDir(), "second")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	added, _, err := rt.GetWorkspace(t.Context(), string(KindApp), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.UpdateWorkspaceRoot(t.Context(), string(KindApp), created.ID, workspaceRuntimeTestRoot, "", second, "ro", "stale"); errcode.CodeOf(err) != errcode.WorkspaceConfigChanged {
		t.Fatalf("stale UpdateRoot error = %v", err)
	}
	updated, err := rt.UpdateWorkspaceRoot(t.Context(), string(KindApp), created.ID, workspaceRuntimeTestRoot, "renamed", second, "ro", added.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 3, EventWorkspaceRootUpdated)
	assertUpdatedWorkspaceRuntimeRoot(t, updated, second)
	removed, err := rt.RemoveWorkspaceRoot(t.Context(), string(KindApp), created.ID, "renamed", updated.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 4, EventWorkspaceRootRemoved)
	if removed.ConfigRevision != 4 || removed.PolicyRevision != 4 || len(removed.Roots) != 0 {
		t.Fatalf("removed root workspace = %+v", removed)
	}
	return removed
}

func assertUpdatedWorkspaceRuntimeRoot(t *testing.T, ws *WorkspaceView, expectedPath string) {
	t.Helper()
	resolvedSecond, err := filepath.EvalSymlinks(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Roots[0].Name != "renamed" || ws.Roots[0].Path != resolvedSecond || ws.Roots[0].Access != "ro" {
		t.Fatalf("updated root = %+v", ws.Roots)
	}
}

func exerciseWorkspaceDeleteRuntime(t *testing.T, rt *HostRuntime, stream *Subscription, removed *WorkspaceView, name string) {
	t.Helper()
	if err := rt.DeleteWorkspace(t.Context(), string(KindApp), removed.ID, removed.ConfigHash, "wrong"); errcode.CodeOf(err) != errcode.WorkspaceConfigUnconfirmed {
		t.Fatalf("wrong Delete confirmation error = %v", err)
	}
	if err := rt.DeleteWorkspace(t.Context(), string(KindApp), removed.ID, removed.ConfigHash, name); err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 5, EventWorkspaceDeleted)
}

func workspaceRuntimeFixture(t *testing.T) (*HostRuntime, *Subscription, string) {
	t.Helper()
	dataDir := t.TempDir()
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Unix(100, 0))
	deps := testHostDeps(dataDir, clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	host, startErr := StartHost(t.Context(), deps)
	if startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if closeErr := host.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	rt := host.Runtime()
	stream, subscribeErr := rt.Subscribe(t.Context(), SubscribeParams{Principal: string(KindApp), Topic: TopicWorkspaces})
	if subscribeErr != nil {
		t.Fatal(subscribeErr)
	}
	t.Cleanup(stream.Close)
	state, seq, ok := stream.Snapshot()
	if !ok || seq != 0 || string(state) != "[]" {
		t.Fatalf("initial snapshot = %s at %d, ok %v", state, seq, ok)
	}
	return rt, stream, fugueHome
}

func workspaceCreateAndRead(t *testing.T, rt *HostRuntime, stream *Subscription) *WorkspaceView {
	t.Helper()
	if _, deniedErr := rt.CreateWorkspace(t.Context(), "session", "denied"); errcode.CodeOf(deniedErr) != errcode.PermissionDenied {
		t.Fatalf("session workspace create error = %v", deniedErr)
	}
	created, createErr := rt.CreateWorkspace(t.Context(), string(KindApp), "app-workspace")
	if createErr != nil {
		t.Fatal(createErr)
	}
	assertAppWorkspaceEvent(t, stream, 1, EventWorkspaceCreated)
	list, listErr := rt.ListWorkspaces(t.Context(), string(KindApp))
	if listErr != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("workspace list = %+v, err %v", list, listErr)
	}
	got, raw, getErr := rt.GetWorkspace(t.Context(), string(KindApp), created.ID)
	if getErr != nil || got.ConfigHash == "" || len(raw) == 0 {
		t.Fatalf("workspace get = %+v, raw %q, err %v", got, raw, getErr)
	}
	effective, _, effectiveErr := rt.GetEffectiveWorkspaceConfig(t.Context(), string(KindApp), created.ID)
	if effectiveErr != nil || len(effective.Values) == 0 || len(effective.Provenance) == 0 {
		t.Fatalf("effective config = %+v, err %v", effective, effectiveErr)
	}
	return created
}

func workspaceValidateAndUpdate(t *testing.T, rt *HostRuntime, stream *Subscription, created *WorkspaceView) {
	t.Helper()
	valid, validErr := rt.ValidateWorkspaceConfig(t.Context(), string(KindApp), []byte("name = \"ok\"\n"))
	if validErr != nil || valid.Key != "" {
		t.Fatalf("valid config result = %+v, err %v", valid, validErr)
	}
	issue, invalidErr := rt.ValidateWorkspaceConfig(t.Context(), string(KindApp), []byte("unknown_key = \"secret-value\"\n"))
	if invalidErr != nil || issue.Key != "unknown_key" || issue.Line == 0 || strings.Contains(issue.Message, "secret-value") {
		t.Fatalf("invalid config issue = %+v, err %v", issue, invalidErr)
	}
	oversized := make([]byte, WorkspaceConfigMaxBytes+1)
	if _, sizeErr := rt.ValidateWorkspaceConfig(t.Context(), string(KindApp), oversized); errcode.CodeOf(sizeErr) != errcode.WorkspaceConfigTooLarge {
		t.Fatalf("oversized validation error = %v", sizeErr)
	}
	if _, staleErr := rt.UpdateWorkspaceConfig(t.Context(), string(KindApp), created.ID, []byte("name = \"stale\"\n"), "stale"); errcode.CodeOf(staleErr) != errcode.WorkspaceConfigChanged {
		t.Fatalf("stale config update error = %v", staleErr)
	}
	updatedRaw := []byte("name = \"updated\"\n")
	updated, updateErr := rt.UpdateWorkspaceConfig(t.Context(), string(KindApp), created.ID, updatedRaw, created.ConfigHash)
	if updateErr != nil || updated.Name != "updated" {
		t.Fatalf("workspace update = %+v, err %v", updated, updateErr)
	}
	assertAppWorkspaceEvent(t, stream, 2, EventWorkspaceConfigChanged)
	if _, sizeErr := rt.UpdateWorkspaceConfig(t.Context(), string(KindApp), created.ID, oversized, updated.ConfigHash); errcode.CodeOf(sizeErr) != errcode.WorkspaceConfigTooLarge {
		t.Fatalf("oversized update error = %v", sizeErr)
	}
}

func workspaceConfirmAndReject(t *testing.T, rt *HostRuntime, stream *Subscription, home, id string) {
	t.Helper()
	configPath := filepath.Join(home, "workspaces", id, "fugue.toml")
	acceptedHash := confirmWorkspacePolicy(t, rt, stream, configPath, id)
	rejectWorkspacePolicy(t, rt, stream, configPath, id, acceptedHash)
}

func confirmWorkspacePolicy(t *testing.T, rt *HostRuntime, stream *Subscription, path, id string) string {
	t.Helper()
	if writeErr := os.WriteFile(path, []byte("name = \"updated\"\n\n[policy]\napproval_mode = \"full\"\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	pending, _, getErr := rt.GetWorkspace(t.Context(), string(KindApp), id)
	if getErr != nil || !pending.PendingPolicyConfirmation || len(pending.PolicyDiff) == 0 {
		t.Fatalf("pending workspace = %+v, err %v", pending, getErr)
	}
	assertAppWorkspaceEvent(t, stream, 3, EventWorkspacePendingConfirm)
	if _, staleErr := rt.ConfirmWorkspaceConfig(t.Context(), string(KindApp), id, "wrong"); errcode.CodeOf(staleErr) != errcode.WorkspaceConfigChanged {
		t.Fatalf("stale confirmation error = %v", staleErr)
	}
	confirmed, confirmErr := rt.ConfirmWorkspaceConfig(t.Context(), string(KindApp), id, pending.ConfigHash)
	if confirmErr != nil || confirmed.PendingPolicyConfirmation || confirmed.PolicyRevision != 2 {
		t.Fatalf("workspace confirmation = %+v, err %v", confirmed, confirmErr)
	}
	assertAppWorkspaceEvent(t, stream, 4, EventWorkspaceConfirmed)
	return confirmed.ConfigHash
}

func rejectWorkspacePolicy(t *testing.T, rt *HostRuntime, stream *Subscription, path, id, acceptedHash string) {
	t.Helper()
	if writeErr := os.WriteFile(path, []byte("name = \"updated\"\n\n[policy]\napproval_mode = \"plan\"\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	pending, _, getErr := rt.GetWorkspace(t.Context(), string(KindApp), id)
	if getErr != nil || !pending.PendingPolicyConfirmation {
		t.Fatalf("second pending workspace = %+v, err %v", pending, getErr)
	}
	assertAppWorkspaceEvent(t, stream, 5, EventWorkspacePendingConfirm)
	rejected, rejectErr := rt.RejectWorkspaceConfig(t.Context(), string(KindApp), id, pending.ConfigHash)
	if rejectErr != nil || rejected.PendingPolicyConfirmation || rejected.ConfigHash != acceptedHash {
		t.Fatalf("workspace rejection = %+v, err %v", rejected, rejectErr)
	}
	assertAppWorkspaceEvent(t, stream, 6, EventWorkspaceRejected)
	data, marshalErr := json.Marshal(WorkspaceSummary{ID: rejected.ID, Name: rejected.Name, MissingRoots: rejected.MissingRoots, ConfigRevision: rejected.ConfigRevision, PendingPolicyConfirmation: rejected.PendingPolicyConfirmation})
	if marshalErr != nil || strings.Contains(string(data), "raw_toml") {
		t.Fatalf("workspace summary = %s, err %v", data, marshalErr)
	}
}

func assertAppWorkspaceEvent(t *testing.T, sub *Subscription, seq uint64, eventType string) {
	t.Helper()
	event, ok := sub.Next(t.Context())
	if !ok || event.Seq != seq || event.Type != eventType {
		t.Fatalf("workspace event = %+v, ok %v; want %d %s", event, ok, seq, eventType)
	}
}

func TestWorkspaceServiceDependenciesAndErrorMapping(t *testing.T) {
	t.Parallel()
	hub := NewEventHub("workspace-errors")
	if _, err := NewWorkspaceService(nil, hub); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("nil domain error = %v", err)
	}
	var svc *workspace.Service
	if _, err := NewWorkspaceService(svc, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("nil hub error = %v", err)
	}
	for _, item := range []struct {
		code workspace.Code
		want errcode.Code
	}{
		{workspace.ErrNotFound, errcode.WorkspaceNotFound},
		{workspace.ErrAlreadyExists, errcode.WorkspaceAlreadyExists},
		{workspace.ErrConfigInvalid, errcode.WorkspaceConfigInvalid},
		{workspace.ErrConfigChanged, errcode.WorkspaceConfigChanged},
		{workspace.ErrConfigUnconfirmed, errcode.WorkspaceConfigUnconfirmed},
		{workspace.ErrRootInvalid, errcode.WorkspaceRootInvalid},
		{workspace.ErrRootMissing, errcode.WorkspaceRootMissing},
		{workspace.ErrPathDenied, errcode.WorkspacePathDenied},
		{workspace.ErrOwnershipInvalid, errcode.WorkspaceOwnershipInvalid},
	} {
		if got := errcode.CodeOf(publicWorkspaceError(workspace.NewError(item.code, "safe"))); got != item.want {
			t.Errorf("workspace error %s mapped to %s, want %s", item.code, got, item.want)
		}
	}
	if got := errcode.CodeOf(publicWorkspaceError(context.Canceled)); got != errcode.Internal {
		t.Fatalf("untyped workspace error mapped to %s", got)
	}
	invalid := workspace.NewError(workspace.ErrConfigInvalid, "secret-value")
	invalid.Key, invalid.Line, invalid.Column = "policy.mode", 3, 4
	message := publicWorkspaceError(invalid).Error()
	if strings.Contains(message, "secret-value") || !strings.Contains(message, "policy.mode") || !strings.Contains(message, "line 3, column 4") {
		t.Fatalf("safe config error message = %q", message)
	}
}
