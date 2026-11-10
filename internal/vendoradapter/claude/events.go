package claude

// Event handlers for the non-tool stream-json shapes: system/init and
// system/permission_denied, result (turn end and usage), rate_limit_event
// (quota windows) and stream_event (partial deltas). Each handler decodes its
// payload from the raw line; a payload that does not match the expected shape
// becomes a notice(unsupported_event) — vendor output is data, never a crash.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/transcript"
)

func (n *Normalizer) normalizeSystem(line []byte, env *envelope) Observation {
	switch env.Subtype {
	case "init":
		return n.normalizeInit(line, env)
	case "permission_denied":
		return n.normalizePermissionDenied(line, env)
	default:
		return n.unsupported(env, "system/"+env.Subtype)
	}
}

func (n *Normalizer) normalizeInit(line []byte, env *envelope) Observation {
	var w wireInit
	if json.Unmarshal(line, &w) != nil {
		return n.unsupported(env, "system/init")
	}
	init := &Init{
		Version:        w.ClaudeCodeVersion,
		SessionID:      w.SessionID,
		Model:          w.Model,
		PermissionMode: w.PermissionMode,
		SlashCommands:  hideTerminalCommands(w.SlashCommands, w.TerminalSlashCommands),
	}
	n.init = init
	return Observation{Init: init}
}

func (n *Normalizer) normalizePermissionDenied(line []byte, env *envelope) Observation {
	var w wirePermissionDenied
	if json.Unmarshal(line, &w) != nil || w.ToolUseID == "" {
		return n.unsupported(env, "system/permission_denied")
	}
	item := n.items[w.ToolUseID]
	// Only a pending tool call can be denied; a denial naming anything else
	// (a plan id, a subagent id, an id never seen) is data about an event the
	// normalizer did not map, and becomes a notice.
	if item == nil || item.Kind != transcript.ToolCall {
		return n.unsupported(env, "system/permission_denied")
	}
	before := *item
	next := bumpRevision(item)
	if meta, ok := next.Metadata.(transcript.ToolCallMetadata); ok {
		meta.Status = toolDenied
		next.Metadata = meta
	}
	*item = next
	return Observation{Updates: []Update{{Before: before, After: next}}}
}

func (n *Normalizer) normalizeResult(line []byte, env *envelope) Observation {
	var w wireResult
	currentTurn := n.currentTurnID()
	if json.Unmarshal(line, &w) != nil || currentTurn == "" {
		return n.unsupported(env, "result")
	}
	usage := UsageData{
		InputTokens:  w.Usage.InputTokens,
		OutputTokens: w.Usage.OutputTokens,
		CacheTokens:  w.Usage.CacheCreationInputTokens + w.Usage.CacheReadInputTokens,
		Cost:         w.TotalCostUSD,
	}
	item := transcript.Item{
		ID:        currentTurn + "-usage",
		SessionID: n.session,
		TurnID:    currentTurn,
		Kind:      transcript.Usage,
		RawRef:    n.rawRef(env),
		Metadata:  transcript.UsageMetadata{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheTokens: usage.CacheTokens, Cost: usage.Cost},
		Content:   transcript.UsageContent{},
	}
	end := &TurnEnd{TurnID: currentTurn, Usage: usage, CredentialExpiresAt: expiryTime(w.CredentialExpiresAt)}
	if w.Subtype != "success" || w.IsError {
		end.Outcome, end.FailureCode = OutcomeFailed, w.Subtype
	} else {
		end.Outcome = OutcomeCompleted
	}
	if n.vendorTurn != "" {
		n.vendorTurn = ""
	} else {
		if n.turnFinished == nil {
			n.turnFinished = map[string]bool{}
		}
		n.turnFinished[currentTurn] = true
		n.turn = ""
	}
	return Observation{Records: []transcript.Item{item}, TurnEnd: end}
}

func expiryTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

func (n *Normalizer) normalizeRateLimit(line []byte, env *envelope) Observation {
	var w wireRateLimit
	if json.Unmarshal(line, &w) != nil {
		return n.unsupported(env, "rate_limit_event")
	}
	info := w.RateLimitInfo
	quota := &RateLimit{}
	if info.UnifiedWindows.FiveHour != nil {
		quota.Windows = append(quota.Windows, window("five_hour", info.UnifiedWindows.FiveHour))
	}
	if info.UnifiedWindows.SevenDay != nil {
		quota.Windows = append(quota.Windows, window("seven_day", info.UnifiedWindows.SevenDay))
	}
	if len(quota.Windows) == 0 {
		quota.Windows = []Window{{
			Name:     info.RateLimitType,
			ResetsAt: time.Unix(info.ResetsAt, 0).UTC(),
			Status:   info.Status,
		}}
	}
	obs := Observation{Quota: quota}
	for _, win := range quota.Windows {
		obs.Records = append(obs.Records, transcript.Item{
			ID:        "quota-" + n.rawRef(env) + "-" + win.Name,
			SessionID: n.session,
			TurnID:    n.currentTurnID(),
			Kind:      transcript.Quota,
			RawRef:    n.rawRef(env),
			// Account stays empty until the quota owner exists (F9.10).
			Metadata: transcript.QuotaMetadata{Window: win.Name, Used: win.Utilization, ResetsAt: win.ResetsAt},
			Content:  transcript.QuotaContent{},
		})
	}
	return obs
}

func window(name string, w *wireWindow) Window {
	return Window{
		Name:        name,
		Utilization: w.Utilization,
		ResetsAt:    time.Unix(w.ResetsAt, 0).UTC(),
		Status:      w.Status,
	}
}

func (n *Normalizer) normalizeStream(line []byte, env *envelope) Observation {
	var w wireStream
	if json.Unmarshal(line, &w) != nil || w.Event.Type == "" {
		return n.unsupported(env, "stream_event")
	}
	if w.Event.Delta == nil {
		return Observation{}
	}
	blockType, fragment := deltaFragment(w.Event.Delta)
	if blockType == "" {
		return Observation{}
	}
	return Observation{Partial: &Partial{
		MessageID: w.APIMessageID,
		BlockType: blockType,
		DeltaType: w.Event.Delta.Type,
		Fragment:  fragment,
	}}
}

// deltaFragment maps a delta to its block type and fragment; lifecycle deltas
// (message_start, block starts and stops, signature fragments) carry no
// display content.
func deltaFragment(d *wireDelta) (blockType, fragment string) {
	switch d.Type {
	case "text_delta":
		return contentTypeText, d.Text
	case "thinking_delta":
		return "thinking", d.Thinking
	case "input_json_delta":
		return "tool_use", d.PartialJSON
	default:
		return "", ""
	}
}

// hideTerminalCommands removes the terminal-only commands from the vendor's
// own list, preserving the vendor's order (vendor-runtime §9).
func hideTerminalCommands(commands, terminal []string) []string {
	if len(terminal) == 0 {
		return commands
	}
	hidden := make(map[string]bool, len(terminal))
	for _, name := range terminal {
		hidden[name] = true
	}
	out := make([]string, 0, len(commands))
	for _, name := range commands {
		if !hidden[name] {
			out = append(out, name)
		}
	}
	return out
}

// bumpRevision clones the item with the next revision for an update.
func bumpRevision(item *transcript.Item) transcript.Item {
	next := *item
	next.Revision++
	return next
}

// noticeText builds the unsupported-event label.
func noticeLabel(env *envelope) string {
	if env.Subtype == "" {
		return env.Type
	}
	return fmt.Sprintf("%s/%s", env.Type, env.Subtype)
}
