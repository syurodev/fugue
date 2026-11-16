package agy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/transcript"
)

func (n *Normalizer) normalizeUpdate(wire *UpdateWire) Observation {
	switch wire.SessionUpdate {
	case UpdateUserMessageChunk:
		return n.normalizeTextChunk(wire, "user", transcript.UserMessage)
	case UpdateAgentMessageChunk:
		return n.normalizeTextChunk(wire, "msg", transcript.AssistantMessage)
	case UpdateAgentThoughtChunk:
		return n.normalizeTextChunk(wire, "thought", transcript.Reasoning)
	case UpdateToolCall, UpdateToolCallUpdate:
		return n.normalizeToolCall(wire)
	case UpdatePlan:
		return n.normalizePlan(wire)
	case UpdateAvailableCommands:
		return n.normalizeAvailableCommands(wire)
	default:
		return n.unsupported(n.lineRef(), "acp:"+wire.SessionUpdate)
	}
}

func (n *Normalizer) normalizeTextChunk(wire *UpdateWire, prefix string, kind transcript.Kind) Observation {
	var block ContentBlock
	if len(wire.Content) == 0 || json.Unmarshal(wire.Content, &block) != nil {
		return n.unsupported(n.lineRef(), "acp:"+wire.SessionUpdate)
	}
	text := block.Text
	if text == "" {
		return Observation{}
	}
	key := n.scopeKey() + ":" + prefix
	if wire.MessageID != "" {
		key = prefix + ":" + wire.MessageID
	}
	full := n.textBufs[key] + text
	n.textBufs[key] = full

	meta, content := n.chunkPayload(kind, full)
	existing, exists := n.items[key]
	if !exists {
		obs := n.recordNew(key, n.currentTurnID(), n.itemID(prefix, key), kind, meta, content)
		n.attachPartial(&obs, kind, obs.Records[0].ID, text)
		return obs
	}
	obs := n.updateExisting(existing, meta, content)
	n.attachPartial(&obs, kind, existing.ID, text)
	return obs
}

func (n *Normalizer) chunkPayload(kind transcript.Kind, text string) (transcript.Metadata, transcript.Content) {
	if kind == transcript.UserMessage {
		return transcript.UserMessageMetadata{Origin: "user"}, transcript.UserMessageContent{Text: text}
	}
	if kind == transcript.Reasoning {
		return transcript.ReasoningMetadata{Status: "plain"}, transcript.ReasoningContent{Text: text}
	}
	return transcript.AssistantMessageMetadata{ModelID: n.model}, transcript.AssistantMessageContent{Text: text}
}

func (n *Normalizer) attachPartial(obs *Observation, kind transcript.Kind, itemID, delta string) {
	if kind == transcript.UserMessage {
		return
	}
	blockType, deltaType := "text", "text_delta"
	if kind == transcript.Reasoning {
		blockType, deltaType = "thinking", "thinking_delta"
	}
	obs.Partial = &Partial{
		MessageID: itemID,
		BlockType: blockType,
		DeltaType: deltaType,
		Fragment:  delta,
	}
}

func (n *Normalizer) normalizeToolCall(wire *UpdateWire) Observation {
	if wire.ToolCallID == "" {
		return n.unsupported(n.lineRef(), "acp:"+wire.SessionUpdate)
	}
	st := n.mergeToolState(wire)
	meta := transcript.ToolCallMetadata{
		ToolName: st.name,
		Status:   st.status,
		ExitCode: st.exitCode,
	}
	content := transcript.ToolCallContent{
		Title:  st.title,
		Input:  st.input,
		Output: st.output,
	}

	var obs Observation
	existing, ok := n.items[wire.ToolCallID]
	if !ok {
		obs = n.recordNew(wire.ToolCallID, n.currentTurnID(), wire.ToolCallID, transcript.ToolCall, meta, content)
	} else {
		obs = n.updateExisting(existing, meta, content)
	}
	n.appendToolDiffItems(wire, &obs)
	return obs
}

func (n *Normalizer) mergeToolState(wire *UpdateWire) *toolState {
	st, ok := n.tools[wire.ToolCallID]
	if !ok {
		st = &toolState{status: "pending"}
		n.tools[wire.ToolCallID] = st
	}
	if wire.Name != "" {
		st.name = wire.Name
	} else if st.name == "" && wire.Kind != "" {
		st.name = wire.Kind
	}
	if st.name == "" {
		st.name = "tool"
	}
	if wire.Title != "" {
		st.title = wire.Title
	}
	if mapped := mapToolStatus(wire.Status); mapped != "" {
		st.status = mapped
	}
	if len(wire.RawInput) > 0 && string(wire.RawInput) != jsonNull {
		st.input = strings.TrimSpace(string(wire.RawInput))
	}
	n.mergeToolOutput(wire, st)
	return st
}

func (n *Normalizer) mergeToolOutput(wire *UpdateWire, st *toolState) {
	if text := extractToolContentText(wire.Content); text != "" {
		st.output = text
	}
	if len(wire.RawOutput) == 0 || string(wire.RawOutput) == jsonNull {
		return
	}
	var outObj struct {
		Output   string `json:"output"`
		ExitCode *int   `json:"exit_code"`
	}
	if json.Unmarshal(wire.RawOutput, &outObj) == nil {
		if outObj.ExitCode != nil {
			ec := *outObj.ExitCode
			st.exitCode = &ec
		}
		if st.output == "" && outObj.Output != "" {
			st.output = outObj.Output
		}
	}
	if st.output == "" {
		st.output = strings.TrimSpace(string(wire.RawOutput))
	}
}

func extractToolContentText(raw json.RawMessage) string {
	var entries []ToolCallContent
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil {
		return ""
	}
	var parts []string
	for _, entry := range entries {
		if entry.Type == ToolContentTypeContent && entry.Content != nil && entry.Content.Text != "" {
			parts = append(parts, entry.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (n *Normalizer) appendToolDiffItems(wire *UpdateWire, obs *Observation) {
	var entries []ToolCallContent
	if len(wire.Content) == 0 || json.Unmarshal(wire.Content, &entries) != nil {
		return
	}
	for idx, entry := range entries {
		if entry.Type != ToolContentTypeDiff || entry.Path == "" {
			continue
		}
		key := fmt.Sprintf("%s:diff:%d", wire.ToolCallID, idx)
		change := "modify"
		if entry.OldText == nil {
			change = "add"
		}
		diffText := formatDiffText(entry)
		meta := transcript.FileChangeMetadata{Change: change}
		content := transcript.FileChangeContent{Path: entry.Path, Diff: diffText}
		if existing, exists := n.items[key]; exists {
			upd := n.updateExisting(existing, meta, content)
			obs.Updates = append(obs.Updates, upd.Updates...)
			continue
		}
		rec := n.recordNew(key, n.currentTurnID(), wire.ToolCallID+"-diff-"+strconv.Itoa(idx+1), transcript.FileChange, meta, content)
		obs.Records = append(obs.Records, rec.Records...)
	}
}

func formatDiffText(entry ToolCallContent) string {
	if entry.OldText == nil {
		return entry.NewText
	}
	return fmt.Sprintf("--- %s\n+++ %s\n%s", entry.Path, entry.Path, entry.NewText)
}

func mapToolStatus(wireStatus string) string {
	switch wireStatus {
	case "":
		return ""
	case ToolStatusPendingWire:
		return "pending"
	case ToolStatusInProgressWire:
		return "running"
	case ToolStatusCompletedWire:
		return "succeeded"
	case ToolStatusFailedWire, ToolStatusErrorWire:
		return "failed"
	case "denied", "declined", "rejected":
		return "denied"
	default:
		return "unknown"
	}
}

func (n *Normalizer) normalizePlan(wire *UpdateWire) Observation {
	steps := make([]string, 0, len(wire.Entries))
	current := 0
	foundActive := false
	for idx, entry := range wire.Entries {
		line := entry.Content
		if entry.Status != "" {
			line = fmt.Sprintf("[%s] %s", entry.Status, entry.Content)
		}
		steps = append(steps, line)
		if !foundActive && entry.Status == ToolStatusInProgressWire {
			current = idx
			foundActive = true
		} else if !foundActive && entry.Status == ToolStatusCompletedWire {
			current = idx + 1
		}
	}
	if current > len(steps) {
		current = len(steps)
	}
	key := "plan:" + n.scopeKey()
	meta := transcript.PlanMetadata{Steps: len(steps), Current: current}
	content := transcript.PlanContent{Steps: steps}
	if existing, exists := n.items[key]; exists {
		return n.updateExisting(existing, meta, content)
	}
	return n.recordNew(key, n.currentTurnID(), n.itemID("plan", n.scopeKey()), transcript.Plan, meta, content)
}
