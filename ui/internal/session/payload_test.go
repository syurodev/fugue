package session

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/pkg/client"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

// mustItemPayload encodes one item upsert the way the host writes it.
func mustItemPayload(t *testing.T, item client.TranscriptItem) []byte {
	t.Helper()
	wire := &transcriptv1.TranscriptItemEvent{
		ItemId: item.ItemID, TurnId: item.TurnID, Kind: protoKind(item.Kind),
		Sequence: item.Sequence, Revision: item.Revision,
		View: &transcriptv1.TranscriptView{Text: item.View.Text},
	}
	payload, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// mustDeltaPayload encodes one transcript-delta event.
func mustDeltaPayload(t *testing.T, itemID string, base uint64, text string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&transcriptv1.TranscriptDelta{
		ItemId: itemID, BaseRevision: base, View: &transcriptv1.TranscriptView{Text: text},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// mustSnapshotPayload encodes the durable topic's snapshot.
func mustSnapshotPayload(t *testing.T, seq uint64, state string, items ...client.TranscriptItem) []byte {
	t.Helper()
	wire := &transcriptv1.TranscriptSnapshot{Sequence: seq, SessionState: state}
	for _, item := range items {
		wire.Items = append(wire.Items, &transcriptv1.TranscriptDurableItem{
			ItemId: item.ItemID, TurnId: item.TurnID, Kind: protoKind(item.Kind),
			Sequence: item.Sequence, Revision: item.Revision,
			View: &transcriptv1.TranscriptView{Text: item.View.Text},
		})
	}
	payload, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// protoKind maps the kind name onto the wire enum.
func protoKind(kind string) transcriptv1.TranscriptKind {
	switch kind {
	case "assistant_message":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_ASSISTANT_MESSAGE
	case "user_message":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USER_MESSAGE
	default:
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_UNSPECIFIED
	}
}
