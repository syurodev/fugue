package app

// F11.3b wake and mailbox delivery (§6.1, Q-121–Q-124). The pump is
// event-driven: an enqueue, a finished turn, a vault unlock or the 30 s
// digest window each wake it. Delivery claims pending rows into one batch and
// sends ONE Fugue-origin turn that starts with the batch marker; the rows
// become `delivered` only when the send was recorded. A crash leaves the
// claim `delivering`, and recovery resolves it against the session
// transcript: found means delivered, absent means resend with the same ID, an
// unreadable transcript means hold — never guess.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// digestWait is the §6.1 batching window of a lead or manager digest.
// resumeBackoff bounds how often a failed detached resume is retried.
const (
	digestWait    = 30 * time.Second
	resumeBackoff = 5 * time.Minute
)

// mailboxScanCap bounds the transcript scan of a recovery: beyond it the
// transcript counts as unreadable and the batch is held (Q-122).
const (
	mailboxScanCap  = 50_000
	mailboxScanPage = 1000
)

// mailboxBatchPrefix marks a delivered batch in the session transcript.
func mailboxBatchPrefix(batchID string) string {
	return "[fugue-batch " + batchID + "]"
}

// TopicTeamMailbox is the ephemeral topic reporting mailbox hold status (F11.3c).
const TopicTeamMailbox = "team-mailbox"

// HeldMailboxEntry describes a recipient whose mailbox delivery is held.
type HeldMailboxEntry struct {
	TeamID    string `json:"team_id"`
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	Vendor    string `json:"vendor"`
	Reason    string `json:"reason"`
}

// teamWakeDeps are the ports of the wake pump.
type teamWakeDeps struct {
	Store    *vendorstore.Store
	Sessions taskSessions
	Roots    rootResolver
	Clock    clock.Clock
	IDs      ids.Generator
	Vault    *vault.Service
	Hub      *EventHub
	// DataDir roots the task worktrees of a detached session's cwd (Q-119).
	DataDir string
}

// teamWake delivers the team mailbox. One run at a time: the mutex serializes
// pumps so two triggers never claim one recipient twice.
type teamWake struct {
	deps           teamWakeDeps
	mu             sync.Mutex
	wake           chan struct{}
	resumeRetryAt  map[string]time.Time
	resumeAttempts map[string]int
	heldMu         sync.RWMutex
	held           map[string]HeldMailboxEntry
}

func newTeamWake(deps *teamWakeDeps) *teamWake {
	w := &teamWake{
		deps:           *deps,
		wake:           make(chan struct{}, 1),
		resumeRetryAt:  make(map[string]time.Time),
		resumeAttempts: make(map[string]int),
		held:           make(map[string]HeldMailboxEntry),
	}
	if deps.Hub != nil {
		if err := deps.Hub.RegisterEphemeral(TopicTeamMailbox, w.heldSnapshot, defaultEphemeralBufferCap); err != nil {
			slog.Warn("registering team mailbox topic failed", "error_code", string(errcode.CodeOf(err)))
		}
	}
	return w
}

// Notify asks for one delivery pass; the mailbox may have new rows or a
// session may have become able to take a turn.
func (w *teamWake) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start runs the pump on the host's lifetime. A failed pass is logged by its
// code only and retried by the next trigger.
func (w *teamWake) Start(base context.Context) *supervise.Task {
	return supervise.Go(base, "team-wake", w.loop)
}

func (w *teamWake) loop(ctx context.Context) error {
	// Recovery runs ONCE, before the first pump: a crash leftover resolves
	// before the pump claims its recipient (Q-122). The pump runs
	// single-flight in this goroutine — a wake that arrives during a pump
	// stays in the channel and causes exactly one more pump afterwards, so a
	// recovery can never race a send that is still in flight.
	w.logWake(ctx, w.RecoverBatches(ctx))
	w.logWake(ctx, w.Pump(ctx))
	var (
		timer   clock.Timer
		timerCh <-chan time.Time
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	rearm := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerCh = nil
		}
		deadline, ok := w.earliestDigestDeadline(ctx)
		if !ok {
			return
		}
		timer = w.deps.Clock.NewTimer(deadline.Sub(w.deps.Clock.Now()))
		timerCh = timer.C()
	}
	rearm()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.wake:
		case <-timerCh:
		}
		w.logWake(ctx, w.Pump(ctx))
		rearm()
	}
}

// earliestDigestDeadline returns the moment the oldest waiting digest window
// ends (§6.1: at most 30 s), if any recipient waits on one.
func (w *teamWake) earliestDigestDeadline(ctx context.Context) (time.Time, bool) {
	recipients, err := w.deps.Store.PendingMailbox(ctx)
	if err != nil {
		return time.Time{}, false
	}
	deadline, ok := time.Time{}, false
	for _, r := range recipients {
		if r.HasImmediate {
			continue
		}
		at := time.UnixMilli(r.EarliestPending).Add(digestWait)
		if !ok || at.Before(deadline) {
			deadline, ok = at, true
		}
	}
	return deadline, ok
}

// Pump delivers what is deliverable now: an immediate kind at once, a lead or
// manager digest after its 30 s window or when the whole dependency group is
// done (Q-121, Q-123). Busy, closed, detached and vault-locked sessions hold
// their mail.
func (w *teamWake) Pump(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.VendorVaultLocked, "the vault is locked")
	}
	recipients, err := w.deps.Store.PendingMailbox(ctx)
	if err != nil {
		return boundaryError("list the pending mailbox", err)
	}
	for _, r := range recipients {
		if err := ctx.Err(); err != nil {
			return errcode.Wrap(errcode.Canceled, "the wake pass was interrupted", err)
		}
		if err := w.deliverRecipient(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// deliverRecipient delivers one recipient's mail when its session can take a
// turn and the batching rule allows it.
func (w *teamWake) deliverRecipient(ctx context.Context, r vendorstore.MailboxRecipient) error {
	sessionID, task, err := w.resolveRecipient(ctx, r)
	if err != nil || task == nil {
		return err
	}
	session, err := w.deps.Store.Snapshot(ctx, sessionID)
	if errors.Is(err, vendorstore.ErrSessionNotFound) {
		w.pruneSession(ctx, sessionID)
		return nil
	}
	if err != nil {
		return boundaryError("read the recipient session", err)
	}
	if session.State == vendorstore.StateClosed {
		w.pruneSession(ctx, sessionID)
		return nil
	}
	sendable := w.prepareSession(ctx, r, &session)
	if !sendable {
		return nil
	}
	if !r.HasImmediate && task.HierarchyRole != vendorstore.RoleMember {
		ready, err := w.digestReady(ctx, r, task)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
	}
	return w.deliverBatch(ctx, r, sessionID)
}

// resolveRecipient returns the session and task row a recipient delivers to.
func (w *teamWake) resolveRecipient(ctx context.Context, r vendorstore.MailboxRecipient) (string, *vendorstore.TaskRecord, error) {
	task, err := w.recipientTask(ctx, r.TeamID, r.RecipientTaskID)
	if err != nil {
		return "", nil, err
	}
	if task == nil {
		return "", nil, nil
	}
	sessionID, err := w.recipientSessionID(ctx, r.TeamID, task)
	if err != nil {
		return "", nil, err
	}
	return sessionID, task, nil
}

// prepareSession reports whether the recipient session can take its turn
// now (Q-123): a detached session is resumed first and delivers on the next
// trigger; starting, busy, waiting, closed and unmeasured agy sessions hold
// their mail.
func (w *teamWake) prepareSession(ctx context.Context, r vendorstore.MailboxRecipient, session *vendorstore.Session) bool {
	if session.Vendor == VendorAGY {
		w.markHeld(ctx, &HeldMailboxEntry{
			TeamID: r.TeamID, TaskID: r.RecipientTaskID, SessionID: session.ID,
			Vendor: session.Vendor, Reason: "agy_wake_unmeasured_until_f11_6",
		})
		return false
	}
	w.clearHeld(ctx, session.ID)
	if session.State == vendorstore.StateDetached {
		w.resumeDetached(ctx, session)
		return false
	}
	if session.State == vendorstore.StateReady {
		delete(w.resumeRetryAt, session.ID)
		delete(w.resumeAttempts, session.ID)
		return true
	}
	return false
}

// recipientSessionID resolves the session a recipient task delivers to: its
// own binding, or the proposing session while the dispatcher has not bound
// the lead yet (Q-117 — they are the same session).
func (w *teamWake) recipientSessionID(ctx context.Context, teamID string, task *vendorstore.TaskRecord) (string, error) {
	if task.SessionID != "" {
		return task.SessionID, nil
	}
	rec, _, err := w.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return "", boundaryError("read the mailbox team", err)
	}
	return rec.SessionID, nil
}

// resumeDetached attaches a fresh process to a detached recipient session
// (Q-123). A locked vault holds the mail; a failed resume holds it too and is
// retried by the next trigger. TODO(F11.6): measure agy idle-send and
// session/load resume with a logged-in agy, then enable; until measured the
// fail-closed hold covers an unmeasured resume (Q-123).
func (w *teamWake) resumeDetached(ctx context.Context, session *vendorstore.Session) {
	if session.Vendor == VendorAGY {
		return
	}
	if until, ok := w.resumeRetryAt[session.ID]; ok && w.deps.Clock.Now().Before(until) {
		return
	}
	cwd, err := w.sessionCwd(ctx, session)
	if err != nil {
		w.backoffResume(session.ID)
		slog.WarnContext(ctx, "deriving a detached session cwd failed", "session_id", session.ID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if err := w.deps.Sessions.ResumeTaskSession(ctx, session.ID, cwd); err != nil {
		w.backoffResume(session.ID)
		slog.WarnContext(ctx, "resuming a detached session failed", "session_id", session.ID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	// Arm backoff on resume too so a session that immediately falls back to
	// detached is not retried on every trigger; reaching StateReady clears it.
	w.backoffResume(session.ID)
}

func (w *teamWake) backoffResume(sessionID string) {
	attempts := w.resumeAttempts[sessionID]
	delay := resumeBackoff << min(attempts, 4)
	w.resumeRetryAt[sessionID] = w.deps.Clock.Now().Add(delay)
	w.resumeAttempts[sessionID] = attempts + 1
}

func (w *teamWake) pruneSession(ctx context.Context, sessionID string) {
	delete(w.resumeRetryAt, sessionID)
	delete(w.resumeAttempts, sessionID)
	w.clearHeld(ctx, sessionID)
}

// sessionCwd derives the working directory of a detached session host-side
// (Q-119): its task worktree, or the workspace root of the proposing session.
func (w *teamWake) sessionCwd(ctx context.Context, session *vendorstore.Session) (string, error) {
	root, err := w.deps.Roots.SelectSessionCwd(ctx, session.WorkspaceID, session.WorkspaceRoot)
	if err != nil {
		return "", boundaryError("resolve the session workspace root", err)
	}
	if session.TaskID == "" {
		return root, nil
	}
	repoID := repoIDFor(root)
	path, err := gitops.TaskWorktreePath(w.deps.DataDir, repoID, gitTaskID(session.TaskID))
	if err != nil {
		return "", boundaryError("derive the resumed session worktree path", err)
	}
	return path, nil
}

// digestReady reports whether a lead or manager digest may go out: the 30 s
// window ended, or every direct child of the recipient is terminal (§6.1).
func (w *teamWake) digestReady(ctx context.Context, r vendorstore.MailboxRecipient, task *vendorstore.TaskRecord) (bool, error) {
	if w.deps.Clock.Now().UnixMilli() >= r.EarliestPending+digestWait.Milliseconds() {
		return true, nil
	}
	_, tasks, err := w.deps.Store.TeamSnapshot(ctx, r.TeamID)
	if err != nil {
		return false, boundaryError("read the dependency group", err)
	}
	for i := range tasks {
		c := &tasks[i]
		if c.ParentTaskID != task.TaskID || teamTaskTerminal(c.State) {
			continue
		}
		return false, nil
	}
	// Every direct child is terminal (or there are none): nothing can still
	// append to this digest.
	return true, nil
}

// teamTaskTerminal reports a terminal task state (§3.1); unknown is not
// terminal.
func teamTaskTerminal(state vendorstore.TaskState) bool {
	return state == vendorstore.TaskStateDone || state == vendorstore.TaskStateFailed ||
		state == vendorstore.TaskStateStopped || state == vendorstore.TaskStateCancelled
}

// deliverBatch claims the recipient's pending rows and sends them as one
// Fugue-origin turn (Q-122). A refused send releases the claim and waits for
// the next trigger.
func (w *teamWake) deliverBatch(ctx context.Context, r vendorstore.MailboxRecipient, sessionID string) error {
	claimed, err := w.deps.Store.ClaimMailbox(ctx, r.TeamID, r.RecipientTaskID)
	if err != nil {
		return boundaryError("claim the mailbox", err)
	}
	if len(claimed) == 0 {
		return nil
	}
	batchID := claimed[0].BatchID
	text, err := w.batchText(ctx, claimed)
	if err != nil {
		if releaseErr := w.deps.Store.ReleaseMailboxBatch(ctx, batchID); releaseErr != nil {
			return boundaryError("release the mailbox batch", releaseErr)
		}
		return err
	}
	if err := w.deps.Sessions.Send(ctx, sessionID, vendor.Input{Text: text}); err != nil {
		if !refusedSend(err) {
			// The outcome is unknown: the turn may have reached the vendor.
			// The claim stays `delivering` and only the transcript recovery
			// resolves it — never re-send an unknown effect (Q-122).
			slog.WarnContext(ctx, "the mailbox send outcome is unknown", "team_id", r.TeamID, "batch_id", batchID, "error_code", string(errcode.CodeOf(err)))
			return nil
		}
		if releaseErr := w.deps.Store.ReleaseMailboxBatch(ctx, batchID); releaseErr != nil {
			return boundaryError("release the mailbox batch", releaseErr)
		}
		slog.WarnContext(ctx, "sending the mailbox turn was refused", "team_id", r.TeamID, "batch_id", batchID, "error_code", string(errcode.CodeOf(err)))
		return nil
	}
	if err := w.deps.Store.MarkMailboxBatchDelivered(ctx, batchID); err != nil {
		return boundaryError("mark the mailbox batch delivered", err)
	}
	return nil
}

// recipientSession resolves the session a mailbox recipient delivers to.
func (w *teamWake) recipientSession(ctx context.Context, teamID, recipientTaskID string) (string, error) {
	task, err := w.recipientTask(ctx, teamID, recipientTaskID)
	if err != nil || task == nil {
		return "", err
	}
	return w.recipientSessionID(ctx, teamID, task)
}

// errRecipientTaskGone holds the delivery when the recipient task no longer
// exists: the team moved on, and there is nobody to deliver to.
var errRecipientTaskGone = errcode.New(errcode.FailedPrecondition, "the mailbox recipient task no longer exists")

// recipientTask reads one task row of the team; a missing task fails typed.
func (w *teamWake) recipientTask(ctx context.Context, teamID, recipientTaskID string) (*vendorstore.TaskRecord, error) {
	_, tasks, err := w.deps.Store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return nil, boundaryError("read the mailbox team", err)
	}
	for i := range tasks {
		if tasks[i].TaskID == recipientTaskID {
			return &tasks[i], nil
		}
	}
	return nil, errRecipientTaskGone
}
