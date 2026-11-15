package codex

import (
	"time"

	"github.com/syurodev/fugue/internal/transcript"
)

const (
	keyStatus    = "status"
	keyText      = "text"
	keyTimestamp = "resets_at"
)

func renderItem(it *transcript.Item) map[string]any {
	return map[string]any{
		PermissionScopeTurn: it.TurnID,
		"id":                it.ID,
		"kind":              string(it.Kind),
		"revision":          it.Revision,
		"raw":               it.RawRef,
		"metadata":          renderMetadata(it.Metadata),
		"content":           renderContent(it.Content),
	}
}

func renderMetadata(m transcript.Metadata) map[string]any {
	switch v := m.(type) {
	case transcript.UserMessageMetadata:
		return map[string]any{"origin": v.Origin}
	case transcript.AssistantMessageMetadata:
		return map[string]any{"model": v.ModelID}
	case transcript.ReasoningMetadata:
		return map[string]any{keyStatus: v.Status}
	case transcript.ToolCallMetadata:
		return renderToolMeta(v)
	case transcript.FileChangeMetadata:
		return map[string]any{"change": v.Change, "added": v.Added, "removed": v.Removed}
	default:
		return renderControlMetadata(m)
	}
}

func renderToolMeta(v transcript.ToolCallMetadata) map[string]any {
	out := map[string]any{"tool": v.ToolName, keyStatus: v.Status, "duration_ms": v.Duration.Milliseconds()}
	if v.ExitCode != nil {
		out["exit_code"] = *v.ExitCode
	}
	return out
}

func renderControlMetadata(m transcript.Metadata) map[string]any {
	switch v := m.(type) {
	case transcript.ApprovalMetadata:
		return map[string]any{"request": v.RequestType, "decision": v.Decision, "decider": v.Decider, "receipt": v.Receipt}
	case transcript.PlanMetadata:
		return map[string]any{"steps": v.Steps, "current": v.Current}
	case transcript.SubagentMetadata:
		return map[string]any{keyStatus: v.Status}
	case transcript.CommandMetadata:
		return map[string]any{"source": v.Source}
	case transcript.UsageMetadata:
		return map[string]any{
			"input_tokens": v.InputTokens, "output_tokens": v.OutputTokens,
			"cache_tokens": v.CacheTokens, "cost": v.Cost,
		}
	case transcript.QuotaMetadata:
		return map[string]any{"account": v.AccountID, "window": v.Window, "used": v.Used, keyTimestamp: renderTime(v.ResetsAt)}
	case transcript.NoticeMetadata:
		return map[string]any{"level": v.Level, "code": v.Code}
	default:
		return map[string]any{"unknown": true}
	}
}

func renderContent(c transcript.Content) map[string]any {
	switch v := c.(type) {
	case transcript.UserMessageContent:
		return map[string]any{keyText: v.Text, "attachments": anySlice(v.Attachments)}
	case transcript.AssistantMessageContent:
		return map[string]any{keyText: v.Text}
	case transcript.ReasoningContent:
		return map[string]any{keyText: v.Text}
	case transcript.ToolCallContent:
		return map[string]any{"title": v.Title, "input": v.Input, "output": v.Output}
	case transcript.FileChangeContent:
		return map[string]any{"path": v.Path, "old_path": v.OldPath, "diff": v.Diff}
	case transcript.PlanContent:
		return map[string]any{"steps": anySlice(v.Steps)}
	case transcript.CommandContent:
		return map[string]any{"name": v.Name, "args": anySlice(v.Args)}
	case transcript.UsageContent, transcript.QuotaContent:
		return map[string]any{}
	case transcript.NoticeContent:
		return map[string]any{keyText: v.Text}
	default:
		return map[string]any{"unknown": true}
	}
}

func anySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

func renderTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
