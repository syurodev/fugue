package app

// Mapping between the neutral transcript item model and the persisted vendor
// store projection: metadata projection, typed-content fit before JSON
// encoding (TR-09) and typed-content decoding for the redacted reads.
// Owner: application layer. Allowed callers: none outside this package.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// viewKindFor maps an item kind to its display limit (transcript §7): tool
// output keeps head and tail, diffs fold per file, everything else is text.
func viewKindFor(kind transcript.Kind) transcript.ViewKind {
	switch kind {
	case transcript.ToolCall:
		return transcript.ViewToolOutput
	case transcript.FileChange:
		return transcript.ViewDiff
	case transcript.UserMessage, transcript.AssistantMessage, transcript.Reasoning,
		transcript.Approval, transcript.Plan, transcript.Subagent, transcript.Command,
		transcript.TeamProposal, transcript.TaskLink, transcript.Usage, transcript.Quota,
		transcript.Notice:
		return transcript.ViewText
	default:
		return transcript.ViewText
	}
}

// storeMetadata projects the typed metadata onto the persisted allowlist
// (INV-09): enums, counts, IDs and timestamps only. The type switch is split
// in two to stay within the complexity gate.
func storeMetadata(m transcript.Metadata) vendorstore.ItemMetadata {
	switch v := m.(type) {
	case transcript.UserMessageMetadata:
		return vendorstore.ItemMetadata{Origin: v.Origin}
	case transcript.AssistantMessageMetadata:
		return vendorstore.ItemMetadata{Model: v.ModelID}
	case transcript.ReasoningMetadata:
		return vendorstore.ItemMetadata{ReasoningState: v.Status}
	case transcript.ToolCallMetadata:
		return vendorstore.ItemMetadata{Tool: v.ToolName, Status: v.Status, ExitCode: v.ExitCode, DurationMillis: v.Duration.Milliseconds()}
	case transcript.FileChangeMetadata:
		return vendorstore.ItemMetadata{ChangeKind: v.Change, LinesAdded: int64(v.Added), LinesRemoved: int64(v.Removed)}
	case transcript.ApprovalMetadata:
		return vendorstore.ItemMetadata{RequestKind: v.RequestType, Decision: v.Decision, Decider: v.Decider, Receipt: v.Receipt}
	case transcript.PlanMetadata:
		return vendorstore.ItemMetadata{StepCount: int64(v.Steps), CurrentStep: int64(v.Current)}
	default:
		return storeMetadataRest(m)
	}
}

func storeMetadataRest(m transcript.Metadata) vendorstore.ItemMetadata {
	switch v := m.(type) {
	case transcript.SubagentMetadata:
		return vendorstore.ItemMetadata{Status: v.Status}
	case transcript.CommandMetadata:
		return vendorstore.ItemMetadata{Source: v.Source}
	case transcript.TeamProposalMetadata:
		return vendorstore.ItemMetadata{ProposalID: v.ProposalID, AgentCount: int64(v.Agents), Decision: v.Decision}
	case transcript.TaskLinkMetadata:
		return vendorstore.ItemMetadata{TaskID: v.TaskID, Agent: v.AgentID, Status: v.Status}
	case transcript.UsageMetadata:
		return vendorstore.ItemMetadata{InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, CacheTokens: v.CacheTokens, Cost: v.Cost}
	case transcript.QuotaMetadata:
		return vendorstore.ItemMetadata{Account: v.AccountID, Window: v.Window, Used: v.Used, ResetAt: v.ResetsAt.UTC().Format(time.RFC3339Nano)}
	case transcript.NoticeMetadata:
		return vendorstore.ItemMetadata{Level: v.Level, Code: v.Code}
	default:
		return vendorstore.ItemMetadata{}
	}
}

// decodeJSON decodes stored JSON; the wrap satisfies the boundary rule once
// for every decoder.
func decodeJSON(data []byte, target any) error {
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode the stored transcript content: %w", err)
	}
	return nil
}

// decodeContent decodes the stored content JSON for one item kind. The
// decoder table is built by two helpers to stay within the function-size
// gate; usage and quota items carry no content.
func decodeContent(kind transcript.Kind, data []byte) (transcript.Content, error) {
	decoders := messageContentDecoders()
	for kindKey, decode := range itemContentDecoders() {
		decoders[kindKey] = decode
	}
	decode, ok := decoders[kind]
	if !ok {
		return nil, errcode.New(errcode.Internal, "stored transcript content has an unknown kind")
	}
	return decode(data)
}

// decodeAs decodes one call's JSON into a fresh copy of the typed content.
func decodeAs[T transcript.Content](target T) func([]byte) (transcript.Content, error) {
	return func(data []byte) (transcript.Content, error) {
		content := target
		if err := decodeJSON(data, &content); err != nil {
			return nil, err
		}
		return content, nil
	}
}

// messageContentDecoders decodes the content of the message-shaped items.
func messageContentDecoders() map[transcript.Kind]func([]byte) (transcript.Content, error) {
	return map[transcript.Kind]func([]byte) (transcript.Content, error){
		transcript.UserMessage:      decodeAs(transcript.UserMessageContent{}),
		transcript.AssistantMessage: decodeAs(transcript.AssistantMessageContent{}),
		transcript.Reasoning:        decodeAs(transcript.ReasoningContent{}),
		transcript.ToolCall:         decodeAs(transcript.ToolCallContent{}),
		transcript.FileChange:       decodeAs(transcript.FileChangeContent{}),
		transcript.Approval:         decodeAs(transcript.ApprovalContent{}),
		transcript.Plan:             decodeAs(transcript.PlanContent{}),
	}
}

// itemContentDecoders decodes the content of the remaining item kinds.
func itemContentDecoders() map[transcript.Kind]func([]byte) (transcript.Content, error) {
	return map[transcript.Kind]func([]byte) (transcript.Content, error){
		transcript.Subagent:     decodeAs(transcript.SubagentContent{}),
		transcript.Command:      decodeAs(transcript.CommandContent{}),
		transcript.TeamProposal: decodeAs(transcript.TeamProposalContent{}),
		transcript.TaskLink:     decodeAs(transcript.TaskLinkContent{}),
		transcript.Notice:       decodeAs(transcript.NoticeContent{}),
		transcript.Usage: func([]byte) (transcript.Content, error) {
			return transcript.UsageContent{}, nil
		},
		transcript.Quota: func([]byte) (transcript.Content, error) {
			return transcript.QuotaContent{}, nil
		},
	}
}

// contentStrings returns the content's string fields that can grow large, as
// pointers into a working copy, plus a function that boxes that copy as it is
// at call time — boxing a value copies it, so the caller mutates the fields
// first and marshals the boxed result after. The caller's content is never
// mutated. The write path fits the largest field to the payload limit before
// JSON encoding, never the serialized bytes (TR-09); list fields stay whole
// and their vendor normalizers bound them (F9.5).
func contentStrings(content transcript.Content) ([]*string, func() transcript.Content) {
	switch v := content.(type) {
	case transcript.UserMessageContent:
		return []*string{&v.Text}, func() transcript.Content { return v }
	case transcript.AssistantMessageContent:
		return []*string{&v.Text}, func() transcript.Content { return v }
	case transcript.ReasoningContent:
		return []*string{&v.Text}, func() transcript.Content { return v }
	case transcript.ToolCallContent:
		return []*string{&v.Title, &v.Input, &v.Output}, func() transcript.Content { return v }
	case transcript.FileChangeContent:
		return []*string{&v.Path, &v.OldPath, &v.Diff}, func() transcript.Content { return v }
	case transcript.ApprovalContent:
		return []*string{&v.Request}, func() transcript.Content { return v }
	case transcript.SubagentContent:
		return []*string{&v.Name, &v.Description, &v.Result}, func() transcript.Content { return v }
	case transcript.TeamProposalContent:
		return []*string{&v.Plan}, func() transcript.Content { return v }
	case transcript.TaskLinkContent:
		return []*string{&v.Summary}, func() transcript.Content { return v }
	case transcript.NoticeContent:
		return []*string{&v.Text}, func() transcript.Content { return v }
	default:
		return nil, func() transcript.Content { return content }
	}
}

// maxFitRounds bounds the fit convergence: every round shrinks the largest
// remaining field, so a small typed content reaches the limit in a few rounds.
const maxFitRounds = 64

// fitContent fits the typed content's largest string field to limit before
// JSON encoding and reports the fitted encoding, whether anything was cut, the
// original size of the largest field when it was, and the fitted display
// field the views are built from.
func fitContent(content transcript.Content, limit int) (data []byte, truncated bool, originalSize int, display string, err error) {
	if content == nil {
		return nil, false, 0, "", nil
	}
	fields, box := contentStrings(content)
	for _, p := range fields {
		originalSize = max(originalSize, len(*p))
	}
	for range maxFitRounds {
		if data, err = json.Marshal(box()); err != nil {
			return nil, false, 0, "", errcode.Wrap(errcode.Internal, "encode transcript content", err)
		}
		if string(data) == "{}" {
			// A content that encodes to an empty object carries nothing
			// protected: no payload row, and the event stays metadata-only.
			return nil, false, 0, "", nil
		}
		if len(data) <= limit {
			return data, truncated, sizeWhen(truncated, originalSize), fittedDisplay(fields), nil
		}
		if !shrinkLargest(fields, len(data), limit) {
			break
		}
		truncated = true
	}
	return nil, false, 0, "", errcode.New(errcode.InvalidArgument, "transcript content cannot fit the payload limit")
}

// shrinkLargest fits the largest remaining field tighter by the overshoot
// ratio. It reports whether any field could still shrink.
func shrinkLargest(fields []*string, dataSize, limit int) bool {
	field := largest(fields)
	if field == nil || len(*field) <= 1 {
		return false
	}
	shrink := len(*field) * limit / dataSize
	if shrink >= len(*field) {
		shrink = len(*field) - 1
	}
	fitted, _, _ := transcript.FitStorage([]byte(*field), shrink)
	*field = string(fitted)
	return true
}

// fittedDisplay is the largest field after fitting: the text the views are
// built from.
func fittedDisplay(fields []*string) string {
	if field := largest(fields); field != nil {
		return *field
	}
	return ""
}

// fitRaw fits the raw vendor event to the payload limit (TR-09).
func fitRaw(raw []byte) ([]byte, bool, int) {
	fitted, truncated, original := transcript.FitStorage(raw, sealing.MaxPayloadPlaintext)
	return fitted, truncated, sizeWhen(truncated, original)
}

func sizeWhen(truncated bool, size int) int {
	if truncated {
		return size
	}
	return 0
}

// largest returns the longest field, preferring the later field on ties so the
// tool output wins over its input when both fill the limit.
func largest(fields []*string) *string {
	var best *string
	for _, p := range fields {
		if p != nil && (best == nil || len(*p) >= len(*best)) {
			best = p
		}
	}
	return best
}

// redactContent masks every string field of the typed content on the decoded
// copy: detection runs on each full field before any shortening (TR-10).
func redactContent(content transcript.Content, redactor transcript.Redactor) transcript.Content {
	fields, box := contentStrings(content)
	for _, p := range fields {
		*p = redactor.Redact(*p)
	}
	return box()
}
