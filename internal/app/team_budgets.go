package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	defaultTaskDeadlineSeconds    = 3600
	defaultTaskMaxTurns           = 50
	defaultSilenceTimeoutSeconds  = 600
	defaultTeamDeadlineSeconds    = 14400
	defaultTeamMaxAgents          = 12
	taskFailureDeadlineExceeded   = "deadline_exceeded"
	taskFailureTurnBudgetExceeded = "turn_budget_exceeded"
	teamDeadlineToolName          = "fugue_team_deadline"
	teamDeadlineQuestionID        = "decision"
	teamDeadlineOptionExtend      = "extend"
	teamDeadlineOptionCancel      = "cancel_queued"
	teamDeadlineCancelCode        = "team_deadline_cancelled"
)

type teamDeadlineResolver interface {
	validateTeamDeadlineAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
	applyTeamDeadlineAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
}

// SetTeamDeadlineResolver wires the team deadline card consumer on startup.
func (s *InteractionService) SetTeamDeadlineResolver(r teamDeadlineResolver) { s.teamDeadlines = r }

func isTeamDeadlineCard(ticket *interaction.Ticket) bool {
	return ticket != nil && ticket.Binding.ToolName == teamDeadlineToolName
}

type teamDeadlineEventPayload struct {
	Generation    int64  `json:"generation"`
	DeadlineMs    int64  `json:"deadline_ms"`
	ExtendSeconds int    `json:"extend_seconds,omitempty"`
	DecidedBy     string `json:"decided_by,omitempty"`
}

type teamTaskKey struct{ teamID, taskID string }

type silenceWatch struct {
	teamID, taskID, sessionID     string
	lastNonStallSeq, lastStallSeq int64
	lastActivity, lastEscalatedAt time.Time
	escalated                     bool
}

type taskStalledPayload struct {
	TaskID        string `json:"task_id"`
	WaitingOn     string `json:"waiting_on"`
	SilentSeconds int64  `json:"silent_seconds"`
	TeamWide      bool   `json:"team_wide"`
}

type teamBudgetsDeps struct {
	Store        *vendorstore.Store
	Sessions     taskSessions
	Interactions *InteractionService
	TeamsConfig  func(context.Context, string) (workspace.TeamsConfig, error)
	Vault        *vault.Service
	Clock        clock.Clock
	IDs          ids.Generator
	Wake         *teamWake
	OnTeamResume func(teamID string)
}

type teamBudgets struct {
	deps            teamBudgetsDeps
	wakeCh, stopCh  chan struct{}
	stopOnce        sync.Once
	taskMu, mu      sync.Mutex
	loopTask        *supervise.Task
	steps           atomic.Int64
	watches         map[teamTaskKey]*silenceWatch
	bySession       map[string]*silenceWatch
	sessionActivity map[string]time.Time
}

func newTeamBudgets(deps *teamBudgetsDeps) *teamBudgets {
	cp := *deps
	if cp.Clock == nil {
		cp.Clock = clock.Real{}
	}
	return &teamBudgets{
		deps: cp, wakeCh: make(chan struct{}, 1), stopCh: make(chan struct{}),
		watches: make(map[teamTaskKey]*silenceWatch), bySession: make(map[string]*silenceWatch),
		sessionActivity: make(map[string]time.Time),
	}
}

func (b *teamBudgets) now() time.Time { return b.deps.Clock.Now() }

func (b *teamBudgets) Start(base context.Context) {
	b.taskMu.Lock()
	defer b.taskMu.Unlock()
	if b.loopTask == nil {
		b.loopTask = supervise.Go(base, "team.budgets-watchdog", b.runLoop)
		b.Notify()
	}
}

func (b *teamBudgets) Stop() error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.taskMu.Lock()
	task := b.loopTask
	b.taskMu.Unlock()
	if task != nil {
		if err := task.Wait(); err != nil {
			return errcode.Wrap(errcode.Internal, "stop team budgets loop", err)
		}
	}
	return nil
}

func (b *teamBudgets) Notify() {
	select {
	case b.wakeCh <- struct{}{}:
	default:
	}
}

func (b *teamBudgets) ObserveSession(sessionID string) {
	if sessionID == "" {
		return
	}
	now := b.now()
	b.mu.Lock()
	b.sessionActivity[sessionID] = now
	if w := b.bySession[sessionID]; w != nil {
		w.lastActivity, w.escalated = now, false
	}
	b.mu.Unlock()
}

// OnTurnStarted enforces the §11 per-task turn budget and resets silence.
// One "turn" is one durable VendorTurnStarted row in vendor_turns for the
// task's vendor session (Fugue-origin or vendor-origin, across idle detach
// and resume cycles; retries spawn a new task row and session with a fresh
// counter). When a newly started turn causes turns > max_turns, the running
// turn is interrupted via Sessions.Interrupt, the task fails with
// turn_budget_exceeded keeping its worktree, and the direct delegator is
// notified via mailbox in the same SQLite transaction (§11, Q-149).
func (b *teamBudgets) OnTurnStarted(ctx context.Context, sessionID string) {
	b.ObserveSession(sessionID)
	if !b.vaultUnlocked() {
		return
	}
	task, found, err := b.deps.Store.TaskForSession(ctx, sessionID)
	if err != nil || !found || !isWatchedTask(&task) {
		return
	}
	rec, _, err := b.deps.Store.TeamSnapshot(ctx, task.TeamID)
	if err != nil || rec.State != vendorstore.TeamStateActive {
		return
	}
	maxTurns := resolveTaskMaxTurns(ctx, b.deps.Store, b.deps.TeamsConfig, &rec, &task)
	turns, err := b.deps.Store.CountSessionTurns(ctx, sessionID)
	if err == nil && maxTurns > 0 && turns > maxTurns {
		b.failTaskForBudget(ctx, &task, taskFailureTurnBudgetExceeded, nil)
	}
}

func (b *teamBudgets) vaultUnlocked() bool {
	return b.deps.Vault == nil || b.deps.Vault.Snapshot().State == vault.StateUnlocked
}

func isWatchedTask(t *vendorstore.TaskRecord) bool {
	return t.HierarchyRole != vendorstore.RoleLead && t.HierarchyRole != vendorstore.RoleSolo &&
		(t.State == vendorstore.TaskStateStarting || t.State == vendorstore.TaskStateRunning)
}

func (b *teamBudgets) runLoop(ctx context.Context) error {
	var timer clock.Timer
	var timerCh <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer, timerCh = nil, nil
		}
	}
	defer stopTimer()
	for {
		stopTimer()
		if next, ok := b.Step(ctx); ok {
			timer = b.deps.Clock.NewTimer(max(next.Sub(b.now()), time.Millisecond))
			timerCh = timer.C()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-b.stopCh:
			return nil
		case <-b.wakeCh:
		case <-timerCh:
		}
	}
}

func (b *teamBudgets) Step(ctx context.Context) (time.Time, bool) {
	if !b.vaultUnlocked() {
		return time.Time{}, false
	}
	b.steps.Add(1)
	now := b.now()
	t1, ok1 := b.processDurableTimers(ctx, now)
	t2, ok2 := b.processSilenceWatches(ctx, now)
	if ok1 && (!ok2 || t1.Before(t2)) {
		return t1, true
	}
	return t2, ok2
}

func (b *teamBudgets) processDurableTimers(ctx context.Context, now time.Time) (time.Time, bool) {
	timers, err := b.deps.Store.ListArmedTeamTimers(ctx)
	if err != nil {
		return time.Time{}, false
	}
	nowMs := now.UnixMilli()
	var earliest time.Time
	for i := range timers {
		if tm := &timers[i]; tm.DeadlineMs <= nowMs {
			b.fireTimer(ctx, tm)
		} else if due := time.UnixMilli(tm.DeadlineMs); earliest.IsZero() || due.Before(earliest) {
			earliest = due
		}
	}
	b.reconcileWaitingDeadlineCards(ctx)
	return earliest, !earliest.IsZero()
}

func (b *teamBudgets) fireTimer(ctx context.Context, tm *vendorstore.TeamTimer) {
	if tm.Kind == vendorstore.TimerKindTeamDeadline {
		b.fireTeamDeadline(ctx, tm)
		return
	}
	ref := vendorstore.TeamTimerRef{TeamID: tm.TeamID, TaskID: tm.TaskID, Kind: tm.Kind, Generation: tm.Generation}
	rec, tasks, err := b.deps.Store.TeamSnapshot(ctx, tm.TeamID)
	if err == nil && rec.State == vendorstore.TeamStateActive {
		if task, found := findTask(tasks, tm.TaskID); found && isWatchedTask(task) {
			b.failTaskForBudget(ctx, task, taskFailureDeadlineExceeded, &ref)
			return
		}
	}
	if cancelErr := b.deps.Store.CancelTeamTimer(ctx, ref); cancelErr != nil {
		slog.WarnContext(ctx, "cancelling stale task deadline timer failed", "team_id", tm.TeamID, "task_id", tm.TaskID, "error_code", string(errcode.CodeOf(cancelErr)))
	}
}

func (b *teamBudgets) failTaskForBudget(ctx context.Context, task *vendorstore.TaskRecord, failureCode string, timerRef *vendorstore.TeamTimerRef) {
	b.interruptTaskSession(ctx, task)
	if err := b.appendTeamEventWithRetry(ctx, task.TeamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		return b.buildBudgetFailureEvent(task.TaskID, failureCode, timerRef, tasks)
	}); err != nil {
		return
	}
	b.mu.Lock()
	b.deleteWatchLocked(teamTaskKey{teamID: task.TeamID, taskID: task.TaskID})
	b.mu.Unlock()
	b.closeTaskSessionPreservingWorktree(ctx, task)
	if b.deps.Wake != nil {
		b.deps.Wake.Notify()
	}
}

func (b *teamBudgets) interruptTaskSession(ctx context.Context, task *vendorstore.TaskRecord) {
	if task.SessionID != "" && b.deps.Sessions != nil {
		if err := b.deps.Sessions.Interrupt(ctx, task.SessionID); err != nil &&
			!errors.Is(err, vendor.ErrTurnNotRunning) && !errors.Is(err, vendorstore.ErrSessionNotFound) && !errors.Is(err, vendor.ErrSessionClosed) {
			slog.WarnContext(ctx, "interrupting task session on budget limit failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		}
	}
}

func (b *teamBudgets) closeTaskSessionPreservingWorktree(ctx context.Context, task *vendorstore.TaskRecord) {
	if task.SessionID != "" && b.deps.Sessions != nil {
		if err := b.deps.Sessions.Close(ctx, task.SessionID, vendor.CloseHostStop); err != nil &&
			!errors.Is(err, vendorstore.ErrSessionNotFound) && !errors.Is(err, vendor.ErrSessionClosed) {
			slog.WarnContext(ctx, "closing task session on budget limit failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		}
	}
}

func (b *teamBudgets) buildBudgetFailureEvent(taskID, failureCode string, timerRef *vendorstore.TeamTimerRef, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
	cur, ok := findTask(tasks, taskID)
	if !ok || !isWatchedTask(cur) {
		return nil, errcode.New(errcode.FailedPrecondition, "task is no longer starting or running")
	}
	evID, err := b.deps.IDs.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate budget failure event ID", err)
	}
	mail, err := buildSendMailbox(resolvedParentID(cur.ParentTaskID, tasks), mailboxDigestItem{
		TaskID: cur.TaskID, State: string(vendorstore.TaskStateFailed), FailureCode: failureCode, Summary: "task failed: " + failureCode,
	})
	if err != nil {
		return nil, err
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskEnded,
		Outcome: vendorstore.TaskOutcomeFailed, FailureCode: failureCode, Mailbox: mail, FireTimer: timerRef,
	}, nil
}

func buildSendMailbox(recipientTaskID string, item mailboxDigestItem) ([]vendorstore.MailboxEnqueueInput, error) {
	if recipientTaskID == "" {
		return nil, nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal mailbox item", err)
	}
	return []vendorstore.MailboxEnqueueInput{{RecipientTaskID: recipientTaskID, Kind: vendorstore.MailboxKindSend, Payload: raw}}, nil
}

func (b *teamBudgets) processSilenceWatches(ctx context.Context, now time.Time) (time.Time, bool) {
	teamIDs, err := b.deps.Store.ActiveTeamIDs(ctx)
	if err != nil {
		return time.Time{}, false
	}
	activeKeys := make(map[teamTaskKey]struct{})
	var earliest time.Time
	for _, teamID := range teamIDs {
		if due := b.checkTeamSilence(ctx, teamID, now, activeKeys); !due.IsZero() && (earliest.IsZero() || due.Before(earliest)) {
			earliest = due
		}
	}
	b.pruneInactiveWatches(activeKeys)
	return earliest, !earliest.IsZero()
}

func (b *teamBudgets) checkTeamSilence(ctx context.Context, teamID string, now time.Time, activeKeys map[teamTaskKey]struct{}) time.Time {
	rec, tasks, err := b.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil || rec.State != vendorstore.TeamStateActive {
		return time.Time{}
	}
	if timeoutSec := resolveSilenceTimeoutSeconds(ctx, b.deps.TeamsConfig, rec.WorkspaceID); timeoutSec > 0 {
		return b.evalTeamSilenceWatches(ctx, &rec, tasks, now, time.Duration(timeoutSec)*time.Second, activeKeys)
	}
	return time.Time{}
}

func (b *teamBudgets) pruneInactiveWatches(activeKeys map[teamTaskKey]struct{}) {
	b.mu.Lock()
	for k := range b.watches {
		if _, ok := activeKeys[k]; !ok {
			b.deleteWatchLocked(k)
		}
	}
	b.mu.Unlock()
}

func (b *teamBudgets) deleteWatchLocked(key teamTaskKey) {
	if w, ok := b.watches[key]; ok {
		delete(b.watches, key)
		if w.sessionID != "" {
			if b.bySession[w.sessionID] == w {
				delete(b.bySession, w.sessionID)
			}
			delete(b.sessionActivity, w.sessionID)
		}
	}
}

func (b *teamBudgets) evalTeamSilenceWatches(ctx context.Context, rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, now time.Time, timeout time.Duration, activeKeys map[teamTaskKey]struct{}) time.Time {
	var watched []*silenceWatch
	for i := range tasks {
		if isWatchedTask(&tasks[i]) {
			key := teamTaskKey{teamID: rec.TeamID, taskID: tasks[i].TaskID}
			activeKeys[key] = struct{}{}
			watched = append(watched, b.syncTaskWatch(key, &tasks[i]))
		}
	}
	teamWide := allWatchesSilent(watched, now, timeout)
	var earliest time.Time
	for _, w := range watched {
		if elapsed := now.Sub(w.lastActivity); elapsed >= timeout && !w.escalated {
			b.recordTaskStalled(ctx, rec.TeamID, w, int64(elapsed.Seconds()), teamWide, now)
		} else if !w.escalated {
			if due := w.lastActivity.Add(timeout); earliest.IsZero() || due.Before(earliest) {
				earliest = due
			}
		}
	}
	return earliest
}

func allWatchesSilent(watched []*silenceWatch, now time.Time, timeout time.Duration) bool {
	for _, w := range watched {
		if now.Sub(w.lastActivity) < timeout {
			return false
		}
	}
	return len(watched) > 0
}

func (b *teamBudgets) syncTaskWatch(key teamTaskKey, t *vendorstore.TaskRecord) *silenceWatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	eventTime := time.UnixMilli(t.UpdatedAt)
	if obsTime, ok := b.sessionActivity[t.SessionID]; ok && obsTime.After(eventTime) {
		eventTime = obsTime
	}
	w, ok := b.watches[key]
	if !ok {
		w = &silenceWatch{teamID: key.teamID, taskID: key.taskID, sessionID: t.SessionID, lastNonStallSeq: t.LastEventSeq, lastActivity: eventTime}
		b.watches[key] = w
		b.indexWatchSessionLocked(w, t.SessionID)
		return w
	}
	b.indexWatchSessionLocked(w, t.SessionID)
	if t.LastEventSeq != w.lastNonStallSeq && t.LastEventSeq != w.lastStallSeq {
		w.lastNonStallSeq, w.escalated = t.LastEventSeq, false
		if eventTime.After(w.lastActivity) {
			w.lastActivity = eventTime
		}
	}
	if obsTime, hasObs := b.sessionActivity[t.SessionID]; hasObs && obsTime.After(w.lastActivity) {
		w.lastActivity, w.escalated = obsTime, false
	}
	return w
}

func (b *teamBudgets) indexWatchSessionLocked(w *silenceWatch, sessionID string) {
	if w.sessionID != sessionID && w.sessionID != "" && b.bySession[w.sessionID] == w {
		delete(b.bySession, w.sessionID)
	}
	w.sessionID = sessionID
	if sessionID != "" {
		b.bySession[sessionID] = w
	}
}

func (b *teamBudgets) recordTaskStalled(ctx context.Context, teamID string, w *silenceWatch, silentSeconds int64, teamWide bool, now time.Time) {
	var recordedSeq int64
	err := b.appendTeamEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		return b.buildTaskStalledInput(tasks, w.taskID, silentSeconds, teamWide, &recordedSeq)
	})
	if err != nil {
		return
	}
	b.mu.Lock()
	if cur, ok := b.watches[teamTaskKey{teamID: teamID, taskID: w.taskID}]; ok {
		cur.lastStallSeq, cur.escalated, cur.lastEscalatedAt = recordedSeq, true, now
	}
	b.mu.Unlock()
	if b.deps.Wake != nil {
		b.deps.Wake.Notify()
	}
}

func (b *teamBudgets) buildTaskStalledInput(tasks []vendorstore.TaskRecord, taskID string, silentSeconds int64, teamWide bool, recordedSeq *int64) (*vendorstore.AppendTeamEventInput, error) {
	cur, ok := findTask(tasks, taskID)
	if !ok || !isWatchedTask(cur) {
		return nil, errcode.New(errcode.FailedPrecondition, "task is no longer starting or running")
	}
	waitingOn := "vendor"
	if cur.State == vendorstore.TaskStateStarting {
		waitingOn = "startup"
	}
	payload, pErr := json.Marshal(taskStalledPayload{TaskID: cur.TaskID, WaitingOn: waitingOn, SilentSeconds: silentSeconds, TeamWide: teamWide})
	if pErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal task stalled payload", pErr)
	}
	evID, idErr := b.deps.IDs.NewID()
	if idErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate task stalled event ID", idErr)
	}
	mail, mErr := buildSendMailbox(resolvedParentID(cur.ParentTaskID, tasks), mailboxDigestItem{
		TaskID: cur.TaskID, State: "stalled", Summary: fmt.Sprintf("task silent for %ds (waiting_on=%s)", silentSeconds, waitingOn),
	})
	if mErr != nil {
		return nil, mErr
	}
	*recordedSeq = cur.LastEventSeq + 1
	return &vendorstore.AppendTeamEventInput{EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskStalled, ProtectedPayload: payload, Mailbox: mail}, nil
}
