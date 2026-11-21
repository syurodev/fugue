package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

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
	deps       []depSummary
	nonce      string
	retry      retryIntro
	req        taskSessionRequest
	repoDir    string
	repoID     string
	parentRef  string
	headCommit string
}

// retryIntro carries the §5.4 retry facts the intro states (§6.7, F11.10a):
// the attempt number, the chain root and the previous attempt's failure code.
// The history itself is never copied into the intro; the agent reads it from
// its channel thread with fugue_team_read.
type retryIntro struct {
	attempt  int
	root     string
	prevCode string
}

// startChild starts the vendor session of one child task in its own worktree
// (Q-119). Reading and slot reservation come before TaskAssigned so capacity
// lost between Admit and reservation leaves the task queued rather than failed.
// Once the assignment is recorded every failure ends the task failed with a
// stable code, and a crash leaves it `starting` for recovery (never re-run).
func (d *teamDispatcher) startChild(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error) {
	launch, code, err := d.prepareLaunch(ctx, rec, task, tasks)
	if err != nil {
		return true, err
	}
	if code != "" {
		return true, d.failTaskInState(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateQueued, "", code)
	}
	releaseSlot, capErr := d.deps.Sessions.ReserveSlot(ctx, launch.req.SessionID, launch.req.Vendor, vendorDefaultAccount)
	if capErr != nil {
		if errors.Is(capErr, ErrVendorSessionCapacity) || errcode.CodeOf(capErr) == errcode.VendorCapacity {
			return false, nil
		}
		return false, boundaryError("reserve task slot", capErr)
	}
	defer releaseSlot()
	launch.req.SlotReserved = true
	if appErr := d.appendTaskEvent(ctx, rec.TeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: task.TaskID, EventType: vendorstore.EventTaskAssigned, SessionID: launch.req.SessionID,
	}); appErr != nil {
		return true, appErr
	}
	d.deps.Budgets.trackTaskWatch(rec.TeamID, task, vendorstore.TaskStateStarting, launch.req.SessionID, rec.LastSeq+1, d.now().UnixMilli())
	cwd, err := d.createWorktree(ctx, rec.TeamID, task, launch)
	if err != nil {
		slog.WarnContext(ctx, "creating the task worktree failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		if isDiskFull(err) {
			return true, d.failDiskFull(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, launch.req.SessionID, err)
		}
		return true, d.failTaskInState(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, launch.req.SessionID, failWorktree)
	}
	launch.req.Cwd = cwd
	if err := d.deps.Sessions.StartTaskSession(ctx, &launch.req); err != nil {
		slog.WarnContext(ctx, "starting the task session failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		d.removeWorktree(ctx, launch, task.TaskID)
		if isDiskFull(err) {
			return true, d.failDiskFull(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, launch.req.SessionID, err)
		}
		return true, d.failTaskInState(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, launch.req.SessionID, failSessionStart)
	}
	if err := d.appendTaskStartedWithRetry(ctx, rec.TeamID, task.TaskID, launch.req.SessionID); err != nil {
		d.closeTaskSession(ctx, launch.req.SessionID)
		d.removeWorktree(ctx, launch, task.TaskID)
		return true, d.failTaskInState(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, launch.req.SessionID, failSessionStart)
	}
	return d.sendTaskIntro(ctx, rec.TeamID, task, launch)
}

func (d *teamDispatcher) sendTaskIntro(ctx context.Context, teamID string, task *vendorstore.TaskRecord, launch *taskLaunch) (bool, error) {
	intro := taskIntro(task, &launch.spec, launch.brief, launch.deps, launch.nonce, launch.retry)
	opCtx := context.WithoutCancel(ctx)
	taskID, sessionID := task.TaskID, launch.req.SessionID
	err := d.deps.Sessions.SendAsync(ctx, sessionID, vendor.Input{Text: intro, Origin: vendor.OriginFugue}, func(sendErr error) {
		if sendErr == nil || errors.Is(sendErr, ErrHostStopping) {
			return
		}
		d.closeTaskSession(opCtx, sessionID)
		if failErr := d.failTaskInState(opCtx, teamID, taskID, vendorstore.TaskStateRunning, sessionID, failIntro); failErr != nil {
			slog.WarnContext(opCtx, "failing task after paced intro error failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(failErr)))
		}
	})
	if err != nil {
		d.closeTaskSession(ctx, sessionID)
		return true, d.failTaskInState(ctx, teamID, taskID, vendorstore.TaskStateRunning, sessionID, failIntro)
	}
	return true, nil
}

// prepareLaunch reads the sealed spec, the decided profile and the repository
// of the task. A non-empty code means the task cannot start and must fail.
func (d *teamDispatcher) prepareLaunch(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (*taskLaunch, string, error) {
	spec, role, code, err := resolveTaskProfile(ctx, d.deps.Store, rec.TeamID, task.TaskID, task.CatalogRole)
	if err != nil || code != "" {
		return nil, code, err
	}
	launch := &taskLaunch{spec: spec}
	if task.RetryOf != "" {
		launch.retry.attempt = task.Attempt
		launch.retry.root = vendorstore.ThreadRootTaskID(task.TaskID)
		for i := range tasks {
			if tasks[i].TaskID == task.RetryOf {
				launch.retry.prevCode = tasks[i].FailureCode
				break
			}
		}
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
	launch.deps = d.dependencySummaries(ctx, rec.TeamID, task, tasks)
	if len(launch.deps) > 0 {
		nonce, nErr := d.deps.IDs.NewID()
		if nErr != nil {
			return nil, "", errcode.Wrap(errcode.Internal, "allocate the intro nonce", nErr)
		}
		launch.nonce = nonce
	}
	launch.req = taskSessionRequest{
		SessionID: sessionID, Vendor: role.Vendor, Model: role.Model, Effort: role.Effort,
		WorkspaceID: lead.WorkspaceID, RootName: lead.WorkspaceRoot, TeamID: rec.TeamID, TaskID: task.TaskID,
		Role: task.HierarchyRole, ApprovalMode: approvalModeFor(role.Permission),
	}
	return launch, "", nil
}

// resolveTaskProfile decodes a task's sealed spec, looks up its decided catalog
// role, and applies any task-level profile overlay (OR-E13).
func resolveTaskProfile(ctx context.Context, store *vendorstore.Store, teamID, taskID, catalogRole string) (teams.ProtectedTaskContent, decidedRole, string, error) {
	raw, err := store.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil {
		return teams.ProtectedTaskContent{}, decidedRole{}, "", boundaryError("open the task spec", err)
	}
	if !json.Valid(raw) {
		return teams.ProtectedTaskContent{}, decidedRole{}, failSpecUnreadable, nil
	}
	var spec teams.ProtectedTaskContent
	if decodeErr := json.Unmarshal(raw, &spec); decodeErr != nil {
		return teams.ProtectedTaskContent{}, decidedRole{}, "", errcode.Wrap(errcode.Internal, "decode the task spec", decodeErr)
	}
	role, found, err := decidedRoleFor(ctx, store, teamID, catalogRole)
	if err != nil {
		return teams.ProtectedTaskContent{}, decidedRole{}, "", err
	}
	if !found {
		return teams.ProtectedTaskContent{}, decidedRole{}, failProfile, nil
	}
	if spec.Profile != nil {
		overlayProfile(&role, spec.Profile)
	}
	return spec, role, "", nil
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

// repoIDFor derives the stable repository identifier from the root directory;
// the wake re-derives a detached session's worktree path with it (Q-119).
func repoIDFor(repoDir string) string {
	sum := sha256.Sum256([]byte(repoDir))
	return "r" + hex.EncodeToString(sum[:8])
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
	launch.repoID = repoIDFor(dir)
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

// gitTaskID maps durable task IDs (which may include ~r<n> for retry attempts)
// to git-check-ref-format safe identifiers; user proposal task IDs never
// contain '.', so replacing '~' with '.' cannot collide.
func gitTaskID(taskID string) string {
	return strings.ReplaceAll(taskID, "~", ".")
}

// parentBranch names the branch a task starts from: the team branch for a
// direct child of the lead, the manager's task branch for a member of a
// manager (§9).
func parentBranch(task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (string, error) {
	byID, retryChild := resolveTaskChain(tasks)
	if parent := latestAttempt(task.ParentTaskID, byID, retryChild); parent != nil && parent.HierarchyRole == vendorstore.RoleTeamManager {
		return wrapGit(gitops.TaskBranchName(gitTaskID(parent.TaskID)))
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
	if d.deps.TestWorktreeErr != nil {
		return "", d.deps.TestWorktreeErr
	}
	git := d.deps.Git
	if err := git.CreateTeamBranch(ctx, launch.repoDir, teamID, launch.headCommit); err != nil && errcode.CodeOf(err) != errcode.AlreadyExists {
		return "", boundaryError("create the team branch", err)
	}
	path, err := git.CreateTaskWorktree(ctx, d.deps.DataDir, launch.repoDir, launch.repoID, gitTaskID(task.TaskID), launch.parentRef)
	if err != nil {
		return "", boundaryError("create the task worktree", err)
	}
	return path, nil
}

// removeWorktree drops the worktree of a task that never got a session.
func (d *teamDispatcher) removeWorktree(ctx context.Context, launch *taskLaunch, taskID string) {
	if err := d.deps.Git.RemoveTaskWorktree(ctx, launch.repoDir, d.deps.DataDir, launch.repoID, gitTaskID(taskID)); err != nil {
		slog.WarnContext(ctx, "removing the task worktree failed", "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
	}
}

// closeTaskSession closes a session whose task could not go on.
func (d *teamDispatcher) closeTaskSession(ctx context.Context, sessionID string) {
	if err := d.deps.Sessions.Close(ctx, sessionID, vendor.CloseHostStop); err != nil {
		slog.WarnContext(ctx, "closing the task session failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

// failTaskInState ends a task failed with a stable code after re-reading the
// snapshot and verifying expectedState (and expectedSessionID when non-empty),
// retrying on ErrSequenceConflict so a concurrent append never leaves a task
// stuck in `starting` with no owner.
func (d *teamDispatcher) failTaskInState(
	ctx context.Context,
	teamID, taskID string,
	expectedState vendorstore.TaskState,
	expectedSessionID, code string,
) error {
	for attempt := range maxAppendRetries {
		err := d.failTaskAttempt(ctx, teamID, taskID, expectedState, expectedSessionID, code)
		if err == nil {
			d.notifyMailbox()
			return nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return err
	}
	return errcode.New(errcode.FailedPrecondition, "task failure sequence conflict")
}

func (d *teamDispatcher) failTaskAttempt(
	ctx context.Context,
	teamID, taskID string,
	expectedState vendorstore.TaskState,
	expectedSessionID, code string,
) error {
	rec, tasks, err := d.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the team is not active")
	}
	cur, ok := findTask(tasks, taskID)
	if !ok || cur.State != expectedState || (expectedSessionID != "" && cur.SessionID != expectedSessionID) {
		return errcode.New(errcode.FailedPrecondition, "task is no longer in expected state")
	}
	if d.deps.TestBeforeFailAppend != nil {
		d.deps.TestBeforeFailAppend(ctx)
	}
	return d.appendTaskEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: taskID, EventType: vendorstore.EventTaskEnded, Outcome: vendorstore.TaskOutcomeFailed, FailureCode: code,
		Mailbox: digestItemRows(taskID, resolvedParentID(delegatorOf(taskID, tasks), tasks), code),
	})
}

// delegatorOf returns the recipient of a task's failure digest: its parent
// task. A lead task has no delegator; the user sees its failure on the board.
func delegatorOf(taskID string, tasks []vendorstore.TaskRecord) string {
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i].ParentTaskID
		}
	}
	return ""
}

// digestItemRows seals one digest item (Q-122); the state is Fugue's own and
// the summary stays empty until the task tools report one (F11.3c).
func digestItemRows(taskID, recipientTaskID, code string) []vendorstore.MailboxEnqueueInput {
	if recipientTaskID == "" {
		return nil
	}
	payload, err := json.Marshal(mailboxDigestItem{TaskID: taskID, State: string(vendorstore.TaskStateFailed), FailureCode: code})
	if err != nil {
		return nil
	}
	return []vendorstore.MailboxEnqueueInput{{RecipientTaskID: recipientTaskID, Kind: vendorstore.MailboxKindDigest, Payload: payload}}
}
