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
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// digestWait is the §6.1 batching window of a lead or manager digest.
const digestWait = 30 * time.Second

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

// mailboxDigestItem is the sealed payload of one digest row. Summary stays
// empty until the task tools report one (F11.3c); the state is Fugue's own.
// teamWakeDeps are the ports of the wake pump.
type teamWakeDeps struct {
	Store    *vendorstore.Store
	Sessions taskSessions
	Roots    rootResolver
	Clock    clock.Clock
	Vault    *vault.Service
	// DataDir roots the task worktrees of a detached session's cwd (Q-119).
	DataDir string
}

// teamWake delivers the team mailbox. One run at a time: the mutex serializes
// pumps so two triggers never claim one recipient twice.
type teamWake struct {
	deps teamWakeDeps
	mu   sync.Mutex
	wake chan struct{}
}

func newTeamWake(deps *teamWakeDeps) *teamWake {
	return &teamWake{deps: *deps, wake: make(chan struct{}, 1)}
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
	w.recoverAsync(ctx)
	w.pumpAsync(ctx)
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
		w.recoverAsync(ctx)
		w.pumpAsync(ctx)
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
		if ctx.Err() != nil {
			return ctx.Err()
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
	task, err := w.recipientTask(ctx, r.TeamID, r.RecipientTaskID)
	if err != nil || task == nil {
		return err
	}
	sessionID, err := w.recipientSessionID(ctx, r.TeamID, task)
	if err != nil || sessionID == "" {
		return err
	}
	session, err := w.deps.Store.Snapshot(ctx, sessionID)
	if errors.Is(err, vendorstore.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return boundaryError("read the recipient session", err)
	}
	switch session.State {
	case vendorstore.StateReady:
		// sendable below
	case vendorstore.StateDetached:
		if err := w.resumeDetached(ctx, &session); err != nil {
			return err
		}
		return nil // the resumed session takes its mail on the next trigger
	default:
		return nil // starting, busy, waiting, closed: hold (Q-123)
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
// retried by the next trigger.
func (w *teamWake) resumeDetached(ctx context.Context, session *vendorstore.Session) error {
	cwd, err := w.sessionCwd(ctx, session)
	if err != nil {
		return err
	}
	if err := w.deps.Sessions.ResumeTaskSession(ctx, session.ID, cwd); err != nil {
		slog.WarnContext(ctx, "resuming a detached session failed", "session_id", session.ID, "error_code", string(errcode.CodeOf(err)))
	}
	return nil
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
	return gitops.TaskWorktreePath(w.deps.DataDir, repoID, session.TaskID)
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
	switch state {
	case vendorstore.TaskStateDone, vendorstore.TaskStateFailed,
		vendorstore.TaskStateStopped, vendorstore.TaskStateCancelled:
		return true
	}
	return false
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
			return releaseErr
		}
		return err
	}
	if err := w.deps.Sessions.Send(ctx, sessionID, vendor.Input{Text: text}); err != nil {
		if releaseErr := w.deps.Store.ReleaseMailboxBatch(ctx, batchID); releaseErr != nil {
			return releaseErr
		}
		slog.WarnContext(ctx, "sending the mailbox turn failed", "team_id", r.TeamID, "batch_id", batchID, "error_code", string(errcode.CodeOf(err)))
		return nil
	}
	return w.deps.Store.MarkMailboxBatchDelivered(ctx, batchID)
}

// recipientSession resolves the session a mailbox recipient delivers to.
func (w *teamWake) recipientSession(ctx context.Context, teamID, recipientTaskID string) (string, error) {
	task, err := w.recipientTask(ctx, teamID, recipientTaskID)
	if err != nil || task == nil {
		return "", err
	}
	return w.recipientSessionID(ctx, teamID, task)
}

// recipientTask reads one task row of the team; a missing task yields nil.
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
	return nil, nil
}
