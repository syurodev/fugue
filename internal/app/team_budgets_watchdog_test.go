package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const testBudgetLeadGoal = "Coordinate budget test tasks"

type budgetsTestFixture struct {
	*dispatchFixture
	budgets *teamBudgets
	tasks   *teamTaskService
}

type budgetsPassGate struct{}

func (budgetsPassGate) Evaluate(context.Context, string, string) (ResultGateVerdict, error) {
	return ResultGateVerdict{Ready: true, Passed: true, Summary: "ok"}, nil
}

func newBudgetsTestFixture(t *testing.T, tomlExtra string) *budgetsTestFixture {
	t.Helper()
	f := newTeamProposalFixture(t, tomlExtra)
	initRepo(t, f.root)
	dataDir := filepath.Join(t.TempDir(), "data")
	gitMgr, err := gitops.NewManager(gitBinary(t), dataDir, f.clk)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTaskSessions{}
	teamsCfg := globalOrWorkspaceTeamsConfigReader(f.rt.workspaces)
	b := newTeamBudgets(&teamBudgetsDeps{
		Store: f.hp.vendorStore, Sessions: fake, Interactions: f.rt.interactions,
		TeamsConfig: teamsCfg, Vault: f.hp.vault, Clock: f.clk,
		IDs: ids.NewSequence("bgt-"), Wake: f.rt.teamWake,
	})
	f.rt.interactions.SetTeamDeadlineResolver(b)
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.hp.vendorStore, Sessions: fake, Admit: localAdmitter{capacity: fake},
		Roots: f.rt.workspaces.domain, Git: gitMgr, DataDir: dataDir,
		IDs: ids.NewSequence("disp-"), Vault: f.hp.vault, Clock: f.clk,
		TeamsConfig: teamsCfg, Budgets: b,
	})
	b.deps.OnTeamResume = func(teamID string) {
		if dispErr := disp.Dispatch(t.Context(), teamID); dispErr != nil {
			t.Errorf("OnTeamResume dispatch: %v", dispErr)
		}
	}
	f.rt.teamProposals.SetClock(f.clk)
	f.rt.teamProposals.SetOnTimerArmed(b.Notify)
	tt := newTeamTaskService(f.hp.vendorStore, fake, f.rt.workspaces.domain, gitMgr, dataDir, ids.NewSequence("tt-"), f.hp.vault, budgetsPassGate{})
	tt.SetRuntime(disp, f.rt.teamWake)
	tt.SetTeamsConfig(teamsCfg)
	tt.SetBudgets(b)
	return &budgetsTestFixture{
		dispatchFixture: &dispatchFixture{teamProposalFixture: f, sessions: fake, disp: disp, dataDir: dataDir},
		budgets:         b, tasks: tt,
	}
}

func createFakeSessionTurns(t *testing.T, store *vendorstore.Store, sessionID, teamID, taskID string, count int) {
	t.Helper()
	err := store.CreateSession(t.Context(), &vendorstore.Session{
		ID: sessionID, Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleMember), State: vendorstore.StateReady,
		TeamID: teamID, TaskID: taskID,
	})
	if err != nil {
		t.Fatalf("CreateSession(%s): %v", sessionID, err)
	}
	snap, err := store.Snapshot(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("Snapshot(%s): %v", sessionID, err)
	}
	seq := snap.LastSequence
	for i := 1; i <= count; i++ {
		next, appErr := store.Append(t.Context(), sessionID, seq, &vendorstore.Event{
			Type: vendorstore.VendorTurnStarted, TurnID: sessionID + "-t-" + string(rune('0'+i)),
			TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateRunning,
		})
		if appErr != nil {
			t.Fatalf("Append turn %d: %v", i, appErr)
		}
		seq = next
	}
}

// TestTaskDeadlineAndTurnBudgetsInterruptAndFailKeepingWorktree verifies §11
// task deadline and turn budget enforcement (Q-149 A).
func TestTaskDeadlineAndTurnBudgetsInterruptAndFailKeepingWorktree(t *testing.T) {
	t.Parallel()
	f := newBudgetsTestFixture(t, "[teams]\ntask_deadline_seconds = 120\ntask_max_turns = 2\n")

	args := validTeamProposalArgs()
	args.Tasks = []teams.ProposedTask{
		{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testBudgetLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "deadline-worker", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Runs past deadline", WriteScope: []string{"a.txt"}, Deliverable: teams.DeliverableDiff},
		{ID: "turns-worker", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Runs past max turns", WriteScope: []string{"b.txt"}, Deliverable: teams.DeliverableDiff},
	}
	teamID := f.approve(t, args, decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadlineTaskID := durableTaskID(teamID, "deadline-worker")
	turnsTaskID := durableTaskID(teamID, "turns-worker")
	leadTaskID := durableTaskID(teamID, teamTestLeadTask)

	verifyTurnBudgetEnforcement(t, f, teamID, turnsTaskID)
	verifyTaskDeadlineEnforcement(t, f, teamID, deadlineTaskID)
	verifyBudgetInterruptsAndMailbox(t, f, teamID, leadTaskID, turnsTaskID, deadlineTaskID)
}

func (f *budgetsTestFixture) mustTask(t *testing.T, teamID, taskID string) *vendorstore.TaskRecord {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	cur, ok := findTask(tasks, taskID)
	if !ok {
		t.Fatalf("task %s not found", taskID)
	}
	return cur
}

func (f *budgetsTestFixture) assertTimerState(t *testing.T, teamID, taskID string, kind vendorstore.TeamTimerKind, want vendorstore.TeamTimerState) {
	t.Helper()
	tm, found, err := f.hp.vendorStore.GetTeamTimer(t.Context(), teamID, taskID, kind)
	if err != nil || !found || tm.State != want {
		t.Fatalf("timer (%s, %s, %s) = (%+v, %v, %v), want %s", teamID, taskID, kind, tm, found, err, want)
	}
}

func verifyTurnBudgetEnforcement(t *testing.T, f *budgetsTestFixture, teamID, turnsTaskID string) {
	t.Helper()
	turnsStart, ok := f.sessions.startFor(turnsTaskID)
	if !ok || turnsStart.Cwd == "" {
		t.Fatalf("turns-worker start not recorded: %+v", turnsStart)
	}
	createFakeSessionTurns(t, f.hp.vendorStore, turnsStart.SessionID, teamID, turnsTaskID, 2)
	f.budgets.OnTurnStarted(t.Context(), turnsStart.SessionID)
	if cur := f.mustTask(t, teamID, turnsTaskID); cur.State != vendorstore.TaskStateRunning {
		t.Fatalf("after 2 turns state = %s, want running", cur.State)
	}
	snapAfter2, err := f.hp.vendorStore.Snapshot(t.Context(), turnsStart.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, appErr := f.hp.vendorStore.Append(t.Context(), turnsStart.SessionID, snapAfter2.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: "turn-3", TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateRunning,
	}); appErr != nil {
		t.Fatal(appErr)
	}
	f.budgets.OnTurnStarted(t.Context(), turnsStart.SessionID)

	turnsTask := f.mustTask(t, teamID, turnsTaskID)
	if turnsTask.State != vendorstore.TaskStateFailed || turnsTask.FailureCode != taskFailureTurnBudgetExceeded {
		t.Fatalf("turns-worker = (%s, %s), want (failed, %s)", turnsTask.State, turnsTask.FailureCode, taskFailureTurnBudgetExceeded)
	}
	if _, statErr := os.Stat(turnsStart.Cwd); statErr != nil {
		t.Fatalf("turns-worker worktree removed (%s): %v", turnsStart.Cwd, statErr)
	}
	f.assertTimerState(t, teamID, turnsTaskID, vendorstore.TimerKindTaskDeadline, vendorstore.TimerStateCancelled)
}

func verifyTaskDeadlineEnforcement(t *testing.T, f *budgetsTestFixture, teamID, deadlineTaskID string) {
	t.Helper()
	deadlineStart, ok := f.sessions.startFor(deadlineTaskID)
	if !ok || deadlineStart.Cwd == "" {
		t.Fatalf("deadline-worker start not recorded: %+v", deadlineStart)
	}
	f.assertTimerState(t, teamID, deadlineTaskID, vendorstore.TimerKindTaskDeadline, vendorstore.TimerStateArmed)
	f.clk.Advance(121 * time.Second)
	f.budgets.Step(t.Context())

	dlTask := f.mustTask(t, teamID, deadlineTaskID)
	if dlTask.State != vendorstore.TaskStateFailed || dlTask.FailureCode != taskFailureDeadlineExceeded {
		t.Fatalf("deadline-worker = (%s, %s), want (failed, %s)", dlTask.State, dlTask.FailureCode, taskFailureDeadlineExceeded)
	}
	if _, statErr := os.Stat(deadlineStart.Cwd); statErr != nil {
		t.Fatalf("deadline-worker worktree removed (%s): %v", deadlineStart.Cwd, statErr)
	}
	f.assertTimerState(t, teamID, deadlineTaskID, vendorstore.TimerKindTaskDeadline, vendorstore.TimerStateFired)
}

func verifyBudgetInterruptsAndMailbox(t *testing.T, f *budgetsTestFixture, teamID, leadTaskID, turnsTaskID, deadlineTaskID string) {
	t.Helper()
	turnsStart, _ := f.sessions.startFor(turnsTaskID)
	deadlineStart, _ := f.sessions.startFor(deadlineTaskID)
	f.sessions.mu.Lock()
	interrupted := slices.Clone(f.sessions.interrupts)
	f.sessions.mu.Unlock()
	if !slices.Contains(interrupted, turnsStart.SessionID) || !slices.Contains(interrupted, deadlineStart.SessionID) {
		t.Fatalf("interrupts = %v, want both %s and %s", interrupted, turnsStart.SessionID, deadlineStart.SessionID)
	}
	mail, err := f.hp.vendorStore.ClaimMailbox(t.Context(), teamID, leadTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var gotCodes []string
	for i := range mail {
		raw, openErr := f.hp.vendorStore.OpenMailboxPayload(t.Context(), teamID, mail[i].MessageID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		var item mailboxDigestItem
		if json.Unmarshal(raw, &item) == nil && item.FailureCode != "" {
			gotCodes = append(gotCodes, item.FailureCode)
		}
	}
	if !slices.Contains(gotCodes, taskFailureTurnBudgetExceeded) || !slices.Contains(gotCodes, taskFailureDeadlineExceeded) {
		t.Fatalf("delegator mailbox failure codes = %v, want both budget failure codes", gotCodes)
	}
}

// TestTeamAgentCapAndDeadlineStopNewDispatches verifies §11 team agent cap
// (counting retries), optional TeamBudget on propose/amend, and team deadline
// pausing new dispatches without killing running tasks (Q-149 A).
func TestTeamAgentCapAndDeadlineStopNewDispatches(t *testing.T) {
	t.Parallel()
	f := newBudgetsTestFixture(t, "")

	verifyProposeRejectsOverAgentCap(t, f)
	teamID, w1RetryID, w2ID := verifyAgentCapRetryAndAmend(t, f)
	verifyTeamDeadlinePauseExtendAndCancel(t, f, teamID, w1RetryID, w2ID)
}

func verifyProposeRejectsOverAgentCap(t *testing.T, f *budgetsTestFixture) {
	t.Helper()
	tooMany := validTeamProposalArgs()
	tooMany.Tasks = []teams.ProposedTask{{
		ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testBudgetLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport,
	}}
	for i := 1; i <= 13; i++ {
		tooMany.Tasks = append(tooMany.Tasks, teams.ProposedTask{
			ID: "m" + string(rune('a'+i)), ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember,
			Role: teamTestRole, Goal: "Member", WriteScope: []string{}, Deliverable: teams.DeliverableReport,
		})
	}
	if _, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, tooMany); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Propose(13 non-lead agents) err = %v, want InvalidArgument", err)
	}
}

func verifyAgentCapRetryAndAmend(t *testing.T, f *budgetsTestFixture) (string, string, string) {
	t.Helper()
	args := validTeamProposalArgs()
	args.Budget = &teams.TeamBudget{MaxAgents: 2, MaxDurationSeconds: 300}
	args.Tasks = []teams.ProposedTask{
		{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testBudgetLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "w1", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "First worker", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "w2", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Second worker", WriteScope: []string{}, Deliverable: teams.DeliverableReport, DependsOn: []string{"w1"}},
	}
	teamID := f.approve(t, args, decisionApprove, "")
	w1ID, w2ID := durableTaskID(teamID, "w1"), durableTaskID(teamID, "w2")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch initial: %v", err)
	}
	w1Start, _ := f.sessions.startFor(w1ID)
	if _, err := f.tasks.Done(t.Context(), w1Start.SessionID, "failed attempt", nil, taskStatusFailed); err != nil {
		t.Fatalf("w1 Done(failed): %v", err)
	}
	_, retryErr := f.tasks.Retry(t.Context(), f.session.ID, w1ID, "", "")
	if errcode.CodeOf(retryErr) != errcode.FailedPrecondition || !strings.Contains(retryErr.Error(), "fugue_team_amend") {
		t.Fatalf("Retry at agent cap err = %v, want FailedPrecondition mentioning fugue_team_amend", retryErr)
	}
	amendMsg, err := f.rt.teamProposals.Amend(t.Context(), f.session.ID, &AmendArgs{
		TeamID: teamID,
		Budget: &teams.TeamBudget{MaxAgents: 4, MaxDurationSeconds: 300},
		AddTasks: []teams.ProposedTask{{
			ID: "w3", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember,
			Role: teamTestRole, Goal: "Third worker", WriteScope: []string{}, Deliverable: teams.DeliverableReport, DependsOn: []string{"w2"},
		}},
	})
	if err != nil || !strings.Contains(amendMsg, "approval inbox") {
		t.Fatalf("Amend = (%q, %v), want approval card", amendMsg, err)
	}
	if cardErr := f.answerCard(t, "amend-"+teamID, teamAmendToolName, teamAmendQuestionID, decisionApprove, ""); cardErr != nil {
		t.Fatalf("answer amend card: %v", cardErr)
	}
	if _, rErr := f.tasks.Retry(t.Context(), f.session.ID, w1ID, "", ""); rErr != nil {
		t.Fatalf("Retry after user-approved amend: %v", rErr)
	}
	if dispErr := f.disp.Dispatch(t.Context(), teamID); dispErr != nil {
		t.Fatalf("Dispatch retry: %v", dispErr)
	}
	return teamID, w1ID + "~r1", w2ID
}

func verifyTeamDeadlinePauseExtendAndCancel(t *testing.T, f *budgetsTestFixture, teamID, w1RetryID, w2ID string) {
	t.Helper()
	f.clk.Advance(301 * time.Second)
	f.budgets.Step(t.Context())

	rec, _, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != vendorstore.TeamStateActive || rec.WaitingReason != vendorstore.TeamWaitingDeadline {
		t.Fatalf("team state = (%s, waiting=%q), want (active, %q)", rec.State, rec.WaitingReason, vendorstore.TeamWaitingDeadline)
	}
	if cur := f.mustTask(t, teamID, w1RetryID); cur.State != vendorstore.TaskStateRunning {
		t.Fatalf("running task state on team deadline = %s, want running", cur.State)
	}
	completeAndAcceptTask(t, f, w1RetryID)
	if dispErr := f.disp.Dispatch(t.Context(), teamID); dispErr != nil {
		t.Fatalf("Dispatch while waiting(team_deadline): %v", dispErr)
	}
	if _, started := f.sessions.startFor(w2ID); started {
		t.Fatal("w2 started while team is in waiting(team_deadline)")
	}
	if startErr := f.disp.StartTask(t.Context(), teamID, w2ID); errcode.CodeOf(startErr) != errcode.FailedPrecondition {
		t.Fatalf("StartTask while waiting(team_deadline) = %v, want FailedPrecondition", startErr)
	}
	verifyTeamDeadlineExtendAndCancelQueued(t, f, teamID, w2ID)
}

func verifyTeamDeadlineExtendAndCancelQueued(t *testing.T, f *budgetsTestFixture, teamID, w2ID string) {
	t.Helper()
	f.answerDeadlineCard(t, teamID, teamDeadlineOptionExtend, "180")
	recAfterExtend, _, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil || recAfterExtend.WaitingReason != "" {
		t.Fatalf("waiting_reason after extend = (%q, %v), want empty", recAfterExtend.WaitingReason, err)
	}
	if _, started := f.sessions.startFor(w2ID); !started {
		t.Fatal("w2 did not start after team deadline extension")
	}

	f.clk.Advance(181 * time.Second)
	f.budgets.Step(t.Context())
	f.answerDeadlineCard(t, teamID, teamDeadlineOptionCancel, "")

	w2Final := f.mustTask(t, teamID, w2ID)
	w3Final := f.mustTask(t, teamID, durableTaskID(teamID, "w3"))
	if w2Final.State != vendorstore.TaskStateRunning || w3Final.State != vendorstore.TaskStateCancelled || w3Final.FailureCode != teamDeadlineCancelCode {
		t.Fatalf("final states: w2=%s, w3=(%s, %s)", w2Final.State, w3Final.State, w3Final.FailureCode)
	}
}

func completeAndAcceptTask(t *testing.T, f *budgetsTestFixture, taskID string) {
	t.Helper()
	st, _ := f.sessions.startFor(taskID)
	if _, err := f.tasks.Done(t.Context(), st.SessionID, "done", nil, taskStatusSucceeded); err != nil {
		t.Fatalf("%s Done: %v", taskID, err)
	}
	if _, err := f.tasks.Accept(t.Context(), f.session.ID, taskID, "ok"); err != nil {
		t.Fatalf("Accept %s: %v", taskID, err)
	}
}

func (f *budgetsTestFixture) answerDeadlineCard(t *testing.T, teamID, optionID, text string) {
	t.Helper()
	if err := f.answerCard(t, "dl-"+teamID+"-"+optionID+"-"+text, teamDeadlineToolName, teamDeadlineQuestionID, optionID, text); err != nil {
		t.Fatalf("Answer deadline card (%s): %v", optionID, err)
	}
}

// TestWatchdogEscalatesSilenceWithoutKillingAndRecordsStallEvent verifies §6.7
// watchdog silence tracking (Q-150 A).
func TestWatchdogEscalatesSilenceWithoutKillingAndRecordsStallEvent(t *testing.T) {
	t.Parallel()
	f := newBudgetsTestFixture(t, "")

	args := validTeamProposalArgs()
	args.Tasks = []teams.ProposedTask{
		{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testBudgetLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "silent-worker", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Stays silent", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "asking-worker", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Asks question", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "queued-worker", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Queued behind silent", WriteScope: []string{}, Deliverable: teams.DeliverableReport, DependsOn: []string{"silent-worker"}},
	}
	teamID := f.approve(t, args, decisionApprove, "")
	if err := f.disp.Dispatch(t.Context(), teamID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	silentID := durableTaskID(teamID, "silent-worker")
	askingID := durableTaskID(teamID, "asking-worker")
	queuedID := durableTaskID(teamID, "queued-worker")
	leadID := durableTaskID(teamID, teamTestLeadTask)

	verifyWatchdogSilenceResetAndStall(t, f, teamID, silentID, askingID, queuedID)
	verifyWatchdogNoKillAndCooldown(t, f, teamID, silentID, leadID)
}

func verifyWatchdogSilenceResetAndStall(t *testing.T, f *budgetsTestFixture, teamID, silentID, askingID, queuedID string) {
	t.Helper()
	silentStart, _ := f.sessions.startFor(silentID)
	askingStart, _ := f.sessions.startFor(askingID)
	if _, err := f.tasks.Ask(t.Context(), askingStart.SessionID, "Need clarification?", askTargetParent); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	f.clk.Advance(400 * time.Second)
	f.budgets.Step(t.Context())
	f.budgets.ObserveSession(silentStart.SessionID)

	f.clk.Advance(400 * time.Second)
	f.budgets.Step(t.Context())
	f.assertNoStallEvent(t, teamID, silentID)

	f.clk.Advance(200 * time.Second)
	f.budgets.Step(t.Context())

	rawStall, found, err := f.hp.vendorStore.OpenLatestTaskEventPayload(t.Context(), teamID, silentID, vendorstore.EventTaskStalled)
	if err != nil || !found {
		t.Fatalf("OpenLatestTaskEventPayload(TaskStalled) = (%v, %v), want found", found, err)
	}
	var stalled taskStalledPayload
	if unmarshalErr := json.Unmarshal(rawStall, &stalled); unmarshalErr != nil {
		t.Fatalf("Unmarshal stalled payload: %v", unmarshalErr)
	}
	if stalled.TaskID != silentID || stalled.WaitingOn != "vendor" || stalled.SilentSeconds < 600 || !stalled.TeamWide {
		t.Fatalf("stalled payload = %+v, want task_id=%s waiting_on=vendor silent_seconds>=600 team_wide=true", stalled, silentID)
	}
	f.assertNoStallEvent(t, teamID, askingID)
	f.assertNoStallEvent(t, teamID, queuedID)
}

func (f *budgetsTestFixture) assertNoStallEvent(t *testing.T, teamID, taskID string) {
	t.Helper()
	if _, hasStall, openErr := f.hp.vendorStore.OpenLatestTaskEventPayload(t.Context(), teamID, taskID, vendorstore.EventTaskStalled); openErr != nil || hasStall {
		t.Fatalf("task %s recorded unexpected EventTaskStalled (%v, %v)", taskID, hasStall, openErr)
	}
}

func verifyWatchdogNoKillAndCooldown(t *testing.T, f *budgetsTestFixture, teamID, silentID, leadID string) {
	t.Helper()
	if cur := f.mustTask(t, teamID, silentID); cur.State != vendorstore.TaskStateRunning {
		t.Fatalf("silent-worker state = %s, want running", cur.State)
	}
	f.sessions.mu.Lock()
	intCount, closeCount := len(f.sessions.interrupts), len(f.sessions.closed)
	f.sessions.mu.Unlock()
	if intCount != 0 || closeCount != 0 {
		t.Fatalf("watchdog must not interrupt or close session: interrupts=%d closed=%d", intCount, closeCount)
	}

	f.clk.Advance(300 * time.Second)
	f.budgets.Step(t.Context())

	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	stallCount := 0
	for i := range events {
		if events[i].EventType == vendorstore.EventTaskStalled {
			stallCount++
		}
	}
	if stallCount != 1 {
		t.Fatalf("EventTaskStalled count = %d, want 1 in the same silent stretch", stallCount)
	}
	assertSingleStallMailbox(t, f, teamID, silentID, leadID)
}

func assertSingleStallMailbox(t *testing.T, f *budgetsTestFixture, teamID, silentID, leadID string) {
	t.Helper()
	mail, err := f.hp.vendorStore.ClaimMailbox(t.Context(), teamID, leadID)
	if err != nil {
		t.Fatal(err)
	}
	stallMail := 0
	for i := range mail {
		raw, openErr := f.hp.vendorStore.OpenMailboxPayload(t.Context(), teamID, mail[i].MessageID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		var item mailboxDigestItem
		if json.Unmarshal(raw, &item) == nil && item.TaskID == silentID && item.State == "stalled" {
			stallMail++
		}
	}
	if stallMail != 1 {
		t.Fatalf("delegator stall mailbox count = %d, want 1", stallMail)
	}
}
