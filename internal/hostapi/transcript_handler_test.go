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

func TestTranscriptTopicSnapshotMatchesRPCItemMapping(t *testing.T) {
	t.Parallel()
	item := transcriptTopicItemFixture(t)
	snapshot := app.TranscriptTopicSnapshot{Sequence: 24, SessionState: "ready", ContentLocked: false}
	snapshotPayload := []byte(`{"sequence":24,"session_state":"ready","turns":[{"turn_id":"vendor-turn-7","turn_origin":"vendor","state":"running","outcome":""}],"content_locked":false,"items":[` + string(marshalTopicPayload(t, item)) + `]}`)
	if err := json.Unmarshal(snapshotPayload, &snapshot); err != nil {
		t.Fatal(err)
	}
	mapped := decodeSnapshotTopic(t, app.TopicTranscript+"session", marshalTopicPayload(t, snapshot))
	wantEntry := transcriptTopicRPCEntry(&item)
	wantItem := &transcriptv1.TranscriptDurableItem{
		ItemId: wantEntry.ItemId, TurnId: wantEntry.TurnId, Kind: wantEntry.Kind,
		Sequence: wantEntry.Sequence, Revision: wantEntry.Revision, Metadata: wantEntry.Metadata,
		View: wantEntry.View, TurnOrigin: wantEntry.TurnOrigin,
	}
	if mapped.Sequence != 24 || len(mapped.Turns) != 1 || mapped.Turns[0].State != "running" || len(mapped.Items) != 1 || !proto.Equal(mapped.Items[0], wantItem) {
		t.Fatalf("mapped snapshot = %+v; want item from RPC mapping %+v", &mapped, wantItem)
	}
}

func TestTranscriptItemTopicEventMatchesRPCMapping(t *testing.T) {
	t.Parallel()
	item := transcriptTopicItemFixture(t)
	mapped := decodeItemTopicEvent(t, app.TopicTranscript+"session", marshalTopicPayload(t, item))
	entry := transcriptTopicRPCEntry(&item)
	want := &transcriptv1.TranscriptItemEvent{
		ItemId: entry.ItemId, TurnId: entry.TurnId, Kind: entry.Kind,
		Sequence: entry.Sequence, Revision: entry.Revision, Metadata: entry.Metadata,
		View: entry.View, TurnOrigin: entry.TurnOrigin,
	}
	if !proto.Equal(mapped, want) {
		t.Fatalf("mapped item event = %+v; want RPC mapping %+v", mapped, want)
	}
}

func TestTranscriptStateTopicEventMapsTypedAppPayload(t *testing.T) {
	t.Parallel()
	var state app.TranscriptTopicStateEvent
	if err := json.Unmarshal([]byte(`{"turn_id":"vendor-turn-7","state":"running","waiting_reason":"tool","outcome":"","failure_code":"","stop_proof":"","turn_origin":"vendor"}`), &state); err != nil {
		t.Fatal(err)
	}
	mapped := decodeStateTopicEvent(t, app.TopicTranscript+"session", marshalTopicPayload(t, state))
	want := &transcriptv1.TranscriptStateEvent{
		TurnId: state.TurnID, State: state.State, WaitingReason: state.WaitingReason,
		Outcome: state.Outcome, FailureCode: state.FailureCode, StopProof: state.StopProof, TurnOrigin: state.TurnOrigin,
	}
	if !proto.Equal(mapped, want) {
		t.Fatalf("mapped state event = %+v; want %+v", mapped, want)
	}
}

func TestTranscriptDeltaTopicPayloadsMatchRPCViewMapping(t *testing.T) {
	t.Parallel()
	var delta app.TranscriptTopicDelta
	if err := json.Unmarshal([]byte(`{"item_id":"item-7","base_revision":3,"view":{"text":"redacted display","truncated":true,"cut_at":16,"omitted_bytes":8,"files":[{"Header":"change.go","Added":3,"Removed":1}]}}`), &delta); err != nil {
		t.Fatal(err)
	}
	wantView := protoTranscriptEntry(&app.TranscriptEntry{View: delta.View}).View
	mapped := decodeDeltaTopicEvent(t, app.TopicTranscriptDelta+"session", marshalTopicPayload(t, delta))
	wantDelta := &transcriptv1.TranscriptDelta{ItemId: delta.ItemID, BaseRevision: delta.BaseRevision, View: wantView}
	if !proto.Equal(mapped, wantDelta) {
		t.Fatalf("mapped delta = %+v; want RPC view mapping %+v", &mapped, wantDelta)
	}

	var snapshot app.TranscriptDeltaSnapshot
	if err := json.Unmarshal([]byte(`{"content_locked":false,"items":[{"item_id":"item-7","base_revision":3,"view":{"text":"redacted display","truncated":true,"cut_at":16,"omitted_bytes":8,"files":[{"Header":"change.go","Added":3,"Removed":1}]}}]}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	mappedSnapshot := decodeDeltaSnapshotTopic(t, app.TopicTranscriptDelta+"session", marshalTopicPayload(t, snapshot))
	if len(mappedSnapshot.Items) != 1 || !proto.Equal(mappedSnapshot.Items[0].View, wantView) {
		t.Fatalf("mapped delta snapshot = %+v; want RPC view mapping %+v", &mappedSnapshot, wantView)
	}
}

func transcriptTopicItemFixture(t *testing.T) app.TranscriptTopicItemEvent {
	t.Helper()
	var item app.TranscriptTopicItemEvent
	if err := json.Unmarshal([]byte(`{"item_id":"item-7","turn_id":"vendor-turn-7","kind":"assistant_message","sequence":21,"revision":4,"metadata":{"model":"model-x","status":"complete"},"view":{"text":"redacted display","truncated":true,"cut_at":16,"omitted_bytes":8,"files":[{"Header":"change.go","Added":3,"Removed":1}]},"turn_origin":"vendor"}`), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func transcriptTopicRPCEntry(item *app.TranscriptTopicItemEvent) *transcriptv1.TranscriptEntry {
	return protoTranscriptEntry(&app.TranscriptEntry{
		ID: item.ItemID, TurnID: item.TurnID, Kind: app.TranscriptKind(item.Kind),
		Sequence: 21, Revision: item.Revision, Metadata: item.Metadata,
		View: *item.View, TurnOrigin: item.TurnOrigin,
	})
}

func marshalTopicPayload(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func decodeSnapshotTopic(t *testing.T, topic string, payload []byte) *transcriptv1.TranscriptSnapshot {
	t.Helper()
	encoded, err := transcriptSnapshotProto(topic, payload)
	if err != nil {
		t.Fatal(err)
	}
	var value transcriptv1.TranscriptSnapshot
	if err := proto.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return &value
}

func decodeDeltaSnapshotTopic(t *testing.T, topic string, payload []byte) *transcriptv1.TranscriptDeltaSnapshot {
	t.Helper()
	encoded, err := transcriptSnapshotProto(topic, payload)
	if err != nil {
		t.Fatal(err)
	}
	var value transcriptv1.TranscriptDeltaSnapshot
	if err := proto.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return &value
}

func decodeItemTopicEvent(t *testing.T, topic string, payload []byte) *transcriptv1.TranscriptItemEvent {
	t.Helper()
	encoded, err := transcriptEventProto(topic, app.EventTranscriptItem, payload)
	if err != nil {
		t.Fatal(err)
	}
	var value transcriptv1.TranscriptItemEvent
	if err := proto.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return &value
}

func decodeStateTopicEvent(t *testing.T, topic string, payload []byte) *transcriptv1.TranscriptStateEvent {
	t.Helper()
	encoded, err := transcriptEventProto(topic, app.EventTranscriptState, payload)
	if err != nil {
		t.Fatal(err)
	}
	var value transcriptv1.TranscriptStateEvent
	if err := proto.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return &value
}

func decodeDeltaTopicEvent(t *testing.T, topic string, payload []byte) *transcriptv1.TranscriptDelta {
	t.Helper()
	encoded, err := transcriptEventProto(topic, app.EventTranscriptDelta, payload)
	if err != nil {
		t.Fatal(err)
	}
	var value transcriptv1.TranscriptDelta
	if err := proto.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return &value
}
