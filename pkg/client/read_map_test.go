package client_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1/transcriptv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
)

// The strings the mapping tests assert on; named once for the goconst gate.
const (
	testStateRunning = "running"
	testTurn         = "turn-1"
	testOrigin       = "vendor"
)

// readHandler scripts the reads the client maps: the hello every dial needs,
// a scripted session list, and scripted transcript page and item answers.
type readHandler struct {
	hostv1connect.UnimplementedHostServiceHandler
	vendorsv1connect.UnimplementedVendorServiceHandler
	transcriptv1connect.UnimplementedTranscriptServiceHandler

	list     *vendorsv1.ListSessionsResponse
	page     *transcriptv1.TranscriptPageResponse
	detail   *transcriptv1.TranscriptDetail
	itemKind transcriptv1.TranscriptKind
}

func (h *readHandler) Hello(ctx context.Context, req *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{ApiVersion: "v1", HostVersion: "test-host"}), nil
}

func (h *readHandler) ListSessions(ctx context.Context, req *connect.Request[vendorsv1.ListSessionsRequest]) (*connect.Response[vendorsv1.ListSessionsResponse], error) {
	if h.list != nil {
		return connect.NewResponse(h.list), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, errNoSubscriptionScripted)
}

func (h *readHandler) TranscriptPage(ctx context.Context, req *connect.Request[transcriptv1.TranscriptPageRequest]) (*connect.Response[transcriptv1.TranscriptPageResponse], error) {
	if h.page != nil {
		return connect.NewResponse(h.page), nil
	}
	return connect.NewResponse(&transcriptv1.TranscriptPageResponse{Entries: []*transcriptv1.TranscriptEntry{{
		ItemId: "i1", Kind: h.itemKind, View: &transcriptv1.TranscriptView{Text: "content"},
	}}}), nil
}

func (h *readHandler) TranscriptItem(ctx context.Context, req *connect.Request[transcriptv1.TranscriptItemRequest]) (*connect.Response[transcriptv1.TranscriptItemResponse], error) {
	if h.detail != nil {
		return connect.NewResponse(&transcriptv1.TranscriptItemResponse{Item: h.detail}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, errNoSubscriptionScripted)
}

// serveReadHost runs the handler on a fresh unix socket over unencrypted
// HTTP/2 with the read services mounted, as the real host serves them.
func serveReadHost(t *testing.T, h *readHandler) string {
	t.Helper()
	dir := shortDir(t)
	socket := filepath.Join(dir, "fugue.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(ln) })
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(h))
	mux.Handle(vendorsv1connect.NewVendorServiceHandler(h))
	mux.Handle(transcriptv1connect.NewTranscriptServiceHandler(h))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)
	return socket
}

// TestListSessionsCarriesTheSessionFields pins the ListSessions mapping: the
// session's identity and state arrive whole, every vendor name maps, and the
// topic sequence rides along for the subscribe.
func TestListSessionsCarriesTheSessionFields(t *testing.T) {
	t.Parallel()
	socket := serveReadHost(t, &readHandler{list: &vendorsv1.ListSessionsResponse{
		Sessions: []*vendorsv1.VendorSession{
			{SessionId: "s1", Vendor: vendorsv1.Vendor_VENDOR_CLAUDE, State: testStateRunning, CreatedAt: "t1", UpdatedAt: "t1"},
			{SessionId: "s2", Vendor: vendorsv1.Vendor_VENDOR_CODEX, State: "waiting_input", WaitingReason: "approval"},
			{SessionId: "s3", Vendor: vendorsv1.Vendor_VENDOR_AGY, State: "starting"},
			{SessionId: "s4", Vendor: vendorsv1.Vendor_VENDOR_UNSPECIFIED, State: "closed"},
		},
		Sequence: 9,
	}})
	c := dialApp(t, socket)
	list, err := c.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []client.Session{
		{ID: "s1", Vendor: "claude", State: testStateRunning, CreatedAt: "t1", UpdatedAt: "t1"},
		{ID: "s2", Vendor: "codex", State: "waiting_input", WaitingReason: "approval"},
		{ID: "s3", Vendor: "agy", State: "starting"},
		{ID: "s4", Vendor: "", State: "closed"},
	}
	if len(list.Sessions) != len(want) {
		t.Fatalf("sessions = %+v, want %d entries", list.Sessions, len(want))
	}
	for i := range want {
		if list.Sessions[i] != want[i] {
			t.Fatalf("session %d = %+v, want %+v", i, list.Sessions[i], want[i])
		}
	}
	if list.Sequence != 9 {
		t.Fatalf("sequence = %d, want 9", list.Sequence)
	}
}

// TestTranscriptItemCarriesTheDetail pins the TranscriptItem mapping: the
// entry's metadata (numbers included), the view's file summaries, the lock
// flags and the stored content arrive whole.
func TestTranscriptItemCarriesTheDetail(t *testing.T) {
	t.Parallel()
	exit := int32(3)
	socket := serveReadHost(t, &readHandler{detail: &transcriptv1.TranscriptDetail{
		Entry: &transcriptv1.TranscriptEntry{
			ItemId: "i1", TurnId: testTurn, Kind: transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TOOL_CALL,
			Sequence: 4, Revision: 2, CreatedAt: "c", UpdatedAt: "u", TurnOrigin: testOrigin,
			ContentLocked: true,
			Metadata: &transcriptv1.TranscriptMetadata{
				Tool: "read_file", Status: "failed", ExitCode: &exit, DurationMillis: 12,
				ContentTruncated: true, OriginalContentBytes: 99,
			},
			View: &transcriptv1.TranscriptView{
				Text: "output", Truncated: true, CutAt: 5, OmittedBytes: 7,
				Files: []*transcriptv1.TranscriptFileSummary{{Header: "a.go", Added: 1, Removed: 2}},
			},
		},
		ContentJson: []byte(`{"full":"content"}`),
	}})
	c := dialApp(t, socket)
	detail, err := c.TranscriptItem(t.Context(), "s1", "i1")
	if err != nil {
		t.Fatal(err)
	}
	item := detail.Item
	wantIdentity(t, &item)
	if item.ContentLocked != true || item.TurnOrigin != testOrigin || item.CreatedAt != "c" || item.UpdatedAt != "u" {
		t.Fatalf("item flags = %+v", item)
	}
	wantDetailMetadata(t, &item.Metadata)
	wantDetailView(t, item.View)
	if string(detail.Content) != `{"full":"content"}` {
		t.Fatalf("content = %s", detail.Content)
	}
}

// TestDecodeTranscriptSnapshotCarriesTheTopicState pins the snapshot decoder:
// the sequence, session and lock state, and the open items arrive whole.
func TestDecodeTranscriptSnapshotCarriesTheTopicState(t *testing.T) {
	t.Parallel()
	snapshot, err := proto.Marshal(&transcriptv1.TranscriptSnapshot{
		Sequence: 12, SessionState: testStateRunning, ContentLocked: true,
		Items: []*transcriptv1.TranscriptDurableItem{{
			ItemId: "i1", TurnId: testTurn, Kind: transcriptv1.TranscriptKind_TRANSCRIPT_KIND_NOTICE,
			Sequence: 11, Revision: 1, TurnOrigin: testOrigin,
			View: &transcriptv1.TranscriptView{Text: "hello"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := client.DecodeTranscriptSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Sequence != 12 || snap.SessionState != testStateRunning || !snap.ContentLocked {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Items) != 1 || snap.Items[0].ItemID != "i1" || snap.Items[0].Kind != "notice" || snap.Items[0].View.Text != "hello" {
		t.Fatalf("snapshot items = %+v", snap.Items)
	}
}

// TestDecodeTranscriptDeltaCarriesTheBaseAndView pins the delta decoder: the
// base revision and the complete view with its file summaries arrive whole.
func TestDecodeTranscriptDeltaCarriesTheBaseAndView(t *testing.T) {
	t.Parallel()
	delta, err := proto.Marshal(&transcriptv1.TranscriptDelta{
		ItemId: "i2", BaseRevision: 3,
		View: &transcriptv1.TranscriptView{Text: "text", Files: []*transcriptv1.TranscriptFileSummary{{Header: "b.go", Added: 2, Removed: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.DecodeTranscriptDelta(delta)
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemID != "i2" || got.BaseRevision != 3 || got.View.Text != "text" {
		t.Fatalf("delta = %+v", got)
	}
	if len(got.View.Files) != 1 || got.View.Files[0].Header != "b.go" {
		t.Fatalf("delta files = %+v", got.View.Files)
	}
}

// TestDecodeTranscriptTopicRejectsForeignPayloads pins that a payload the
// host did not write fails every decoder.
func TestDecodeTranscriptTopicRejectsForeignPayloads(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string][]byte{
		"snapshot": []byte("not a transcript message"),
		"item":     []byte("not a transcript message"),
		"delta":    []byte("not a transcript message"),
	} {
		_, snapErr := client.DecodeTranscriptSnapshot(payload)
		_, itemErr := client.DecodeTranscriptItem(payload)
		_, deltaErr := client.DecodeTranscriptDelta(payload)
		if snapErr == nil || itemErr == nil || deltaErr == nil {
			t.Fatalf("%s payload passed a decoder: snapshot %v, item %v, delta %v", name, snapErr, itemErr, deltaErr)
		}
	}
}

// wantIdentity asserts the item's identity fields.
func wantIdentity(t *testing.T, item *client.TranscriptItem) {
	t.Helper()
	if item.Kind != "tool_call" || item.TurnID != testTurn || item.Sequence != 4 || item.Revision != 2 {
		t.Fatalf("item identity = %+v", item)
	}
}

// wantDetailMetadata asserts the mapped metadata, numbers included.
func wantDetailMetadata(t *testing.T, m *client.TranscriptMetadata) {
	t.Helper()
	if m.Tool != "read_file" || m.Status != "failed" || m.ExitCode == nil || *m.ExitCode != 3 {
		t.Fatalf("metadata = %+v", m)
	}
	if !m.ContentTruncated || m.OriginalContentBytes != 99 || m.DurationMillis != 12 {
		t.Fatalf("metadata truncation = %+v", m)
	}
}

// wantDetailView asserts the mapped view and its file summaries.
func wantDetailView(t *testing.T, v client.TranscriptView) {
	t.Helper()
	if v.Text != "output" || !v.Truncated || v.CutAt != 5 || v.OmittedBytes != 7 {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Files) != 1 || v.Files[0].Header != "a.go" || v.Files[0].Added != 1 || v.Files[0].Removed != 2 {
		t.Fatalf("view files = %+v", v.Files)
	}
}
