package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestModelSnapshotAndEvent(t *testing.T) {
	t.Parallel()
	m := NewModel()
	snap, err := json.Marshal(client.MemoryTopicSnapshot{
		Unverified: map[string]int{"ws-1": 3, "ws-2": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplySnapshot(snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if !m.HasSnapshot() || m.UnverifiedCount("ws-1") != 3 || m.UnverifiedCount("ws-2") != 0 {
		t.Fatalf("unexpected counts after snapshot: ws-1=%d ws-2=%d", m.UnverifiedCount("ws-1"), m.UnverifiedCount("ws-2"))
	}

	evBytes, err := json.Marshal(client.MemoryWrittenEvent{
		EntryID:         "mem-1",
		WorkspaceID:     "ws-1",
		Revision:        2,
		Author:          client.MemoryAuthorUser,
		Op:              client.MemoryOpVerify,
		UnverifiedCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyEvent(client.EventMemoryEntryWritten, evBytes); err != nil {
		t.Fatalf("ApplyEvent: %v", err)
	}
	if m.UnverifiedCount("ws-1") != 2 {
		t.Fatalf("UnverifiedCount(ws-1) = %d, want 2", m.UnverifiedCount("ws-1"))
	}
}

type fakeMemoryClient struct {
	exportData  []byte
	previewResp client.MemoryImportPreview
	previewGot  []byte
}

func (f *fakeMemoryClient) ListMemoryEntries(context.Context, client.ListMemoryInput) ([]client.MemoryEntry, error) {
	return nil, nil
}

func (f *fakeMemoryClient) SearchMemoryEntries(context.Context, client.SearchMemoryInput) (client.MemorySearchResponse, error) {
	return client.MemorySearchResponse{}, nil
}

func (f *fakeMemoryClient) GetMemoryEntry(context.Context, string, string, string) (client.MemoryEntry, error) {
	return client.MemoryEntry{}, nil
}

func (f *fakeMemoryClient) GetMemoryEntryHistory(context.Context, string, string) ([]client.MemoryHistoryEntry, error) {
	return nil, nil
}

func (f *fakeMemoryClient) WriteMemoryEntry(context.Context, *client.WriteMemoryInput) (client.MemoryEntry, error) {
	return client.MemoryEntry{}, nil
}

func (f *fakeMemoryClient) VerifyMemoryEntry(context.Context, string, string, uint64) (client.MemoryEntry, error) {
	return client.MemoryEntry{}, nil
}

func (f *fakeMemoryClient) RestoreMemoryEntry(context.Context, string, string, uint64, uint64) (client.MemoryEntry, error) {
	return client.MemoryEntry{}, nil
}

func (f *fakeMemoryClient) ExportMemory(context.Context, string) ([]byte, error) {
	return f.exportData, nil
}

func (f *fakeMemoryClient) PreviewMemoryImport(_ context.Context, _ string, content []byte) (client.MemoryImportPreview, error) {
	f.previewGot = append([]byte(nil), content...)
	return f.previewResp, nil
}

func (f *fakeMemoryClient) ApplyMemoryImport(context.Context, *client.ApplyMemoryImportInput) (client.MemoryImportResult, error) {
	return client.MemoryImportResult{}, nil
}

func TestWorkerExportAndPreviewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fc := &fakeMemoryClient{
		exportData:  []byte("export-md"),
		previewResp: client.MemoryImportPreview{PreviewID: "p-1"},
	}
	w := NewWorker(func() Client { return fc }, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	outPath := filepath.Join(dir, "ws-memory.md")
	w.Do(&Request{Kind: OpExportToFile, WorkspaceID: "ws-1", FilePath: outPath})
	res := <-w.Results()
	if res.Err != nil {
		t.Fatalf("OpExportToFile: %v", res.Err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil || string(got) != "export-md" {
		t.Fatalf("exported file = (%q, %v)", string(got), err)
	}

	w.Do(&Request{Kind: OpPreviewImportFile, WorkspaceID: "ws-1", FilePath: outPath})
	res = <-w.Results()
	if res.Err != nil || res.Preview.PreviewID != "p-1" || string(fc.previewGot) != "export-md" {
		t.Fatalf("OpPreviewImportFile = %+v", res)
	}

	// Disconnected client fails closed.
	wNil := NewWorker(func() Client { return nil }, nil)
	wNil.Start(ctx)
	defer wNil.Stop()
	wNil.Do(&Request{Kind: OpListEntries, WorkspaceID: "ws-1"})
	res = <-wNil.Results()
	if !errors.Is(res.Err, client.ErrDisconnected) {
		t.Fatalf("expected ErrDisconnected, got %v", res.Err)
	}
}
