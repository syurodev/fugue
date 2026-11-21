package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const teamFinalReviewToolName = "fugue_team_final_review"

type teamFinalReviewResolver interface {
	validateTeamFinalReviewAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
	applyTeamFinalReviewAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
}

// SetTeamFinalReviewResolver wires the final user review card consumer.
func (s *InteractionService) SetTeamFinalReviewResolver(r teamFinalReviewResolver) {
	s.teamFinalReviews = r
}

func isTeamFinalReviewCard(ticket *interaction.Ticket) bool {
	return ticket != nil && ticket.Binding.ToolName == teamFinalReviewToolName
}

// maybeTriggerFinalReview checks whether an active team has finished all
// non-lead tasks with unmerged commits on its team branch; if so, it moves
// the team to TeamStateFinalReview and raises one final user review card (§9).
func (s *teamTaskService) maybeTriggerFinalReview(ctx context.Context, teamID string) {
	rec, tasks, repoDir, targetBranch, teamBranch, ok := s.readyForFinalReview(ctx, teamID)
	if !ok {
		return
	}
	payload, err := json.Marshal(struct {
		TargetBranch string `json:"target_branch"`
		TeamBranch   string `json:"team_branch"`
	}{TargetBranch: targetBranch, TeamBranch: teamBranch})
	if err != nil {
		return
	}
	if appErr := s.appendTeamEventWithRetry(ctx, teamID, func(cur *vendorstore.TeamRecord, curTasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if cur.State != vendorstore.TeamStateActive || !allNonLeadTasksTerminal(curTasks) {
			return nil, errcode.New(errcode.FailedPrecondition, "team not ready for final review")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate final review event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, EventType: vendorstore.EventTeamFinalReview, ProtectedPayload: payload,
		}, nil
	}); appErr != nil {
		return
	}
	rec.State = vendorstore.TeamStateFinalReview
	if cardErr := s.ensureFinalReviewCard(ctx, rec, tasks, repoDir, targetBranch, teamBranch); cardErr != nil {
		slog.WarnContext(ctx, "raising final review card failed", "team_id", teamID, "error_code", string(errcode.CodeOf(cardErr)))
	}
}

func (s *teamTaskService) readyForFinalReview(
	ctx context.Context,
	teamID string,
) (rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, repoDir, targetBranch, teamBranch string, ok bool) {
	if s.git == nil || s.interactions == nil || s.interactions.owner == nil {
		return nil, nil, "", "", "", false
	}
	snap, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil || snap.State != vendorstore.TeamStateActive || !allNonLeadTasksTerminal(tasks) {
		return nil, nil, "", "", "", false
	}
	repoDir, targetBranch, teamBranch, ok = s.resolveFinalReviewBranches(ctx, &snap)
	if !ok {
		return nil, nil, "", "", "", false
	}
	return &snap, tasks, repoDir, targetBranch, teamBranch, true
}

func (s *teamTaskService) resolveFinalReviewBranches(
	ctx context.Context,
	snap *vendorstore.TeamRecord,
) (repoDir, targetBranch, teamBranch string, ok bool) {
	repoDir, _ = resolveTeamRepo(ctx, s.workspaces, snap)
	if repoDir == "" {
		return "", "", "", false
	}
	targetBranch, err := s.git.CurrentBranch(ctx, repoDir)
	teamBranch, tbErr := gitops.TeamBranchName(snap.TeamID)
	if err != nil || tbErr != nil || targetBranch == "" {
		return "", "", "", false
	}
	unmerged, err := s.git.HasUnmergedCommits(ctx, repoDir, targetBranch, teamBranch)
	if err != nil || !unmerged {
		return "", "", "", false
	}
	return repoDir, targetBranch, teamBranch, true
}

func allNonLeadTasksTerminal(tasks []vendorstore.TaskRecord) bool {
	hasChild := false
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			continue
		}
		hasChild = true
		if !teamTaskTerminal(tasks[i].State) {
			return false
		}
	}
	return hasChild
}

func (s *teamTaskService) ensureFinalReviewCard(
	ctx context.Context,
	rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	repoDir, targetBranch, teamBranch string,
) error {
	if s.interactions == nil || s.interactions.owner == nil {
		return nil
	}
	spec, err := s.finalReviewCardSpec(ctx, rec, tasks, repoDir, targetBranch, teamBranch)
	if err != nil {
		return err
	}
	return ensureRebuildableCard(s.interactions, spec)
}

func (s *teamTaskService) finalReviewCardSpec(
	ctx context.Context,
	rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	repoDir, targetBranch, teamBranch string,
) (*interaction.Spec, error) {
	opID, err := interaction.NativeOperationKey(rec.TeamID, rec.TeamID, 2)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "key final review card", err)
	}
	body := s.buildFinalReviewBody(ctx, rec.TeamID, tasks, repoDir, targetBranch, teamBranch)
	return &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindQuestion,
		Summary:     truncateCardText("Final review: team "+rec.TeamID, interaction.MaxSummaryBytes),
		Body:        truncateCardText(body, interaction.MaxPromptBytes),
		Questions: []interaction.Question{{
			ID:       teamDecisionQuestionID,
			Question: "Approve merging the team branch into the workspace target branch?",
			Header:   "final review",
			Options: []interaction.Option{
				{ID: decisionApprove, Label: cardOptionApproveLabel, Description: "Rebase onto latest target, rerun checks, and fast-forward merge"},
				{ID: decisionReject, Label: cardOptionRejectLabel, Description: "Return to the lead with your reason; target branch stays untouched"},
			},
		}},
		Binding: interaction.Binding{
			SessionID:       rec.SessionID,
			WorkspaceID:     rec.WorkspaceID,
			InvocationID:    rec.TeamID,
			AttemptID:       rec.TeamID,
			CheckGeneration: 2,
			ToolName:        teamFinalReviewToolName,
		},
	}, nil
}

func (s *teamTaskService) buildFinalReviewBody(
	ctx context.Context,
	teamID string,
	tasks []vendorstore.TaskRecord,
	repoDir, targetBranch, teamBranch string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target: %s <- %s\n\nBranches:\n", targetBranch, teamBranch)
	sorted := slices.Clone(tasks)
	slices.SortFunc(sorted, func(a, c vendorstore.TaskRecord) int { return strings.Compare(a.TaskID, c.TaskID) })
	for i := range sorted {
		t := &sorted[i]
		if t.HierarchyRole == vendorstore.RoleLead {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", t.TaskID, t.CatalogRole, t.State)
	}
	covLine := s.aggregateCrossReviewSummary(ctx, teamID, sorted)
	if covLine != "" {
		fmt.Fprintf(&b, "\nCross review:\n%s\n", covLine)
	}
	diffRes, err := s.git.DiffBetween(ctx, repoDir, targetBranch, teamBranch, interaction.MaxPromptBytes)
	if err != nil {
		fmt.Fprintf(&b, "\nDiff unavailable (%s).\n", errcode.CodeOf(err))
		return b.String()
	}
	header := fmt.Sprintf("\nDiff (%d bytes):\n", diffRes.TotalBytes)
	// Link to the F10.4e diff viewer once it lands.
	if diffRes.HasMore || b.Len()+len(header)+len(diffRes.Text) > interaction.MaxPromptBytes {
		fmt.Fprintf(&b, "\nFull diff (%d bytes) exceeds card limit; inspect `git diff %s..%s`.\n",
			diffRes.TotalBytes, targetBranch, teamBranch)
		return b.String()
	}
	b.WriteString(header)
	b.WriteString(diffRes.Text)
	return b.String()
}

func (s *teamTaskService) aggregateCrossReviewSummary(ctx context.Context, teamID string, tasks []vendorstore.TaskRecord) string {
	var reviewed, total, droppedLow, droppedPos int
	var modes []string
	var skips []string
	var findings int
	seenMode := make(map[string]bool)
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			continue
		}
		raw, found, err := s.store.OpenLatestTaskEventPayload(ctx, teamID, tasks[i].TaskID, vendorstore.EventTaskCheckCompleted)
		if err != nil || !found || len(raw) == 0 {
			continue
		}
		var cr CrossReviewReport
		if json.Unmarshal(raw, &cr) != nil || cr.Coverage.ModeLabel == "" {
			continue
		}
		reviewed += cr.Coverage.ReviewedFiles
		total += cr.Coverage.TotalFiles
		droppedLow += cr.Coverage.DroppedLowCount
		droppedPos += cr.Coverage.DroppedPositionCount
		findings += len(cr.Findings)
		if !seenMode[cr.Coverage.ModeLabel] {
			seenMode[cr.Coverage.ModeLabel] = true
			modes = append(modes, cr.Coverage.ModeLabel)
		}
		skips = append(skips, cr.Coverage.SkipReasons...)
	}
	if len(modes) == 0 {
		return ""
	}
	cov := CrossReviewCoverage{
		ReviewedFiles:        reviewed,
		TotalFiles:           total,
		ModeLabel:            strings.Join(modes, "+"),
		SkipReasons:          skips,
		DroppedLowCount:      droppedLow,
		DroppedPositionCount: droppedPos,
	}
	return fmt.Sprintf("%s; %d finding(s)", cov.Format(), findings)
}

func (s *teamTaskService) appendTeamEventWithRetry(
	ctx context.Context,
	teamID string,
	build func(*vendorstore.TeamRecord, []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error),
) error {
	for attempt := range maxAppendRetries {
		rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
		if err != nil {
			return boundaryError("read team for event append", err)
		}
		ev, err := build(&rec, tasks)
		if err != nil {
			return err
		}
		if _, appErr := s.store.AppendTeamEvents(ctx, teamID, rec.LastSeq, []*vendorstore.AppendTeamEventInput{ev}); appErr != nil {
			if errors.Is(appErr, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
				continue
			}
			return boundaryError("append team event", appErr)
		}
		return nil
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}
