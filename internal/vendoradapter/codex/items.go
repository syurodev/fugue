package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/transcript"
)

const (
	toolStatusRunning   = "running"
	toolStatusSucceeded = "succeeded"
	toolStatusFailed    = "failed"
	toolStatusDenied    = "denied"
	toolStatusUnknown   = "unknown"
)

func (n *Normalizer) normalizeItem(frame *Frame, completed bool) Observation {
	var notif ItemNotification
	if json.Unmarshal(frame.Params, &notif) != nil || len(notif.Item) == 0 {
		return n.unsupported(n.lineRef(), frame.Method)
	}
	var hdr ItemHeader
	if json.Unmarshal(notif.Item, &hdr) != nil || hdr.ID == "" || hdr.Type == "" {
		return n.unsupported(n.lineRef(), frame.Method)
	}
	return n.dispatchItemByType(&hdr, notif.Item, n.resolveTurnID(notif.TurnID), frame.Method, completed)
}

func (n *Normalizer) dispatchItemByType(
	hdr *ItemHeader,
	rawItem json.RawMessage,
	turnID, method string,
	completed bool,
) Observation {
	switch hdr.Type {
	case ItemTypeUserMessage:
		return n.normalizeUserMessageItem(rawItem, turnID, completed)
	case ItemTypeAgentMessage:
		return n.normalizeAgentMessageItem(rawItem, turnID, completed)
	case ItemTypeReasoning:
		return n.normalizeReasoningItem(rawItem, turnID, completed)
	case ItemTypeCommandExecution:
		return n.normalizeCommandItem(rawItem, turnID, completed)
	case ItemTypeFileChange:
		return n.normalizeFileChangeItem(rawItem, turnID, completed)
	case ItemTypeMcpToolCall:
		return n.normalizeMcpToolCallItem(rawItem, turnID, completed)
	case ItemTypePlan:
		return n.normalizePlanItem(rawItem, turnID)
	case ItemTypeContextCompaction, ItemTypeEnteredReview, ItemTypeExitedReview:
		return n.normalizeCommandModeItem(hdr.Type, rawItem, turnID, completed)
	default:
		return n.unsupported(hdr.ID, method+"/"+hdr.Type)
	}
}

func (n *Normalizer) normalizeUserMessageItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item UserMessageItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeUserMessage)
	}
	text := extractUserText(item.Content)
	if existing, ok := n.items[item.ID]; ok {
		if cur, ok := existing.Content.(transcript.UserMessageContent); ok && cur.Text == text {
			return Observation{}
		}
		if completed {
			return n.updateExisting(existing, transcript.UserMessageMetadata{Origin: "user"}, transcript.UserMessageContent{Text: text})
		}
		return Observation{}
	}
	return n.recordNew(item.ID, turnID, item.ID, transcript.UserMessage,
		transcript.UserMessageMetadata{Origin: "user"},
		transcript.UserMessageContent{Text: text},
	)
}

func extractUserText(elements []json.RawMessage) string {
	var parts []string
	for _, raw := range elements {
		var elem struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &elem) == nil && elem.Text != "" {
			parts = append(parts, elem.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (n *Normalizer) normalizeAgentMessageItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item AgentMessageItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeAgentMessage)
	}
	text := item.Text
	if text == "" {
		text = n.deltas[item.ID]
	}
	meta := transcript.AssistantMessageMetadata{ModelID: n.model}
	content := transcript.AssistantMessageContent{Text: text}
	if !completed {
		return n.recordNew(item.ID, turnID, item.ID, transcript.AssistantMessage, meta, content)
	}
	return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.AssistantMessage, meta, content)
}

func (n *Normalizer) normalizeReasoningItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item ReasoningItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeReasoning)
	}
	status, text := reasoningStatusAndText(item, n.deltas[item.ID])
	meta := transcript.ReasoningMetadata{Status: status}
	content := transcript.ReasoningContent{Text: text}
	if !completed {
		return n.recordNew(item.ID, turnID, item.ID, transcript.Reasoning, meta, content)
	}
	return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.Reasoning, meta, content)
}

func reasoningStatusAndText(item ReasoningItem, deltaFallback string) (status, text string) {
	if len(item.Content) > 0 {
		return "plain", strings.Join(item.Content, "\n")
	}
	if len(item.Summary) > 0 {
		return "summary", strings.Join(item.Summary, "\n")
	}
	return "summary", deltaFallback
}

func (n *Normalizer) normalizeCommandItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item CommandExecutionItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeCommandExecution)
	}
	output := derefStr(item.AggregatedOutput)
	if output == "" {
		output = n.deltas[item.ID]
	}
	var dur time.Duration
	if item.DurationMs != nil && *item.DurationMs > 0 {
		dur = time.Duration(*item.DurationMs) * time.Millisecond
	}
	meta := transcript.ToolCallMetadata{
		ToolName: ItemTypeCommandExecution,
		Status:   mapToolStatus(item.Status),
		ExitCode: item.ExitCode,
		Duration: dur,
	}
	content := transcript.ToolCallContent{
		Title:  item.Command,
		Input:  item.Command,
		Output: output,
	}
	if !completed {
		return n.recordNew(item.ID, turnID, item.ID, transcript.ToolCall, meta, content)
	}
	return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.ToolCall, meta, content)
}

func (n *Normalizer) normalizeFileChangeItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item FileChangeItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeFileChange)
	}
	if len(item.Changes) > 0 {
		n.fileChanges[item.ID] = item.Changes
	}
	if !completed || item.Status == ItemStatusDeclined || item.Status == ItemStatusFailed {
		return Observation{}
	}
	changes := item.Changes
	if len(changes) == 0 {
		changes = n.fileChanges[item.ID]
	}
	var obs Observation
	for i, ch := range changes {
		changeID := item.ID
		if len(changes) > 1 {
			changeID = fmt.Sprintf("%s-%d", item.ID, i+1)
		}
		kind, oldPath := mapPatchChangeKind(ch.Kind)
		added, removed := countDiffLines(ch.Diff)
		meta := transcript.FileChangeMetadata{Change: kind, Added: added, Removed: removed}
		content := transcript.FileChangeContent{Path: ch.Path, OldPath: oldPath, Diff: ch.Diff}
		step := n.recordOrUpdate(changeID, turnID, item.ID, transcript.FileChange, meta, content)
		obs.Records = append(obs.Records, step.Records...)
		obs.Updates = append(obs.Updates, step.Updates...)
	}
	return obs
}

func mapPatchChangeKind(k PatchChangeKind) (change, oldPath string) {
	if k.MovePath != nil && *k.MovePath != "" {
		return "rename", *k.MovePath
	}
	switch k.Type {
	case "add":
		return "add", ""
	case "delete":
		return "delete", ""
	default:
		return "modify", ""
	}
}

func countDiffLines(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			added++
		} else if strings.HasPrefix(line, "-") {
			removed++
		}
	}
	return added, removed
}

func (n *Normalizer) normalizeMcpToolCallItem(raw json.RawMessage, turnID string, completed bool) Observation {
	var item McpToolCallItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" || item.Tool == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypeMcpToolCall)
	}
	toolName := item.Tool
	if item.Server != "" {
		toolName = item.Server + "/" + item.Tool
	}
	var dur time.Duration
	if item.DurationMs != nil && *item.DurationMs > 0 {
		dur = time.Duration(*item.DurationMs) * time.Millisecond
	}
	output := ""
	if len(item.Result) > 0 && string(item.Result) != "null" {
		output = compactJSON(item.Result)
	} else if item.Error != nil {
		output = item.Error.Message
	}
	meta := transcript.ToolCallMetadata{
		ToolName: toolName,
		Status:   mapToolStatus(item.Status),
		Duration: dur,
	}
	content := transcript.ToolCallContent{
		Title:  toolName,
		Input:  compactJSON(item.Arguments),
		Output: output,
	}
	if !completed {
		return n.recordNew(item.ID, turnID, item.ID, transcript.ToolCall, meta, content)
	}
	return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.ToolCall, meta, content)
}

func (n *Normalizer) normalizePlanItem(raw json.RawMessage, turnID string) Observation {
	var item PlanItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), MethodItemStarted+"/"+ItemTypePlan)
	}
	text := item.Text
	if text == "" {
		text = n.deltas[item.ID]
	}
	var steps []string
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			steps = append(steps, trimmed)
		}
	}
	meta := transcript.PlanMetadata{Steps: len(steps)}
	content := transcript.PlanContent{Steps: steps}
	return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.Plan, meta, content)
}

func (n *Normalizer) normalizeTurnPlanUpdated(frame *Frame) Observation {
	var notif TurnPlanUpdatedNotification
	if json.Unmarshal(frame.Params, &notif) != nil || notif.TurnID == "" {
		return n.unsupported(n.lineRef(), MethodTurnPlanUpdated)
	}
	turnID := n.resolveTurnID(notif.TurnID)
	steps := make([]string, 0, len(notif.Plan))
	current := 0
	for i, s := range notif.Plan {
		steps = append(steps, s.Step)
		if s.Status == "inProgress" {
			current = i + 1
		}
	}
	planID := turnID + "-plan"
	meta := transcript.PlanMetadata{Steps: len(steps), Current: current}
	content := transcript.PlanContent{Steps: steps}
	return n.recordOrUpdate(planID, turnID, n.lineRef(), transcript.Plan, meta, content)
}

func (n *Normalizer) normalizeCommandModeItem(itemType string, raw json.RawMessage, turnID string, completed bool) Observation {
	var item ReviewModeItem
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return n.unsupported(n.lineRef(), itemType)
	}
	if itemType == ItemTypeExitedReview && completed && item.Review != "" {
		return n.recordOrUpdate(item.ID, turnID, item.ID, transcript.AssistantMessage,
			transcript.AssistantMessageMetadata{ModelID: n.model},
			transcript.AssistantMessageContent{Text: item.Review},
		)
	}
	if completed {
		return Observation{}
	}
	cmdName := SlashCompact
	if itemType != ItemTypeContextCompaction {
		cmdName = SlashReview
	}
	return n.recordNew(item.ID, turnID, item.ID, transcript.Command,
		transcript.CommandMetadata{Source: "vendor"},
		transcript.CommandContent{Name: cmdName},
	)
}

func (n *Normalizer) normalizeDeltaOrUnknown(frame *Frame) Observation {
	switch frame.Method {
	case MethodAgentMessageDelta, MethodCommandOutputDelta, MethodFileChangeOutputDelta, MethodPlanDelta:
		var d DeltaNotification
		if json.Unmarshal(frame.Params, &d) != nil || d.ItemID == "" {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		n.deltas[d.ItemID] += d.Delta
		blockType, deltaType := mapDeltaKind(frame.Method)
		return Observation{Partial: &Partial{
			MessageID: d.ItemID,
			BlockType: blockType,
			DeltaType: deltaType,
			Fragment:  d.Delta,
		}}
	case MethodReasoningSummaryDelta:
		var d ReasoningSummaryDeltaNotification
		if json.Unmarshal(frame.Params, &d) != nil || d.ItemID == "" {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		n.deltas[d.ItemID] += d.Delta
		return Observation{Partial: &Partial{
			MessageID: d.ItemID,
			BlockType: "thinking",
			DeltaType: "thinking_delta",
			Fragment:  d.Delta,
		}}
	case MethodReasoningTextDelta:
		var d ReasoningTextDeltaNotification
		if json.Unmarshal(frame.Params, &d) != nil || d.ItemID == "" {
			return n.unsupported(n.lineRef(), frame.Method)
		}
		n.deltas[d.ItemID] += d.Delta
		return Observation{Partial: &Partial{
			MessageID: d.ItemID,
			BlockType: "thinking",
			DeltaType: "thinking_delta",
			Fragment:  d.Delta,
		}}
	default:
		return n.unsupported(n.lineRef(), frame.Method)
	}
}

func mapDeltaKind(method string) (blockType, deltaType string) {
	switch method {
	case MethodAgentMessageDelta:
		return "text", "text_delta"
	case MethodPlanDelta:
		return "plan", "plan_delta"
	default:
		return "tool_use", "output_delta"
	}
}

func mapToolStatus(status string) string {
	switch status {
	case ItemStatusInProgress:
		return toolStatusRunning
	case ItemStatusCompleted:
		return toolStatusSucceeded
	case ItemStatusFailed:
		return toolStatusFailed
	case ItemStatusDeclined:
		return toolStatusDenied
	default:
		return toolStatusUnknown
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (n *Normalizer) recordNew(id, turnID, rawRef string, kind transcript.Kind, meta transcript.Metadata, content transcript.Content) Observation {
	item := transcript.Item{
		ID:        id,
		SessionID: n.session,
		TurnID:    turnID,
		Kind:      kind,
		RawRef:    rawRef,
		Metadata:  meta,
		Content:   content,
	}
	n.items[id] = &item
	return Observation{Records: []transcript.Item{item}}
}

func (n *Normalizer) recordOrUpdate(id, turnID, rawRef string, kind transcript.Kind, meta transcript.Metadata, content transcript.Content) Observation {
	if existing, ok := n.items[id]; ok {
		return n.updateExisting(existing, meta, content)
	}
	return n.recordNew(id, turnID, rawRef, kind, meta, content)
}

func (n *Normalizer) updateExisting(existing *transcript.Item, meta transcript.Metadata, content transcript.Content) Observation {
	before := *existing
	next := *existing
	next.Revision++
	next.Metadata = meta
	next.Content = content
	*existing = next
	return Observation{Updates: []Update{{Before: before, After: next}}}
}
