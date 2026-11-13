package hostapi

import (
	"encoding/json"
	"math"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/internal/app"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

const testVendorTurnOrigin = "vendor"

func TestTranscriptItemRPCReturnsDetailAndMapsAllPublicFields(t *testing.T) {
	t.Parallel()
	v := startVendorHost(t, true)
	wsID := v.workspaceWithRoot(t, t.TempDir())
	session := v.start(t, "transcript-detail-start", wsID)
	if err := v.send(t, "transcript-detail-send", session.SessionId, "detail-body"); err != nil {
		t.Fatal(err)
	}
	page, err := v.transcripts.TranscriptPage(t.Context(), connect.NewRequest(&transcriptv1.TranscriptPageRequest{SessionId: session.SessionId}))
	if err != nil {
		t.Fatal(err)
	}
	var itemID string
	for _, entry := range page.Msg.Entries {
		if entry.Kind == transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USER_MESSAGE {
			itemID = entry.ItemId
			break
		}
	}
	if itemID == "" {
		t.Fatal("transcript page did not contain the user message")
	}
	detail, err := v.transcripts.TranscriptItem(t.Context(), connect.NewRequest(&transcriptv1.TranscriptItemRequest{SessionId: session.SessionId, ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Msg.Item == nil || detail.Msg.Item.Entry.ItemId != itemID || json.Valid(detail.Msg.Item.ContentJson) == false {
		t.Fatalf("transcript item detail = %+v", detail.Msg.Item)
	}
	var content map[string]any
	if err := json.Unmarshal(detail.Msg.Item.ContentJson, &content); err != nil {
		t.Fatal(err)
	}
	if content["Text"] != "detail-body" {
		t.Fatalf("redacted detail content = %v", content)
	}
}

func TestTranscriptProtoMappingCoversKindsMetadataAndNumericBounds(t *testing.T) {
	t.Parallel()
	kinds := []struct {
		name string
		want transcriptv1.TranscriptKind
	}{
		{"user_message", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USER_MESSAGE},
		{"assistant_message", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_ASSISTANT_MESSAGE},
		{"reasoning", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_REASONING},
		{"tool_call", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TOOL_CALL},
		{"file_change", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_FILE_CHANGE},
		{"approval", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_APPROVAL},
		{"plan", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_PLAN},
		{"subagent", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_SUBAGENT},
		{"command", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_COMMAND},
		{"team_proposal", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TEAM_PROPOSAL},
		{"task_link", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TASK_LINK},
		{"usage", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USAGE},
		{"quota", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_QUOTA},
		{"notice", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_NOTICE},
		{"future", transcriptv1.TranscriptKind_TRANSCRIPT_KIND_UNSPECIFIED},
	}
	for _, test := range kinds {
		if got := protoTranscriptKind(app.TranscriptKind(test.name)); got != test.want {
			t.Errorf("protoTranscriptKind(%q) = %v, want %v", test.name, got, test.want)
		}
	}
	exit := math.MaxInt
	entry := &app.TranscriptEntry{
		ID: "item", TurnID: "vendor-turn", Kind: app.TranscriptKind("notice"), Sequence: 2, Revision: 3,
		Metadata: app.TranscriptMetadata{ExitCode: &exit},
		View: app.TranscriptView{
			Text: "redacted", Truncated: true, CutAt: math.MaxInt, OmittedBytes: -1,
		},
		ContentLocked: true, TurnOrigin: testVendorTurnOrigin,
	}
	mapped := protoTranscriptEntry(entry)
	if mapped.TurnOrigin != testVendorTurnOrigin || !mapped.ContentLocked || mapped.Metadata.ExitCode == nil || *mapped.Metadata.ExitCode != math.MaxInt32 {
		t.Fatalf("mapped transcript metadata = %+v", mapped)
	}
	if mapped.View.CutAt != math.MaxUint32 || mapped.View.OmittedBytes != 0 {
		t.Fatalf("mapped transcript view = %+v", mapped.View)
	}
	if safeUint32(-1) != 0 || safeUint32(int(uint64(math.MaxUint32)+1)) != math.MaxUint32 || safeInt32(math.MinInt) != math.MinInt32 {
		t.Fatal("numeric bounds were not clamped")
	}
}

func TestTranscriptTopicConversionMapsNestedKindsAndRejectsMalformedPayload(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"items":[{"kind":"team_proposal","view":{"files":[{"Header":"file.go","Added":2,"Removed":1}]}}]}`)
	encoded, err := transcriptSnapshotProto(app.TopicTranscript+"session", payload)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot transcriptv1.TranscriptSnapshot
	if err := proto.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Kind != transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TEAM_PROPOSAL || len(snapshot.Items[0].View.Files) != 1 || snapshot.Items[0].View.Files[0].Header != "file.go" {
		t.Fatalf("mapped topic snapshot = %+v", &snapshot)
	}
	if _, err := transcriptEventProto(app.TopicTranscript+"session", app.EventTranscriptItem, []byte("{")); err == nil {
		t.Fatal("malformed transcript event was accepted")
	}
	if _, err := transcriptSnapshotProto("other:topic", []byte(`{"state":"kept"}`)); err != nil {
		t.Fatal(err)
	}
}
