package claude

// Mapping of the tool-bearing events into transcript items (transcript §4):
// assistant text and thinking blocks, tool_use into tool_call, plan and
// subagent items, tool_result updates, and the file_change items the Edit,
// Write and MultiEdit tools produce on success. Item identity is the vendor's
// own id — the tool_use id for tools, the message id plus a per-message block
// counter for text and thinking — so updates match what the CLI reports.

import (
	"encoding/json"
	"strconv"

	"github.com/syurodev/fugue/internal/transcript"
)

// Tool statuses (transcript §3).
const (
	toolPending   = "pending"
	toolSucceeded = "succeeded"
	toolFailed    = "failed"
	toolDenied    = "denied"
)

// Tool names the normalizer maps specially (transcript §4).
const (
	toolEdit      = "Edit"
	toolWrite     = "Write"
	toolMultiEdit = "MultiEdit"
	toolTodoWrite = "TodoWrite"
	toolTask      = "Task"
)

func (n *Normalizer) normalizeAssistant(line []byte, env *envelope) Observation {
	var w wireAssistant
	if json.Unmarshal(line, &w) != nil {
		return n.unsupported(env, noticeLabel(env))
	}
	var obs Observation
	for i := range w.Message.Content {
		records, updates := n.recordBlock(w.Message, &w.Message.Content[i], env)
		obs.Records = append(obs.Records, records...)
		obs.Updates = append(obs.Updates, updates...)
	}
	return obs
}

// recordBlock maps one content block. A message can arrive as several events
// (one block each), so text and thinking ids count per message.
func (n *Normalizer) recordBlock(msg wireMessage, block *wireBlock, env *envelope) ([]transcript.Item, []Update) {
	switch block.Type {
	case "text":
		return []transcript.Item{n.textItem(msg, block, transcript.AssistantMessage,
			transcript.AssistantMessageMetadata{ModelID: msg.Model},
			transcript.AssistantMessageContent{Text: block.Text})}, nil
	case "thinking":
		return []transcript.Item{n.textItem(msg, block, transcript.Reasoning,
			transcript.ReasoningMetadata{Status: "plain"},
			transcript.ReasoningContent{Text: block.Thinking})}, nil
	case "tool_use":
		return n.recordToolUse(block, env)
	default:
		return n.unsupported(env, "assistant/"+block.Type).Records, nil
	}
}

// textItem mints the per-message text/thinking item with a stable counter id.
func (n *Normalizer) textItem(msg wireMessage, block *wireBlock, kind transcript.Kind,
	metadata transcript.Metadata, content transcript.Content,
) transcript.Item {
	key := msg.ID + ":" + block.Type
	n.msgCounts[key]++
	return transcript.Item{
		ID:        msg.ID + "-" + block.Type + "-" + strconv.Itoa(n.msgCounts[key]),
		SessionID: n.session,
		TurnID:    n.turn,
		Kind:      kind,
		RawRef:    msg.ID,
		Metadata:  metadata,
		Content:   content,
	}
}

func (n *Normalizer) recordToolUse(block *wireBlock, env *envelope) ([]transcript.Item, []Update) {
	// Tool identity is the vendor's tool_use id; without one no update can
	// ever match the item, so the event becomes a notice instead of an
	// item with an empty id the transcript model rejects.
	if block.ID == "" {
		return n.unsupported(env, "assistant/tool_use").Records, nil
	}
	switch block.Name {
	case toolTodoWrite:
		return n.recordPlan(block, env)
	case toolTask:
		return []transcript.Item{n.subagentItem(block, env)}, nil
	default:
		return []transcript.Item{n.toolCallItem(block, env)}, nil
	}
}

func (n *Normalizer) toolCallItem(block *wireBlock, env *envelope) transcript.Item {
	item := transcript.Item{
		ID:        block.ID,
		SessionID: n.session,
		TurnID:    n.turn,
		Kind:      transcript.ToolCall,
		RawRef:    n.rawRef(env),
		Metadata:  transcript.ToolCallMetadata{ToolName: block.Name, Status: toolPending},
		Content:   transcript.ToolCallContent{Title: block.Name, Input: string(block.Input)},
	}
	n.items[block.ID] = &item
	return item
}

// subagentItem records a Task tool call as a subagent item (§4); nested
// events under it are counted on the observation until F9.5b/F9.9 enrich it.
func (n *Normalizer) subagentItem(block *wireBlock, env *envelope) transcript.Item {
	var input struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
	}
	// A Task input the decoder cannot read still records the subagent with
	// the fields it has; the raw event keeps the rest (TR-05).
	if err := json.Unmarshal(block.Input, &input); err != nil {
		input.SubagentType, input.Description = "", ""
	}
	item := transcript.Item{
		ID:        block.ID,
		SessionID: n.session,
		TurnID:    n.turn,
		Kind:      transcript.Subagent,
		RawRef:    n.rawRef(env),
		Metadata:  transcript.SubagentMetadata{Status: toolPending},
		Content:   transcript.SubagentContent{Name: input.SubagentType, Description: input.Description},
	}
	n.items[block.ID] = &item
	return item
}

// recordPlan maps a TodoWrite call: the plan item carries the whole list and
// every later call in the same turn updates it (§4).
func (n *Normalizer) recordPlan(block *wireBlock, env *envelope) ([]transcript.Item, []Update) {
	var input struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if json.Unmarshal(block.Input, &input) != nil {
		return n.unsupported(env, "assistant/TodoWrite").Records, nil
	}
	steps := make([]string, 0, len(input.Todos))
	current := 0
	for i, todo := range input.Todos {
		steps = append(steps, todo.Content)
		if todo.Status == "in_progress" {
			current = i + 1
		}
	}
	meta := transcript.PlanMetadata{Steps: len(steps), Current: current}
	content := transcript.PlanContent{Steps: steps}
	planID := n.turn + "-plan"
	if item, ok := n.items[planID]; ok {
		next := bumpRevision(item)
		next.Metadata = meta
		next.Content = content
		return nil, []Update{{Before: *item, After: next}}
	}
	item := transcript.Item{
		ID:        planID,
		SessionID: n.session,
		TurnID:    n.turn,
		Kind:      transcript.Plan,
		RawRef:    n.rawRef(env),
		Metadata:  meta,
		Content:   content,
	}
	n.items[planID] = &item
	return []transcript.Item{item}, nil
}

func (n *Normalizer) normalizeUser(line []byte, env *envelope) Observation {
	var w wireUser
	if json.Unmarshal(line, &w) != nil {
		return n.unsupported(env, noticeLabel(env))
	}
	blocks := toolResultBlocks(w.Message.Content)
	if len(blocks) == 0 {
		return n.unsupported(env, noticeLabel(env))
	}
	var obs Observation
	for _, result := range blocks {
		records, updates := n.applyToolResult(result, env)
		obs.Records = append(obs.Records, records...)
		obs.Updates = append(obs.Updates, updates...)
	}
	return obs
}

// applyToolResult resolves a pending item by the tool_use id: tool calls take
// status and output, subagents take result and status. A successful file-edit
// tool also records its file_change item, with the diff computed from the tool
// input (§4). A tool result no pending item waits for becomes a notice.
func (n *Normalizer) applyToolResult(result wireToolResult, env *envelope) ([]transcript.Item, []Update) {
	item := n.items[result.ToolUseID]
	if item == nil {
		return n.unsupported(env, "user/tool_result").Records, nil
	}
	output := toolResultText(result.Content)
	// The kind decides the update shape; plan items update from TodoWrite
	// inputs, not from tool results.
	if item.Kind == transcript.Subagent {
		return nil, []Update{n.subagentUpdate(item, result.IsError, output)}
	}
	if item.Kind == transcript.ToolCall {
		updates := []Update{n.toolCallUpdate(item, result.IsError, output)}
		return n.fileChangeOnSuccess(item, result.IsError), updates
	}
	return nil, nil
}

func (n *Normalizer) subagentUpdate(item *transcript.Item, isError bool, output string) Update {
	before := *item
	next := bumpRevision(item)
	status := toolSucceeded
	if isError {
		status = toolFailed
	}
	if meta, ok := next.Metadata.(transcript.SubagentMetadata); ok {
		meta.Status = status
		next.Metadata = meta
	}
	if content, ok := next.Content.(transcript.SubagentContent); ok {
		content.Result = output
		next.Content = content
	}
	*item = next
	return Update{Before: before, After: next}
}

func (n *Normalizer) toolCallUpdate(item *transcript.Item, isError bool, output string) Update {
	before := *item
	next := bumpRevision(item)
	status := toolSucceeded
	if isError {
		status = toolFailed
	}
	if meta, ok := next.Metadata.(transcript.ToolCallMetadata); ok {
		// A denial keeps its status when the error tool result follows it:
		// the CLI refused to run the tool, it did not fail running (VR-04).
		if meta.Status != toolDenied {
			meta.Status = status
		}
		next.Metadata = meta
	}
	if content, ok := next.Content.(transcript.ToolCallContent); ok && output != "" {
		content.Output = output
		next.Content = content
	}
	*item = next
	return Update{Before: before, After: next}
}

// fileChangeOnSuccess records the file_change item of a successful Edit, Write
// or MultiEdit call, with the diff computed from the recorded tool input.
func (n *Normalizer) fileChangeOnSuccess(item *transcript.Item, isError bool) []transcript.Item {
	meta, ok := item.Metadata.(transcript.ToolCallMetadata)
	if isError || !ok || (meta.ToolName != toolEdit && meta.ToolName != toolWrite && meta.ToolName != toolMultiEdit) {
		return nil
	}
	content, hasContent := item.Content.(transcript.ToolCallContent)
	if !hasContent {
		return nil
	}
	change, path, diff, added, removed := fileChange(meta.ToolName, []byte(content.Input))
	return []transcript.Item{{
		ID:        item.ID + "-file",
		SessionID: n.session,
		TurnID:    item.TurnID,
		Kind:      transcript.FileChange,
		RawRef:    item.RawRef,
		Metadata:  transcript.FileChangeMetadata{Change: change, Added: added, Removed: removed},
		Content:   transcript.FileChangeContent{Path: path, Diff: diff},
	}}
}
