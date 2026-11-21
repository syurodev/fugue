package app

// F11.10b content extraction of the team channel (host-api v0.17 §5.3): the
// text one line carries on the wire and in the topic events, extracted from
// the sealed source payload per kind. Model- and user-authored text is data
// on the client side too (OR-13); the escaped rendering of the
// fugue_team_read tool lives in team_channel_service.go.

import (
	"encoding/json"
	"fmt"

	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// channelPayloadParts renders the sealed payload of one line kind as data.
// Unknown payload shapes degrade to one escaped raw block.
func channelPayloadParts(kind string, raw []byte, label string) []string {
	switch kind {
	case vendorstore.ChannelKindPlan:
		return channelPlanParts(raw, label)
	case vendorstore.ChannelKindDecision:
		return channelDecisionParts(raw, label)
	case vendorstore.ChannelKindNote:
		return unmarshalOrRaw(channelNoteParts)(raw, label)
	case vendorstore.ChannelKindQuestion:
		return unmarshalOrRaw(channelQuestionParts)(raw, label)
	case vendorstore.ChannelKindAnswer:
		return unmarshalOrRaw(channelAnswerParts)(raw, label)
	case vendorstore.ChannelKindResult:
		return unmarshalOrRaw(channelResultParts)(raw, label)
	case vendorstore.ChannelKindSend:
		return unmarshalOrRaw(channelSendParts)(raw, label)
	case vendorstore.ChannelKindStall:
		return unmarshalOrRaw(channelStallParts)(raw, label)
	case vendorstore.ChannelKindFailed, vendorstore.ChannelKindTask, vendorstore.ChannelKindApproval:
		return rawBlockParts(raw, label)
	default:
		return rawBlockParts(raw, label)
	}
}

// unmarshalOrRaw wraps a payload renderer: an unparseable payload renders as
// one escaped raw block instead of structured parts.
func unmarshalOrRaw(render func(raw []byte, label string) (parts []string, ok bool)) func([]byte, string) []string {
	return func(raw []byte, label string) []string {
		if parts, ok := render(raw, label); ok {
			return parts
		}
		return rawBlockParts(raw, label)
	}
}

// rawBlockParts renders an unreadable payload as one escaped block.
func rawBlockParts(raw []byte, label string) []string {
	return []string{channelBlock(label, string(raw))}
}

func channelPlanParts(raw []byte, label string) []string {
	var p teams.ProposalInput
	if json.Unmarshal(raw, &p) != nil {
		return rawBlockParts(raw, label)
	}
	parts := []string{"Plan proposal: " + p.Title}
	if p.Brief != "" {
		parts = append(parts, channelBlock(label, p.Brief))
	}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		parts = append(parts, "task "+teams.EscapeDigestText(t.ID)+":")
		parts = append(parts, channelBlock(label, t.Goal))
	}
	return parts
}

func channelDecisionParts(raw []byte, label string) []string {
	var d teamDecisionPayload
	if err := json.Unmarshal(raw, &d); err == nil && d.Decision != "" {
		parts := []string{"The user decided \"" + d.Decision + "\"."}
		if d.DecidedBy != "" {
			parts = append(parts, "Decided by: "+d.DecidedBy+".")
		}
		return parts
	}
	var a teamAmendPayload
	if err := json.Unmarshal(raw, &a); err == nil && (len(a.RoleEdits) > 0 || len(a.AddedTaskIDs) > 0 || a.Budget != nil) {
		return []string{"The user amended the team."}
	}
	return rawBlockParts(raw, label)
}

func channelNoteParts(raw []byte, label string) ([]string, bool) {
	var p teamNotePayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return []string{channelBlock(label, p.Note)}, true
}

func channelQuestionParts(raw []byte, label string) ([]string, bool) {
	var p taskQuestionPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return []string{"to " + p.To, channelBlock(label, p.Question)}, true
}

func channelAnswerParts(raw []byte, label string) ([]string, bool) {
	var p struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return []string{channelBlock(label, p.Answer)}, true
}

func channelResultParts(raw []byte, label string) ([]string, bool) {
	var p taskReportPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	parts := []string{"status " + p.Status}
	if p.Summary != "" {
		parts = append(parts, channelBlock(label, p.Summary))
	}
	return parts, true
}

func channelSendParts(raw []byte, label string) ([]string, bool) {
	var p taskMessagePayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	return []string{"to task " + teams.EscapeDigestText(p.ToTaskID), channelBlock(label, p.Message)}, true
}

func channelStallParts(raw []byte, label string) ([]string, bool) {
	var p taskStalledPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	parts := []string{fmt.Sprintf("silent for %ds waiting_on=%s", p.SilentSeconds, p.WaitingOn)}
	if p.TeamWide {
		parts = append(parts, "team-wide: every task is waiting")
	}
	return parts, true
}

// channelLifecycleContent renders the metadata-only content of the lifecycle
// kinds whose events carry no payload (§6.7).
func channelLifecycleContent(row *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent) string {
	if src == nil {
		return ""
	}
	switch row.Kind {
	case vendorstore.ChannelKindFailed:
		if src.FailureCode != "" {
			return "failed(" + src.FailureCode + ")"
		}
	case vendorstore.ChannelKindTask:
		if src.EventType == vendorstore.EventTaskSuperseded {
			return fmt.Sprintf("attempt %d begins after failed(%s)", retryAttemptAfter(src.TaskID), src.FailureCode)
		}
	}
	return ""
}

// channelPayloadContent renders the content kinds from their sealed payload.
func channelPayloadContent(row *vendorstore.TeamChannelMessage, raw []byte) string {
	switch row.Kind {
	case vendorstore.ChannelKindPlan:
		return planContent(raw)
	case vendorstore.ChannelKindDecision:
		return decisionContent(raw)
	case vendorstore.ChannelKindNote:
		return noteContent(raw)
	case vendorstore.ChannelKindQuestion:
		return questionContent(raw)
	case vendorstore.ChannelKindAnswer:
		return answerContent(raw)
	case vendorstore.ChannelKindResult:
		return resultContent(raw)
	case vendorstore.ChannelKindSend:
		return sendContent(raw)
	case vendorstore.ChannelKindStall:
		return stallContent(raw)
	case vendorstore.ChannelKindApproval:
		return approvalContent(raw)
	default:
		return string(raw)
	}
}

// planContent is the proposal brief.
func planContent(raw []byte) string {
	var p teams.ProposalInput
	if json.Unmarshal(raw, &p) == nil {
		return p.Brief
	}
	return ""
}

// decisionContent is the user's decision text.
func decisionContent(raw []byte) string {
	var d teamDecisionPayload
	if json.Unmarshal(raw, &d) == nil && d.Decision != "" {
		return d.Decision
	}
	var a teamAmendPayload
	if json.Unmarshal(raw, &a) == nil {
		return "the team was amended"
	}
	return ""
}

// noteContent is the team note text.
func noteContent(raw []byte) string {
	var p teamNotePayload
	if json.Unmarshal(raw, &p) == nil {
		return p.Note
	}
	return ""
}

// questionContent is the agent's question text.
func questionContent(raw []byte) string {
	var p taskQuestionPayload
	if json.Unmarshal(raw, &p) == nil {
		return p.Question
	}
	return ""
}

// answerContent is the answer text.
func answerContent(raw []byte) string {
	var p struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal(raw, &p) == nil {
		return p.Answer
	}
	return ""
}

// resultContent is the report summary.
func resultContent(raw []byte) string {
	var p taskReportPayload
	if json.Unmarshal(raw, &p) == nil {
		return p.Summary
	}
	return ""
}

// sendContent is the message of a sibling send or a user post.
func sendContent(raw []byte) string {
	var m taskMessagePayload
	if json.Unmarshal(raw, &m) == nil {
		return m.Message
	}
	var u teamUserPostedPayload
	if json.Unmarshal(raw, &u) == nil {
		return u.Message
	}
	return ""
}

// stallContent is the watchdog's waiting summary.
func stallContent(raw []byte) string {
	var p taskStalledPayload
	if json.Unmarshal(raw, &p) == nil {
		return fmt.Sprintf("silent for %ds waiting_on=%s", p.SilentSeconds, p.WaitingOn)
	}
	return ""
}

// approvalContent is the approval reference: never the request or answer
// content (interaction §7, VR-05).
func approvalContent(raw []byte) string {
	var p taskApprovalRefPayload
	if json.Unmarshal(raw, &p) == nil {
		ref := "approval request " + p.RequestID + " (" + p.Tool + ")"
		if p.Outcome != "" {
			ref += ": " + p.Outcome
		}
		return ref
	}
	return ""
}
