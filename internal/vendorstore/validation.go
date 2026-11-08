package vendorstore

import (
	"reflect"
	"slices"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ItemMetadata field names that carry an enumerated or shared value.
const (
	metadataOrigin         = "Origin"
	metadataReasoningState = "ReasoningState"
	metadataStatus         = "Status"
	metadataChangeKind     = "ChangeKind"
	metadataDecider        = "Decider"
	metadataSource         = "Source"
	metadataLevel          = "Level"
)

func validateItemMetadata(item *Item) error {
	fields := map[string][]string{
		KindUserMessage:      {metadataOrigin},
		KindAssistantMessage: {"Model"},
		KindReasoning:        {metadataReasoningState},
		KindToolCall:         {"Tool", "Title", metadataStatus, "ExitCode", "DurationMillis"},
		KindFileChange:       {"Path", metadataChangeKind, "LinesAdded", "LinesRemoved"},
		KindApproval:         {"RequestKind", "Decision", metadataDecider, "Receipt"},
		KindPlan:             {"StepCount", "CurrentStep"},
		KindSubagent:         {"Name", metadataStatus},
		KindCommand:          {"Name", metadataSource},
		KindTeamProposal:     {"ProposalID", "AgentCount", "Decision"},
		KindTaskLink:         {"TaskID", "Agent", metadataStatus},
		KindUsage:            {"InputTokens", "OutputTokens", "CacheTokens", "Cost"},
		KindQuota:            {"Account", "Window", "Used", "ResetAt"},
		KindNotice:           {metadataLevel, "Code"},
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
		metadataOrigin:         {"user", "lead", "fugue"},
		metadataReasoningState: {"plain", "summary", "redacted"},
		metadataChangeKind:     {"add", "modify", "delete", "rename"},
		metadataDecider:        {"user", "policy", "lead"},
		metadataSource:         {"vendor", "fugue"},
		metadataLevel:          {"info", "warning", "error"},
	}
	values := map[string]string{
		metadataOrigin:         metadata.Origin,
		metadataReasoningState: metadata.ReasoningState,
		metadataChangeKind:     metadata.ChangeKind,
		metadataDecider:        metadata.Decider,
		metadataSource:         metadata.Source,
		metadataLevel:          metadata.Level,
	}
	for field, value := range values {
		if value != "" && !slices.Contains(enums[field], value) {
			return errcode.New(errcode.InvalidArgument, "invalid item metadata enum")
		}
	}
	if item.Kind == KindToolCall && metadata.Status != "" && !validToolStatus(metadata.Status) {
		return errcode.New(errcode.InvalidArgument, "invalid tool status")
	}
	return nil
}
