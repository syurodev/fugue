package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type activeTeamFixture struct {
	*teamProposalFixture
	teamID         string
	leadTaskID     string
	managerTaskID  string
	memberTaskID   string
	siblingTaskID  string
	leadBinding    SessionBinding
	managerBinding SessionBinding
	memberBinding  SessionBinding
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seedTeamSessions(t *testing.T, f *teamProposalFixture, teamID string, idsAndRoles [][3]string) {
	t.Helper()
	for _, s := range idsAndRoles {
		if err := f.hp.vendorStore.CreateSession(t.Context(), &vendorstore.Session{
			ID: s[0], Vendor: VendorClaude, AccountID: vendorDefaultAccount,
			Role: s[2], State: vendorstore.StateReady, ApprovalMode: vendorstore.ApprovalPlan,
			TeamID: teamID, TaskID: s[1], WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func mustAttachRole(t *testing.T, f *teamProposalFixture, sessionID string, role vendorstore.TeamRole) SessionBinding {
	t.Helper()
	tok, err := f.tools.tokens.mint(sessionID, string(role))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.tools.Attach(tok.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newActiveTeamFixture(t *testing.T, outputSchema json.RawMessage) *activeTeamFixture {
	t.Helper()
	f := newTeamProposalFixture(t, "")
	ctx := t.Context()
	store := f.hp.vendorStore
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionRead)}},
	})
	propBytes := mustMarshalJSON(t, teams.ProposalInput{Title: "T", Brief: "Shared team brief.", Roles: []teams.ProposedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: teams.PermissionRead}}})
	specDefault := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Do work."})
	specWithSchema := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Do structured work.", OutputSchema: outputSchema})

	teamID, leadID, mgrID, memID, sibID := "t-tools", "t-lead", "t-mgr", "t-mem", "t-sib"
	mgrSess, memSess, sibSess := "sess-mgr", "sess-mem", "sess-sib"
	seedTeamSessions(t, f, teamID, [][3]string{
		{mgrSess, mgrID, string(vendorstore.RoleTeamManager)},
		{memSess, memID, string(vendorstore.RoleMember)},
		{sibSess, sibID, string(vendorstore.RoleMember)},
	})
	rec, _, err := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "prop-ev", ProposedPayload: propBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c1", ProtectedSpec: specDefault},
			{TaskID: mgrID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleTeamManager, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c2", ProtectedSpec: specDefault},
			{TaskID: memID, ParentTaskID: mgrID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c3", ProtectedSpec: specWithSchema},
			{TaskID: sibID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c4", ProtectedSpec: specDefault, ExplicitStart: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := store.AppendTeamEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "start-ev", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bind := range [][4]string{{leadID, f.session.ID, "a1", "s1"}, {mgrID, mgrSess, "a2", "s2"}, {memID, memSess, "a3", "s3"}} {
		if seq, err = store.AppendTeamEvent(ctx, teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: bind[2], TaskID: bind[0], EventType: vendorstore.EventTaskAssigned, SessionID: bind[1],
		}); err != nil {
			t.Fatal(err)
		}
		if seq, err = store.AppendTeamEvent(ctx, teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: bind[3], TaskID: bind[0], EventType: vendorstore.EventTaskStarted,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return &activeTeamFixture{
		teamProposalFixture: f, teamID: teamID, leadTaskID: leadID, managerTaskID: mgrID, memberTaskID: memID, siblingTaskID: sibID,
		leadBinding: f.binding, managerBinding: mustAttachRole(t, f, mgrSess, vendorstore.RoleTeamManager),
		memberBinding: mustAttachRole(t, f, memSess, vendorstore.RoleMember),
	}
}

func TestTaskStartReturnsImmediatelyAndTheDelegatorIsWokenOnce(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	initRepo(t, f.root)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{}
	f.rt.teamDispatch.deps.Sessions = fakeSess
	f.rt.teamDispatch.deps.Admit = localAdmitter{capacity: fakeSess}

	res, err := f.tools.Call(ctx, f.leadBinding, taskStartToolName, []byte(`{"task_id":"`+f.siblingTaskID+`"}`))
	if err != nil || res.IsError {
		_, directErr := f.rt.teamTasks.Start(ctx, f.leadBinding.SessionID, f.siblingTaskID)
		t.Fatalf("fugue_task_start = (%+v, %v), directErr=%v", res, err, directErr)
	}
	if fakeSess.startCount() != 1 {
		_, tasks, snapErr := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
		if snapErr != nil {
			t.Fatal(snapErr)
		}
		sib, _ := findTask(tasks, f.siblingTaskID)
		t.Fatalf("started sessions = %d, want 1 (sib=%+v)", fakeSess.startCount(), sib)
	}

	// Child is running and has not reported; no mailbox row for lead yet.
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending before done = %+v, want 0", pending)
	}

	// Child finishes with fugue_task_done: delegator (manager) is woken once.
	doneRes, err := f.tools.Call(ctx, f.memberBinding, taskDoneToolName, []byte(`{"summary":"completed work","status":"succeeded"}`))
	if err != nil || doneRes.IsError {
		t.Fatalf("fugue_task_done = (%+v, %v), want success", doneRes, err)
	}
	pending, err = f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].RecipientTaskID != f.managerTaskID {
		t.Fatalf("pending after done = %+v, want 1 row for %s", pending, f.managerTaskID)
	}
}

func TestWrongOutputSchemaKeepsTheResultWithSchemaValidFalse(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	f := newActiveTeamFixture(t, schema)
	ctx := t.Context()

	res, err := f.tools.Call(ctx, f.memberBinding, taskDoneToolName, []byte(`{"summary":"done","output":{"count":"not-an-int"},"status":"succeeded"}`))
	if err != nil || res.IsError {
		t.Fatalf("fugue_task_done = (%+v, %v), want non-error keeping result", res, err)
	}
	if !strings.Contains(res.Text, "schema_valid=false") {
		t.Fatalf("result text = %q, want schema_valid=false", res.Text)
	}

	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := findTask(tasks, f.memberTaskID)
	if mem.State != vendorstore.TaskStateChecking {
		t.Fatalf("member state = %s, want checking", mem.State)
	}

	pulled, err := f.tools.Call(ctx, f.managerBinding, taskResultToolName, []byte(`{"task_id":"`+f.memberTaskID+`","part":"output"}`))
	if err != nil || pulled.IsError {
		t.Fatalf("fugue_task_result = (%+v, %v)", pulled, err)
	}
	if !strings.Contains(pulled.Text, "schema_valid=false") || !strings.Contains(pulled.Text, `"not-an-int"`) {
		t.Fatalf("pulled output = %q, want schema_valid=false and kept output", pulled.Text)
	}
}

func TestMemberCannotCallDelegationTools(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	for _, call := range []struct {
		name string
		args string
	}{
		{taskStartToolName, `{"task_id":"t-sib"}`},
		{taskSendToolName, `{"task_id":"t-sib","message":"hi"}`},
		{taskCancelToolName, `{"task_id":"t-sib"}`},
		{teamNoteToolName, `{"note":"member note"}`},
		{teamProposeToolName, `{"title":"x","brief":"y","tasks":[]}`},
		{teamAmendToolName, `{"add":[]}`},
	} {
		if _, err := f.tools.Call(ctx, f.memberBinding, call.name, []byte(call.args)); errcode.CodeOf(err) != errcode.NotFound {
			t.Fatalf("member Call(%s) code = %v, want NotFound", call.name, errcode.CodeOf(err))
		}
	}
}

func TestMemberTokenCannotOpenALeadTool(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	views, err := f.tools.List(ctx, f.memberBinding)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Name == taskStartToolName || v.Name == teamProposeToolName || v.Name == teamAmendToolName || v.Name == teamNoteToolName {
			t.Fatalf("member token listed lead tool %q", v.Name)
		}
	}
	if _, err := f.tools.Call(ctx, f.memberBinding, taskStartToolName, []byte(`{"task_id":"`+f.siblingTaskID+`"}`)); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("member Call(fugue_task_start) code = %v, want NotFound", errcode.CodeOf(err))
	}
	// Even direct service invocation rejects the member session.
	if _, err := f.rt.teamTasks.Start(ctx, f.memberBinding.SessionID, f.siblingTaskID); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("direct Start by member code = %v, want PermissionDenied", errcode.CodeOf(err))
	}
}

func TestTeamManagerCannotDelegateOutsideItsPart(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	for _, tc := range []struct {
		name string
		args string
	}{
		{taskStartToolName, `{"task_id":"` + f.siblingTaskID + `"}`},
		{taskCancelToolName, `{"task_id":"` + f.siblingTaskID + `"}`},
		{taskStatusToolName, `{"task_id":"` + f.siblingTaskID + `"}`},
	} {
		res, err := f.tools.Call(ctx, f.managerBinding, tc.name, []byte(tc.args))
		if err != nil {
			t.Fatalf("Call(%s) transport err = %v", tc.name, err)
		}
		if !res.IsError {
			t.Fatalf("manager Call(%s) on sibling succeeded: %+v", tc.name, res)
		}
	}
}

func recordAssistantTurn(t *testing.T, f *activeTeamFixture, sessionID, text string) {
	t.Helper()
	ctx := t.Context()
	info, err := f.hp.vendorStore.Snapshot(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.hp.vendorStore.Append(ctx, sessionID, info.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: "t1", TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, appErr := f.hp.vendorStore.Append(ctx, sessionID, seq, &vendorstore.Event{
		Type: vendorstore.TranscriptItemRecorded, TurnID: "t1",
		Item: &vendorstore.Item{
			ID: "asst-1", TurnID: "t1", Kind: string(transcript.AssistantMessage),
			Metadata: vendorstore.ItemMetadata{}, Content: mustMarshalJSON(t, struct{ Text string }{Text: text}),
		},
	}); appErr != nil {
		t.Fatal(appErr)
	}
}

func TestEndedTurnWithoutReportIsNudgedOnceThenFailsNoReport(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{}
	f.rt.teamTasks.sessions = fakeSess
	recordAssistantTurn(t, f, f.memberBinding.SessionID, "partial findings from turn")

	// First completed turn without fugue_task_done: nudges once.
	f.rt.teamTasks.OnTurnCompleted(ctx, f.memberBinding.SessionID)
	sent := fakeSess.sends[f.memberBinding.SessionID]
	if len(sent) != 1 || !strings.Contains(sent[0], "fugue_task_done") {
		t.Fatalf("first miss sent = %+v, want 1 nudge mentioning fugue_task_done", sent)
	}

	// Second completed turn without fugue_task_done: fails with no_report and wakes delegator.
	f.rt.teamTasks.OnTurnCompleted(ctx, f.memberBinding.SessionID)
	if len(fakeSess.sends[f.memberBinding.SessionID]) != 1 {
		t.Fatalf("second miss sent extra nudge: %+v", fakeSess.sends[f.memberBinding.SessionID])
	}
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := findTask(tasks, f.memberTaskID)
	if mem.State != vendorstore.TaskStateFailed || mem.FailureCode != "no_report" {
		t.Fatalf("member = (%s, %s), want (failed, no_report)", mem.State, mem.FailureCode)
	}
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].RecipientTaskID != f.managerTaskID {
		t.Fatalf("pending = %+v, want 1 wake for %s", pending, f.managerTaskID)
	}
}

func mustCallTool(t *testing.T, f *activeTeamFixture, b SessionBinding, name, args string) FugueToolResult {
	t.Helper()
	res, err := f.tools.Call(t.Context(), b, name, []byte(args))
	if err != nil || res.IsError {
		t.Fatalf("%s = (%+v, %v), want success", name, res, err)
	}
	return res
}

func TestTeamNoteBriefAskSendCancelAndStatus(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	mustCallTool(t, f, f.leadBinding, teamNoteToolName, `{"note":"use v2 API"}`)
	brief := mustCallTool(t, f, f.memberBinding, teamBriefToolName, `{}`)
	if !strings.Contains(brief.Text, "Shared team brief.") || !strings.Contains(brief.Text, "use v2 API") || !strings.Contains(brief.Text, "⟪untrusted") {
		t.Fatalf("brief = %q, want brief, note, and untrusted block", brief.Text)
	}
	mustCallTool(t, f, f.managerBinding, taskSendToolName, `{"task_id":"`+f.memberTaskID+`","message":"focus on tests"}`)
	st := mustCallTool(t, f, f.managerBinding, taskStatusToolName, `{"task_id":"`+f.memberTaskID+`"}`)
	if !strings.Contains(st.Text, `"state":"running"`) {
		t.Fatalf("fugue_task_status = %q", st.Text)
	}
	mustCallTool(t, f, f.memberBinding, taskAskToolName, `{"question":"which branch?"}`)
	mustCallTool(t, f, f.managerBinding, taskCancelToolName, `{"task_id":"`+f.memberTaskID+`","reason":"no_longer_needed"}`)

	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := findTask(tasks, f.memberTaskID)
	if mem.State != vendorstore.TaskStateCancelled {
		t.Fatalf("member state = %s, want cancelled", mem.State)
	}
}

var _ = []any{context.Background, slices.Contains[[]string, string], time.Second, testutil.Spin, vendor.Input{}}
