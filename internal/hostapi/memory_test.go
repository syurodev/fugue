package hostapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
)

const (
	testMemUnhelloedName = "memory unhelloed client"
	testMemSessionName   = "memory session principal"
	testMemDeniedText    = "denied entry"
	testMemArchLabel     = "api-architecture"
)

func TestMemoryHostAPIPrincipalRefusals(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")

	for _, tc := range []struct {
		name   string
		client func(t *testing.T) memoryv1connect.MemoryServiceClient
	}{
		{
			name: testMemUnhelloedName,
			client: func(t *testing.T) memoryv1connect.MemoryServiceClient {
				t.Helper()
				_, hc := newTestClient(t, v.sockPath)
				return memoryv1connect.NewMemoryServiceClient(hc, testBaseURL)
			},
		},
		{
			name: testMemSessionName,
			client: func(t *testing.T) memoryv1connect.MemoryServiceClient {
				t.Helper()
				wsID := v.workspaceWithRoot(t, t.TempDir())
				_ = v.readySession(t, "mem-deny-sess-1", wsID, "")
				tokens := sessionTokens(t, 1)
				_, sessHTTP, err := attachSession(t, v.sockPath, tokens[0])
				if err != nil {
					t.Fatal(err)
				}
				return memoryv1connect.NewMemoryServiceClient(sessHTTP, testBaseURL)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertAllMemoryRPCsDenied(t, tc.client(t))
		})
	}

	t.Run("guard principal rejected by MemoryService", func(t *testing.T) {
		t.Parallel()
		memSvc, err := v.hp.Runtime().MemoryService()
		if err != nil {
			t.Fatalf("MemoryService: %v", err)
		}
		if _, err := memSvc.ListEntries(t.Context(), "guard", app.MemoryListFilter{WorkspaceID: "w1"}); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("guard ListEntries = %v, want permission_denied", err)
		}
		if _, err := memSvc.WriteEntry(t.Context(), "guard", &app.MemoryWriteCommand{
			WorkspaceID: "w1", Op: app.MemoryOpAdd, Kind: app.MemoryKindNote,
			Label: "g", Summary: "guard", Body: "guard",
		}); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("guard WriteEntry = %v, want permission_denied", err)
		}
	})
}

func assertAllMemoryRPCsDenied(t *testing.T, c memoryv1connect.MemoryServiceClient) {
	t.Helper()
	ctx := t.Context()
	if _, err := c.ListEntries(ctx, connect.NewRequest(&memoryv1.ListEntriesRequest{WorkspaceId: "w1"})); err == nil {
		t.Fatal("ListEntries allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.SearchEntries(ctx, connect.NewRequest(&memoryv1.SearchEntriesRequest{WorkspaceId: "w1", Query: "x"})); err == nil {
		t.Fatal("SearchEntries allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.GetEntry(ctx, connect.NewRequest(&memoryv1.GetEntryRequest{WorkspaceId: "w1", Id: "e1"})); err == nil {
		t.Fatal("GetEntry allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.GetEntryHistory(ctx, connect.NewRequest(&memoryv1.GetEntryHistoryRequest{WorkspaceId: "w1", EntryId: "e1"})); err == nil {
		t.Fatal("GetEntryHistory allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: "w1", Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label: "denied-label", Summary: testMemDeniedText, Body: testMemDeniedText,
	})); err == nil {
		t.Fatal("WriteEntry allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.VerifyEntry(ctx, connect.NewRequest(&memoryv1.VerifyEntryRequest{WorkspaceId: "w1", Id: "e1", ExpectedRevision: 1})); err == nil {
		t.Fatal("VerifyEntry allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.RestoreEntry(ctx, connect.NewRequest(&memoryv1.RestoreEntryRequest{WorkspaceId: "w1", Id: "e1", RestoreRevision: 1, ExpectedRevision: 1})); err == nil {
		t.Fatal("RestoreEntry allowed unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
}

func TestMemoryHostAPIAllRPCsAndErrors(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	wsID := v.workspaceWithRoot(t, t.TempDir())
	host, hc := newTestClient(t, v.sockPath)
	if _, err := host.Hello(t.Context(), helloRequest(hostv1.ClientKind_CLIENT_KIND_CLI, "v1")); err != nil {
		t.Fatalf("Hello cli: %v", err)
	}
	c := memoryv1connect.NewMemoryServiceClient(hc, testBaseURL)

	entry := assertMemoryWriteUpdateSearchHistory(t, c, wsID)
	assertMemoryVerifyRestoreAndErrors(t, v, c, wsID, entry)
	assertSessionGetInstructions(t, v, c, wsID)
}

func assertMemoryWriteUpdateSearchHistory(
	t *testing.T,
	c memoryv1connect.MemoryServiceClient,
	wsID string,
) *memoryv1.MemoryEntry {
	t.Helper()
	ctx := t.Context()
	added, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label: testMemArchLabel, Summary: "Initial architecture note.", Body: "Use layers strictly from top to bottom.",
		Keywords: []string{"layers", "arch"},
	}))
	if err != nil {
		t.Fatalf("WriteEntry add: %v", err)
	}
	e := added.Msg.GetEntry()
	if !e.GetVerified() || e.GetAuthor() != memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER || e.GetRevision() != 1 {
		t.Fatalf("added entry = %+v, want verified user rev 1", e)
	}

	updated, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_UPDATE, Id: e.GetId(), ExpectedRevision: 1,
		Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT, Label: testMemArchLabel,
		Summary: "Updated architecture note.", Body: "Layer five imports app and proto only.",
		Keywords: []string{"layers", "hostapi"},
	}))
	if err != nil {
		t.Fatalf("WriteEntry update: %v", err)
	}
	e = updated.Msg.GetEntry()
	if e.GetRevision() != 2 {
		t.Fatalf("updated revision = %d, want 2", e.GetRevision())
	}

	assertMemoryReadsAndFilters(t, ctx, c, wsID, e)
	return e
}

func assertMemoryReadsAndFilters(
	t *testing.T,
	ctx context.Context,
	c memoryv1connect.MemoryServiceClient,
	wsID string,
	e *memoryv1.MemoryEntry,
) {
	t.Helper()
	byID, err := c.GetEntry(ctx, connect.NewRequest(&memoryv1.GetEntryRequest{WorkspaceId: wsID, Id: e.GetId()}))
	if err != nil || byID.Msg.GetEntry().GetId() != e.GetId() {
		t.Fatalf("GetEntry by id = (%+v, %v)", byID, err)
	}
	byLabel, err := c.GetEntry(ctx, connect.NewRequest(&memoryv1.GetEntryRequest{WorkspaceId: wsID, Label: testMemArchLabel}))
	if err != nil || byLabel.Msg.GetEntry().GetId() != e.GetId() {
		t.Fatalf("GetEntry by label = (%+v, %v)", byLabel, err)
	}

	verifiedTrue := true
	listed, err := c.ListEntries(ctx, connect.NewRequest(&memoryv1.ListEntriesRequest{
		WorkspaceId: wsID, Verified: &verifiedTrue, Author: memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER, Validity: memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID,
	}))
	if err != nil || len(listed.Msg.GetEntries()) != 1 {
		t.Fatalf("ListEntries = (%+v, %v)", listed, err)
	}

	searched, err := c.SearchEntries(ctx, connect.NewRequest(&memoryv1.SearchEntriesRequest{
		WorkspaceId: wsID, Query: "hostapi", Verified: &verifiedTrue,
	}))
	if err != nil || len(searched.Msg.GetResults()) != 1 || searched.Msg.GetResults()[0].GetId() != e.GetId() {
		t.Fatalf("SearchEntries = (%+v, %v)", searched, err)
	}

	hist, err := c.GetEntryHistory(ctx, connect.NewRequest(&memoryv1.GetEntryHistoryRequest{WorkspaceId: wsID, EntryId: e.GetId()}))
	if err != nil || len(hist.Msg.GetHistory()) != 2 {
		t.Fatalf("GetEntryHistory = (%+v, %v), want 2 revisions", hist, err)
	}
}

func assertMemoryVerifyRestoreAndErrors(
	t *testing.T,
	v *vendorHost,
	c memoryv1connect.MemoryServiceClient,
	wsID string,
	e *memoryv1.MemoryEntry,
) {
	t.Helper()
	ctx := t.Context()
	_ = v.readySession(t, "mem-agent-write", wsID, "")
	tokens := sessionTokens(t, 1)
	sessClient, _, err := attachSession(t, v.sockPath, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	if callRes, callErr := sessClient.CallTool(ctx, connect.NewRequest(&sessiontoolsv1.CallToolRequest{
		Name:      "fugue_memory_write",
		Arguments: []byte(`{"op":"add","kind":"note","label":"agent-draft","summary":"Draft from agent.","body":"Needs user verification."}`),
	})); callErr != nil || callRes.Msg.GetIsError() {
		t.Fatalf("agent CallTool fugue_memory_write = (%+v, %v)", callRes, callErr)
	}

	agentGot, err := c.GetEntry(ctx, connect.NewRequest(&memoryv1.GetEntryRequest{WorkspaceId: wsID, Label: "agent-draft"}))
	if err != nil || agentGot.Msg.GetEntry().GetVerified() {
		t.Fatalf("agent GetEntry = (%+v, %v), want unverified entry", agentGot, err)
	}
	agentID := agentGot.Msg.GetEntry().GetId()

	verified, err := c.VerifyEntry(ctx, connect.NewRequest(&memoryv1.VerifyEntryRequest{
		WorkspaceId: wsID, Id: agentID, ExpectedRevision: 1,
	}))
	if err != nil || !verified.Msg.GetEntry().GetVerified() || verified.Msg.GetEntry().GetRevision() != 2 {
		t.Fatalf("VerifyEntry = (%+v, %v)", verified, err)
	}

	restored, err := c.RestoreEntry(ctx, connect.NewRequest(&memoryv1.RestoreEntryRequest{
		WorkspaceId: wsID, Id: e.GetId(), RestoreRevision: 1, ExpectedRevision: e.GetRevision(),
	}))
	if err != nil || restored.Msg.GetEntry().GetSummary() != "Initial architecture note." || restored.Msg.GetEntry().GetRevision() != 3 {
		t.Fatalf("RestoreEntry = (%+v, %v)", restored, err)
	}

	assertMemoryConflictAndDuplicateErrors(t, c, wsID, agentID)
	assertMemoryContentValidationErrors(t, c, wsID)
}

func assertMemoryConflictAndDuplicateErrors(
	t *testing.T,
	c memoryv1connect.MemoryServiceClient,
	wsID, agentID string,
) {
	t.Helper()
	ctx := t.Context()
	if _, err := c.VerifyEntry(ctx, connect.NewRequest(&memoryv1.VerifyEntryRequest{
		WorkspaceId: wsID, Id: agentID, ExpectedRevision: 1,
	})); err == nil {
		t.Fatal("stale VerifyEntry succeeded")
	} else {
		assertConnectCode(t, err, connect.CodeFailedPrecondition, errcode.MemoryRevisionConflict)
	}

	if _, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label: testMemArchLabel, Summary: "Duplicate label", Body: "Should fail label_taken.",
	})); err == nil {
		t.Fatal("duplicate label succeeded")
	} else {
		assertConnectCode(t, err, connect.CodeAlreadyExists, errcode.MemoryLabelTaken)
	}
}

func assertMemoryContentValidationErrors(
	t *testing.T,
	c memoryv1connect.MemoryServiceClient,
	wsID string,
) {
	t.Helper()
	assertSecretRejectedRPC(t, c, wsID)
	ctx := t.Context()
	if _, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_PINNED,
		Label: "oversize-pin", Summary: "Too big", Body: strings.Repeat("x", app.MemoryMaxPinnedBodyBytes+10),
	})); err == nil {
		t.Fatal("too-large WriteEntry succeeded")
	} else {
		assertConnectCode(t, err, connect.CodeResourceExhausted, errcode.MemoryTooLarge)
	}

	if _, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label: "non-english", Summary: "Kiểm tra tiếng Việt bị từ chối.", Body: "Đây là một câu tiếng Việt dài đủ để vượt ngưỡng ngôn ngữ của hệ thống.",
	})); err == nil {
		t.Fatal("non-English WriteEntry succeeded")
	} else {
		assertConnectCode(t, err, connect.CodeInvalidArgument, errcode.MemoryLanguage)
	}

	if _, err := c.GetEntry(ctx, connect.NewRequest(&memoryv1.GetEntryRequest{WorkspaceId: wsID, Id: "missing-id"})); err == nil {
		t.Fatal("missing GetEntry succeeded")
	} else {
		assertConnectCode(t, err, connect.CodeNotFound, errcode.NotFound)
	}
}

func assertSecretRejectedRPC(t *testing.T, c memoryv1connect.MemoryServiceClient, wsID string) {
	t.Helper()
	fakeTok := "gh" + "p_" + strings.Repeat("a1", 18)
	_, err := c.WriteEntry(t.Context(), connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label: "secret-rpc", Summary: "Contains secret", Body: "Token is " + fakeTok + " inside body.",
	}))
	if err == nil {
		t.Fatal("secret WriteEntry succeeded")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("secret WriteEntry code = %v, want InvalidArgument (err=%v)", code, err)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) || len(cerr.Details()) == 0 {
		t.Fatal("secret WriteEntry missing connect error detail")
	}
	val, derr := cerr.Details()[0].Value()
	if derr != nil {
		t.Fatal(derr)
	}
	detail, ok := val.(*hostv1.ErrorDetail)
	if !ok || detail.GetCode() != string(errcode.MemoryContainsSecret) {
		t.Fatal("secret WriteEntry public code did not match memory.contains_secret")
	}
}

func assertSessionGetInstructions(
	t *testing.T,
	v *vendorHost,
	c memoryv1connect.MemoryServiceClient,
	wsID string,
) {
	t.Helper()
	ctx := t.Context()
	if _, err := c.WriteEntry(ctx, connect.NewRequest(&memoryv1.WriteEntryRequest{
		WorkspaceId: wsID, Op: memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD, Kind: memoryv1.MemoryKind_MEMORY_KIND_PINNED,
		Label: "pin-instr", Summary: "Session start summary", Body: "PINNED_INSTRUCTION_BODY",
	})); err != nil {
		t.Fatalf("WriteEntry pinned: %v", err)
	}
	_ = v.readySession(t, "mem-instr-sess", wsID, "")
	tokens := sessionTokens(t, 2)
	sessClient, _, err := attachSession(t, v.sockPath, tokens[1])
	if err != nil {
		t.Fatal(err)
	}
	res, err := sessClient.GetInstructions(ctx, connect.NewRequest(&sessiontoolsv1.GetInstructionsRequest{}))
	if err != nil {
		t.Fatalf("GetInstructions: %v", err)
	}
	if !strings.Contains(res.Msg.GetInstructions(), "PINNED_INSTRUCTION_BODY") || !strings.Contains(res.Msg.GetInstructions(), "verified=true") {
		t.Fatalf("GetInstructions = %q, want pinned body and metadata", res.Msg.GetInstructions())
	}
}
