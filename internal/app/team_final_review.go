package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/secrets"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func (s *teamTaskService) validateTeamFinalReviewAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	if decision.Outcome != interaction.OutcomeAnswered {
		return nil
	}
	opt, _, err := decisionAnswerOf(teamDecisionQuestionID, decision)
	if err != nil {
		return err
	}
	if opt != decisionApprove && opt != decisionReject {
		return errcode.New(errcode.InvalidArgument, "final review decision must be approve or reject")
	}
	rec, _, err := s.store.TeamSnapshot(ctx, ticket.Binding.InvocationID)
	if err != nil {
		return boundaryError("read team for final review validation", err)
	}
	if rec.State != vendorstore.TeamStateFinalReview {
		return errcode.New(errcode.FailedPrecondition, "team is not awaiting final review")
	}
	return nil
}

func (s *teamTaskService) applyTeamFinalReviewAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	if decision.Outcome != interaction.OutcomeAnswered {
		return nil
	}
	opt, text, err := decisionAnswerOf(teamDecisionQuestionID, decision)
	if err != nil {
		return err
	}
	teamID := ticket.Binding.InvocationID
	switch opt {
	case decisionApprove:
		if s.mergeQueue == nil {
			return errcode.New(errcode.FailedPrecondition, "merge queue is not configured")
		}
		return s.mergeQueue.EnqueueFinalTeamMerge(ctx, teamID)
	case decisionReject:
		reason := strings.TrimSpace(text)
		if reason == "" {
			reason = "rejected by user"
		}
		return s.rejectFinalReview(ctx, teamID, reason)
	default:
		return errcode.New(errcode.InvalidArgument, "final review decision must be approve or reject")
	}
}

func (s *teamTaskService) rejectFinalReview(ctx context.Context, teamID, reason string) error {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode final review rejection", err)
	}
	if appErr := s.appendTeamEventWithRetry(ctx, teamID, func(cur *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if cur.State != vendorstore.TeamStateFinalReview {
			return nil, errcode.New(errcode.FailedPrecondition, "team is not awaiting final review")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate final review revision event ID", idErr)
		}
		leadID := leadTaskIDOf(tasks)
		return &vendorstore.AppendTeamEventInput{
			EventID:          evID,
			EventType:        vendorstore.EventTeamDecided,
			Outcome:          "revision",
			ProtectedPayload: payload,
			Mailbox:          digestSummaryRows(teamID, leadID, "final_review_rejected", "", reason, false),
		}, nil
	}); appErr != nil {
		return appErr
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return nil
}

func (s *teamTaskService) completeFinalReview(ctx context.Context, teamID, targetBranch, commitSHA string) error {
	payload, err := json.Marshal(struct {
		TargetBranch string `json:"target_branch"`
		CommitSHA    string `json:"commit_sha"`
	}{TargetBranch: targetBranch, CommitSHA: commitSHA})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode final review completion", err)
	}
	for attempt := range maxAppendRetries {
		rec, tasks, snapErr := s.store.TeamSnapshot(ctx, teamID)
		if snapErr != nil {
			return boundaryError("read team for final review completion", snapErr)
		}
		if rec.State != vendorstore.TeamStateFinalReview {
			return errcode.New(errcode.FailedPrecondition, "team is not awaiting final review")
		}
		evs, evErr := s.buildFinalCompleteEvents(tasks, payload)
		if evErr != nil {
			return evErr
		}
		if _, appErr := s.store.AppendTeamEvents(ctx, teamID, rec.LastSeq, evs); appErr != nil {
			if errors.Is(appErr, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
				continue
			}
			return boundaryError("complete final review", appErr)
		}
		return nil
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) buildFinalCompleteEvents(tasks []vendorstore.TaskRecord, payload []byte) ([]*vendorstore.AppendTeamEventInput, error) {
	var evs []*vendorstore.AppendTeamEventInput
	for i := range tasks {
		t := &tasks[i]
		if t.HierarchyRole != vendorstore.RoleLead || teamTaskTerminal(t.State) {
			continue
		}
		leadEvID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate lead settle event ID", idErr)
		}
		evs = append(evs, leadSettleEvent(leadEvID, t))
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate final review complete event ID", idErr)
	}
	evs = append(evs, &vendorstore.AppendTeamEventInput{
		EventID:          evID,
		EventType:        vendorstore.EventTeamDecided,
		Outcome:          "complete",
		ProtectedPayload: payload,
	})
	return evs, nil
}

func leadSettleEvent(eventID string, t *vendorstore.TaskRecord) *vendorstore.AppendTeamEventInput {
	if t.State == vendorstore.TaskStateUnknown {
		return &vendorstore.AppendTeamEventInput{
			EventID: eventID, TaskID: t.TaskID, EventType: vendorstore.EventTaskRecoveryDecided, Outcome: "cancel",
		}
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: eventID, TaskID: t.TaskID, EventType: vendorstore.EventTaskEnded, Outcome: string(vendorstore.TaskStateCancelled),
	}
}

func (q *teamMergeQueue) executeFinalTeamMerge(ctx context.Context, teamID string) {
	rec, tasks, repoDir, repoID, targetBranch, teamBranch, ok := q.resolveFinalMergePaths(ctx, teamID)
	if !ok {
		return
	}
	wt, err := q.svc.git.AddTeamWorktree(ctx, q.svc.dataDir, repoDir, repoID, teamID, teamBranch)
	if err != nil {
		slog.WarnContext(ctx, "add team worktree for final merge failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	defer func() {
		if rmErr := q.svc.git.RemoveTeamWorktree(context.WithoutCancel(ctx), repoDir, q.svc.dataDir, repoID, teamID); rmErr != nil {
			slog.WarnContext(ctx, "remove team worktree failed", "team_id", teamID, "error_code", string(errcode.CodeOf(rmErr)))
		}
	}()

	reb, rebErr := q.svc.git.Rebase(ctx, wt, targetBranch)
	if rebErr != nil {
		slog.WarnContext(ctx, "final merge rebase failed", "team_id", teamID, "error_code", string(errcode.CodeOf(rebErr)))
		return
	}
	if reb.Conflicted {
		q.sendFinalMergeBackToLead(ctx, teamID, "target merge conflict in files: "+strings.Join(reb.ConflictedFiles, ", "))
		return
	}
	if gateReason, passed := q.evaluateFinalTeamGates(ctx, rec, tasks, repoDir, wt, targetBranch, teamBranch); !passed {
		q.sendFinalMergeBackToLead(ctx, teamID, gateReason)
		return
	}
	mergedSHA, ffErr := q.svc.git.FastForwardBranch(ctx, repoDir, targetBranch, teamBranch)
	if ffErr != nil {
		slog.WarnContext(ctx, "final merge fast-forward failed", "team_id", teamID, "error_code", string(errcode.CodeOf(ffErr)))
		return
	}
	if compErr := q.svc.completeFinalReview(ctx, teamID, targetBranch, mergedSHA); compErr != nil {
		slog.WarnContext(ctx, "record final review complete failed", "team_id", teamID, "error_code", string(errcode.CodeOf(compErr)))
	}
}

func (q *teamMergeQueue) resolveFinalMergePaths(
	ctx context.Context,
	teamID string,
) (rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, repoDir, repoID, targetBranch, teamBranch string, ok bool) {
	snap, tasks, err := q.svc.store.TeamSnapshot(ctx, teamID)
	if err != nil || snap.State != vendorstore.TeamStateFinalReview || q.svc.git == nil {
		return nil, nil, "", "", "", "", false
	}
	repoDir, repoID = resolveTeamRepo(ctx, q.svc.workspaces, &snap)
	if repoDir == "" {
		return nil, nil, "", "", "", "", false
	}
	targetBranch, err = q.svc.git.CurrentBranch(ctx, repoDir)
	if err != nil || targetBranch == "" {
		return nil, nil, "", "", "", "", false
	}
	teamBranch, err = gitops.TeamBranchName(teamID)
	if err != nil {
		return nil, nil, "", "", "", "", false
	}
	return &snap, tasks, repoDir, repoID, targetBranch, teamBranch, true
}

func (q *teamMergeQueue) sendFinalMergeBackToLead(ctx context.Context, teamID, reason string) {
	if err := q.svc.rejectFinalReview(ctx, teamID, reason); err != nil {
		slog.WarnContext(ctx, "returning final merge to lead failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
	}
}

func (q *teamMergeQueue) evaluateFinalTeamGates(
	ctx context.Context,
	rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	repoDir, wt, targetBranch, teamBranch string,
) (string, bool) {
	diffRes, err := q.svc.git.DiffBetween(ctx, repoDir, targetBranch, teamBranch, 1<<20)
	if err != nil {
		return "final diff check failed", false
	}
	det := secrets.New(nil, secrets.Options{})
	if len(det.FindIn(diffRes.Text, teamBranch)) > 0 {
		return "final diff failed secret scan", false
	}
	gate, ok := q.svc.resultGate.(*hostResultGate)
	if !ok {
		return "", true
	}
	for i := range tasks {
		if tasks[i].HierarchyRole != vendorstore.RoleLead {
			continue
		}
		spec, specErr := gate.openSpec(ctx, rec.TeamID, tasks[i].TaskID)
		if specErr != nil || len(spec.Checks) == 0 {
			return "", true
		}
		verdict, done, chkErr := gate.runChecks(ctx, rec, &tasks[i], spec.Checks, wt)
		if chkErr != nil {
			return fmt.Sprintf("final checks failed (%s)", errcode.CodeOf(chkErr)), false
		}
		if done && !verdict.Passed {
			return verdict.Summary, false
		}
		return "", true
	}
	return "", true
}

func leadTaskIDOf(tasks []vendorstore.TaskRecord) string {
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			return tasks[i].TaskID
		}
	}
	return ""
}
