package hostapi

import (
	"bytes"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

func TestMemoryExportImportPrincipalKinds(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	v := startTokenHost(t, "")
	wsID := v.workspaceWithRoot(t, t.TempDir())

	assertNonAppExportImportPrincipals(t, v, wsID)

	appHost, appHC := newTestClient(t, v.sockPath)
	if _, hErr := appHost.Hello(ctx, helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); hErr != nil {
		t.Fatalf("Hello(app): %v", hErr)
	}
	appClient := memoryv1connect.NewMemoryServiceClient(
		appHC, testBaseURL,
		connect.WithReadMaxBytes(app.MemoryExportImportWireMaxBytes),
		connect.WithSendMaxBytes(app.MemoryExportImportWireMaxBytes),
	)
	if _, wErr := appClient.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD,
		Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT, Label: "hostapi-roundtrip",
		Summary: "Host API roundtrip summary.", Body: "This entry tests the Host API export and import flow in valid English.",
	})); wErr != nil {
		t.Fatalf("WriteEntry: %v", wErr)
	}
	expRes, err := appClient.ExportMemory(ctx, connect.NewRequest(&memoryv1.ExportMemoryRequest{WorkspaceId: wsID}))
	if err != nil || len(expRes.Msg.GetContent()) == 0 {
		t.Fatalf("ExportMemory(app) = (%v, %v)", len(expRes.Msg.GetContent()), err)
	}

	t.Run("cross-host import and 32 MiB cap", func(t *testing.T) {
		t.Parallel()
		assertCrossHostImportAndSizeCap(t, expRes.Msg.GetContent())
	})
}

func assertNonAppExportImportPrincipals(t *testing.T, v *vendorHost, wsID string) {
	t.Helper()
	ctx := t.Context()
	_, unhelloedHC := newTestClient(t, v.sockPath)
	assertExportImportDenied(t, memoryv1connect.NewMemoryServiceClient(unhelloedHC, testBaseURL), wsID, true)

	memSvc, err := v.hp.Runtime().MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}
	if _, eErr := memSvc.ExportMemory(ctx, "guard", wsID); errcode.CodeOf(eErr) != errcode.PermissionDenied {
		t.Fatalf("guard ExportMemory = %v, want permission_denied", eErr)
	}
	if _, pErr := memSvc.PreviewImport(ctx, "guard", wsID, []byte("x")); errcode.CodeOf(pErr) != errcode.PermissionDenied {
		t.Fatalf("guard PreviewImport = %v, want permission_denied", pErr)
	}
	if _, aErr := memSvc.ApplyImport(ctx, "guard", &app.MemoryApplyImportCommand{WorkspaceID: wsID, PreviewID: "p"}); errcode.CodeOf(aErr) != errcode.PermissionDenied {
		t.Fatalf("guard ApplyImport = %v, want permission_denied", aErr)
	}

	_ = v.readySession(t, "mem-imp-sess", wsID, "")
	tokens := sessionTokens(t, 1)
	_, sessHTTP, err := attachSession(t, v.sockPath, tokens[0])
	if err != nil {
		t.Fatalf("attachSession: %v", err)
	}
	assertExportImportDenied(t, memoryv1connect.NewMemoryServiceClient(sessHTTP, testBaseURL), wsID, true)

	for _, kind := range []hostv1.ClientKind{hostv1.ClientKind_CLIENT_KIND_CLI, hostv1.ClientKind_CLIENT_KIND_TUI} {
		host, hc := newTestClient(t, v.sockPath)
		if _, hErr := host.Hello(ctx, helloRequest(kind, "v1")); hErr != nil {
			t.Fatalf("Hello(%v): %v", kind, hErr)
		}
		c := memoryv1connect.NewMemoryServiceClient(hc, testBaseURL)
		if _, eErr := c.ExportMemory(ctx, connect.NewRequest(&memoryv1.ExportMemoryRequest{WorkspaceId: wsID})); eErr != nil {
			t.Fatalf("ExportMemory(%v) = %v, want allowed", kind, eErr)
		}
		assertExportImportDenied(t, c, wsID, false)
	}
}

func assertCrossHostImportAndSizeCap(t *testing.T, exportedContent []byte) {
	t.Helper()
	subCtx := t.Context()
	v2 := startTokenHost(t, "")
	appHost2, appHC2 := newTestClient(t, v2.sockPath)
	if _, hErr := appHost2.Hello(subCtx, helloRequest(hostv1.ClientKind_CLIENT_KIND_APP, "v1")); hErr != nil {
		t.Fatalf("Hello(app2): %v", hErr)
	}
	appClient2 := memoryv1connect.NewMemoryServiceClient(
		appHC2, testBaseURL,
		connect.WithReadMaxBytes(app.MemoryExportImportWireMaxBytes),
		connect.WithSendMaxBytes(app.MemoryExportImportWireMaxBytes),
	)
	ws2 := v2.workspaceWithRoot(t, t.TempDir())
	prevRes, prevErr := appClient2.PreviewImport(subCtx, connect.NewRequest(&memoryv1.PreviewImportRequest{
		WorkspaceId: ws2, Content: exportedContent,
	}))
	if prevErr != nil || prevRes.Msg.GetPreviewId() == "" || len(prevRes.Msg.GetItems()) != 1 {
		t.Fatalf("PreviewImport(app) = (%+v, %v)", prevRes, prevErr)
	}
	applyRes, applyErr := appClient2.ApplyImport(subCtx, connect.NewRequest(&memoryv1.ApplyImportRequest{
		WorkspaceId: ws2, PreviewId: prevRes.Msg.GetPreviewId(), ImportFile: "hostapi.md",
	}))
	if applyErr != nil || applyRes.Msg.GetAdded() != 1 || len(applyRes.Msg.GetWritten()) != 1 {
		t.Fatalf("ApplyImport(app) = (%+v, %v)", applyRes, applyErr)
	}

	oversize := bytes.Repeat([]byte("a"), app.MemoryExportImportMaxBytes+1)
	if _, ovErr := appClient2.PreviewImport(subCtx, connect.NewRequest(&memoryv1.PreviewImportRequest{
		WorkspaceId: ws2, Content: oversize,
	})); ovErr == nil {
		t.Fatal("oversize PreviewImport succeeded")
	} else {
		assertConnectCode(t, ovErr, connect.CodeInvalidArgument, errcode.MemoryImportInvalid)
	}
}

func assertExportImportDenied(t *testing.T, c memoryv1connect.MemoryServiceClient, wsID string, includeExport bool) {
	t.Helper()
	ctx := t.Context()
	if includeExport {
		if _, err := c.ExportMemory(ctx, connect.NewRequest(&memoryv1.ExportMemoryRequest{WorkspaceId: wsID})); err == nil {
			t.Fatal("ExportMemory allowed unauthorized principal")
		} else {
			assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
		}
	}
	if _, err := c.PreviewImport(ctx, connect.NewRequest(&memoryv1.PreviewImportRequest{WorkspaceId: wsID, Content: []byte("x")})); err == nil {
		t.Fatal("PreviewImport allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.ApplyImport(ctx, connect.NewRequest(&memoryv1.ApplyImportRequest{WorkspaceId: wsID, PreviewId: "p"})); err == nil {
		t.Fatal("ApplyImport allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
}
