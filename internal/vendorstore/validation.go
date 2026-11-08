package vendorstore

import (
	"reflect"
	"slices"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func validateItemMetadata(item *Item) error {
	fields := map[string][]string{
		"user_message": {metadataOrigin}, "assistant_message": {"Model"}, "reasoning": {metadataReasoningState},
		"tool_call":   {"Tool", "Title", metadataStatus, "ExitCode", "DurationMillis"},
		"file_change": {"Path", metadataChangeKind, "LinesAdded", "LinesRemoved"},
		"approval":    {"RequestKind", "Decision", metadataDecider, "Receipt"}, ItemPlan: {"StepCount", "CurrentStep"},
		"subagent": {"Name", metadataStatus}, "command": {"Name", metadataSource},
		"team_proposal": {"ProposalID", "AgentCount", "Decision"}, "task_link": {"TaskID", "Agent", metadataStatus},
		"usage": {"InputTokens", "OutputTokens", "CacheTokens", "Cost"}, ItemQuota: {"Account", "Window", "Used", "ResetAt"},
		"notice": {metadataLevel, "Code"},
	}
	if _, ok := fields[item.Kind]; !ok {
		return errcode.New(errcode.InvalidArgument, "unknown transcript kind")
	}
	if err := validateMetadataEnums(item); err != nil {
		return err
	}
	value := reflect.ValueOf(item.Metadata)
	for i := range value.NumField() {
		if !value.Field(i).IsZero() && !slices.Contains(fields[item.Kind], value.Type().Field(i).Name) {
			return errcode.New(errcode.InvalidArgument, "metadata field does not belong to item kind")
		}
	}
	return nil
}

func validateMetadataEnums(item *Item) error {
	metadata := item.Metadata
	enums := map[string][]string{
		metadataOrigin: {"user", "lead", "fugue"}, metadataReasoningState: {"plain", "summary", "redacted"},
		metadataChangeKind: {"add", "modify", "delete", "rename"}, metadataDecider: {"user", "policy", "lead"},
		metadataSource: {"vendor", "fugue"}, metadataLevel: {"info", "warning", "error"},
	}
	values := map[string]string{metadataOrigin: metadata.Origin, metadataReasoningState: metadata.ReasoningState, metadataChangeKind: metadata.ChangeKind, metadataDecider: metadata.Decider, metadataSource: metadata.Source, metadataLevel: metadata.Level}
	for field, value := range values {
		if value != "" && !slices.Contains(enums[field], value) {
			return errcode.New(errcode.InvalidArgument, "invalid item metadata enum")
		}
	}
	if item.Kind == "tool_call" && metadata.Status != "" && !slices.Contains([]string{"pending", "running", "succeeded", OutcomeFailed, "denied", OutcomeUnknown}, metadata.Status) {
		return errcode.New(errcode.InvalidArgument, "invalid tool status")
	}
	return nil
}

const (
	metadataOrigin         = "Origin"
	metadataReasoningState = "ReasoningState"
	metadataChangeKind     = "ChangeKind"
	metadataDecider        = "Decider"
	metadataSource         = "Source"
	metadataLevel          = "Level"
)
