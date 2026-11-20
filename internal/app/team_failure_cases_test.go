package app

// The F11.9 §12 failure-case evidence: an unexpected mid-turn process exit
// moves the bound team task to unknown and notifies its direct delegator,
// while every clean exit path (stop, close, detach, idle detach, task end,
// host shutdown) never does.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	exitTeamID     = "team-exit"
	exitLeadTask   = "ex-lead"
	exitMemTask    = "ex-mem"
	exitCleanTask  = "ex-clean"
	exitEndTask    = "ex-end"
	exitIdleTask   = "ex-idle"
	exitLeadSess   = "ex-lead-sess"
	exitMemSess    = "ex-mem-sess"
	exitCleanSess  = "ex-clean-sess"
	exitEndSess    = "ex-end-sess"
	exitIdleSess   = "ex-idle-sess"
	exitResumeHnd  = "888e4567-e89b-12d3-a456-426614174000"
	exitDirtyChunk = "uncommitted work"
)

// exitDiedHook records the session-died hook calls and runs the production
// handler synchronously so the assertions see its effects deterministically.
type exitDiedHook struct {
	mu      sync.Mutex
	calls   []string
	handler func(context.Context, string)
}

func (h *exitDiedHook) bind(m *VendorSessions, tasks *teamTaskService) {
	h.handler = tasks.OnSessionDied
	m.SetSessionDiedHook(h.call)
}

func (h *exitDiedHook) call(sessionID string) {
	h.mu.Lock()
	handler := h.handler
	h.mu.Unlock()
	if handler != nil {
		// Recording after the handler returns is what makes the hook's
		// effects observable: a recorded call means the whole OnSessionDied
		// run, its event append and its notice enqueue have landed.
		handler(context.Background(), sessionID)
	}
	h.mu.Lock()
	h.calls = append(h.calls, sessionID)
	h.mu.Unlock()
}

func (h *exitDiedHook) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// exitHarness is a real vendor session layer over a fake adapter with a
// controllable observations channel, a real store holding an active team, and
// the real teamTaskService behind the died hook.
type exitHarness struct {
	f       *transcriptFixture
	ad      *pdAdapter
	m       *VendorSessions
	tasks   *teamTaskService
	hook    *exitDiedHook
	git     *gitops.Manager
	dataDir string
	repoDir string
	idleSec *int
}

func newExitHarness(t *testing.T) *exitHarness {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	dataDir := t.TempDir()
	repoDir := t.TempDir()
	initRepo(t, repoDir)
	gm, err := gitops.NewManager(gitBinary(t), dataDir, clock.NewFake(time.Unix(400, 0)))
	if err != nil {
		t.Fatal(err)
	}
	idleSecs := 0
	ad := newPDAdapter(f.clock, vendor.Capability{Resume: true}, nil)
	m := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad, Vault: f.vault, Clock: f.clock,
		IDs: ids.NewSequence("exit-"),
		TeamsConfig: func(context.Context, string) (workspace.TeamsConfig, error) {
			return workspace.TeamsConfig{IdleDetachSeconds: idleSecs}, nil
		},
	})
	tasks := newTeamTaskService(f.store, nil, pdRootResolver{dir: repoDir}, gm, dataDir,
		ids.NewSequence("exit-task-"), f.vault, nil)
	tasks.SetClock(f.clock)
	hook := &exitDiedHook{}
	hook.bind(m, tasks)
	h := &exitHarness{f: f, ad: ad, m: m, tasks: tasks, hook: hook, git: gm, dataDir: dataDir, repoDir: repoDir, idleSec: &idleSecs}
	seedExitTeam(t, h)
	return h
}

// exitTeamTasks builds the seeded team's task list through a loop over a
// spec table so the fixture shares no literal block with the other seeds.
func exitTeamTasks(spec []byte) []vendorstore.NewTaskInput {
	tasks := make([]vendorstore.NewTaskInput, 0, 4)
	tasks = append(tasks, vendorstore.NewTaskInput{
		TaskID: exitLeadTask, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole,
		Deliverable: vendorstore.DeliverableReport, CreatedEventID: "ex-c1", ProtectedSpec: spec,
	})
	for _, m := range []struct {
		id, event   string
		deliverable vendorstore.TeamDeliverable
	}{
		{exitMemTask, "ex-c2", vendorstore.DeliverableDiff},
		{exitCleanTask, "ex-c3", vendorstore.DeliverableDiff},
		{exitEndTask, "ex-c4", vendorstore.DeliverableReport},
	} {
		tasks = append(tasks, vendorstore.NewTaskInput{
			TaskID: m.id, ParentTaskID: exitLeadTask, HierarchyRole: vendorstore.RoleMember,
			CatalogRole: teamTestRole, Deliverable: m.deliverable, CreatedEventID: m.event, ProtectedSpec: spec,
		})
	}
	return tasks
}

// seedExitTeam creates the active team with one lead and three member tasks
// and binds the lead and two members to live sessions.
func seedExitTeam(t *testing.T, h *exitHarness) {
	t.Helper()
	ctx := t.Context()
	spec := []byte(`{"goal":"exit case"}`)
	rec, _, err := h.f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: exitTeamID, WorkspaceID: pdWorkspaceID, SessionID: exitLeadSess, ProposedEventID: "ex-prop",
		Tasks: exitTeamTasks(spec),
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := []vendorstore.AppendTeamEventInput{
		{EventID: "ex-start", EventType: vendorstore.EventTeamStarted},
		{EventID: "ex-a-l", TaskID: exitLeadTask, EventType: vendorstore.EventTaskAssigned, SessionID: exitLeadSess},
		{EventID: "ex-s-l", TaskID: exitLeadTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "ex-a-m", TaskID: exitMemTask, EventType: vendorstore.EventTaskAssigned, SessionID: exitMemSess},
		{EventID: "ex-s-m", TaskID: exitMemTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "ex-a-c", TaskID: exitCleanTask, EventType: vendorstore.EventTaskAssigned, SessionID: exitCleanSess},
		{EventID: "ex-s-c", TaskID: exitCleanTask, EventType: vendorstore.EventTaskStarted},
		{EventID: "ex-a-e", TaskID: exitEndTask, EventType: vendorstore.EventTaskAssigned, SessionID: exitEndSess},
		{EventID: "ex-s-e", TaskID: exitEndTask, EventType: vendorstore.EventTaskStarted},
	}
	seq := rec.LastSeq
	for i := range evs {
		if seq, err = h.f.store.AppendTeamEvent(ctx, exitTeamID, seq, &evs[i]); err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", evs[i].EventID, err)
		}
	}
	head, err := h.git.WorktreeHead(ctx, h.repoDir)
	if err != nil || head == "" {
		t.Fatalf("WorktreeHead: %q, %v", head, err)
	}
	if err = h.git.CreateTeamBranch(ctx, h.repoDir, exitTeamID, head); err != nil {
		t.Fatal(err)
	}
	teamBranch, err := gitops.TeamBranchName(exitTeamID)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := h.git.CreateTaskWorktree(ctx, h.dataDir, h.repoDir, repoIDFor(h.repoDir), gitTaskID(exitMemTask), teamBranch)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(wt, "wip.txt"), []byte(exitDirtyChunk), 0o600); err != nil {
		t.Fatal(err)
	}
	startPDSession(t, h.m, exitLeadSess, VendorClaude, vendorstore.RoleLead, exitTeamID, exitLeadTask, "")
	startPDSession(t, h.m, exitMemSess, VendorClaude, vendorstore.RoleMember, exitTeamID, exitMemTask, exitResumeHnd)
	startPDSession(t, h.m, exitCleanSess, VendorClaude, vendorstore.RoleMember, exitTeamID, exitCleanTask, exitResumeHnd)
	startPDSession(t, h.m, exitEndSess, VendorClaude, vendorstore.RoleMember, exitTeamID, exitEndTask, exitResumeHnd)
	for _, sid := range []string{exitLeadSess, exitMemSess, exitCleanSess, exitEndSess} {
		awaitPDReady(ctx, t, h.f.store, sid, 0)
	}
}

// exitHoldTurn starts a live user-origin turn on a session so the process is
// mid-turn when it dies.
func exitHoldTurn(t *testing.T, h *exitHarness, sessionID string) {
	t.Helper()
	if err := h.m.Send(t.Context(), sessionID, vendor.Input{Text: "hold while working", Origin: vendor.OriginUser}); err != nil {
		t.Fatalf("Send(%s): %v", sessionID, err)
	}
}

// exitCloseObservations ends the adapter process: the observations channel
// closing is what the actor sees as an unexpected process exit.
func exitCloseObservations(h *exitHarness, sessionID string) {
	sess := h.ad.session(sessionID)
	if sess != nil {
		close(sess.observations)
	}
}

func exitTaskState(t *testing.T, h *exitHarness, taskID string) vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := h.f.store.TeamSnapshot(t.Context(), exitTeamID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i]
		}
	}
	t.Fatalf("task %s not found", taskID)
	return vendorstore.TaskRecord{}
}

func exitMarkedUnknownEvents(t *testing.T, h *exitHarness) []vendorstore.RecordedTeamEvent {
	t.Helper()
	evs, err := h.f.store.TeamEventsSince(t.Context(), exitTeamID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []vendorstore.RecordedTeamEvent
	for i := range evs {
		if evs[i].EventType == vendorstore.EventTaskMarkedUnknown {
			out = append(out, evs[i])
		}
	}
	return out
}

// TestAgentExitMidTurnMarksTaskUnknownAndNotifiesDelegator pins §12 (F11.9):
// killing the vendor process mid-turn moves the bound task to unknown with a
// delegator notice that carries the worktree snapshot, the lead task follows
// the same path without a notice, and the clean exits — requested close,
// detach, idle detach, a task that already ended, and host shutdown — never
// mark a task unknown. A task marked unknown is never rerun.
func TestAgentExitMidTurnMarksTaskUnknownAndNotifiesDelegator(t *testing.T) {
	t.Parallel()
	h := newExitHarness(t)
	defer mustCloseExitSessions(t, h)

	markedSeq := assertExitUnknownMemberAndNotice(t, h)
	assertExitUnknownLeadWithoutNotice(t, h)
	assertCleanCloseKeepsTask(t, h)
	assertEndedTaskStaysUntouched(t, h)
	assertIdleDetachKeepsTask(t, h)
	assertHostShutdownKeepsTasks(t, h, markedSeq)
}

// assertExitUnknownMemberAndNotice kills the member's process mid-turn and
// asserts the task went unknown with the delegator notice carrying the
// worktree snapshot under the unlock recovery's idempotent key. It returns
// the TaskMarkedUnknown sequence.
func assertExitUnknownMemberAndNotice(t *testing.T, h *exitHarness) int64 {
	t.Helper()
	exitHoldTurn(t, h, exitMemSess)
	exitCloseObservations(h, exitMemSess)

	testutil.Spin(t, "member task marked unknown after its process died", func() bool {
		return exitTaskState(t, h, exitMemTask).State == vendorstore.TaskStateUnknown
	})
	mem := exitTaskState(t, h, exitMemTask)
	if mem.SessionID != exitMemSess {
		t.Fatalf("unknown member session binding = %q, want the dead session kept (never rerun)", mem.SessionID)
	}
	markedSeq := exitTaskMarkedSeq(t, h, exitMemTask)
	// The recorded hook call proves OnSessionDied completed: the notice is
	// durable by the time this Spin returns.
	testutil.Spin(t, "session-died handler completed for the member", func() bool {
		return len(h.hook.snapshot()) >= 1
	})
	raw, err := h.f.store.OpenMailboxPayload(t.Context(), exitTeamID, fmt.Sprintf("%s:unknown-notice:%s:%d:%s", exitTeamID, exitMemTask, markedSeq, exitLeadTask))
	if err != nil {
		t.Fatalf("delegator notice not enqueued: %v", err)
	}
	if !strings.Contains(string(raw), "dirty") || !strings.Contains(string(raw), "fugue_task_recover") {
		t.Fatalf("notice payload = %q, want the worktree snapshot and the recover hint", raw)
	}
	if pending, pendErr := h.f.store.PendingMailbox(t.Context()); pendErr != nil || len(pending) != 1 {
		t.Fatalf("PendingMailbox = (%+v, %v), want exactly the member notice", pending, pendErr)
	}
	return markedSeq
}

// exitTaskMarkedSeq returns the sequence of the task's TaskMarkedUnknown
// event, asserting its from-state is running.
func exitTaskMarkedSeq(t *testing.T, h *exitHarness, taskID string) int64 {
	t.Helper()
	events := exitMarkedUnknownEvents(t, h)
	for i := range events {
		if events[i].TaskID != taskID {
			continue
		}
		if events[i].FromState != string(vendorstore.TaskStateRunning) {
			t.Fatalf("TaskMarkedUnknown from_state = %q, want running", events[i].FromState)
		}
		return events[i].Sequence
	}
	t.Fatalf("missing TaskMarkedUnknown for %s", taskID)
	return 0
}

// assertExitUnknownLeadWithoutNotice kills the lead's process mid-turn: its
// task goes unknown but it has no delegator, so no notice is enqueued.
func assertExitUnknownLeadWithoutNotice(t *testing.T, h *exitHarness) {
	t.Helper()
	exitHoldTurn(t, h, exitLeadSess)
	exitCloseObservations(h, exitLeadSess)
	testutil.Spin(t, "session-died handler completed for the lead", func() bool {
		return exitTaskState(t, h, exitLeadTask).State == vendorstore.TaskStateUnknown && len(h.hook.snapshot()) == 2
	})
	if pending, pendErr := h.f.store.PendingMailbox(t.Context()); pendErr != nil || len(pending) != 1 {
		t.Fatalf("PendingMailbox after lead death = (%+v, %v), want no lead notice", pending, pendErr)
	}
	if events := exitMarkedUnknownEvents(t, h); len(events) != 2 {
		t.Fatalf("TaskMarkedUnknown events = %d, want exactly the member and the lead", len(events))
	}
}

// assertCleanCloseKeepsTask pins that a clean close mid-turn never marks the
// task unknown.
func assertCleanCloseKeepsTask(t *testing.T, h *exitHarness) {
	t.Helper()
	exitHoldTurn(t, h, exitCleanSess)
	if err := h.m.Close(t.Context(), exitCleanSess, vendor.CloseHostStop); err != nil {
		t.Fatalf("clean Close: %v", err)
	}
	if st := exitTaskState(t, h, exitCleanTask).State; st != vendorstore.TaskStateRunning {
		t.Fatalf("cleanly closed task state = %s, want running", st)
	}
	if got := len(exitMarkedUnknownEvents(t, h)); got != 2 {
		t.Fatalf("TaskMarkedUnknown events after the clean close = %d, want 2", got)
	}
}

// assertEndedTaskStaysUntouched pins that a task that already ended is not
// marked unknown by its session exit.
func assertEndedTaskStaysUntouched(t *testing.T, h *exitHarness) {
	t.Helper()
	if _, evErr := appendExitEvent(t, h, exitEndTask, vendorstore.EventTaskReported, ""); evErr != nil {
		t.Fatal(evErr)
	}
	h.tasks.OnSessionDied(t.Context(), exitEndSess)
	if st := exitTaskState(t, h, exitEndTask).State; st != vendorstore.TaskStateChecking {
		t.Fatalf("ended task state = %s, want checking untouched", st)
	}
	if got := len(exitMarkedUnknownEvents(t, h)); got != 2 {
		t.Fatalf("TaskMarkedUnknown events after the ended task = %d, want 2", got)
	}
}

// assertIdleDetachKeepsTask pins that the F11.6b idle detach never marks the
// task unknown.
func assertIdleDetachKeepsTask(t *testing.T, h *exitHarness) {
	t.Helper()
	*h.idleSec = 2
	startPDSession(t, h.m, exitIdleSess, VendorClaude, vendorstore.RoleMember, exitTeamID, "", exitResumeHnd)
	awaitPDReady(t.Context(), t, h.f.store, exitIdleSess, 0)
	testutil.Spin(t, "idle session detached", func() bool {
		h.f.clock.Advance(time.Second)
		snap, snapErr := h.f.store.Snapshot(t.Context(), exitIdleSess)
		return snapErr == nil && snap.State == vendorstore.StateDetached
	})
	if got := len(exitMarkedUnknownEvents(t, h)); got != 2 {
		t.Fatalf("TaskMarkedUnknown events after idle detach = %d, want 2", got)
	}
}

// assertHostShutdownKeepsTasks pins that the manager-wide shutdown closes
// every session through the requested-stop path without marking unknown.
func assertHostShutdownKeepsTasks(t *testing.T, h *exitHarness, markedSeq int64) {
	t.Helper()
	if err := h.m.CloseAll(t.Context()); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	if got := len(exitMarkedUnknownEvents(t, h)); got != 2 {
		t.Fatalf("TaskMarkedUnknown events after host shutdown = %d, want 2", got)
	}
	if died := h.hook.snapshot(); len(died) != 2 || died[0] != exitMemSess || died[1] != exitLeadSess {
		t.Fatalf("session-died hook calls = %v, want exactly the two unexpected exits", died)
	}
	if markedSeq <= 0 {
		t.Fatal("the member notice lost its idempotent sequence")
	}
}

func appendExitEvent(t *testing.T, h *exitHarness, taskID string, evType vendorstore.TeamEventType, sessionID string) (int64, error) {
	t.Helper()
	rec, _, err := h.f.store.TeamSnapshot(t.Context(), exitTeamID)
	if err != nil {
		return 0, fmt.Errorf("read the exit team: %w", err)
	}
	seq, appErr := h.f.store.AppendTeamEvent(t.Context(), exitTeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "ex-pin-" + taskID + "-" + string(evType), TaskID: taskID, EventType: evType, SessionID: sessionID,
	})
	if appErr != nil {
		return 0, fmt.Errorf("append %s for %s: %w", evType, taskID, appErr)
	}
	return seq, nil
}

// mustCloseExitSessions closes the manager while advancing the fake clock so
// the idle timers of the detach pin cannot deadlock the close.
func mustCloseExitSessions(t *testing.T, h *exitHarness) {
	t.Helper()
	done := make(chan struct{})
	var closeErr error
	go func() {
		defer close(done)
		closeErr = h.m.CloseAll(context.Background())
	}()
	testutil.Spin(t, "close exit sessions", func() bool {
		h.f.clock.Advance(time.Millisecond)
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if closeErr != nil {
		t.Errorf("close exit sessions: %v", closeErr)
	}
}

// TestSessionDiedHookFiresOnlyOnUnexpectedExit pins the actor half of §12
// (F11.9): the session-died hook runs once for an exit with no requested
// stop, and never for a clean close, a detach, or the manager-wide host
// shutdown.
func TestSessionDiedHookFiresOnlyOnUnexpectedExit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	ad := newPDAdapter(f.clock, vendor.Capability{Resume: true}, nil)
	m := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad, Vault: f.vault, Clock: f.clock,
		IDs: ids.NewSequence("died-"),
	})
	var mu sync.Mutex
	var died []string
	m.SetSessionDiedHook(func(sessionID string) {
		mu.Lock()
		died = append(died, sessionID)
		mu.Unlock()
	})

	startPDSession(t, m, "died-exit", VendorClaude, vendorstore.RoleMember, "", "", "")
	if err := m.Send(ctx, "died-exit", vendor.Input{Text: "mid-turn", Origin: vendor.OriginUser}); err != nil {
		t.Fatal(err)
	}
	close(ad.session("died-exit").observations)
	testutil.Spin(t, "unexpected exit fired the hook", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(died) == 1
	})

	// A requested close mid-turn: the close reason is set before the process
	// stops, so the exit is clean and no hook fires.
	startPDSession(t, m, "died-close", VendorClaude, vendorstore.RoleMember, "", "", "")
	if err := m.Send(ctx, "died-close", vendor.Input{Text: "mid-turn", Origin: vendor.OriginUser}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx, "died-close", vendor.CloseHostStop); err != nil {
		t.Fatal(err)
	}
	close(ad.session("died-close").observations)

	// A clean detach of an idle session: the process stops under the detach
	// path and no hook fires.
	startPDSession(t, m, "died-detach", VendorClaude, vendorstore.RoleMember, "", "", "999e4567-e89b-12d3-a456-426614174000")
	awaitPDReady(ctx, t, f.store, "died-detach", 0)
	if err := m.Detach(ctx, "died-detach"); err != nil {
		t.Fatal(err)
	}
	close(ad.session("died-detach").observations)

	// Host shutdown: CloseAll requests every stop, so no hook fires.
	startPDSession(t, m, "died-stop", VendorClaude, vendorstore.RoleMember, "", "", "")
	awaitPDReady(ctx, t, f.store, "died-stop", 0)
	if err := m.CloseAll(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(died) != 1 || died[0] != "died-exit" {
		t.Fatalf("hook calls = %v, want only the unexpected exit", died)
	}
}
