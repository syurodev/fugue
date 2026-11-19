package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type passingResultGate struct{}

func (passingResultGate) Evaluate(context.Context, string, string) (ResultGateVerdict, error) {
	return ResultGateVerdict{Ready: true, Passed: true, Summary: "ok"}, nil
}

func TestMemberPermissionRequestReachesTheUserInboxWithoutAMailboxMessage(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	const confirmTool = "fugue_test_member_write"
	f.tools.tools[confirmTool] = &FugueTool{
		Name:        confirmTool,
		Description: "test write tool requiring confirmation",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Descriptor:  interaction.Descriptor{Risk: interaction.RiskWrite, Confirm: interaction.ConfirmAlways},
		Plan:        noFacts,
		Run:         func(context.Context, string, json.RawMessage) (string, error) { return "wrote", nil },
	}

	sub, err := f.rt.Subscribe(ctx, SubscribeParams{Principal: testAppPrincipal, Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	var (
		wg      sync.WaitGroup
		callRes FugueToolResult
		callErr error
	)
	wg.Go(func() {
		callRes, callErr = f.tools.Call(ctx, f.memberBinding, confirmTool, []byte(`{}`))
	})

	view := awaitRequested(t, sub)
	if view.SessionID != "sess-mem" || view.Tool != confirmTool {
		t.Fatalf("pending interaction = %+v, want member session sess-mem and %s", view, confirmTool)
	}

	// Neither lead nor manager receives any mailbox message for a member permission request.
	pendingMail, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingMail) != 0 {
		t.Fatalf("mailbox pending = %+v, want 0 rows", pendingMail)
	}

	if _, ansErr := f.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "perm-allow", Principal: testAppPrincipal, Incarnation: f.rt.interactions.Incarnation(),
		RequestID: view.RequestID, Outcome: OutcomeAllowedOnce,
	}); ansErr != nil {
		t.Fatalf("AnswerInteraction: %v", ansErr)
	}

	wg.Wait()
	if callErr != nil || callRes.IsError || callRes.Text != "wrote" {
		t.Fatalf("member tool call = (%+v, %v), want wrote", callRes, callErr)
	}
	pendingMail, err = f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingMail) != 0 {
		t.Fatalf("mailbox pending after approval = %+v, want 0 rows", pendingMail)
	}
}

func TestParentQuestionReachesOnlyTheDirectDelegator(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	fakeSess := &fakeTaskSessions{}
	wake := newTeamWake(&teamWakeDeps{
		Store: f.hp.vendorStore, Sessions: fakeSess, Roots: f.rt.workspaces.domain,
		Clock: f.clk, IDs: ids.NewSequence("q-"), Vault: f.hp.vault, DataDir: t.TempDir(),
	})
	f.rt.teamTasks.SetRuntime(f.rt.teamDispatch, wake)
	setSessionReady(t, f, f.session.ID)

	assertDirectQuestionFlow(t, f, wake, fakeSess)
	assertForwardedQuestionFlow(t, f, wake, fakeSess)
}

func setSessionReady(t *testing.T, f *activeTeamFixture, sessionID string) {
	t.Helper()
	ctx := t.Context()
	snap, err := f.hp.vendorStore.Snapshot(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, appErr := f.hp.vendorStore.Append(ctx, sessionID, snap.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateReady,
	}); appErr != nil {
		t.Fatal(appErr)
	}
}

func assertDirectQuestionFlow(t *testing.T, f *activeTeamFixture, wake *teamWake, fakeSess *fakeTaskSessions) {
	t.Helper()
	ctx := t.Context()
	mustCallTool(t, f, f.memberBinding, taskAskToolName, `{"question":"which endpoint?"}`)
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].RecipientTaskID != f.managerTaskID {
		t.Fatalf("pending after member ask = %+v, want only manager %s", pending, f.managerTaskID)
	}
	f.clk.Advance(digestWait)
	if pumpErr := wake.Pump(ctx); pumpErr != nil {
		t.Fatalf("Pump: %v", pumpErr)
	}
	if len(fakeSess.sends[f.session.ID]) != 0 {
		t.Fatalf("lead received unforwarded question: %+v", fakeSess.sends[f.session.ID])
	}
	if got := fakeSess.sends["sess-mgr"]; len(got) != 1 || !strings.Contains(got[0], "which endpoint?") {
		t.Fatalf("manager sends = %+v, want question", got)
	}
	mustCallTool(t, f, f.managerBinding, taskAnswerToolName, `{"task_id":"`+f.memberTaskID+`","answer":"use /v2"}`)
	if pumpErr := wake.Pump(ctx); pumpErr != nil {
		t.Fatalf("Pump answer: %v", pumpErr)
	}
	if len(fakeSess.sends[f.session.ID]) != 0 {
		t.Fatalf("lead received direct answer traffic: %+v", fakeSess.sends[f.session.ID])
	}
	if got := fakeSess.sends["sess-mem"]; len(got) != 1 || !strings.Contains(got[0], "use /v2") {
		t.Fatalf("member sends = %+v, want answer", got)
	}
}

func assertForwardedQuestionFlow(t *testing.T, f *activeTeamFixture, wake *teamWake, fakeSess *fakeTaskSessions) {
	t.Helper()
	ctx := t.Context()
	mustCallTool(t, f, f.memberBinding, taskAskToolName, `{"question":"prod or staging?"}`)
	mustCallTool(t, f, f.managerBinding, taskAnswerToolName, `{"task_id":"`+f.memberTaskID+`","forward_to":"parent"}`)
	assertTaskStates(t, f, vendorstore.TaskStateAsking, vendorstore.TaskStateAsking)

	f.clk.Advance(digestWait)
	if pumpErr := wake.Pump(ctx); pumpErr != nil {
		t.Fatalf("Pump forwarded: %v", pumpErr)
	}
	if got := fakeSess.sends[f.session.ID]; len(got) != 1 || !strings.Contains(got[0], "prod or staging?") {
		t.Fatalf("lead sends after forward = %+v, want forwarded question", got)
	}
	if _, leadFwdErr := f.rt.teamTasks.Answer(ctx, f.leadBinding.SessionID, f.managerTaskID, "", askTargetParent); errcode.CodeOf(leadFwdErr) != errcode.FailedPrecondition {
		t.Fatalf("lead forward_to=parent err = %v, want FailedPrecondition", leadFwdErr)
	}
	mustCallTool(t, f, f.leadBinding, taskAnswerToolName, `{"task_id":"`+f.managerTaskID+`","forward_to":"user"}`)
	qCard := findQuestionCard(t, f)
	if _, ansErr := f.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "user-q-ans", Principal: testAppPrincipal, Incarnation: f.rt.interactions.Incarnation(),
		RequestID: qCard.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: taskQuestionCardID, Text: "use staging"}},
	}); ansErr != nil {
		t.Fatalf("AnswerInteraction: %v", ansErr)
	}
	if pumpErr := wake.Pump(ctx); pumpErr != nil {
		t.Fatalf("Pump routed user answer: %v", pumpErr)
	}
	assertTaskStates(t, f, vendorstore.TaskStateRunning, vendorstore.TaskStateRunning)
	if got := fakeSess.sends["sess-mem"]; len(got) != 2 || !strings.Contains(got[1], "use staging") {
		t.Fatalf("member sends = %+v, want routed user answer", got)
	}
	if got := fakeSess.sends["sess-mgr"]; len(got) != 3 || !strings.Contains(got[2], "use staging") {
		t.Fatalf("manager sends = %+v, want routed user answer", got)
	}
}

func assertTaskStates(t *testing.T, f *activeTeamFixture, wantMgr, wantMem vendorstore.TaskState) {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := findTask(tasks, f.managerTaskID)
	mem, _ := findTask(tasks, f.memberTaskID)
	if mgr.State != wantMgr || mem.State != wantMem {
		t.Fatalf("states = (mgr:%s, mem:%s), want (%s, %s)", mgr.State, mem.State, wantMgr, wantMem)
	}
}

func findQuestionCard(t *testing.T, f *activeTeamFixture) PendingInteractionView {
	t.Helper()
	views, _, err := f.rt.ListPendingInteractions(t.Context(), testAppPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	for i := range views {
		if views[i].Tool == taskAskToolName {
			if views[i].Questions[0].QuestionID != taskQuestionCardID {
				t.Fatalf("user question card wrong id: %+v", views[i])
			}
			return views[i]
		}
	}
	t.Fatalf("user question card missing: %+v", views)
	return PendingInteractionView{}
}

func TestAskToUserCreatesQuestionCardAndDeliversAnswer(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{}
	wake := newTeamWake(&teamWakeDeps{
		Store: f.hp.vendorStore, Sessions: fakeSess, Roots: f.rt.workspaces.domain,
		Clock: f.clk, IDs: ids.NewSequence("qu-"), Vault: f.hp.vault, DataDir: t.TempDir(),
	})
	f.rt.teamTasks.SetRuntime(f.rt.teamDispatch, wake)

	mustCallTool(t, f, f.memberBinding, taskAskToolName, `{"question":"need staging URL","to":"user"}`)
	views, _, err := f.rt.ListPendingInteractions(ctx, testAppPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	var qCard *PendingInteractionView
	for i := range views {
		if views[i].Tool == taskAskToolName {
			qCard = &views[i]
			break
		}
	}
	if qCard == nil || !strings.Contains(qCard.Questions[0].Prompt, "need staging URL") {
		t.Fatalf("expected user question card, got %+v", views)
	}
	if _, ansErr := f.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "direct-user-ans", Principal: testAppPrincipal, Incarnation: f.rt.interactions.Incarnation(),
		RequestID: qCard.RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: taskQuestionCardID, Text: "https://staging.example.invalid"}},
	}); ansErr != nil {
		t.Fatalf("AnswerInteraction: %v", ansErr)
	}
	if pumpErr := wake.Pump(ctx); pumpErr != nil {
		t.Fatalf("Pump: %v", pumpErr)
	}
	if got := fakeSess.sends["sess-mem"]; len(got) != 1 || !strings.Contains(got[0], "https://staging.example.invalid") {
		t.Fatalf("member sends = %+v, want user answer", got)
	}
}

type ore15Team struct {
	teamID      string
	leadID      string
	leadBind    SessionBinding
	managers    []string
	mgrBinds    []SessionBinding
	members     [][]string
	memberBinds [][]SessionBinding
	wake        *teamWake
	fakeSess    *fakeTaskSessions
}

func setupORE15Team(t *testing.T) (*activeTeamFixture, *ore15Team) {
	t.Helper()
	f := newActiveTeamFixture(t, nil)
	f.rt.teamTasks.resultGate = passingResultGate{}

	teamID, leadID, leadSess := "ore15-team", "ore15-lead", "ore15-sess-lead"
	mgrIDs := []string{"ore15-m1", "ore15-m2", "ore15-m3"}
	memIDs := [][]string{{"ore15-m1a", "ore15-m1b"}, {"ore15-m2a", "ore15-m2b"}, {"ore15-m3a", "ore15-m3b"}}
	sessSeed, tasks := buildORE15Plan(t, leadID, leadSess, mgrIDs, memIDs)
	seedAndStartORE15(t, f, teamID, leadSess, sessSeed, tasks)

	fakeSess := &fakeTaskSessions{}
	wake := newTeamWake(&teamWakeDeps{
		Store: f.hp.vendorStore, Sessions: fakeSess, Roots: f.rt.workspaces.domain,
		Clock: f.clk, IDs: ids.NewSequence("or15-"), Vault: f.hp.vault, DataDir: t.TempDir(),
	})
	f.rt.teamTasks.SetRuntime(f.rt.teamDispatch, wake)

	out := &ore15Team{
		teamID: teamID, leadID: leadID, leadBind: mustAttachRole(t, f.teamProposalFixture, leadSess, vendorstore.RoleLead),
		managers: mgrIDs, members: memIDs, wake: wake, fakeSess: fakeSess,
	}
	for i, mid := range mgrIDs {
		out.mgrBinds = append(out.mgrBinds, mustAttachRole(t, f.teamProposalFixture, "ore15-sess-"+mid, vendorstore.RoleTeamManager))
		var mbs []SessionBinding
		for _, cid := range memIDs[i] {
			mbs = append(mbs, mustAttachRole(t, f.teamProposalFixture, "ore15-sess-"+cid, vendorstore.RoleMember))
		}
		out.memberBinds = append(out.memberBinds, mbs)
	}
	return f, out
}

func buildORE15Plan(t *testing.T, leadID, leadSess string, mgrIDs []string, memIDs [][]string) ([][3]string, []vendorstore.NewTaskInput) {
	t.Helper()
	sessSeed := [][3]string{{leadSess, leadID, string(vendorstore.RoleLead)}}
	spec := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "run work"})
	tasks := []vendorstore.NewTaskInput{
		{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "ol", ProtectedSpec: spec},
	}
	for i, mid := range mgrIDs {
		sessSeed = append(sessSeed, [3]string{"ore15-sess-" + mid, mid, string(vendorstore.RoleTeamManager)})
		tasks = append(tasks, vendorstore.NewTaskInput{
			TaskID: mid, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleTeamManager,
			CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "om-" + mid, ProtectedSpec: spec,
		})
		for _, cid := range memIDs[i] {
			sessSeed = append(sessSeed, [3]string{"ore15-sess-" + cid, cid, string(vendorstore.RoleMember)})
			tasks = append(tasks, vendorstore.NewTaskInput{
				TaskID: cid, ParentTaskID: mid, HierarchyRole: vendorstore.RoleMember,
				CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "oc-" + cid, ProtectedSpec: spec,
			})
		}
	}
	return sessSeed, tasks
}

func seedAndStartORE15(t *testing.T, f *activeTeamFixture, teamID, leadSess string, sessSeed [][3]string, tasks []vendorstore.NewTaskInput) {
	t.Helper()
	ctx, store := t.Context(), f.hp.vendorStore
	seedTeamSessions(t, f.teamProposalFixture, teamID, sessSeed)
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionRead)}},
	})
	rec, _, err := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: f.wsID, SessionID: leadSess,
		ProposedEventID: "op-15", ProposedPayload: mustMarshalJSON(t, teams.ProposalInput{Title: "OR15", Brief: "brief"}),
		Tasks: tasks,
	})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := store.AppendTeamEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "os-15", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessSeed {
		for _, et := range []vendorstore.TeamEventType{vendorstore.EventTaskAssigned, vendorstore.EventTaskStarted} {
			if seq, err = store.AppendTeamEvent(ctx, teamID, seq, &vendorstore.AppendTeamEventInput{
				EventID: etName(s[1], et), TaskID: s[1], EventType: et, SessionID: s[0],
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func etName(taskID string, et vendorstore.TeamEventType) string {
	return "o15-" + taskID + "-" + string(et)
}

func TestLeadLoadWithFakeVendorsStaysUnderTheWakeBound(t *testing.T) {
	t.Parallel()
	f, o := setupORE15Team(t)
	ctx := t.Context()

	// Member 0a asks and manager 0 forwards to lead (1 forwarded question); lead answers.
	mustCallTool(t, f, o.memberBinds[0][0], taskAskToolName, `{"question":"schema choice?"}`)
	mustCallTool(t, f, o.mgrBinds[0], taskAnswerToolName, `{"task_id":"`+o.members[0][0]+`","forward_to":"parent"}`)
	f.clk.Advance(digestWait)
	if err := o.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	mustCallTool(t, f, o.leadBind, taskAnswerToolName, `{"task_id":"`+o.managers[0]+`","answer":"use v1"}`)

	// Member 1a asks and manager 1 answers directly (never reaches lead).
	mustCallTool(t, f, o.memberBinds[1][0], taskAskToolName, `{"question":"local detail?"}`)
	mustCallTool(t, f, o.mgrBinds[1], taskAnswerToolName, `{"task_id":"`+o.members[1][0]+`","answer":"keep local"}`)

	completeORE15Subtree(t, f, o)
	assertORE15LeadWakes(t, f, o)
}

func completeORE15Subtree(t *testing.T, f *activeTeamFixture, o *ore15Team) {
	t.Helper()
	for i := range o.managers {
		for j := range o.members[i] {
			mustCallTool(t, f, o.memberBinds[i][j], taskDoneToolName, `{"summary":"member done","status":"succeeded"}`)
			mustCallTool(t, f, o.mgrBinds[i], taskAcceptToolName, `{"task_id":"`+o.members[i][j]+`"}`)
		}
		mustCallTool(t, f, o.mgrBinds[i], taskDoneToolName, `{"summary":"manager summary","status":"succeeded"}`)
	}
	f.clk.Advance(digestWait)
	if err := o.wake.Pump(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, mid := range o.managers {
		mustCallTool(t, f, o.leadBind, taskAcceptToolName, `{"task_id":"`+mid+`"}`)
	}
}

func assertORE15LeadWakes(t *testing.T, f *activeTeamFixture, o *ore15Team) {
	t.Helper()
	leadWakes := o.fakeSess.sends["ore15-sess-lead"]
	const maxAllowedWakes = 3 + 1 // 3 managers + 1 forwarded question
	if len(leadWakes) == 0 || len(leadWakes) > maxAllowedWakes {
		t.Fatalf("lead wake count = %d, want 1..%d", len(leadWakes), maxAllowedWakes)
	}
	var allMembers []string
	for _, group := range o.members {
		allMembers = append(allMembers, group...)
	}
	for idx, turn := range leadWakes {
		if len(turn) > teams.MaxDigestBytes {
			t.Fatalf("lead wake %d exceeds 8 KiB (%d bytes)", idx, len(turn))
		}
		for _, cid := range allMembers {
			if strings.Contains(turn, cid) {
				t.Fatalf("lead wake %d leaked member %s: %q", idx, cid, turn)
			}
		}
	}
	assertORE15TasksDone(t, f, o.teamID, append(slices.Clone(o.managers), allMembers...))
}

func assertORE15TasksDone(t *testing.T, f *activeTeamFixture, teamID string, taskIDs []string) {
	t.Helper()
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range taskIDs {
		task, ok := findTask(tasks, id)
		if !ok || task.State != vendorstore.TaskStateDone {
			t.Fatalf("task %s = (%v, %+v), want done", id, ok, task)
		}
	}
}
