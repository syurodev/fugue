package client_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

type fakeMemoryService struct {
	hostv1connect.UnimplementedHostServiceHandler
	listReq    *memoryv1.ListEntriesRequest
	searchReq  *memoryv1.SearchEntriesRequest
	getReq     *memoryv1.GetEntryRequest
	historyReq *memoryv1.GetEntryHistoryRequest
	writeReq   *memoryv1.WriteEntryRequest
	verifyReq  *memoryv1.VerifyEntryRequest
	restoreReq *memoryv1.RestoreEntryRequest
	previewReq *memoryv1.PreviewImportRequest
	applyReq   *memoryv1.ApplyImportRequest
	writeErr   error
}

func (f *fakeMemoryService) Hello(_ context.Context, _ *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func serveMemoryFake(t *testing.T, svc *fakeMemoryService) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(svc))
	mux.Handle(memoryv1connect.NewMemoryServiceHandler(svc))
	socket := filepath.Join(shortDir(t), "memory.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial scripted memory host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sampleMemoryProtoEntry(rev uint64, verified bool) *memoryv1.MemoryEntry {
	return &memoryv1.MemoryEntry{
		Id:          "mem-1",
		WorkspaceId: "ws-1",
		Kind:        memoryv1.MemoryKind_MEMORY_KIND_FACT,
		Label:       "auth-flow",
		Summary:     "Auth summary",
		Body:        "Auth body",
		Keywords:    []string{"auth"},
		Anchors:     []*memoryv1.MemoryAnchor{{RootName: "main", RelPath: "auth.go", ContentHash: "abc"}},
		AnchorStatuses: []*memoryv1.MemoryAnchorStatus{{
			RootName:    "main",
			RelPath:     "auth.go",
			ContentHash: "abc",
			Freshness:   memoryv1.MemoryFreshness_MEMORY_FRESHNESS_FRESH,
		}},
		Author:    memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER,
		Verified:  verified,
		Validity:  memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID,
		Revision:  rev,
		UpdatedAt: timestamppb.New(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)),
		Freshness: memoryv1.MemoryFreshness_MEMORY_FRESHNESS_FRESH,
	}
}

func (f *fakeMemoryService) ListEntries(_ context.Context, req *connect.Request[memoryv1.ListEntriesRequest]) (*connect.Response[memoryv1.ListEntriesResponse], error) {
	f.listReq = req.Msg
	return connect.NewResponse(&memoryv1.ListEntriesResponse{Entries: []*memoryv1.MemoryEntry{sampleMemoryProtoEntry(1, false)}}), nil
}

func (f *fakeMemoryService) SearchEntries(_ context.Context, req *connect.Request[memoryv1.SearchEntriesRequest]) (*connect.Response[memoryv1.SearchEntriesResponse], error) {
	f.searchReq = req.Msg
	return connect.NewResponse(&memoryv1.SearchEntriesResponse{
		Results: []*memoryv1.MemorySearchResult{{
			Id:        "mem-1",
			Label:     "auth-flow",
			Kind:      memoryv1.MemoryKind_MEMORY_KIND_FACT,
			Summary:   "Auth summary",
			Snippet:   "Auth...",
			Author:    memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER,
			Verified:  true,
			Freshness: memoryv1.MemoryFreshness_MEMORY_FRESHNESS_FRESH,
			Revision:  2,
		}},
		Approximate: true,
	}), nil
}

func (f *fakeMemoryService) GetEntry(_ context.Context, req *connect.Request[memoryv1.GetEntryRequest]) (*connect.Response[memoryv1.GetEntryResponse], error) {
	f.getReq = req.Msg
	return connect.NewResponse(&memoryv1.GetEntryResponse{Entry: sampleMemoryProtoEntry(2, true)}), nil
}

func (f *fakeMemoryService) GetEntryHistory(_ context.Context, req *connect.Request[memoryv1.GetEntryHistoryRequest]) (*connect.Response[memoryv1.GetEntryHistoryResponse], error) {
	f.historyReq = req.Msg
	return connect.NewResponse(&memoryv1.GetEntryHistoryResponse{
		History: []*memoryv1.MemoryHistoryEntry{{
			HistoryId:   10,
			EntryId:     "mem-1",
			WorkspaceId: "ws-1",
			Revision:    1,
			Op:          memoryv1.MemoryOperation_MEMORY_OPERATION_ADD,
			Kind:        memoryv1.MemoryKind_MEMORY_KIND_FACT,
			Label:       "auth-flow",
			Author:      memoryv1.MemoryAuthor_MEMORY_AUTHOR_AGENT,
			Validity:    memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID,
			WrittenAt:   timestamppb.New(time.Date(2026, 11, 28, 11, 0, 0, 0, time.UTC)),
		}},
	}), nil
}

func (f *fakeMemoryService) WriteEntry(_ context.Context, req *connect.Request[memoryv1.WriteEntryRequest]) (*connect.Response[memoryv1.WriteEntryResponse], error) {
	f.writeReq = req.Msg
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	return connect.NewResponse(&memoryv1.WriteEntryResponse{Entry: sampleMemoryProtoEntry(2, true)}), nil
}

func (f *fakeMemoryService) VerifyEntry(_ context.Context, req *connect.Request[memoryv1.VerifyEntryRequest]) (*connect.Response[memoryv1.VerifyEntryResponse], error) {
	f.verifyReq = req.Msg
	return connect.NewResponse(&memoryv1.VerifyEntryResponse{Entry: sampleMemoryProtoEntry(3, true)}), nil
}

func (f *fakeMemoryService) RestoreEntry(_ context.Context, req *connect.Request[memoryv1.RestoreEntryRequest]) (*connect.Response[memoryv1.RestoreEntryResponse], error) {
	f.restoreReq = req.Msg
	return connect.NewResponse(&memoryv1.RestoreEntryResponse{Entry: sampleMemoryProtoEntry(4, true)}), nil
}

func (f *fakeMemoryService) ExportMemory(_ context.Context, _ *connect.Request[memoryv1.ExportMemoryRequest]) (*connect.Response[memoryv1.ExportMemoryResponse], error) {
	return connect.NewResponse(&memoryv1.ExportMemoryResponse{Content: []byte("exported")}), nil
}

func (f *fakeMemoryService) PreviewImport(_ context.Context, req *connect.Request[memoryv1.PreviewImportRequest]) (*connect.Response[memoryv1.PreviewImportResponse], error) {
	f.previewReq = req.Msg
	return connect.NewResponse(&memoryv1.PreviewImportResponse{
		PreviewId:     "prev-1",
		WorkspaceName: "alpha",
		Items: []*memoryv1.MemoryImportPreviewItem{{
			ItemId:        "item-1",
			EntryId:       "mem-1",
			Label:         "auth-flow",
			ItemClass:     memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_CONFLICT,
			DefaultChoice: memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_TAKE_IMPORTED,
			Local:         sampleMemoryProtoEntry(1, false),
			Imported:      sampleMemoryProtoEntry(2, true),
		}},
	}), nil
}

func (f *fakeMemoryService) ApplyImport(_ context.Context, req *connect.Request[memoryv1.ApplyImportRequest]) (*connect.Response[memoryv1.ApplyImportResponse], error) {
	f.applyReq = req.Msg
	return connect.NewResponse(&memoryv1.ApplyImportResponse{
		Written: []*memoryv1.MemoryEntry{sampleMemoryProtoEntry(5, true)},
		Added:   1,
		Updated: 2,
		Skipped: 3,
	}), nil
}

func TestMemoryClientRPCsAndCodeOf(t *testing.T) {
	t.Parallel()
	svc := &fakeMemoryService{}
	c := serveMemoryFake(t, svc)
	ctx := t.Context()

	entries, err := c.ListMemoryEntries(ctx, client.ListMemoryInput{WorkspaceID: "ws-1", Kind: client.MemoryKindFact})
	if err != nil || len(entries) != 1 || entries[0].Label != "auth-flow" {
		t.Fatalf("ListMemoryEntries = (%+v, %v)", entries, err)
	}
	search, err := c.SearchMemoryEntries(ctx, client.SearchMemoryInput{WorkspaceID: "ws-1", Query: "auth"})
	if err != nil || !search.Approximate || len(search.Results) != 1 {
		t.Fatalf("SearchMemoryEntries = (%+v, %v)", search, err)
	}
	entry, err := c.GetMemoryEntry(ctx, "ws-1", "mem-1", "")
	if err != nil || !entry.Verified || len(entry.AnchorStatuses) != 1 {
		t.Fatalf("GetMemoryEntry = (%+v, %v)", entry, err)
	}
	hist, err := c.GetMemoryEntryHistory(ctx, "ws-1", "mem-1")
	if err != nil || len(hist) != 1 || hist[0].Op != client.MemoryOpAdd {
		t.Fatalf("GetMemoryEntryHistory = (%+v, %v)", hist, err)
	}
	if _, wErr := c.WriteMemoryEntry(ctx, &client.WriteMemoryInput{WorkspaceID: "ws-1", Op: client.MemoryOpUpdate, ID: "mem-1", ExpectedRevision: 1, Kind: client.MemoryKindFact, Label: "auth-flow"}); wErr != nil {
		t.Fatalf("WriteMemoryEntry: %v", wErr)
	}
	if _, vErr := c.VerifyMemoryEntry(ctx, "ws-1", "mem-1", 2); vErr != nil {
		t.Fatalf("VerifyMemoryEntry: %v", vErr)
	}
	if _, rErr := c.RestoreMemoryEntry(ctx, "ws-1", "mem-1", 1, 3); rErr != nil {
		t.Fatalf("RestoreMemoryEntry: %v", rErr)
	}
	prev, err := c.PreviewMemoryImport(ctx, "ws-1", []byte("---\n"))
	if err != nil || prev.PreviewID != "prev-1" || len(prev.Items) != 1 || prev.Items[0].DefaultChoice != client.MemoryImportChoiceTakeImported {
		t.Fatalf("PreviewMemoryImport = (%+v, %v)", prev, err)
	}
	res, err := c.ApplyMemoryImport(ctx, &client.ApplyMemoryImportInput{
		WorkspaceID: "ws-1",
		PreviewID:   "prev-1",
		Choices:     []client.MemoryImportChoiceInput{{ItemID: "item-1", Choice: client.MemoryImportChoiceKeepBoth}},
		ImportFile:  "export.md",
	})
	if err != nil || res.Added != 1 || res.Updated != 2 || res.Skipped != 3 {
		t.Fatalf("ApplyMemoryImport = (%+v, %v)", res, err)
	}

	cerr := connect.NewError(connect.CodeFailedPrecondition, errors.New("conflict"))
	det, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.MemoryCodeRevisionConflict})
	if derr != nil {
		t.Fatal(derr)
	}
	cerr.AddDetail(det)
	svc.writeErr = cerr
	_, err = c.WriteMemoryEntry(ctx, &client.WriteMemoryInput{WorkspaceID: "ws-1", Op: client.MemoryOpUpdate, ID: "mem-1", Kind: client.MemoryKindFact})
	if code, ok := client.MemoryCodeOf(err); !ok || code != client.MemoryCodeRevisionConflict {
		t.Fatalf("MemoryCodeOf = (%q, %v), want %q", code, ok, client.MemoryCodeRevisionConflict)
	}
}

func TestMemoryFileHelpers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "ws-memory.md")
	want := []byte("hello memory export")
	if err := client.WriteMemoryExportFile(outPath, want); err != nil {
		t.Fatalf("WriteMemoryExportFile: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", perm)
	}
	got, err := client.ReadMemoryImportFile(outPath, 1024)
	if err != nil {
		t.Fatalf("ReadMemoryImportFile: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ReadMemoryImportFile diff:\n%s", diff)
	}

	bigPath := filepath.Join(dir, "too-big.md")
	if err := os.WriteFile(bigPath, bytes.Repeat([]byte("a"), 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadMemoryImportFile(bigPath, 1024); !errors.Is(err, client.ErrMemoryImportFileTooLarge) {
		t.Fatalf("ReadMemoryImportFile(oversize) err = %v, want ErrMemoryImportFileTooLarge", err)
	}
}
