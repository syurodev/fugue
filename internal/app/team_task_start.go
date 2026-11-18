package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// taskLaunch is everything a child task needs to start, read before any
// effect so that a task that cannot start fails while still queued.
type taskLaunch struct {
	spec       teams.ProtectedTaskContent
	brief      string
	req        taskSessionRequest
	repoDir    string
	repoID     string
	parentRef  string
	headCommit string
}

// startChild starts the vendor session of one child task in its own worktree
// (Q-119). Reading comes first and fails the still-queued task; once the
// assignment is recorded every failure ends the task failed with a stable
// code, and a crash leaves it `starting` for recovery (never re-run).
func (d *teamDispatcher) startChild(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) error {
	launch, code, err := d.prepareLaunch(ctx, rec, task, tasks)
	if err != nil {
		return err
	}
	if code != "" {
		return d.failTask(ctx, rec.TeamID, task.TaskID, code)
	}
	seq, err := d.appendTaskEvent(ctx, rec.TeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: task.TaskID, EventType: vendorstore.EventTaskAssigned, SessionID: launch.req.SessionID,
	})
	if err != nil {
		return err
	}
	cwd, err := d.createWorktree(ctx, rec.TeamID, task, launch)
	if err != nil {
		slog.WarnContext(ctx, "creating the task worktree failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return d.failTask(ctx, rec.TeamID, task.TaskID, failWorktree)
	}
	launch.req.Cwd = cwd
	if err := d.deps.Sessions.StartTaskSession(ctx, &launch.req); err != nil {
		slog.WarnContext(ctx, "starting the task session failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		d.removeWorktree(ctx, launch, task.TaskID)
		return d.failTask(ctx, rec.TeamID, task.TaskID, failSessionStart)
	}
	if _, err := d.appendTaskEvent(ctx, rec.TeamID, seq, &vendorstore.AppendTeamEventInput{
		TaskID: task.TaskID, EventType: vendorstore.EventTaskStarted,
	}); err != nil {
		d.closeTaskSession(ctx, launch.req.SessionID)
		return err
	}
	if err := d.deps.Sessions.Send(ctx, launch.req.SessionID, vendor.Input{Text: taskIntro(task, &launch.spec, launch.brief)}); err != nil {
		d.closeTaskSession(ctx, launch.req.SessionID)
		return d.failTask(ctx, rec.TeamID, task.TaskID, failIntro)
	}
	return nil
}

// prepareLaunch reads the sealed spec, the decided profile and the repository
// of the task. A non-empty code means the task cannot start and must fail.
func (d *teamDispatcher) prepareLaunch(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (*taskLaunch, string, error) {
	launch := &taskLaunch{}
	raw, err := d.deps.Store.OpenTaskSpec(ctx, rec.TeamID, task.TaskID)
	if err != nil {
		return nil, "", boundaryError("open the task spec", err)
	}
	if !json.Valid(raw) {
		return nil, failSpecUnreadable, nil
	}
	if decodeErr := json.Unmarshal(raw, &launch.spec); decodeErr != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "decode the task spec", decodeErr)
	}
	role, found, err := d.decidedRoleFor(ctx, rec.TeamID, task.CatalogRole)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, failProfile, nil
	}
	if p := launch.spec.Profile; p != nil {
		overlayProfile(&role, p)
	}
	lead, err := d.deps.Store.Snapshot(ctx, rec.SessionID)
	if err != nil {
		return nil, "", boundaryError("read the lead session", err)
	}
	if code := d.resolveRepo(ctx, launch, &lead, task, tasks); code != "" {
		return nil, code, nil
	}
	sessionID, idErr := d.deps.IDs.NewID()
	if idErr != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "allocate the task session ID", idErr)
	}
	if brief, ok := d.proposalBrief(ctx, rec.TeamID); ok {
		launch.brief = brief
	}
	launch.req = taskSessionRequest{
		SessionID: sessionID, Vendor: role.Vendor, Model: role.Model, Effort: role.Effort,
		WorkspaceID: lead.WorkspaceID, RootName: lead.WorkspaceRoot, TeamID: rec.TeamID, TaskID: task.TaskID,
		Role: task.HierarchyRole, ApprovalMode: approvalModeFor(role.Permission),
	}
	return launch, "", nil
}

// overlayProfile applies a task's profile override over its decided role.
func overlayProfile(role *decidedRole, p *teams.ProfileOverride) {
	if p.Vendor != "" {
		role.Vendor = string(p.Vendor)
	}
	if p.Model != "" {
		role.Model = p.Model
	}
	if p.Effort != "" {
		role.Effort = string(p.Effort)
	}
}

// approvalModeFor maps a role's permission to the Fugue approval mode of its
// session (Q-120): read roles plan, write roles ask; never allow-always.
func approvalModeFor(permission string) string {
	if permission == string(teams.PermissionWrite) {
		return vendorstore.ApprovalAsk
	}
	return vendorstore.ApprovalPlan
}

// resolveRepo derives the repository of the team from the lead session's
// workspace root and the branch the task starts from (Q-119). A root that is
// not a git repository, or a missing git binary, is a worktree failure.
func (d *teamDispatcher) resolveRepo(ctx context.Context, launch *taskLaunch, lead *vendorstore.Session, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) string {
	if d.deps.Git == nil {
		return failWorktree
	}
	dir, err := d.deps.Roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return worktreeFailure(ctx, err)
	}
	head, err := d.deps.Git.WorktreeHead(ctx, dir)
	if err != nil || head == "" {
		return worktreeFailure(ctx, err)
	}
	launch.repoDir, launch.headCommit = dir, head
	sum := sha256.Sum256([]byte(dir))
	launch.repoID = "r" + hex.EncodeToString(sum[:8])
	parent, err := parentBranch(task, tasks)
	if err != nil {
		return worktreeFailure(ctx, err)
	}
	launch.parentRef = parent
	return ""
}

// worktreeFailure logs why the repository could not be used and returns the
// failure code: a repository problem fails the task, never the dispatcher.
func worktreeFailure(ctx context.Context, err error) string {
	slog.WarnContext(ctx, "the task repository is not usable", "error_code", string(errcode.CodeOf(err)))
	return failWorktree
}

// parentBranch names the branch a task starts from: the team branch for a
// direct child of the lead, the manager's task branch for a member of a
// manager (§9).
func parentBranch(task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (string, error) {
	for i := range tasks {
		if tasks[i].TaskID == task.ParentTaskID && tasks[i].HierarchyRole == vendorstore.RoleTeamManager {
			return wrapGit(gitops.TaskBranchName(tasks[i].TaskID))
		}
	}
	return wrapGit(gitops.TeamBranchName(task.TeamID))
}

// wrapGit carries a gitops result across the boundary with its code.
func wrapGit(value string, err error) (string, error) {
	if err != nil {
		return "", boundaryError("derive the task branch", err)
	}
	return value, nil
}

// createWorktree makes the team branch when it does not exist yet, then the
// task worktree, and returns its host-derived path.
func (d *teamDispatcher) createWorktree(ctx context.Context, teamID string, task *vendorstore.TaskRecord, launch *taskLaunch) (string, error) {
	git := d.deps.Git
	if err := git.CreateTeamBranch(ctx, launch.repoDir, teamID, launch.headCommit); err != nil && errcode.CodeOf(err) != errcode.AlreadyExists {
		return "", boundaryError("create the team branch", err)
	}
	path, err := git.CreateTaskWorktree(ctx, d.deps.DataDir, launch.repoDir, launch.repoID, task.TaskID, launch.parentRef)
	if err != nil {
		return "", boundaryError("create the task worktree", err)
	}
	return path, nil
}

// removeWorktree drops the worktree of a task that never got a session.
func (d *teamDispatcher) removeWorktree(ctx context.Context, launch *taskLaunch, taskID string) {
	if err := d.deps.Git.RemoveTaskWorktree(ctx, launch.repoDir, d.deps.DataDir, launch.repoID, taskID); err != nil {
		slog.WarnContext(ctx, "removing the task worktree failed", "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
	}
}

// closeTaskSession closes a session whose task could not go on.
func (d *teamDispatcher) closeTaskSession(ctx context.Context, sessionID string) {
	if err := d.deps.Sessions.Close(ctx, sessionID, vendor.CloseHostStop); err != nil {
		slog.WarnContext(ctx, "closing the task session failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

// failTask ends a task failed with a stable code. It reads the team's last
// sequence itself, because earlier appends moved it.
func (d *teamDispatcher) failTask(ctx context.Context, teamID, taskID, code string) error {
	rec, _, err := d.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	_, err = d.appendTaskEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: taskID, EventType: vendorstore.EventTaskEnded, Outcome: vendorstore.TaskOutcomeFailed, FailureCode: code,
	})
	return err
}
