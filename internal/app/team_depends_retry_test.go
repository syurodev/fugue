package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const diffLeadTaskID = "diff-lead"

func setupDependencyTeam(t *testing.T) (*activeTeamFixture, *fakeTaskSessions, string, string, string, string) {
	t.Helper()
	f := &activeTeamFixture{teamProposalFixture: newTeamProposalFixture(t, "")}
	initRepo(t, f.root)
	f.rt.teamTasks.resultGate = passingResultGate{}
	fakeSess := &fakeTaskSessions{}
	f.rt.teamDispatch.deps.Sessions = fakeSess
	f.rt.teamDispatch.deps.Admit = localAdmitter{capacity: fakeSess}
	f.rt.teamDispatch.deps.Git = mustTestGit(t, f.rt.teamTasks.dataDir)
	f.rt.teamTasks.SetRuntime(f.rt.teamDispatch, f.rt.teamWake)

	args := validTeamProposalArgs()
	args.Tasks = append(args.Tasks, teams.ProposedTask{
		ID: "task-c", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole,
		Goal: "Independent sibling C", WriteScope: []string{}, Deliverable: teams.DeliverableReport,
	})
	args.Tasks[2].DependsOn = []string{teamTestTaskA}
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if ansErr := f.answerCard(t, "dep-"+teamID, teamProposeToolName, teamDecisionQuestionID, decisionApprove, ""); ansErr != nil {
		t.Fatalf("approve: %v", ansErr)
	}
	if dispErr := f.rt.teamDispatch.Dispatch(t.Context(), teamID); dispErr != nil {
		t.Fatalf("Dispatch: %v", dispErr)
	}
	return f, fakeSess, teamID, teamID + "." + teamTestTaskA, teamID + "." + teamTestTaskB, teamID + ".task-c"
}

func mustTestGit(t *testing.T, dataDir string) *gitops.Manager {
	t.Helper()
	m, err := gitops.NewManager(gitBinary(t), dataDir, clock.NewFake(time.Unix(300, 0)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func bindingForTask(t *testing.T, f *activeTeamFixture, teamID, taskID string) SessionBinding {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	task, ok := findTask(tasks, taskID)
	if !ok || task.SessionID == "" {
		t.Fatalf("task %s missing session: %+v", taskID, task)
	}
	if _, sessErr := f.hp.vendorStore.Snapshot(t.Context(), task.SessionID); sessErr != nil {
		seedTeamSessions(t, f.teamProposalFixture, teamID, [][3]string{{task.SessionID, task.TaskID, string(vendorstore.RoleMember)}})
	}
	return mustAttachRole(t, f.teamProposalFixture, task.SessionID, vendorstore.RoleMember)
}

func TestDependentTaskStartsOnlyAfterItsDependenciesAreDone(t *testing.T) {
	t.Parallel()
	f, fakeSess, teamID, taskA, taskB, _ := setupDependencyTeam(t)
	ctx := t.Context()

	if _, startedB := fakeSess.startFor(taskB); startedB {
		t.Fatalf("dependent %s started before %s completed", taskB, taskA)
	}
	bindA := bindingForTask(t, f, teamID, taskA)
	mustCallTool(t, f, bindA, taskDoneToolName, `{"summary":"A report","status":"succeeded"}`)

	// Task A is now in review (not done yet): B must still remain queued.
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	recA, _ := findTask(tasks, taskA)
	recB, _ := findTask(tasks, taskB)
	if recA.State != vendorstore.TaskStateReview || recB.State != vendorstore.TaskStateQueued {
		t.Fatalf("states before accept = (A:%s, B:%s), want (review, queued)", recA.State, recB.State)
	}
	if _, startedB := fakeSess.startFor(taskB); startedB {
		t.Fatalf("dependent %s started while %s is still in review", taskB, taskA)
	}

	// Reject moves review -> running, keeping B queued.
	mustCallTool(t, f, f.binding, taskRejectToolName, `{"task_id":"`+taskA+`","reason":"add metric"}`)
	_, tasks, err = f.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	recA, _ = findTask(tasks, taskA)
	if recA.State != vendorstore.TaskStateRunning {
		t.Fatalf("A state after reject = %s, want running", recA.State)
	}

	// Report again and accept -> A becomes done, and B starts automatically!
	mustCallTool(t, f, bindA, taskDoneToolName, `{"summary":"A report v2","output":{"metric":7},"status":"succeeded"}`)
	mustCallTool(t, f, f.binding, taskAcceptToolName, `{"task_id":"`+taskA+`"}`)
	if _, startedB := fakeSess.startFor(taskB); !startedB {
		t.Fatalf("dependent %s did not start after %s became done", taskB, taskA)
	}
}

func TestAcceptRefusesDiffTaskUntilF115AndRejectWakesTask(t *testing.T) {
	t.Parallel()
	f := &activeTeamFixture{teamProposalFixture: newTeamProposalFixture(t, "")}
	initRepo(t, f.root)
	ctx := t.Context()

	// Add a diff task in review under lead and start diff-lead so it can review.
	setupDiffTeam(t, f)
	rec, _, err := f.hp.vendorStore.TeamSnapshot(ctx, "diff-team")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "sdl-a", TaskID: diffLeadTaskID, EventType: vendorstore.EventTaskAssigned, SessionID: f.session.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, startErr := f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", seq, &vendorstore.AppendTeamEventInput{
		EventID: "sdl-s", TaskID: diffLeadTaskID, EventType: vendorstore.EventTaskStarted,
	}); startErr != nil {
		t.Fatal(startErr)
	}
	diffBind := mustAttachRole(t, f.teamProposalFixture, "diff-sess", vendorstore.RoleMember)
	mustCallTool(t, f, diffBind, taskDoneToolName, `{"summary":"diff ready","status":"succeeded"}`)

	// Move diff-mem checking -> review using passing result gate.
	f.rt.teamTasks.resultGate = passingResultGate{}
	if gateErr := f.rt.teamTasks.applyResultGate(ctx, "diff-team", diffTaskID, diffLeadTaskID); gateErr != nil {
		t.Fatal(gateErr)
	}

	// Lead of diff-team is f.session.ID; Accept on diff deliverable must refuse typed (Q-125).
	if _, accErr := f.rt.teamTasks.Accept(ctx, f.session.ID, diffTaskID, ""); errcode.CodeOf(accErr) != errcode.FailedPrecondition {
		t.Fatalf("Accept(diff task) err = %v, want FailedPrecondition", accErr)
	}

	// Reject on diff task transitions review -> running and enqueues feedback mailbox row.
	if _, rejErr := f.rt.teamTasks.Reject(ctx, f.session.ID, diffTaskID, "fix edge case"); rejErr != nil {
		t.Fatalf("Reject(diff task) err = %v", rejErr)
	}
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, "diff-team")
	if err != nil {
		t.Fatal(err)
	}
	dt, _ := findTask(tasks, diffTaskID)
	if dt.State != vendorstore.TaskStateRunning {
		t.Fatalf("diff task state after reject = %s, want running", dt.State)
	}
}

func TestDependentReceivesSummariesAndReadAccessToTheResults(t *testing.T) {
	t.Parallel()
	f, fakeSess, teamID, taskA, taskB, taskC := setupDependencyTeam(t)
	ctx := t.Context()

	// Fail taskA once and retry it as taskA~r1; taskB.DependsOn stays [taskA] (never rewritten, Q-126).
	bindA := bindingForTask(t, f, teamID, taskA)
	mustCallTool(t, f, bindA, taskDoneToolName, `{"summary":"first failed","status":"failed"}`)
	mustCallTool(t, f, f.binding, taskRetryToolName, `{"task_id":"`+taskA+`"}`)

	retryA := taskA + "~r1"
	bindR1 := bindingForTask(t, f, teamID, retryA)
	mustCallTool(t, f, bindR1, taskDoneToolName, `{"summary":"Alpha findings from retry","output":{"score":42},"status":"succeeded"}`)
	mustCallTool(t, f, f.binding, taskAcceptToolName, `{"task_id":"`+retryA+`"}`)

	// Verify taskB started and its intro carries the resolved summary of taskA~r1.
	reqB, ok := fakeSess.startFor(taskB)
	if !ok {
		t.Fatalf("dependent %s did not start after %s became done", taskB, retryA)
	}
	intro := fakeSess.sends[reqB.SessionID]
	if len(intro) != 1 || !strings.Contains(intro[0], retryA) || !strings.Contains(intro[0], "Alpha findings from retry") {
		t.Fatalf("dependent intro = %q, want dependency %s and its summary", intro, retryA)
	}

	// Verify taskB.DependsOn was never rewritten.
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	recB, _ := findTask(tasks, taskB)
	if len(recB.DependsOn) != 1 || recB.DependsOn[0] != taskA {
		t.Fatalf("taskB.DependsOn = %v, want unchanged [%s]", recB.DependsOn, taskA)
	}

	// Dependent can read output of both retryA and declared dep taskA, but NOT unrelated sibling taskC.
	bindB := bindingForTask(t, f, teamID, taskB)
	pulled := mustCallTool(t, f, bindB, taskResultToolName, `{"task_id":"`+retryA+`","part":"output"}`)
	if !strings.Contains(pulled.Text, `"score":42`) {
		t.Fatalf("result(retryA) = %q, want output payload", pulled.Text)
	}
	pulledOrig := mustCallTool(t, f, bindB, taskResultToolName, `{"task_id":"`+taskA+`","part":"output"}`)
	if !strings.Contains(pulledOrig.Text, `"score":42`) {
		t.Fatalf("result(taskA) = %q, want resolved latest output", pulledOrig.Text)
	}
	unauthRes, unauthErr := f.tools.Call(ctx, bindB, taskResultToolName, []byte(`{"task_id":"`+taskC+`","part":"summary"}`))
	if unauthErr == nil && !unauthRes.IsError {
		t.Fatalf("reading non-dependency %s unexpectedly succeeded: %+v", taskC, unauthRes)
	}
}

func TestRetryRunsAFailedTaskAgainTwiceWithoutApproval(t *testing.T) {
	t.Parallel()
	f, fakeSess, teamID, taskA, _, _ := setupDependencyTeam(t)
	ctx := t.Context()

	// Attempt 1 fails -> retry 1 (attempt 2, ~r1).
	bindA := bindingForTask(t, f, teamID, taskA)
	mustCallTool(t, f, bindA, taskDoneToolName, `{"summary":"fail 1","status":"failed"}`)
	mustCallTool(t, f, f.binding, taskRetryToolName, `{"task_id":"`+taskA+`"}`)

	r1 := taskA + "~r1"
	req1, ok1 := fakeSess.startFor(r1)
	if !ok1 || req1.Model != "" || req1.Role != vendorstore.RoleMember {
		t.Fatalf("retry 1 request = (%+v, %v)", req1, ok1)
	}

	// Attempt 2 (~r1) fails -> retry 2 (attempt 3, ~r2) without approval.
	bindR1 := bindingForTask(t, f, teamID, r1)
	mustCallTool(t, f, bindR1, taskDoneToolName, `{"summary":"fail 2","status":"failed"}`)
	mustCallTool(t, f, f.binding, taskRetryToolName, `{"task_id":"`+r1+`"}`)

	r2 := taskA + "~r2"
	req2, ok2 := fakeSess.startFor(r2)
	if !ok2 || req2.Role != vendorstore.RoleMember {
		t.Fatalf("retry 2 request = (%+v, %v)", req2, ok2)
	}

	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	t0, _ := findTask(tasks, taskA)
	t1, _ := findTask(tasks, r1)
	t2, _ := findTask(tasks, r2)
	if t0.State != vendorstore.TaskStateStopped || t1.State != vendorstore.TaskStateStopped || t2.State != vendorstore.TaskStateRunning {
		t.Fatalf("chain states = (%s, %s, %s), want (stopped, stopped, running)", t0.State, t1.State, t2.State)
	}
	if t2.Attempt != 3 || t2.RetryOf != r1 {
		t.Fatalf("t2 lineage = (attempt %d, retry_of %q), want (3, %q)", t2.Attempt, t2.RetryOf, r1)
	}
}

func TestThirdRetryNeedsAmend(t *testing.T) {
	t.Parallel()
	f, _, teamID, taskA, _, _ := setupDependencyTeam(t)
	ctx := t.Context()

	// Run through two retries until ~r2 fails.
	bindA := bindingForTask(t, f, teamID, taskA)
	mustCallTool(t, f, bindA, taskDoneToolName, `{"summary":"f1","status":"failed"}`)
	mustCallTool(t, f, f.binding, taskRetryToolName, `{"task_id":"`+taskA+`"}`)

	r1 := taskA + "~r1"
	bindR1 := bindingForTask(t, f, teamID, r1)
	mustCallTool(t, f, bindR1, taskDoneToolName, `{"summary":"f2","status":"failed"}`)
	mustCallTool(t, f, f.binding, taskRetryToolName, `{"task_id":"`+r1+`"}`)

	r2 := taskA + "~r2"
	bindR2 := bindingForTask(t, f, teamID, r2)
	mustCallTool(t, f, bindR2, taskDoneToolName, `{"summary":"f3","status":"failed"}`)

	// Changing role/model on retry is refused too.
	if _, chErr := f.rt.teamTasks.Retry(ctx, f.binding.SessionID, r2, "other-role", ""); errcode.CodeOf(chErr) != errcode.FailedPrecondition {
		t.Fatalf("Retry(changed role) err = %v, want FailedPrecondition", chErr)
	}
	// Third retry via fugue_task_retry is refused and leaves ~r2 failed.
	if _, thirdErr := f.rt.teamTasks.Retry(ctx, f.binding.SessionID, r2, "", ""); errcode.CodeOf(thirdErr) != errcode.FailedPrecondition {
		t.Fatalf("3rd Retry err = %v, want FailedPrecondition", thirdErr)
	}

	// Adding a replacement task via fugue_team_amend goes to the user approval card.
	mustCallTool(t, f, f.binding, teamAmendToolName, `{"team_id":"`+teamID+`","add_tasks":[{"id":"a-retry3","parent_task_id":"`+teamTestLeadTask+`","team_role":"member","role":"`+teamTestRole+`","goal":"Retry via amend","write_scope":[],"deliverable":"report"}]}`)
	if ansErr := f.answerCard(t, "amend-retry3", teamAmendToolName, teamAmendQuestionID, decisionApprove, ""); ansErr != nil {
		t.Fatalf("approve amend: %v", ansErr)
	}
	mustCallTool(t, f, f.binding, taskStartToolName, `{"task_id":"`+teamID+`.a-retry3"}`)
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, teamID)
	if err != nil {
		t.Fatal(err)
	}
	t3, ok := findTask(tasks, teamID+".a-retry3")
	if !ok || t3.State != vendorstore.TaskStateRunning {
		t.Fatalf("amended 3rd retry = (%+v, %v), want running", t3, ok)
	}
}

func bumpTeamSeqForTest(t *testing.T, ctx context.Context, f *activeTeamFixture, evID string) {
	t.Helper()
	rec, _, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, appErr := f.hp.vendorStore.AppendTeamEvent(ctx, f.teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, EventType: vendorstore.EventTeamNoted, ProtectedPayload: []byte(`{"note":"bump"}`),
	}); appErr != nil {
		t.Fatal(appErr)
	}
}

func TestStartChildFailsTaskWhenTaskStartedRetriesExhaustConflict(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	initRepo(t, f.root)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{startErr: errcode.New(errcode.Unavailable, "spawn failed")}
	dataDir := filepath.Join(t.TempDir(), "data")
	failAttempts := 0
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.hp.vendorStore, Sessions: fakeSess, Admit: localAdmitter{capacity: fakeSess},
		Roots: f.rt.workspaces.domain, Git: mustTestGit(t, dataDir), DataDir: dataDir,
		IDs: ids.NewSequence("carry-"), Vault: f.hp.vault,
		TestBeforeFailAppend: func(hookCtx context.Context) {
			failAttempts++
			if failAttempts == 1 {
				bumpTeamSeqForTest(t, hookCtx, f, "mid-fail-conflict")
			}
		},
	})

	// StartTaskSession fails after worktree removal; failTaskAttempt hits ErrSequenceConflict on
	// attempt 1, then retries on attempt 2 (re-reading snapshot & matching starting+session).
	if err := disp.StartTask(ctx, f.teamID, f.siblingTaskID); err != nil {
		t.Fatalf("StartTask err = %v, want nil (failure recorded on task)", err)
	}
	if failAttempts != 2 {
		t.Fatalf("failAttempts = %d, want 2 (1 conflict + 1 retry)", failAttempts)
	}
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	sib, _ := findTask(tasks, f.siblingTaskID)
	if sib.State != vendorstore.TaskStateFailed || sib.FailureCode != failSessionStart {
		t.Fatalf("sibling after StartTaskSession failure = (%s, %s), want (failed, %s)", sib.State, sib.FailureCode, failSessionStart)
	}
}

func TestStartChildClosesSessionAndRecordsFailureOnExhaustedStartRetries(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	initRepo(t, f.root)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{}
	dataDir := filepath.Join(t.TempDir(), "data")
	startAttempts := 0
	disp := newTeamDispatcher(&teamDispatcherDeps{
		Store: f.hp.vendorStore, Sessions: fakeSess, Admit: localAdmitter{capacity: fakeSess},
		Roots: f.rt.workspaces.domain, Git: mustTestGit(t, dataDir), DataDir: dataDir,
		IDs: ids.NewSequence("exh-"), Vault: f.hp.vault,
		TestBeforeStartAppend: func(hookCtx context.Context) {
			startAttempts++
			bumpTeamSeqForTest(t, hookCtx, f, fmt.Sprintf("start-conflict-%d", startAttempts))
		},
	})

	// Every appendTaskStartedAttempt hits ErrSequenceConflict (3 attempts); startChild must close
	// the spawned session, remove its worktree, and fail the starting task (never leaving it stuck starting).
	if err := disp.StartTask(ctx, f.teamID, f.siblingTaskID); err != nil {
		t.Fatalf("StartTask err = %v, want nil (recovery records task failure)", err)
	}
	if startAttempts != maxAppendRetries {
		t.Fatalf("startAttempts = %d, want %d", startAttempts, maxAppendRetries)
	}
	startedReq, ok := fakeSess.startFor(f.siblingTaskID)
	if !ok {
		t.Fatalf("expected spawned session request for %s", f.siblingTaskID)
	}
	if !slices.Contains(fakeSess.closed, startedReq.SessionID) {
		t.Fatalf("closed sessions = %v, want %s", fakeSess.closed, startedReq.SessionID)
	}
	if _, statErr := os.Stat(startedReq.Cwd); !os.IsNotExist(statErr) {
		t.Fatalf("worktree %s still exists after failure recovery (stat err=%v)", startedReq.Cwd, statErr)
	}
	// Mismatched session ID still refuses to fail a starting task.
	if mismatchErr := disp.failTaskInState(ctx, f.teamID, f.siblingTaskID, vendorstore.TaskStateStarting, "wrong-session", failSessionStart); mismatchErr == nil {
		t.Fatal("failTaskInState(wrong session) = nil, want FailedPrecondition")
	}
	_, afterTasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	afterSib, _ := findTask(afterTasks, f.siblingTaskID)
	if afterSib.State != vendorstore.TaskStateFailed || afterSib.FailureCode != failSessionStart {
		t.Fatalf("sibling state = (%s, %s), want (failed, %s)", afterSib.State, afterSib.FailureCode, failSessionStart)
	}
}
