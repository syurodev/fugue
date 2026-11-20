package app

// The F11.3a start path: the dispatcher turns queued tasks of an active team
// into running ones (Q-98, Q-116–Q-119). Approve only queued them (F11.2b);
// the scheduler, quota gate and budgets stay F11.6, behind Admitter.
//
// Every state change is an appended team event, recorded before the effect it
// announces: a crash between the assignment and the session leaves the task
// `starting`, which recovery turns into `unknown` — a task whose start outcome
// is unknown is never started again (AGENTS: no replay of an unknown effect).

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// Stable failure codes of a task that could not start (§3.1 failed(code)).
const (
	failSpecUnreadable = "spec_unreadable"
	failProfile        = "profile_invalid"
	failWorktree       = "worktree"
	failSessionStart   = "session_start"
	failIntro          = "intro"
)

// rootResolver resolves a workspace root name to its directory; the
// workspace service implements it.
type rootResolver interface {
	SelectSessionCwd(ctx context.Context, workspaceID, rootName string) (string, error)
}

// teamDispatcherDeps are the ports of the dispatcher.
type teamDispatcherDeps struct {
	Store    *vendorstore.Store
	Sessions taskSessions
	Admit    Admitter
	Roots    rootResolver
	// Git is nil when no usable git binary was found; a task that needs a
	// worktree then fails closed instead of running in a shared directory.
	Git     *gitops.Manager
	DataDir string
	IDs     ids.Generator
	Vault   *vault.Service
	// Wake is the F11.3b mailbox pump; nil leaves the digest for the next
	// trigger (the decide-path tests admit nothing, so they enqueue none).
	Wake        *teamWake
	Clock       clock.Clock
	TeamsConfig func(context.Context, string) (workspace.TeamsConfig, error)
	Budgets     *teamBudgets
	// Test hooks, nil in production, invoked between TeamSnapshot and
	// appendTaskEvent so tests can inject real races.
	TestBeforeStartAppend func(context.Context)
	TestBeforeFailAppend  func(context.Context)
}

// teamDispatcher starts the queued tasks of active teams. One run at a time:
// the mutex serializes runs, so two triggers never start the same task twice.
type teamDispatcher struct {
	deps teamDispatcherDeps
	mu   sync.Mutex
}

func newTeamDispatcher(deps *teamDispatcherDeps) *teamDispatcher {
	return &teamDispatcher{deps: *deps}
}

// notifyMailbox asks the wake pump for a delivery pass after an append
// enqueued mail; nil leaves it to the next trigger.
func (d *teamDispatcher) notifyMailbox() {
	if d.deps.Wake != nil {
		d.deps.Wake.Notify()
	}
}

// Dispatch starts every startable queued task of the team. A task the
// admitter holds back stays queued for the next trigger. It fails closed
// while the vault is locked: nothing is read or recorded then.
func (d *teamDispatcher) Dispatch(ctx context.Context, teamID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.VendorVaultLocked, "the vault is locked")
	}
	held := make(map[string]bool)
	_, tasks, err := d.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	// Each pass leaves its task out of the queued set or marks it held, so
	// len(tasks)+1 passes always reach a fixed point.
	for range len(tasks) + 1 {
		rec, current, err := d.deps.Store.TeamSnapshot(ctx, teamID)
		if err != nil {
			return boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive || rec.WaitingReason != "" {
			return nil
		}
		next := nextStartable(current, held)
		if next == nil {
			return nil
		}
		started, startErr := d.start(ctx, &rec, next, current)
		if startErr != nil {
			return startErr
		}
		if !started {
			held[next.TaskID] = true
		}
	}
	return nil
}

// resolveTaskChain indexes tasks by ID and maps each superseded task to its
// immediate retry child so dependency resolution follows retries without
// mutating depends_on (Q-126).
func resolveTaskChain(tasks []vendorstore.TaskRecord) (map[string]*vendorstore.TaskRecord, map[string]string) {
	byID := make(map[string]*vendorstore.TaskRecord, len(tasks))
	retryChild := make(map[string]string)
	for i := range tasks {
		t := &tasks[i]
		byID[t.TaskID] = t
		if t.RetryOf != "" {
			retryChild[t.RetryOf] = t.TaskID
		}
	}
	return byID, retryChild
}

// latestAttempt walks the retry chain forward from taskID and returns the
// latest attempt record, or nil when taskID does not exist.
func latestAttempt(taskID string, byID map[string]*vendorstore.TaskRecord, retryChild map[string]string) *vendorstore.TaskRecord {
	cur := taskID
	for range len(byID) + 1 {
		next, ok := retryChild[cur]
		if !ok || next == "" {
			break
		}
		cur = next
	}
	return byID[cur]
}

func activeAttemptID(taskID string, byID map[string]*vendorstore.TaskRecord, retryChild map[string]string) string {
	if taskID == "" {
		return ""
	}
	if latest := latestAttempt(taskID, byID, retryChild); latest != nil {
		return latest.TaskID
	}
	return taskID
}

func sameAttemptChain(a, b string, byID map[string]*vendorstore.TaskRecord, retryChild map[string]string) bool {
	ra := activeAttemptID(a, byID, retryChild)
	rb := activeAttemptID(b, byID, retryChild)
	return ra != "" && ra == rb
}

func resolvedParentID(parentTaskID string, tasks []vendorstore.TaskRecord) string {
	if parentTaskID == "" {
		return ""
	}
	byID, retryChild := resolveTaskChain(tasks)
	return activeAttemptID(parentTaskID, byID, retryChild)
}

func startable(t *vendorstore.TaskRecord, byID map[string]*vendorstore.TaskRecord, retryChild map[string]string) bool {
	if t.ParentTaskID == "" {
		return t.HierarchyRole == vendorstore.RoleLead
	}
	parent := latestAttempt(t.ParentTaskID, byID, retryChild)
	if parent == nil || (parent.State != vendorstore.TaskStateRunning && parent.State != vendorstore.TaskStateAsking) {
		return false
	}
	for _, dep := range t.DependsOn {
		if d := latestAttempt(dep, byID, retryChild); d == nil || d.State != vendorstore.TaskStateDone {
			return false
		}
	}
	return true
}

// start starts one task and reports whether it left the queue. A task that
// is not admitted stays queued; one that fails to start is ended failed.
func (d *teamDispatcher) start(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error) {
	admitted, err := d.deps.Admit.Admit(ctx, task, tasks)
	if err != nil {
		return false, boundaryError("admit the task", err)
	}
	if !admitted {
		return false, nil
	}
	if task.HierarchyRole == vendorstore.RoleLead {
		return d.bindLead(ctx, rec, task)
	}
	return d.startChild(ctx, rec, task, tasks)
}

// appendTaskEvent appends one task event after the team's last sequence and
// returns the new last sequence.
func (d *teamDispatcher) appendTaskEvent(ctx context.Context, teamID string, seq int64, ev *vendorstore.AppendTeamEventInput) error {
	id, err := d.deps.IDs.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate the task event ID", err)
	}
	ev.EventID = id
	if _, err := d.deps.Store.AppendTeamEvent(ctx, teamID, seq, ev); err != nil {
		return boundaryError("record the task event", err)
	}
	return nil
}

// bindLead binds the proposing session to the lead task (Q-117): no process
// starts and no worktree is made, the session keeps its working directory. A
// session that is gone leaves the task queued.
func (d *teamDispatcher) bindLead(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord) (bool, error) {
	info, err := d.deps.Store.Snapshot(ctx, rec.SessionID)
	if errors.Is(err, vendorstore.ErrSessionNotFound) || (err == nil && info.State == vendorstore.StateClosed) {
		return false, nil
	}
	if err != nil {
		return false, boundaryError("read the lead session", err)
	}
	if err := d.appendTaskEvent(ctx, rec.TeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: task.TaskID, EventType: vendorstore.EventTaskAssigned, SessionID: rec.SessionID,
	}); err != nil {
		return false, err
	}
	if err := d.appendTaskStartedWithRetry(ctx, rec.TeamID, task.TaskID, rec.SessionID); err != nil {
		if failErr := d.failTaskInState(ctx, rec.TeamID, task.TaskID, vendorstore.TaskStateStarting, rec.SessionID, failSessionStart); failErr != nil {
			return false, failErr
		}
		return false, err
	}
	return true, nil
}

// appendTaskStartedWithRetry records EventTaskStarted after verifying the task
// is still in TaskStateStarting with the expected sessionID, retrying only
// this append on ErrSequenceConflict without repeating worktree or spawn.
func (d *teamDispatcher) appendTaskStartedWithRetry(ctx context.Context, teamID, taskID, sessionID string) error {
	for attempt := range maxAppendRetries {
		err := d.appendTaskStartedAttempt(ctx, teamID, taskID, sessionID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, vendorstore.ErrSequenceConflict) {
			return err
		}
		if attempt+1 == maxAppendRetries {
			return errcode.New(errcode.FailedPrecondition, "task start sequence conflict")
		}
	}
	return errcode.New(errcode.FailedPrecondition, "task start sequence conflict")
}

func (d *teamDispatcher) appendTaskStartedAttempt(ctx context.Context, teamID, taskID, sessionID string) error {
	rec, tasks, err := d.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the team is not active")
	}
	if rec.WaitingReason != "" {
		return errcode.New(errcode.FailedPrecondition, "team deadline reached; waiting for user decision")
	}
	task, found := findTask(tasks, taskID)
	if !found || task.State != vendorstore.TaskStateStarting || task.SessionID != sessionID {
		return errcode.New(errcode.FailedPrecondition, "task is no longer starting for its spawned session")
	}
	if d.deps.TestBeforeStartAppend != nil {
		d.deps.TestBeforeStartAppend(ctx)
	}
	armTimers := d.taskDeadlineTimers(ctx, &rec, task)
	if err := d.appendTaskEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		TaskID: taskID, EventType: vendorstore.EventTaskStarted, ArmTimers: armTimers,
	}); err != nil {
		return err
	}
	if d.deps.Budgets != nil {
		d.deps.Budgets.trackTaskWatch(teamID, task, vendorstore.TaskStateRunning, sessionID, rec.LastSeq+1, d.now().UnixMilli())
		d.deps.Budgets.Notify()
	}
	return nil
}

func (d *teamDispatcher) taskDeadlineTimers(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord) []vendorstore.TeamTimerArmInput {
	return buildTaskDeadlineTimers(ctx, d.deps.Store, d.deps.TeamsConfig, d.now(), rec, task)
}

func buildTaskDeadlineTimers(
	ctx context.Context,
	store *vendorstore.Store,
	teamsCfg func(context.Context, string) (workspace.TeamsConfig, error),
	now time.Time,
	rec *vendorstore.TeamRecord,
	task *vendorstore.TaskRecord,
) []vendorstore.TeamTimerArmInput {
	if task.HierarchyRole == vendorstore.RoleLead || task.HierarchyRole == vendorstore.RoleSolo {
		return nil
	}
	deadlineSec := resolveTaskDeadlineSeconds(ctx, store, teamsCfg, rec, task)
	if deadlineSec <= 0 {
		return nil
	}
	return []vendorstore.TeamTimerArmInput{{
		TeamID: rec.TeamID, TaskID: task.TaskID, Kind: vendorstore.TimerKindTaskDeadline,
		DeadlineMs: now.Add(time.Duration(deadlineSec) * time.Second).UnixMilli(),
	}}
}

func (d *teamDispatcher) now() time.Time {
	if d.deps.Clock != nil {
		return d.deps.Clock.Now()
	}
	return (clock.Real{}).Now()
}

// StartTask starts one explicitly-named queued task of an active team
// (fugue_task_start). It returns as soon as the task session is spawned and
// its intro turn is sent, without waiting for the task to finish.
func (d *teamDispatcher) StartTask(ctx context.Context, teamID, taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.VendorVaultLocked, "the vault is locked")
	}
	for attempt := range maxAppendRetries {
		err := d.startTaskAttempt(ctx, teamID, taskID)
		if err == nil {
			return nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return err
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (d *teamDispatcher) startTaskAttempt(ctx context.Context, teamID, taskID string) error {
	rec, tasks, err := d.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the team is not active")
	}
	if rec.WaitingReason != "" {
		return errcode.New(errcode.FailedPrecondition, "team deadline reached; waiting for user decision")
	}
	byID, retryChild := resolveTaskChain(tasks)
	task := byID[taskID]
	if task == nil {
		return errcode.New(errcode.NotFound, "team task not found")
	}
	if task.State != vendorstore.TaskStateQueued {
		return errcode.New(errcode.FailedPrecondition, "the task is not queued")
	}
	if !startable(task, byID, retryChild) {
		return errcode.New(errcode.FailedPrecondition, "the task dependencies are not satisfied")
	}
	started, err := d.start(ctx, &rec, task, tasks)
	if err != nil {
		return err
	}
	if !started {
		return errcode.New(errcode.FailedPrecondition, "no capacity to start the task")
	}
	return nil
}
