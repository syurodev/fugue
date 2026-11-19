package app

// F11.3b wake evidence: the §6.1 batching window and the one-digest rule, the
// at-least-once delivery with its transcript-checked recovery (Q-122), the
// resume of a detached session before the wake (Q-123), and the OR-E13
// delivery through the real decide path.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// wakeTestTurnID is the transcript turn the marker test records.
const wakeTestTurnID = "wake-turn"

// teamTestRootName is the workspace root name the fixture TOML defines.
const teamTestRootName = "main"

// wakeFixture is the mailbox pump fixture: a real store and vault with a fake
// vendor layer. The host's own wake keeps running (it is wired at startup),
// but it is never notified here: the tests drive their own pump directly, so
// the two never claim one recipient twice — the claim itself is atomic.
type wakeFixture struct {
	*teamProposalFixture
	sessions *fakeTaskSessions
	wake     *teamWake
	dataDir  string
}

func newWakeFixture(t *testing.T) *wakeFixture {
	t.Helper()
	f := newTeamProposalFixture(t, "")
	fake := &fakeTaskSessions{}
	dataDir := t.TempDir()
	wake := newTeamWake(&teamWakeDeps{
		Store: f.hp.vendorStore, Sessions: fake, Roots: f.rt.workspaces.domain,
		Clock: f.clk, IDs: ids.NewSequence("wake-nonce-"), Vault: f.hp.vault, DataDir: dataDir,
	})
	return &wakeFixture{teamProposalFixture: f, sessions: fake, wake: wake, dataDir: dataDir}
}

// wakeTeam is one stored team whose tasks are running in fake sessions.
type wakeTeam struct {
	teamID, lead, a, b              string
	leadSession, aSession, bSession string
}

// newWakeTeam stores a lead with two running members, each bound to a fake
// session row in the given states.
func (f *wakeFixture) newWakeTeam(t *testing.T, aState string) *wakeTeam {
	t.Helper()
	ctx := t.Context()
	store := f.hp.vendorStore
	team := &wakeTeam{
		teamID: "wake-team", lead: "wake-team.lead", a: "wake-team.a", b: "wake-team.b",
		leadSession: "wake-lead", aSession: "wake-a", bSession: "wake-b",
	}
	spec, err := json.Marshal(teams.ProtectedTaskContent{Goal: "Read the fixture."})
	if err != nil {
		t.Fatal(err)
	}
	task := func(id, parent string, role vendorstore.TeamRole) vendorstore.NewTaskInput {
		return vendorstore.NewTaskInput{
			TaskID: id, ParentTaskID: parent, HierarchyRole: role, CatalogRole: teamTestRole,
			Deliverable: vendorstore.DeliverableReport, CreatedEventID: id + "-created", ProtectedSpec: spec,
		}
	}
	if _, _, err := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: team.teamID, WorkspaceID: f.wsID, SessionID: team.leadSession,
		ProposedEventID: team.teamID + "-proposed", ProposedPayload: spec,
		Tasks: []vendorstore.NewTaskInput{
			task(team.lead, "", vendorstore.RoleLead),
			task(team.a, team.lead, vendorstore.RoleMember),
			task(team.b, team.lead, vendorstore.RoleMember),
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	sessions := []struct {
		id, state string
		taskID    string
	}{
		{team.leadSession, vendorstore.StateReady, team.lead},
		{team.aSession, aState, team.a},
		{team.bSession, vendorstore.StateReady, team.b},
	}
	for _, s := range sessions {
		if err := store.CreateSession(ctx, &vendorstore.Session{
			ID: s.id, Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole, State: s.state,
			WorkspaceID: f.wsID, WorkspaceRoot: teamTestRootName, TeamID: team.teamID, TaskID: s.taskID,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", s.id, err)
		}
		for _, eventType := range []vendorstore.TeamEventType{vendorstore.EventTaskAssigned, vendorstore.EventTaskStarted} {
			f.appendTeamEvent(t, team.teamID, &vendorstore.AppendTeamEventInput{
				TaskID: s.taskID, EventType: eventType, SessionID: s.id,
			})
		}
	}
	return team
}

// appendTeamEvent appends one task event at the team's current sequence.
func (f *wakeFixture) appendTeamEvent(t *testing.T, teamID string, ev *vendorstore.AppendTeamEventInput) {
	t.Helper()
	rec, _, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatalf("TeamSnapshot: %v", err)
	}
	if ev.EventID == "" {
		ev.EventID = fmt.Sprintf("%s:%s:%d", teamID, ev.EventType, rec.LastSeq+1)
	}
	if _, err := f.hp.vendorStore.AppendTeamEvent(t.Context(), teamID, rec.LastSeq, ev); err != nil {
		t.Fatalf("AppendTeamEvent(%s): %v", ev.TaskID, err)
	}
}

// endMember ends one member task and enqueues the digest item its lead
// receives (§6.2: final failures go to the delegator directly).
func (f *wakeFixture) endMember(t *testing.T, team *wakeTeam, member, code string) {
	t.Helper()
	f.appendTeamEvent(t, team.teamID, &vendorstore.AppendTeamEventInput{
		TaskID: member, EventType: vendorstore.EventTaskEnded, Outcome: vendorstore.TaskOutcomeFailed, FailureCode: code,
		Mailbox: digestItemRows(member, team.lead, code),
	})
}

// enqueueMailbox appends a cancelled queued task carrying one answer mailbox
// row, so a test can place immediate mail without the task tools.
func (f *wakeFixture) enqueueMailbox(t *testing.T, team *wakeTeam, recipient string, payload []byte) {
	t.Helper()
	f.appendTeamEvent(t, team.teamID, &vendorstore.AppendTeamEventInput{
		TaskID: team.b, EventType: vendorstore.EventTaskEnded, Outcome: string(vendorstore.TaskStateCancelled),
		Mailbox: []vendorstore.MailboxEnqueueInput{{RecipientTaskID: recipient, Kind: vendorstore.MailboxKindAnswer, Payload: payload}},
	})
}

func digestItemPayload(t *testing.T, taskID string) []byte {
	t.Helper()
	raw, err := json.Marshal(mailboxDigestItem{TaskID: taskID, State: "running", Summary: "summary of " + taskID})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sentTexts returns what the fake layer sent to one session.
func (f *wakeFixture) sentTexts(sessionID string) []string {
	f.sessions.mu.Lock()
	defer f.sessions.mu.Unlock()
	return append([]string(nil), f.sessions.sends[sessionID]...)
}

// TestDigestWaitsThirtySecondsOrTheWholeDependencyGroup pins the §6.1
// batching window: a lead digest waits for the window while any direct child
// still runs, and goes out when the window ends.
func TestDigestWaitsThirtySecondsOrTheWholeDependencyGroup(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if got := f.sentTexts(team.leadSession); len(got) != 0 {
		t.Fatalf("the digest went out during the window: %v", got)
	}
	f.clk.Advance(digestWait + time.Second)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	sent := f.sentTexts(team.leadSession)
	if len(sent) != 1 {
		t.Fatalf("sends = %d, want the one digest of the window", len(sent))
	}
	if !strings.Contains(sent[0], team.a) || !strings.Contains(sent[0], "failed("+failWorktree+")") {
		t.Fatalf("digest = %q", sent[0])
	}
}

// TestOneDigestForAGroupOfTasks pins one digest for a group: both members of
// the lead end, and one turn carries both items.
func TestOneDigestForAGroupOfTasks(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.endMember(t, team, team.b, failSessionStart)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	sent := f.sentTexts(team.leadSession)
	if len(sent) != 1 {
		t.Fatalf("sends = %d, want one digest for the group", len(sent))
	}
	if !strings.Contains(sent[0], team.a) || !strings.Contains(sent[0], team.b) {
		t.Fatalf("digest = %q, want both group items", sent[0])
	}
}

// TestDeliveredMessageIsNeverResent pins the at-least-once rule's delivered
// half: a delivered batch never goes out again, however often the pump runs.
func TestDeliveredMessageIsNeverResent(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)
	for range 3 {
		if err := f.wake.Pump(t.Context()); err != nil {
			t.Fatalf("Pump: %v", err)
		}
	}
	if got := len(f.sentTexts(team.leadSession)); got != 1 {
		t.Fatalf("sends = %d, want exactly one delivery", got)
	}
}

// TestMemberKindIsDeliveredAtOnce pins Q-121: an answer or send message to a
// member never waits for the digest window.
func TestMemberKindIsDeliveredAtOnce(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.enqueueMailbox(t, team, team.a, digestItemPayload(t, team.a))
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	sent := f.sentTexts(team.aSession)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "[fugue-batch ") {
		t.Fatalf("member mail = %v, want one batched turn at once", sent)
	}
}

// TestCrashAfterSendRedeliversOnlyWhenTheTranscriptLacksTheBatch pins the
// recovery of a `delivering` batch whose transcript lacks the marker (Q-122):
// the send never landed, so the rows resend with the same batch ID.
func TestCrashAfterSendRedeliversOnlyWhenTheTranscriptLacksTheBatch(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)

	// A crash after the send recorded nothing: the claim is left delivering.
	claimed, claimErr := f.hp.vendorStore.ClaimMailbox(t.Context(), team.teamID, team.lead)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimMailbox: %v, %d rows", claimErr, len(claimed))
	}
	batchID := claimed[0].BatchID

	// The fake session's transcript holds no marker: absent means resend.
	if err := f.wake.RecoverBatches(t.Context()); err != nil {
		t.Fatalf("RecoverBatches: %v", err)
	}
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	sent := f.sentTexts(team.leadSession)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], mailboxBatchPrefix(batchID)) {
		t.Fatalf("redelivery = %v, want one resend of batch %s", sent, batchID)
	}
}

// TestCrashAfterSendWithTheMarkerRecordedDeliversWithoutResend pins the other
// half of Q-122: the transcript holds the marker, so the crash happened after
// the send landed and the batch is delivered without a resend.
func TestCrashAfterSendWithTheMarkerRecordedDeliversWithoutResend(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)
	claimed, claimErr := f.hp.vendorStore.ClaimMailbox(t.Context(), team.teamID, team.lead)
	if claimErr != nil || len(claimed) != 1 {
		t.Fatalf("ClaimMailbox: %v, %d rows", claimErr, len(claimed))
	}
	f.recordTranscriptMarker(t, team.leadSession, claimed[0].BatchID)
	if err := f.wake.RecoverBatches(t.Context()); err != nil {
		t.Fatalf("RecoverBatches: %v", err)
	}
	if got := f.sentTexts(team.leadSession); len(got) != 0 {
		t.Fatalf("sends = %v, want no resend when the transcript holds the marker", got)
	}
	pending, pendErr := f.hp.vendorStore.PendingMailbox(t.Context())
	if pendErr != nil || len(pending) != 0 {
		t.Fatalf("pending mailbox = %v, %d rows, want empty", pendErr, len(pending))
	}
	delivering, delErr := f.hp.vendorStore.DeliveringMailboxBatches(t.Context())
	if delErr != nil || len(delivering) != 0 {
		t.Fatalf("delivering batches = %v, %d, want empty", delErr, len(delivering))
	}
}

// TestRecoveryNeverResendsABatchThatIsBeingDelivered pins the single-flight
// rule: while a pump is blocked inside Send (the claim is `delivering`, the
// marker is not in the transcript yet), a wake triggers no second Send for
// the batch — recovery runs once at start and the pump runs single-flight in
// the loop goroutine.
func TestRecoveryNeverResendsABatchThatIsBeingDelivered(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	f.sessions.mu.Lock()
	f.sessions.sendStarted = started
	f.sessions.release = release
	f.sessions.mu.Unlock()

	// The first pump is blocked inside Send: the claim is `delivering` and
	// the marker is not in the transcript yet.
	pumpDone := make(chan error, 1)
	pumpCtx := t.Context()
	supervise.Go(pumpCtx, "test.wake-pump", func(context.Context) error {
		pumpDone <- f.wake.Pump(pumpCtx)
		return nil
	})
	<-started

	// Concurrent triggers — a wake burst and a second pump — must neither
	// resend the batch nor claim it again while the send is in flight.
	for range 3 {
		f.wake.Notify()
	}
	second := make(chan error, 1)
	secondCtx := t.Context()
	supervise.Go(secondCtx, "test.wake-pump-second", func(context.Context) error {
		second <- f.wake.Pump(secondCtx)
		return nil
	})

	close(release)
	if err := <-pumpDone; err != nil {
		t.Fatalf("first Pump: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second Pump: %v", err)
	}
	// One in-flight delivery, no resend of the batch it was sending.
	if got := f.sentTexts(team.leadSession); len(got) != 1 {
		t.Fatalf("sends = %d, want exactly one delivery with no resend: %v", len(got), got)
	}
	claims, err := f.hp.vendorStore.DeliveringMailboxBatches(t.Context())
	if err != nil || len(claims) != 0 {
		t.Fatalf("delivering batches = %v, %d, want the batch delivered", err, len(claims))
	}
}

// TestSendWithUnknownOutcomeKeepsTheBatchDelivering pins the refused-only
// release (Q-122). A generic failure after the write keeps the claim
// `delivering` — the outcome is unknown and only the transcript recovery may
// resolve it. A refused send (busy) releases the claim back to pending, so
// the next pump may take it again.
func TestSendWithUnknownOutcomeKeepsTheBatchDelivering(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)

	f.sessions.mu.Lock()
	f.sessions.sendErr = errcode.New(errcode.Internal, "injected send failure")
	f.sessions.mu.Unlock()
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	f.assertBatches(t, 1, 0, "the claim is kept for recovery: an unknown outcome is never re-queued by the pump")
	// The transcript has no marker, so the recovery resolves the claim as
	// undelivered and queues the resend with the same batch ID.
	if err := f.wake.RecoverBatches(t.Context()); err != nil {
		t.Fatalf("RecoverBatches: %v", err)
	}
	f.assertBatches(t, 0, 1, "the claim resolved by the transcript check")

	// A refused send releases the claim back to pending: the turn was never
	// recorded, so the next trigger may take it again.
	f.sessions.mu.Lock()
	f.sessions.sendErr = vendor.ErrBusy
	f.sessions.mu.Unlock()
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump after refused send: %v", err)
	}
	f.assertBatches(t, 0, 1, "the refused claim released back to pending")
}

// assertBatches asserts the delivering and pending mailbox row counts.
func (f *wakeFixture) assertBatches(t *testing.T, delivering, pending int, what string) {
	t.Helper()
	claims, err := f.hp.vendorStore.DeliveringMailboxBatches(t.Context())
	if err != nil || len(claims) != delivering {
		t.Fatalf("%s: delivering batches = %v, %d, want %d", what, err, len(claims), delivering)
	}
	rows, err := f.hp.vendorStore.PendingMailbox(t.Context())
	if err != nil || len(rows) != pending {
		t.Fatalf("%s: pending mailbox = %v, %d, want %d", what, err, len(rows), pending)
	}
}

// recordTranscriptMarker records a Fugue-origin user turn carrying the batch
// marker, the way a send that landed before a crash looks in the transcript.
func (f *wakeFixture) recordTranscriptMarker(t *testing.T, sessionID, batchID string) {
	t.Helper()
	store := f.hp.vendorStore
	info, err := store.Snapshot(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	seq, err := store.Append(t.Context(), sessionID, info.LastSequence, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: wakeTestTurnID, TurnOrigin: vendorstore.TurnOriginFugue,
		State: vendorstore.StateReady,
	})
	if err != nil {
		t.Fatalf("append the turn: %v", err)
	}
	content, err := json.Marshal(transcriptUserMessageText{Text: mailboxBatchPrefix(batchID) + "\n\ndigest"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), sessionID, seq, &vendorstore.Event{
		Type: vendorstore.TranscriptItemRecorded, TurnID: wakeTestTurnID,
		Item: &vendorstore.Item{
			ID: "wake-item", TurnID: wakeTestTurnID, Kind: vendorstore.KindUserMessage,
			Metadata: vendorstore.ItemMetadata{Origin: "fugue"},
			Content:  content,
		},
	}); err != nil {
		t.Fatalf("append the marker item: %v", err)
	}
}

// TestDetachedSessionIsResumedBeforeTheWake pins Q-123: a detached session is
// first resumed from its stored handle through a host-derived worktree path,
// and only a later trigger delivers to it.
func TestDetachedSessionIsResumedBeforeTheWake(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateDetached)
	f.enqueueMailbox(t, team, team.a, digestItemPayload(t, team.a))
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if f.sessions.resumeCount() != 1 || len(f.sentTexts(team.aSession)) != 0 {
		t.Fatalf("the wake sent before the resume: resumes = %d, sends = %v",
			f.sessions.resumeCount(), f.sentTexts(team.aSession))
	}
	resume := f.sessions.resumes[0]
	if resume.SessionID != team.aSession || !strings.HasPrefix(resume.Cwd, f.dataDir) ||
		!strings.HasSuffix(resume.Cwd, team.a) {
		t.Fatalf("resume = %+v, want the task worktree under the data directory", resume)
	}
	// The resumed process turned ready; the next trigger delivers.
	f.setSessionState(t, team.aSession, vendorstore.StateReady)
	if err := f.wake.Pump(t.Context()); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if got := len(f.sentTexts(team.aSession)); got != 1 {
		t.Fatalf("sends after resume = %d, want one", got)
	}
}

// setSessionState moves a session row the way its actor does, through a
// VendorSessionStateChanged event.
func (f *wakeFixture) setSessionState(t *testing.T, sessionID, state string) {
	t.Helper()
	info, err := f.hp.vendorStore.Snapshot(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := f.hp.vendorStore.Append(t.Context(), sessionID, info.LastSequence,
		&vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, State: state}); err != nil {
		t.Fatalf("append the state change: %v", err)
	}
}

// TestWakeHoldsWhileTheVaultIsLocked pins the fail-closed half of Q-123: a
// locked vault holds the mail instead of delivering it.
func TestWakeHoldsWhileTheVaultIsLocked(t *testing.T) {
	t.Parallel()
	f := newWakeFixture(t)
	team := f.newWakeTeam(t, vendorstore.StateReady)
	f.endMember(t, team, team.a, failWorktree)
	f.clk.Advance(digestWait + time.Second)
	f.hp.vault.Lock()
	err := f.wake.Pump(t.Context())
	if err == nil || errcode.CodeOf(err) != errcode.VendorVaultLocked {
		t.Fatalf("Pump error = %v, want vault_locked", err)
	}
	if got := len(f.sentTexts(team.leadSession)); got != 0 {
		t.Fatalf("the wake delivered while the vault was locked: %v", f.sentTexts(team.leadSession))
	}
}

// TestApprovedPlanAndChangeListAreDeliveredToTheProposingSession pins the
// OR-E13 delivery through the real decide path: the approval enqueues the
// message in the decision transaction, and the proposing session — busy in
// its turn — receives it as one Fugue-origin turn once that turn ends.
func TestApprovedPlanAndChangeListAreDeliveredToTheProposingSession(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	args := validTeamProposalArgs()
	edit, err := json.Marshal(teams.EditSet{Version: teams.EditSetVersion, Roles: []teams.RoleEdit{{Name: teamTestRole, Model: teamTestModelEdited}}})
	if err != nil {
		t.Fatal(err)
	}
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, "wake-approve", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(edit)); answerErr != nil {
		t.Fatalf("approve with edits: %v", answerErr)
	}
	lead := teamID + "." + teamTestLeadTask
	pending, err := f.hp.vendorStore.PendingMailbox(t.Context())
	if err != nil || len(pending) != 1 || pending[0].RecipientTaskID != lead || !pending[0].HasImmediate {
		t.Fatalf("pending mailbox = %v, %d, want the lead's immediate decision message", err, len(pending))
	}
	// The proposing session is busy in its held turn, so the mail holds; the
	// turn's end wakes the pump, which delivers the decision turn.
	if _, err := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "wake-interrupt", SessionID: f.session.ID}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	text := f.awaitMailboxTurn(t, f.session.ID)
	for _, want := range []string{decisionApproveEdited, "the approved edits changed its settings", args.Tasks[1].Goal} {
		if !strings.Contains(text, want) {
			t.Fatalf("decision turn = %q, want it to carry %q", text, want)
		}
	}
}

// TestSoloDecisionDeliversTheUserNote pins the solo half of OR-E13: the
// user's note reaches the proposing session as Fugue text, with no plan
// block, once its turn ends.
func TestSoloDecisionDeliversTheUserNote(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	if _, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	const note = "Keep it solo for now."
	if answerErr := f.answerCard(t, "wake-solo", teamProposeToolName, teamDecisionQuestionID, decisionSolo, note); answerErr != nil {
		t.Fatalf("solo: %v", answerErr)
	}
	if _, err := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "wake-interrupt", SessionID: f.session.ID}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	text := f.awaitMailboxTurn(t, f.session.ID)
	if !strings.Contains(text, note) || strings.Contains(text, "⟪untrusted") {
		t.Fatalf("solo turn = %q, want the note as Fugue text and no data block", text)
	}
}

// awaitMailboxTurn waits for the wake pump to deliver one Fugue-origin batch
// turn to the session and returns its text.
func (f *teamProposalFixture) awaitMailboxTurn(t *testing.T, sessionID string) string {
	t.Helper()
	var text string
	testutil.Poll(t, "the mailbox turn", func() bool {
		found, ok := f.mailboxTurnText(t.Context(), sessionID)
		if !ok {
			return false
		}
		text = found
		return true
	})
	return text
}

// mailboxTurnText reads the newest Fugue-origin batch turn of the session.
func (f *teamProposalFixture) mailboxTurnText(ctx context.Context, sessionID string) (string, bool) {
	items, err := f.hp.vendorStore.Transcript(ctx, sessionID, 0, 100)
	if err != nil {
		return "", false
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := &items[i]
		if item.Kind != vendorstore.KindUserMessage || item.Metadata.Origin != vendorstore.TurnOriginFugue {
			continue
		}
		_, content, readErr := f.hp.vendorStore.ReadItem(ctx, sessionID, item.ID)
		if readErr != nil || !strings.Contains(string(content), mailboxBatchMarker) {
			continue
		}
		var msg transcriptUserMessageText
		if json.Unmarshal(content, &msg) == nil && msg.Text != "" {
			return msg.Text, true
		}
	}
	return "", false
}

// mailboxBatchMarker identifies a delivered batch turn in the transcript.
const mailboxBatchMarker = "[fugue-batch "

// transcriptUserMessageText is the transcript content of a Fugue user message
// (transcript.UserMessageContent).
type transcriptUserMessageText struct {
	Text string `json:"Text"`
}
