package hostapi

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/syurodev/fugue/internal/app"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
)

func transcriptSnapshotProto(topic string, payload []byte) ([]byte, error) {
	var target proto.Message
	switch {
	case len(topic) > len(app.TopicTranscriptDelta) && topic[:len(app.TopicTranscriptDelta)] == app.TopicTranscriptDelta:
		target = &transcriptv1.TranscriptDeltaSnapshot{}
	case len(topic) > len(app.TopicTranscript) && topic[:len(app.TopicTranscript)] == app.TopicTranscript:
		target = &transcriptv1.TranscriptSnapshot{}
	default:
		return payload, nil
	}
	return transcodeTranscriptPayload(payload, target)
}

func transcriptEventProto(topic, eventType string, payload []byte) ([]byte, error) {
	var target proto.Message
	switch {
	case len(topic) > len(app.TopicTranscriptDelta) && topic[:len(app.TopicTranscriptDelta)] == app.TopicTranscriptDelta:
		target = &transcriptv1.TranscriptDelta{}
	case len(topic) > len(app.TopicTranscript) && topic[:len(app.TopicTranscript)] == app.TopicTranscript:
		if eventType == app.EventTranscriptItem {
			target = &transcriptv1.TranscriptItemEvent{}
		} else {
			target = &transcriptv1.TranscriptStateEvent{}
		}
	default:
		return payload, nil
	}
	return transcodeTranscriptPayload(payload, target)
}

func transcodeTranscriptPayload(payload []byte, target proto.Message) ([]byte, error) {
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, fmt.Errorf("decode transcript topic payload: %w", err)
	}
	fixTranscriptKinds(wire)
	wireBytes, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode transcript topic mapping: %w", err)
	}
	if mapErr := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(wireBytes, target); mapErr != nil {
		return nil, fmt.Errorf("map transcript topic payload: %w", mapErr)
	}
	encoded, err := proto.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("encode transcript topic payload: %w", err)
	}
	return encoded, nil
}

func fixTranscriptKinds(value map[string]any) {
	if kind, ok := value["kind"].(string); ok {
		value["kind"] = transcriptKindName(kind)
	}
	fixTranscriptViewKinds(value)
	fixTranscriptItemKinds(value)
}

func fixTranscriptViewKinds(value map[string]any) {
	if view, ok := value["view"].(map[string]any); ok {
		if files, ok := view["files"].([]any); ok {
			for _, entry := range files {
				if file, ok := entry.(map[string]any); ok {
					file["header"], file["added"], file["removed"] = file["Header"], file["Added"], file["Removed"]
					delete(file, "Header")
					delete(file, "Added")
					delete(file, "Removed")
				}
			}
		}
	}
}

func fixTranscriptItemKinds(value map[string]any) {
	if items, ok := value["items"].([]any); ok {
		for _, entry := range items {
			if child, ok := entry.(map[string]any); ok {
				fixTranscriptKinds(child)
			}
		}
	}
}

func transcriptKindName(kind string) string {
	const prefix = "TRANSCRIPT_KIND_"
	switch kind {
	case "user_message":
		return prefix + "USER_MESSAGE"
	case "assistant_message":
		return prefix + "ASSISTANT_MESSAGE"
	case "reasoning":
		return prefix + "REASONING"
	case "tool_call":
		return prefix + "TOOL_CALL"
	case "file_change":
		return prefix + "FILE_CHANGE"
	case "approval":
		return prefix + "APPROVAL"
	case "plan":
		return prefix + "PLAN"
	case "subagent":
		return prefix + "SUBAGENT"
	case "command":
		return prefix + "COMMAND"
	default:
		return transcriptSpecialKindName(kind, prefix)
	}
}

func transcriptSpecialKindName(kind, prefix string) string {
	switch kind {
	case "team_proposal":
		return prefix + "TEAM_PROPOSAL"
	case "task_link":
		return prefix + "TASK_LINK"
	case "usage":
		return prefix + "USAGE"
	case "quota":
		return prefix + "QUOTA"
	case "notice":
		return prefix + "NOTICE"
	default:
		return prefix + "UNSPECIFIED"
	}
}
