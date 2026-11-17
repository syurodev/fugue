package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// cardTruncationMarker ends a card field that outgrew its §9 limit. The full
// content always stays in the transcript item.
const cardTruncationMarker = "\n…(truncated; the full plan is in the transcript)"

// proposalDecisionOptions are the fixed choices of the proposal card (§5.3).
// Labels are Fugue's own words, never model content.
func proposalDecisionOptions() []interaction.Option {
	return []interaction.Option{
		{ID: "approve", Label: "Approve", Description: "Activate the team and queue its root tasks (F11.2b applies the decision)"},
		{ID: "approve_edited", Label: "Approve with edits", Description: "Apply your edits and activate the team"},
		{ID: "solo", Label: "Work solo", Description: "Keep the session solo"},
		{ID: "reject", Label: "Reject", Description: "Reject the proposal"},
	}
}

// recordProposalItem records the team_proposal item through the session's
// single-writer actor, so the item's sequence stays contiguous with the
// actor's journal.
func (s *TeamProposalService) recordProposalItem(ctx context.Context, sessionID string,
	teamID string, input *teams.ProposalInput,
) error {
	turnID, err := s.recorder.ActiveTurnID(ctx, sessionID)
	if err != nil {
		return boundaryError("read the active turn", err)
	}
	itemID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate the proposal item ID", err)
	}
	content, err := json.Marshal(transcript.TeamProposalContent{Plan: proposalPlanText(input)})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal the proposal plan", err)
	}
	err = s.recorder.RecordEvent(ctx, sessionID, &vendorstore.Event{
		Type: vendorstore.TranscriptItemRecorded, TurnID: turnID,
		Item: &vendorstore.Item{
			ID: itemID, TurnID: turnID, Kind: vendorstore.KindTeamProposal,
			Metadata: vendorstore.ItemMetadata{
				ProposalID: teamID, AgentCount: int64(teams.AgentCount(input)), Decision: teamPendingDecision,
			},
			Content: content,
		},
	})
	return boundaryError("record the proposal item", err)
}

// ensureCard creates the proposal's inbox card: one question with the four
// §5.3 choices (F11.2b consumes the answer). The operation key is derived
// from the team ID, so supersede can invalidate the exact card (Q-103).
func (s *TeamProposalService) ensureCard(ctx context.Context, snap *vendorstore.Session, teamID string,
	input *teams.ProposalInput, warnings []teams.Warning, eff *workspace.EffectiveConfig,
) error {
	lines, err := s.proposalCardLines(ctx, input, warnings, eff)
	if err != nil {
		return err
	}
	operation, err := interaction.NativeOperationKey(teamID, teamID, 1)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "key the proposal card", err)
	}
	spec := &interaction.Spec{
		OperationID: operation, Kind: interaction.KindQuestion,
		Summary: truncateCardText(input.Title, interaction.MaxSummaryBytes),
		Body:    truncateCardText(strings.Join(lines, "\n"), interaction.MaxPromptBytes),
		Questions: []interaction.Question{{
			ID: "decision", Question: "How should this team proposal proceed?", Header: "team proposal",
			Options: proposalDecisionOptions(),
		}},
		Binding: interaction.Binding{
			SessionID: snap.ID, WorkspaceID: snap.WorkspaceID,
			InvocationID: teamID, AttemptID: teamID, CheckGeneration: 1,
			ToolName: teamProposeToolName,
		},
	}
	if _, err := s.interactions.Ensure(spec); err != nil {
		return err
	}
	return nil
}

// proposalCardLines renders the card body: what will run, the §5.3 account
// and usage lines, and the warnings. The brief and goals stay in the
// transcript item's sealed plan.
func (s *TeamProposalService) proposalCardLines(ctx context.Context, input *teams.ProposalInput,
	warnings []teams.Warning, eff *workspace.EffectiveConfig,
) ([]string, error) {
	profiles := proposalRoleProfiles(input, eff)
	lines := []string{
		input.Title,
		"agents: " + strconv.Itoa(teams.AgentCount(input)) + "; accounts: unknown (the account service is not wired yet)",
		"roles:",
	}
	seen := make(map[string]struct{}, len(profiles))
	for i := range input.Tasks {
		name := input.Tasks[i].Role
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		p := profiles[name]
		line, err := usageEstimateLine(ctx, s.usage, name, p.vendor, p.model)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	if len(warnings) > 0 {
		lines = append(lines, "warnings:")
		for i := range warnings {
			lines = append(lines, "- "+warnings[i].Code+": "+warnings[i].Detail)
		}
	}
	return lines, nil
}

// proposalRoleProfiles resolves each task role's vendor and model: catalog
// definitions win over proposed entries of the same name (§5.2).
func proposalRoleProfiles(input *teams.ProposalInput, eff *workspace.EffectiveConfig) map[string]roleProfile {
	out := make(map[string]roleProfile, len(input.Roles)+len(eff.Roles))
	for i := range eff.Roles {
		r := &eff.Roles[i]
		out[r.Name] = roleProfile{vendor: r.Vendor, model: r.Model}
	}
	for i := range input.Roles {
		p := &input.Roles[i]
		if _, known := out[p.Name]; !known {
			out[p.Name] = roleProfile{vendor: string(p.Vendor), model: p.Model}
		}
	}
	return out
}

type roleProfile struct{ vendor, model string }

// proposalPlanText renders the proposal as the transcript item's protected
// plan content. Everything in it is untrusted proposal data (§13).
func proposalPlanText(input *teams.ProposalInput) string {
	var b strings.Builder
	b.WriteString("# " + input.Title + "\n\n" + input.Brief + "\n\n## roles\n")
	for i := range input.Roles {
		p := &input.Roles[i]
		b.WriteString("- " + p.Name + ": " + string(p.Vendor) + " " + p.Model + " " + string(p.Permission) + "\n")
	}
	b.WriteString("\n## tasks\n")
	for i := range input.Tasks {
		t := &input.Tasks[i]
		b.WriteString("- " + t.ID + " [" + string(t.TeamRole) + "] role=" + t.Role +
			" deliverable=" + string(t.Deliverable) + " scope=" + strings.Join(t.WriteScope, ",") + "\n  " + t.Goal + "\n")
	}
	return b.String()
}

// truncateCardText cuts content at a UTF-8 boundary and marks the cut.
func truncateCardText(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	cut := limit - len(cardTruncationMarker)
	if cut <= 0 {
		return cardTruncationMarker
	}
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + cardTruncationMarker
}
