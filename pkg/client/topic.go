package client

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

// TranscriptTopicSnapshot is the transcript topic's snapshot (transcript §6):
// the sequence it reflects, the session's state, and the open items' current
// display views. History is never in it; the caller reads it by page.
type TranscriptTopicSnapshot struct {
	Sequence      uint64
	SessionState  string
	ContentLocked bool
	Items         []TranscriptItem
}

// TranscriptDelta is one transcript-delta topic event: the latest complete
// display view of an open item, never an increment (transcript §5).
type TranscriptDelta struct {
	ItemID       string
	BaseRevision uint64
	View         TranscriptView
}

// DecodeTranscriptSnapshot decodes the transcript topic's snapshot payload.
func DecodeTranscriptSnapshot(payload []byte) (TranscriptTopicSnapshot, error) {
	var wire transcriptv1.TranscriptSnapshot
	if err := proto.Unmarshal(payload, &wire); err != nil {
		return TranscriptTopicSnapshot{}, fmt.Errorf("decode the transcript snapshot: %w", err)
	}
	out := TranscriptTopicSnapshot{
		Sequence:      wire.GetSequence(),
		SessionState:  wire.GetSessionState(),
		ContentLocked: wire.GetContentLocked(),
	}
	for _, item := range wire.GetItems() {
		out.Items = append(out.Items, topicItem(item.GetItemId(), item.GetTurnId(), item.GetKind(),
			item.GetSequence(), item.GetRevision(), item.GetMetadata(), item.GetView(), item.GetTurnOrigin()))
	}
	return out, nil
}

// DecodeTranscriptItem decodes the transcript topic's item upsert.
func DecodeTranscriptItem(payload []byte) (TranscriptItem, error) {
	var wire transcriptv1.TranscriptItemEvent
	if err := proto.Unmarshal(payload, &wire); err != nil {
		return TranscriptItem{}, fmt.Errorf("decode the transcript item event: %w", err)
	}
	return topicItem(wire.GetItemId(), wire.GetTurnId(), wire.GetKind(), wire.GetSequence(),
		wire.GetRevision(), wire.GetMetadata(), wire.GetView(), wire.GetTurnOrigin()), nil
}

// DecodeTranscriptDelta decodes the transcript-delta topic's partial.
func DecodeTranscriptDelta(payload []byte) (TranscriptDelta, error) {
	var wire transcriptv1.TranscriptDelta
	if err := proto.Unmarshal(payload, &wire); err != nil {
		return TranscriptDelta{}, fmt.Errorf("decode the transcript delta: %w", err)
	}
	return TranscriptDelta{ItemID: wire.GetItemId(), BaseRevision: wire.GetBaseRevision(), View: viewFromProto(wire.GetView())}, nil
}

// topicItem maps one durable topic item onto the public shape.
func topicItem(itemID, turnID string, kind transcriptv1.TranscriptKind, sequence, revision uint64,
	metadata *transcriptv1.TranscriptMetadata, view *transcriptv1.TranscriptView, turnOrigin string,
) TranscriptItem {
	item := TranscriptItem{
		ItemID: itemID, TurnID: turnID, Kind: kindString(kind),
		Sequence: sequence, Revision: revision, TurnOrigin: turnOrigin,
	}
	if metadata != nil {
		item.Metadata = metadataFromProto(metadata)
	}
	if view != nil {
		item.View = viewFromProto(view)
	}
	return item
}
