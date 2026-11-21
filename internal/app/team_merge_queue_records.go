package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

func reparentedTaskBase(
	ctx context.Context,
	git *gitops.Manager,
	repoDir, teamBranch, taskBranch string,
	tasks []vendorstore.TaskRecord,
) (string, bool, error) {
	for i := range tasks {
		t := &tasks[i]
		if t.HierarchyRole != vendorstore.RoleTeamManager || t.State != vendorstore.TaskStateCancelled {
			continue
		}
		mgrBranch, err := gitops.TaskBranchName(gitTaskID(t.TaskID))
		if err != nil {
			continue
		}
		base, err := git.MergeBase(ctx, repoDir, "refs/heads/"+taskBranch, "refs/heads/"+mgrBranch)
		if err != nil {
			continue
		}
		inTeam, contErr := git.BranchContainsCommit(ctx, repoDir, teamBranch, base)
		if contErr != nil {
			return "", false, boundaryError("check branch contains commit", contErr)
		}
		if !inTeam {
			return base, true, nil
		}
	}
	return "", false, nil
}

func (q *teamMergeQueue) recordConflict(ctx context.Context, teamID, taskID, parentID string, files []string) {
	payload, err := json.Marshal(struct {
		ConflictedFiles []string `json:"conflicted_files"`
	}{ConflictedFiles: files})
	if err != nil {
		slog.WarnContext(ctx, "marshal conflict payload failed", "team_id", teamID, "task_id", taskID)
		return
	}
	mailPayload, err := json.Marshal(mailboxDigestItem{
		TaskID:  taskID,
		State:   "conflict",
		Summary: "integration conflict in files: " + strings.Join(files, ", "),
	})
	if err != nil {
		slog.WarnContext(ctx, "marshal conflict mailbox payload failed", "team_id", teamID, "task_id", taskID)
		return
	}
	if err := q.svc.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || cur.State != vendorstore.TaskStateIntegrating {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not integrating")
		}
		evID, idErr := q.svc.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate conflict event ID", idErr)
		}
		var mb []vendorstore.MailboxEnqueueInput
		if parentID != "" {
			mb = []vendorstore.MailboxEnqueueInput{{RecipientTaskID: parentID, Kind: vendorstore.MailboxKindDigest, Payload: mailPayload}}
		}
		return &vendorstore.AppendTeamEventInput{
			EventID:          evID,
			TaskID:           taskID,
			EventType:        vendorstore.EventTaskIntegrated,
			Outcome:          "conflict",
			ProtectedPayload: payload,
			Mailbox:          mb,
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "record integration conflict failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if q.svc.wake != nil {
		q.svc.wake.Notify()
	}
}

func (q *teamMergeQueue) recordPostRebaseGateFailure(
	ctx context.Context,
	teamID, taskID, sessionID, parentID string,
	verdict ResultGateVerdict,
) {
	if err := q.svc.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || cur.State != vendorstore.TaskStateIntegrating {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not integrating")
		}
		return q.svc.buildCheckEventForState(cur, parentID, verdict)
	}); err != nil {
		slog.WarnContext(ctx, "record post-rebase gate failure failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if !verdict.Bounce {
		q.svc.closeTaskSession(ctx, sessionID)
		q.svc.dispatchAfterChange(ctx, teamID)
	}
	if q.svc.wake != nil {
		q.svc.wake.Notify()
	}
}

func (q *teamMergeQueue) recordIntegrated(
	ctx context.Context,
	teamID, taskID, sessionID, parentID, targetBranch, commitSHA string,
) {
	payload, err := json.Marshal(struct {
		TargetBranch string `json:"target_branch"`
		CommitSHA    string `json:"commit_sha"`
	}{TargetBranch: targetBranch, CommitSHA: commitSHA})
	if err != nil {
		slog.WarnContext(ctx, "marshal integrated payload failed", "team_id", teamID, "task_id", taskID)
		return
	}
	if err := q.svc.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || cur.State != vendorstore.TaskStateIntegrating {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not integrating")
		}
		evID, idErr := q.svc.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate integrated event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID:          evID,
			TaskID:           taskID,
			EventType:        vendorstore.EventTaskIntegrated,
			ProtectedPayload: payload,
			Mailbox:          digestSummaryRows(taskID, parentID, string(vendorstore.TaskStateDone), "", fmt.Sprintf("integrated into %s (%s)", targetBranch, commitSHA), false),
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "record task integrated failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	q.svc.closeTaskSession(ctx, sessionID)
	q.svc.dispatchAfterChange(ctx, teamID)
	if q.svc.wake != nil {
		q.svc.wake.Notify()
	}
}

func resolveTeamRepo(ctx context.Context, workspaces *workspace.Service, team *vendorstore.TeamRecord) (repoDir, repoID string) {
	if workspaces != nil && team.WorkspaceID != "" {
		if ws, err := workspaces.GetWorkspace(ctx, team.WorkspaceID); err == nil && len(ws.Roots) > 0 {
			repoDir = ws.Roots[0].Path
		}
	}
	if canonical, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = canonical
	}
	return repoDir, repoIDFor(repoDir)
}
