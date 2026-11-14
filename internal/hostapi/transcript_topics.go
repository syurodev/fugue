package hostapi

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

func transcriptSnapshotProto(topic string, payload []byte) ([]byte, error) {
	switch {
	case transcriptTopicHasPrefix(topic, app.TopicTranscriptDelta):
		var snapshot app.TranscriptDeltaSnapshot
		if err := decodeTranscriptTopicPayload(payload, &snapshot); err != nil {
			return nil, err
		}
		mapped := &transcriptv1.TranscriptDeltaSnapshot{ContentLocked: snapshot.ContentLocked}
		for i := range snapshot.Items {
			mapped.Items = append(mapped.Items, protoTranscriptDelta(&snapshot.Items[i]))
		}
		return marshalTranscriptTopic(mapped)
	case transcriptTopicHasPrefix(topic, app.TopicTranscript):
		var snapshot app.TranscriptTopicSnapshot
		if err := decodeTranscriptTopicPayload(payload, &snapshot); err != nil {
			return nil, err
		}
		mapped := &transcriptv1.TranscriptSnapshot{
			Sequence: snapshot.Sequence, SessionState: snapshot.SessionState, ContentLocked: snapshot.ContentLocked,
		}
		for i := range snapshot.Turns {
			turn := &snapshot.Turns[i]
			mapped.Turns = append(mapped.Turns, &transcriptv1.TranscriptTurnState{
				TurnId: turn.ID, TurnOrigin: turn.Origin, State: turn.State, Outcome: turn.Outcome,
			})
		}
		for i := range snapshot.Items {
			item, itemErr := protoTranscriptTopicItem(&snapshot.Items[i])
			if itemErr != nil {
				return nil, itemErr
			}
			mapped.Items = append(mapped.Items, item)
		}
		return marshalTranscriptTopic(mapped)
	default:
		return payload, nil
	}
}

func transcriptEventProto(topic, eventType string, payload []byte) ([]byte, error) {
	switch {
	case transcriptTopicHasPrefix(topic, app.TopicTranscriptDelta):
		var delta app.TranscriptTopicDelta
		if err := decodeTranscriptTopicPayload(payload, &delta); err != nil {
			return nil, err
		}
		return marshalTranscriptTopic(protoTranscriptDelta(&delta))
	case transcriptTopicHasPrefix(topic, app.TopicTranscript):
		if eventType == app.EventTranscriptItem {
			var item app.TranscriptTopicItemEvent
			if err := decodeTranscriptTopicPayload(payload, &item); err != nil {
				return nil, err
			}
			mapped, mapErr := protoTranscriptTopicItemEvent(&item)
			if mapErr != nil {
				return nil, mapErr
			}
			return marshalTranscriptTopic(mapped)
		}
		var state app.TranscriptTopicStateEvent
		if err := decodeTranscriptTopicPayload(payload, &state); err != nil {
			return nil, err
		}
		return marshalTranscriptTopic(&transcriptv1.TranscriptStateEvent{
			TurnId: state.TurnID, State: state.State, WaitingReason: state.WaitingReason,
			Outcome: state.Outcome, FailureCode: state.FailureCode, StopProof: state.StopProof,
			TurnOrigin: state.TurnOrigin,
		})
	default:
		return payload, nil
	}
}

func transcriptTopicHasPrefix(topic, prefix string) bool {
	return len(topic) > len(prefix) && topic[:len(prefix)] == prefix
}

func decodeTranscriptTopicPayload[T any](payload []byte, value *T) error {
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("decode transcript topic payload: %w", err)
	}
	return nil
}

func marshalTranscriptTopic(message proto.Message) ([]byte, error) {
	encoded, err := proto.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode transcript topic payload: %w", err)
	}
	return encoded, nil
}

func protoTranscriptTopicItem(item *app.TranscriptTopicItemEvent) (*transcriptv1.TranscriptDurableItem, error) {
	appEntry, err := transcriptEntryFromTopicItem(item)
	if err != nil {
		return nil, err
	}
	entry := protoTranscriptEntry(appEntry)
	view := entry.View
	if item.View == nil {
		view = nil
	}
	return &transcriptv1.TranscriptDurableItem{
		ItemId: entry.ItemId, TurnId: entry.TurnId, Kind: entry.Kind,
		Sequence: entry.Sequence, Revision: entry.Revision, Metadata: entry.Metadata,
		View: view, TurnOrigin: entry.TurnOrigin,
	}, nil
}

func protoTranscriptTopicItemEvent(item *app.TranscriptTopicItemEvent) (*transcriptv1.TranscriptItemEvent, error) {
	appEntry, err := transcriptEntryFromTopicItem(item)
	if err != nil {
		return nil, err
	}
	entry := protoTranscriptEntry(appEntry)
	view := entry.View
	if item.View == nil {
		view = nil
	}
	return &transcriptv1.TranscriptItemEvent{
		ItemId: entry.ItemId, TurnId: entry.TurnId, Kind: entry.Kind,
		Sequence: entry.Sequence, Revision: entry.Revision, Metadata: entry.Metadata,
		View: view, TurnOrigin: entry.TurnOrigin,
	}, nil
}

func transcriptEntryFromTopicItem(item *app.TranscriptTopicItemEvent) (*app.TranscriptEntry, error) {
	if item.Sequence < 0 {
		return nil, errcode.New(errcode.Internal, "transcript topic item sequence is negative")
	}
	var view app.TranscriptView
	if item.View != nil {
		view = *item.View
	}
	var sequence uint64
	if item.Sequence > 0 {
		sequence = uint64(item.Sequence)
	}
	return &app.TranscriptEntry{
		ID: item.ItemID, TurnID: item.TurnID, Kind: app.TranscriptKind(item.Kind),
		Sequence: sequence, Revision: item.Revision, Metadata: item.Metadata,
		View: view, TurnOrigin: item.TurnOrigin,
	}, nil
}

func protoTranscriptDelta(delta *app.TranscriptTopicDelta) *transcriptv1.TranscriptDelta {
	return &transcriptv1.TranscriptDelta{
		ItemId: delta.ItemID, BaseRevision: delta.BaseRevision, View: protoTranscriptView(delta.View),
	}
}
