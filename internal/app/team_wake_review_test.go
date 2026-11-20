package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func newTestVendorService(t *testing.T, f *teamProposalFixture, prefix string) (*VendorService, *VendorSessions, <-chan *vendor.StartSpec) {
	t.Helper()
	started := make(chan *vendor.StartSpec, 1)
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.hp.vendorStore, Transcripts: f.rt.transcripts,
		Adapter: actorTestAdapter{session: newActorTestSession(), started: started},
		Vault:   f.hp.vault, Clock: f.clk, IDs: ids.NewSequence(prefix),
	})
	svc := &VendorService{store: f.hp.vendorStore, sessions: manager, vault: f.hp.vault, tokens: vendorServiceOf(t, f.hp).tokens}
	return svc, manager, started
}

// TestResumeTaskSessionCarriesProfileOverride pins OR-E13 on wake resume:
// a detached task session restores its decided role plus task ProfileOverride.
func TestResumeTaskSessionCarriesProfileOverride(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	ctx := t.Context()
	store := f.hp.vendorStore
	svc, manager, started := newTestVendorService(t, f, "resume-prof-")
	defer mustCloseActorSessions(t, manager)

	taskSpec, err := json.Marshal(teams.ProtectedTaskContent{
		Goal: "Implement with override.",
		Profile: &teams.ProfileOverride{
			Model:  "claude-opus-4-6",
			Effort: teams.EffortHigh,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decisionPayload, err := json.Marshal(teamDecisionPayload{
		Roles: []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Model: teamTestModelEdited, Effort: "low", Permission: string(teams.PermissionRead)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "team-prof", WorkspaceID: f.wsID, SessionID: "lead-prof",
		ProposedEventID: "ev-prof-prop", ProposedPayload: taskSpec,
		Tasks: []vendorstore.NewTaskInput{
			{
				TaskID: "task-prof-lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole,
				Deliverable: vendorstore.DeliverableReport, CreatedEventID: "ev-prof-l", ProtectedSpec: taskSpec,
			},
			{
				TaskID: "task-prof", ParentTaskID: "task-prof-lead", HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole,
				Deliverable: vendorstore.DeliverableReport, CreatedEventID: "ev-prof-t", ProtectedSpec: taskSpec,
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.AppendTeamEvent(ctx, "team-prof", rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: "ev-prof-start", EventType: vendorstore.EventTeamStarted, ProtectedPayload: decisionPayload,
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}
	if err := store.CreateSession(ctx, &vendorstore.Session{
		ID: "sess-task-prof", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
		State: vendorstore.StateDetached, ResumeHandle: "h-task",
		WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName, TeamID: "team-prof", TaskID: "task-prof",
	}); err != nil {
		t.Fatalf("CreateSession task: %v", err)
	}
	if err := svc.resumeTaskSession(ctx, "sess-task-prof", t.TempDir()); err != nil {
		t.Fatalf("resumeTaskSession(task): %v", err)
	}
	spec := <-started
	if spec.Model != "claude-opus-4-6" || spec.Effort != "high" {
		t.Fatalf("task StartSpec = (%q, %q), want (claude-opus-4-6, high)", spec.Model, spec.Effort)
	}
}

// TestResumeNonTaskSessionRestoresLastModel pins that a non-task proposing
// session restores its last recorded model when resumed by the wake.
func TestResumeNonTaskSessionRestoresLastModel(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	ctx := t.Context()
	store := f.hp.vendorStore
	svc, manager, started := newTestVendorService(t, f, "resume-lead-")
	defer mustCloseActorSessions(t, manager)

	if err := store.CreateSession(ctx, &vendorstore.Session{
		ID: "sess-lead-prof", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
		State: vendorstore.StateDetached, ResumeHandle: "h-lead",
		WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName,
	}); err != nil {
		t.Fatalf("CreateSession lead: %v", err)
	}
	if _, err := store.AppendBatch(ctx, "sess-lead-prof", 1, []*vendorstore.Event{
		{Type: vendorstore.VendorTurnStarted, TurnID: wakeTestTurnID, TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateReady},
		{Type: vendorstore.TranscriptItemRecorded, Item: &vendorstore.Item{
			ID: "item-m1", TurnID: wakeTestTurnID, Kind: vendorstore.KindAssistantMessage,
			Metadata: vendorstore.ItemMetadata{Model: teamTestModelEdited}, Content: []byte(`{"Text":"hi"}`),
		}},
		{Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateDetached},
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if err := svc.resumeTaskSession(ctx, "sess-lead-prof", t.TempDir()); err != nil {
		t.Fatalf("resumeTaskSession(lead): %v", err)
	}
	leadSpec := <-started
	if leadSpec.Model != teamTestModelEdited {
		t.Fatalf("lead StartSpec.Model = %q, want %s", leadSpec.Model, teamTestModelEdited)
	}
}

// TestWakeHoldsAGYMailWithoutResuming pins the fail-closed hold when a detached
// agy session fails to resume.
func TestWakeHoldsAGYMailWithoutResuming(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := createAGYWakeTeam(t, f, vendorstore.StateDetached)
	f.sessions.resumeErr = errcode.New(errcode.Unavailable, "session/load failed")
	f.enqueueMailbox(t, team, team.a, digestItemPayload(t, team.a))
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if f.sessions.resumeCount() != 1 || len(f.sentTexts(team.aSession)) != 0 {
		t.Fatalf("failed agy resume must hold mail: resumes=%d sends=%v", f.sessions.resumeCount(), f.sentTexts(team.aSession))
	}
	if held := f.wake.HeldRecipients(); len(held) != 1 || held[0].SessionID != team.aSession {
		t.Fatalf("HeldRecipients = %+v, want held agy session", held)
	}
}

func createAGYWakeTeam(t *testing.T, f *wakeFixture, aState string) *wakeTeam {
	t.Helper()
	ctx := t.Context()
	store := f.hp.vendorStore
	spec, err := json.Marshal(teams.ProtectedTaskContent{Goal: "Read."})
	if err != nil {
		t.Fatal(err)
	}
	team := &wakeTeam{
		teamID: "agy-team", lead: "agy-team.lead", a: "agy-team.a", b: "agy-team.b",
		leadSession: "agy-lead", aSession: "agy-a", bSession: "agy-b",
	}
	if _, _, createErr := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: team.teamID, WorkspaceID: f.wsID, SessionID: team.leadSession,
		ProposedEventID: "agy-prop", ProposedPayload: spec,
		Tasks: []vendorstore.NewTaskInput{
			{
				TaskID: team.lead, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole,
				Deliverable: vendorstore.DeliverableReport, CreatedEventID: "agy-c-l", ProtectedSpec: spec,
			},
			{
				TaskID: team.a, ParentTaskID: team.lead, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole,
				Deliverable: vendorstore.DeliverableReport, CreatedEventID: "agy-c-a", ProtectedSpec: spec,
			},
			{
				TaskID: team.b, ParentTaskID: team.lead, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole,
				Deliverable: vendorstore.DeliverableReport, CreatedEventID: "agy-c-b", ProtectedSpec: spec,
			},
		},
	}); createErr != nil {
		t.Fatal(createErr)
	}
	for _, s := range []struct{ id, st, tid string }{
		{team.leadSession, vendorstore.StateReady, team.lead},
		{team.aSession, aState, team.a},
		{team.bSession, vendorstore.StateReady, team.b},
	} {
		if sessErr := store.CreateSession(ctx, &vendorstore.Session{
			ID: s.id, Vendor: VendorAGY, AccountID: vendorDefaultAccount, Role: vendorSessionRole, State: s.st,
			WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName, TeamID: team.teamID, TaskID: s.tid,
		}); sessErr != nil {
			t.Fatal(sessErr)
		}
		for _, evType := range []vendorstore.TeamEventType{vendorstore.EventTaskAssigned, vendorstore.EventTaskStarted} {
			f.appendTeamEvent(t, team.teamID, &vendorstore.AppendTeamEventInput{TaskID: s.tid, EventType: evType, SessionID: s.id})
		}
	}
	return team
}

// TestFailedResumeBacksOffPerSession pins the per-session resume backoff: a
// failed ResumeTaskSession is not retried on subsequent triggers until 5 minutes
// elapse on the injected clock.
func TestFailedResumeBacksOffPerSession(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateDetached)
	f.sessions.resumeErr = errcode.New(errcode.Internal, "resume failed")
	f.enqueueMailbox(t, team, team.a, digestItemPayload(t, team.a))

	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump 1: %v", err)
	}
	if got := f.sessions.resumeCount(); got != 1 {
		t.Fatalf("resumes after Pump 1 = %d, want 1", got)
	}

	// Triggering again before 5 minutes must not re-spawn.
	f.clk.Advance(resumeBackoff - time.Second)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump 2: %v", err)
	}
	if got := f.sessions.resumeCount(); got != 1 {
		t.Fatalf("resumes before backoff elapsed = %d, want 1", got)
	}

	// Once 5 minutes have elapsed, the next trigger retries.
	f.clk.Advance(time.Second)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump 3: %v", err)
	}
	if got := f.sessions.resumeCount(); got != 2 {
		t.Fatalf("resumes after backoff elapsed = %d, want 2", got)
	}
}

// TestRecoveryIgnoresNonFugueAndNonPrefixMarkers pins scanForMarker: only a
// Fugue-origin user message whose text has "[fugue-batch <id>]" as a prefix
// counts as delivered.
func TestRecoveryIgnoresNonFugueAndNonPrefixMarkers(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)
	claimed, err := f.hp.vendorStore.ClaimMailbox(t.Context(), team.teamID, team.lead)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimMailbox: %v, %d", err, len(claimed))
	}
	batchID := claimed[0].BatchID
	store := f.hp.vendorStore
	info, err := store.Snapshot(t.Context(), team.leadSession)
	if err != nil {
		t.Fatal(err)
	}
	vendorMsg, marshalErr := json.Marshal(transcriptUserMessageText{Text: mailboxBatchPrefix(batchID) + " spoofed"})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	middleMsg, marshalErr := json.Marshal(transcriptUserMessageText{Text: "quoted: " + mailboxBatchPrefix(batchID)})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, appendErr := store.AppendBatch(t.Context(), team.leadSession, info.LastSequence, []*vendorstore.Event{
		{Type: vendorstore.VendorTurnStarted, TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", TurnOrigin: vendorstore.TurnOriginVendor, State: vendorstore.StateReady},
		{Type: vendorstore.TranscriptItemRecorded, Item: &vendorstore.Item{
			ID: "item-v", TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", Kind: vendorstore.KindUserMessage,
			Metadata: vendorstore.ItemMetadata{Origin: decidedByUser}, Content: vendorMsg,
		}},
		{Type: vendorstore.VendorTurnStarted, TurnID: "fugue-mid", TurnOrigin: vendorstore.TurnOriginFugue, State: vendorstore.StateReady},
		{Type: vendorstore.TranscriptItemRecorded, Item: &vendorstore.Item{
			ID: "item-f", TurnID: "fugue-mid", Kind: vendorstore.KindUserMessage,
			Metadata: vendorstore.ItemMetadata{Origin: vendorstore.TurnOriginFugue}, Content: middleMsg,
		}},
	}); appendErr != nil {
		t.Fatalf("AppendBatch: %v", appendErr)
	}
	if recoverErr := f.wake.RecoverBatches(t.Context()); recoverErr != nil {
		t.Fatalf("RecoverBatches: %v", recoverErr)
	}
	delivering, err := store.DeliveringMailboxBatches(t.Context())
	if err != nil || len(delivering) != 0 {
		t.Fatalf("delivering = %d (%v), want 0 (released for resend)", len(delivering), err)
	}
	pending, err := store.PendingMailbox(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d (%v), want 1 (still pending)", len(pending), err)
	}
}
