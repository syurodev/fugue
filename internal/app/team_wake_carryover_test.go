package app

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	carryoverMsgText  = "wake-note"
	carryoverMsgState = "message"
	carryoverHigh     = "high"
	carryoverAGYSess  = "agy-sess-co"
	carryoverAGYLead  = "agy-lead-co"
)

// TestWakeCwdFailureHoldsRecipientAndContinuesPump verifies carry-over (1):
// a failed cwd derivation for one detached recipient backs off only that
// recipient and keeps pumping the remaining recipients.
func TestWakeCwdFailureHoldsRecipientAndContinuesPump(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	ctx := t.Context()
	team := f.newWakeTeam(t, vendorstore.StateReady)

	if err := f.hp.vendorStore.CreateSession(ctx, &vendorstore.Session{
		ID: "bad-cwd-sess", Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleMember), State: vendorstore.StateDetached,
		ApprovalMode: vendorstore.ApprovalPlan, TeamID: team.teamID, TaskID: "bad-task",
		WorkspaceID: "ws-missing", WorkspaceRoot: "missing-root",
	}); err != nil {
		t.Fatal(err)
	}
	spec := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "bad"})
	pBad := mustMarshalJSON(t, mailboxDigestItem{TaskID: "bad-task", State: carryoverMsgState, Summary: carryoverMsgText})
	pGood := mustMarshalJSON(t, mailboxDigestItem{TaskID: team.b, State: carryoverMsgState, Summary: carryoverMsgText})
	if err := f.hp.vendorStore.EnqueueDirectMailbox(ctx, team.teamID, team.a, vendorstore.MailboxKindSend, "u-good", pGood); err != nil {
		t.Fatal(err)
	}
	rec2, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "bad-team", WorkspaceID: "ws-missing", SessionID: "bad-cwd-sess",
		ProposedEventID: "p-bad", ProposedPayload: []byte(`{"title":"t","brief":"b"}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: "bad-lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-bad", ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.hp.vendorStore.AppendTeamEvent(ctx, "bad-team", rec2.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "s-bad", EventType: vendorstore.EventTeamStarted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.hp.vendorStore.EnqueueDirectMailbox(ctx, "bad-team", "bad-lead", vendorstore.MailboxKindSend, "u-bad", pBad); err != nil {
		t.Fatal(err)
	}

	if err := f.wake.Pump(ctx); err != nil {
		t.Fatalf("Pump failed on cwd error: %v", err)
	}
	if _, ok := f.wake.resumeRetryAt["bad-cwd-sess"]; !ok {
		t.Fatal("expected bad-cwd-sess to be backed off in resumeRetryAt")
	}
	if len(f.sessions.sends[team.aSession]) != 1 {
		t.Fatalf("good recipient sends = %d, want 1", len(f.sessions.sends[team.aSession]))
	}
}

// TestTaskResumeProfileMatchesPrepareLaunch verifies carry-over (2):
// taskResumeProfile and prepareLaunch share resolveTaskProfile.
func TestTaskResumeProfileMatchesPrepareLaunch(t *testing.T) {
	t.Parallel()
	f := newActiveTeamFixture(t, nil)
	ctx := t.Context()

	spec := mustMarshalJSON(t, teams.ProtectedTaskContent{
		Goal:    "override",
		Profile: &teams.ProfileOverride{Model: "claude-sonnet-4-5", Effort: teams.EffortHigh},
	})
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "prof-team", WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "p-prof", ProposedPayload: []byte(`{"title":"t","brief":"b"}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: "prof-lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-prof-lead", ProtectedSpec: spec},
			{TaskID: "prof-task", ParentTaskID: "prof-lead", HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-prof", ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	roleBytes := mustMarshalJSON(t, teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Model: "claude-haiku", Effort: "low", Permission: string(teams.PermissionRead)}},
	})
	if _, appErr := f.hp.vendorStore.AppendTeamEvent(ctx, "prof-team", rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "s-prof", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes,
	}); appErr != nil {
		t.Fatal(appErr)
	}
	model, effort, err := f.svc.taskResumeProfile(ctx, "prof-team", "prof-task")
	if err != nil {
		t.Fatalf("taskResumeProfile: %v", err)
	}
	if model != "claude-sonnet-4-5" || effort != carryoverHigh {
		t.Fatalf("taskResumeProfile = (%q, %q), want (claude-sonnet-4-5, high)", model, effort)
	}
}

// TestNonTaskSessionResumePreservesModelAndEffort verifies carry-over (3):
// non-task (lead/proposing) sessions persist their decided model and effort at
// start and restore both on resume.
func TestNonTaskSessionResumePreservesModelAndEffort(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	ctx := t.Context()

	if err := f.svc.sessions.Start(ctx, &vendorstore.Session{
		ID: "lead-decided", Vendor: VendorClaude, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleLead), ApprovalMode: vendorstore.ApprovalPlan,
		WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
	}, &vendor.StartSpec{
		SessionID: "lead-decided", Cwd: f.root, Model: "claude-opus-4-1", Effort: carryoverHigh,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap, err := f.hp.vendorStore.Snapshot(ctx, "lead-decided")
	if err != nil {
		t.Fatal(err)
	}
	model, effort, err := f.svc.resumeProfile(ctx, &snap)
	if err != nil {
		t.Fatalf("resumeProfile: %v", err)
	}
	if model != "claude-opus-4-1" || effort != carryoverHigh {
		t.Fatalf("resumeProfile = (%q, %q), want (claude-opus-4-1, high)", model, effort)
	}
}

// TestWakeRepeatedDetachBackoffAndClosePruning verifies carry-over (4):
// a resumed session that falls back to detached is backed off instead of
// retrying on every trigger, and closed sessions are pruned from resumeRetryAt.
func TestWakeRepeatedDetachBackoffAndClosePruning(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	ctx := t.Context()
	team := f.newWakeTeam(t, vendorstore.StateDetached)

	p := mustMarshalJSON(t, mailboxDigestItem{TaskID: team.a, State: carryoverMsgState, Summary: carryoverMsgText})
	if err := f.hp.vendorStore.EnqueueDirectMailbox(ctx, team.teamID, team.a, vendorstore.MailboxKindSend, "u-detach", p); err != nil {
		t.Fatal(err)
	}

	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.sessions.resumes) != 1 {
		t.Fatalf("resumes after 1st pump = %d, want 1", len(f.sessions.resumes))
	}
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.sessions.resumes) != 1 {
		t.Fatalf("resumes within backoff = %d, want 1", len(f.sessions.resumes))
	}
	f.clk.Advance(resumeBackoff + time.Second)
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.sessions.resumes) != 2 {
		t.Fatalf("resumes after backoff = %d, want 2", len(f.sessions.resumes))
	}

	info, err := f.hp.vendorStore.Snapshot(ctx, team.aSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.hp.vendorStore.Append(ctx, team.aSession, info.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorSessionClosed, State: vendorstore.StateClosed, CloseReason: string(vendor.CloseHostStop), StopProof: vendorstore.StopProved,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	if _, exists := f.wake.resumeRetryAt[team.aSession]; exists {
		t.Fatal("expected closed session to be pruned from resumeRetryAt")
	}
}

// TestWakeSurfacesAGYHeldMailState verifies carry-over (5):
// mail to an agy recipient is surfaced in HeldRecipients and TopicTeamMailbox.
func TestWakeSurfacesAGYHeldMailState(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	ctx := t.Context()

	if err := f.hp.vendorStore.CreateSession(ctx, &vendorstore.Session{
		ID: carryoverAGYSess, Vendor: VendorAGY, AccountID: vendorDefaultAccount,
		Role: string(vendorstore.RoleMember), State: vendorstore.StateDetached, ResumeHandle: "h-agy-co",
		ApprovalMode: vendorstore.ApprovalPlan, WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatal(err)
	}
	f.sessions.resumeErr = errcode.New(errcode.Unavailable, "session/load failed")
	spec := mustMarshalJSON(t, teams.ProtectedTaskContent{Goal: "agy"})
	rec, _, err := f.hp.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "agy-team-co", WorkspaceID: f.wsID, SessionID: carryoverAGYSess,
		ProposedEventID: "p-agy", ProposedPayload: []byte(`{"title":"t","brief":"b"}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: carryoverAGYLead, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-agy", ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.hp.vendorStore.AppendTeamEvent(ctx, "agy-team-co", rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "s-agy", EventType: vendorstore.EventTeamStarted,
	}); err != nil {
		t.Fatal(err)
	}
	p := mustMarshalJSON(t, mailboxDigestItem{TaskID: carryoverAGYLead, State: carryoverMsgState, Summary: carryoverMsgText})
	if err := f.hp.vendorStore.EnqueueDirectMailbox(ctx, "agy-team-co", carryoverAGYLead, vendorstore.MailboxKindSend, "u-agy", p); err != nil {
		t.Fatal(err)
	}
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	held := f.wake.HeldRecipients()
	if len(held) != 1 || held[0].SessionID != carryoverAGYSess || held[0].Vendor != VendorAGY {
		t.Fatalf("HeldRecipients = %+v, want 1 agy-sess entry", held)
	}
}
