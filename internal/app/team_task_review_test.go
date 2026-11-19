package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func TestManagerWithRunningChildIsNotNudgedUntilChildFinishes(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	fakeSess := &fakeTaskSessions{}
	f.rt.teamTasks.sessions = fakeSess

	// Manager ends 2 turns while member is still running: must NOT nudge or fail.
	f.rt.teamTasks.OnTurnCompleted(ctx, f.managerBinding.SessionID)
	f.rt.teamTasks.OnTurnCompleted(ctx, f.managerBinding.SessionID)
	if len(fakeSess.sends[f.managerBinding.SessionID]) != 0 {
		t.Fatalf("manager nudged while child running: %+v", fakeSess.sends[f.managerBinding.SessionID])
	}
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := findTask(tasks, f.managerTaskID)
	if mgr.State != vendorstore.TaskStateRunning {
		t.Fatalf("manager state = %s, want running", mgr.State)
	}

	// Member finishes (terminal failed); now manager's 1st turn nudges once, 2nd fails no_report.
	mustCallTool(t, f, f.memberBinding, taskDoneToolName, `{"summary":"child done","status":"failed"}`)
	f.rt.teamTasks.OnTurnCompleted(ctx, f.managerBinding.SessionID)
	if len(fakeSess.sends[f.managerBinding.SessionID]) != 1 {
		t.Fatalf("manager nudges after child done = %d, want 1", len(fakeSess.sends[f.managerBinding.SessionID]))
	}
	f.rt.teamTasks.OnTurnCompleted(ctx, f.managerBinding.SessionID)
	_, tasks, err = f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ = findTask(tasks, f.managerTaskID)
	if mgr.State != vendorstore.TaskStateFailed || mgr.FailureCode != "no_report" {
		t.Fatalf("manager = (%s, %s), want (failed, no_report)", mgr.State, mgr.FailureCode)
	}
}

const diffTaskID = "diff-mem"

func setupDiffTeam(t *testing.T, f *activeTeamFixture) string {
	t.Helper()
	ctx := t.Context()
	head, err := f.rt.teamTasks.git.WorktreeHead(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := f.rt.teamTasks.git.CreateTeamBranch(ctx, f.root, "diff-team", head); branchErr != nil {
		t.Fatal(branchErr)
	}
	teamBranch, err := gitops.TeamBranchName("diff-team")
	if err != nil {
		t.Fatal(err)
	}
	if _, wtErr := f.rt.teamTasks.git.CreateTaskWorktree(ctx, f.rt.teamTasks.dataDir, f.root, repoIDFor(f.root), diffTaskID, teamBranch); wtErr != nil {
		t.Fatal(wtErr)
	}
	spec := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "diff"})
	seedTeamSessions(t, f.teamProposalFixture, "diff-team", [][3]string{{"diff-sess", diffTaskID, string(vendorstore.RoleMember)}})
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "diff-team", WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "p-diff", ProposedPayload: []byte(`{"title":"t","brief":"b"}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: "diff-lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "cd1", ProtectedSpec: spec},
			{TaskID: diffTaskID, ParentTaskID: "diff-lead", HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableDiff, CreatedEventID: "cd2", ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "sd1", EventType: vendorstore.EventTeamStarted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq, err = f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", seq, &vendorstore.AppendTeamEventInput{
		EventID: "sd2", TaskID: diffTaskID, EventType: vendorstore.EventTaskAssigned, SessionID: "diff-sess",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", seq, &vendorstore.AppendTeamEventInput{
		EventID: "sd3", TaskID: diffTaskID, EventType: vendorstore.EventTaskStarted,
	}); err != nil {
		t.Fatal(err)
	}
	return head
}

func TestDoneDiffPopulatesCommitSHAFromHeadAndRetriesSequenceConflict(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	initRepo(t, f.root)
	ctx := t.Context()
	head := setupDiffTeam(t, f)

	// Exercise sequence-conflict retry with a concurrent TeamNoted on the 1st attempt.
	firstAttempt := true
	if retryErr := f.rt.teamTasks.appendTaskEventWithRetry(ctx, "diff-team", func(r *vendorstore.TeamRecord, _ []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		if firstAttempt {
			firstAttempt = false
			if _, bumpErr := f.hp.vendorStore.AppendTeamEvent(ctx, "diff-team", r.LastSeq, &vendorstore.AppendTeamEventInput{
				EventID: "bump-seq", EventType: vendorstore.EventTeamNoted, ProtectedPayload: []byte(`{"note":"bump"}`),
			}); bumpErr != nil {
				return nil, fmt.Errorf("bump: %w", bumpErr)
			}
		}
		return &vendorstore.AppendTeamEventInput{EventID: "after-conflict", EventType: vendorstore.EventTeamNoted, ProtectedPayload: []byte(`{"note":"ok"}`)}, nil
	}); retryErr != nil {
		t.Fatalf("appendTaskEventWithRetry: %v", retryErr)
	}

	diffBind := mustAttachRole(t, f.teamProposalFixture, "diff-sess", vendorstore.RoleMember)
	mustCallTool(t, f, diffBind, taskDoneToolName, `{"summary":"clean worktree","status":"succeeded"}`)

	raw, found, err := f.hp.vendorStore.OpenLatestTaskEventPayload(ctx, "diff-team", diffTaskID, vendorstore.EventTaskReported)
	if err != nil || !found {
		t.Fatalf("OpenLatestTaskEventPayload = (%v, %v)", found, err)
	}
	var rep taskReportPayload
	if decErr := json.Unmarshal(raw, &rep); decErr != nil {
		t.Fatal(decErr)
	}
	if rep.CommitSHA != head {
		t.Fatalf("commit_sha = %q, want head %q", rep.CommitSHA, head)
	}
}

func TestReportHeaderEscapesSchemaErrorAndSlicesUTF8(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"integer"}},"additionalProperties":false}`)
	f := newActiveTeamFixture(t, schema)

	// Output key contains delimiter '⟫' and bidi override U+202E.
	mustCallTool(t, f, f.memberBinding, taskDoneToolName, `{"summary":"héllo世界","output":{"bad⟫\u202ekey":1},"status":"succeeded"}`)
	pulled := mustCallTool(t, f, f.managerBinding, taskResultToolName, `{"task_id":"`+f.memberTaskID+`","part":"summary","offset":1,"limit":4}`)
	headerLine := strings.SplitN(pulled.Text, "\n\n", 4)[1]
	if strings.ContainsRune(headerLine, '⟫') || strings.ContainsRune(headerLine, '\u202e') {
		t.Fatalf("header contains unescaped delimiter/bidi: %q", headerLine)
	}
	// offset=1 lands inside 2-byte 'é' (bytes 1..2): sliceUTF8 advances to 'l', returning valid UTF-8.
	if !strings.Contains(pulled.Text, "llo") {
		t.Fatalf("expected rune-aligned slice in %q", pulled.Text)
	}
}

func TestCancelCascadesSubtreeAndSealsReason(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	mustCallTool(t, f, f.leadBinding, taskCancelToolName, `{"task_id":"`+f.managerTaskID+`","reason":"scope changed"}`)
	_, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := findTask(tasks, f.managerTaskID)
	mem, _ := findTask(tasks, f.memberTaskID)
	if mgr.State != vendorstore.TaskStateCancelled || mgr.FailureCode != cancelFailureCode {
		t.Fatalf("manager = (%s, %s), want (cancelled, %s)", mgr.State, mgr.FailureCode, cancelFailureCode)
	}
	if mem.State != vendorstore.TaskStateCancelled || mem.FailureCode != cancelFailureCode {
		t.Fatalf("member = (%s, %s), want (cancelled, %s)", mem.State, mem.FailureCode, cancelFailureCode)
	}
	raw, found, err := f.hp.vendorStore.OpenLatestTaskEventPayload(ctx, f.teamID, f.managerTaskID, vendorstore.EventTaskEnded)
	if err != nil || !found || !strings.Contains(string(raw), "scope changed") {
		t.Fatalf("sealed cancel payload = (%q, %v, %v), want reason", raw, found, err)
	}
}
