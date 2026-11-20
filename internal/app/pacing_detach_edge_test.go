package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

type edgeSession struct {
	id           string
	observations chan vendor.Observation
	holdSendCh   <-chan struct{}
	sendStarted  chan struct{}
	closeProof   process.StopProof
	closeErr     error
	mu           sync.Mutex
	sentTexts    []string
	turnSeq      atomic.Int64
	closedCh     chan struct{}
	closeOnce    sync.Once
}

func newEdgeSession(id string, proof process.StopProof, closeErr error, holdSend <-chan struct{}) *edgeSession {
	return &edgeSession{
		id:           id,
		observations: make(chan vendor.Observation, 32),
		holdSendCh:   holdSend,
		sendStarted:  make(chan struct{}, 1),
		closeProof:   proof,
		closeErr:     closeErr,
		closedCh:     make(chan struct{}),
	}
}

func (s *edgeSession) Observations() <-chan vendor.Observation { return s.observations }
func (s *edgeSession) Identity() process.Identity {
	return process.Identity{PID: 6100, StartToken: "edge-start"}
}
func (s *edgeSession) PID() int { return 6100 }

func (s *edgeSession) Send(_ context.Context, input vendor.Input) (vendor.TurnRef, error) {
	if s.holdSendCh != nil {
		select {
		case s.sendStarted <- struct{}{}:
		default:
		}
		select {
		case <-s.holdSendCh:
		case <-s.closedCh:
			return vendor.TurnRef{}, vendor.ErrSessionClosed
		}
	}
	s.mu.Lock()
	s.sentTexts = append(s.sentTexts, input.Text)
	s.mu.Unlock()
	return vendor.TurnRef{ID: fmt.Sprintf("edge-turn-%s-%d", s.id, s.turnSeq.Add(1))}, nil
}

func (s *edgeSession) Interrupt(context.Context, vendor.TurnRef) error           { return nil }
func (s *edgeSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error { return nil }
func (s *edgeSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return vendor.ErrUnsupported
}

func (s *edgeSession) Close(context.Context, vendor.CloseReason) (process.StopProof, error) {
	s.closeOnce.Do(func() { close(s.closedCh) })
	return s.closeProof, s.closeErr
}

type edgeAdapter struct {
	mu       sync.Mutex
	sessions map[string]*edgeSession
}

func (a *edgeAdapter) Capability() vendor.Capability { return vendor.Capability{Resume: true} }
func (a *edgeAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{Capability: a.Capability()}, nil
}

func (a *edgeAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[spec.SessionID]; ok {
		return s, nil
	}
	s := newEdgeSession(spec.SessionID, process.StopProof{Status: process.StopProofProved}, nil, nil)
	a.sessions[spec.SessionID] = s
	return s, nil
}

func TestDetachFallsBackToClosedWithoutResumeWhenStopProofUnknownOrCloseErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		proof    process.StopProof
		closeErr error
	}{
		{name: "unknown_proof", proof: process.StopProof{Status: process.StopProofUnknown}},
		{name: "close_error", proof: process.StopProof{Status: process.StopProofProved}, closeErr: errInjectedAppendFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runDetachUnprovedCase(t, tc.name, tc.proof, tc.closeErr)
		})
	}
}

func runDetachUnprovedCase(t *testing.T, name string, proof process.StopProof, closeErr error) {
	t.Helper()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	sessID := "unproved-" + name
	ad := &edgeAdapter{sessions: map[string]*edgeSession{
		sessID: newEdgeSession(sessID, proof, closeErr, nil),
	}}
	m := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad,
		TeamsConfig: func(context.Context, string) (workspace.TeamsConfig, error) {
			return workspace.TeamsConfig{IdleDetachSeconds: 300}, nil
		},
		Vault: f.vault, Clock: f.clock, IDs: ids.NewSequence("edge-" + name + "-"),
	})
	startPDSession(t, m, sessID, VendorClaude, vendorstore.RoleMember, "edge-team-"+name, "edge-task-"+name, pdResumeHandle)
	if err := m.Detach(ctx, sessID); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	snap, err := f.store.Snapshot(ctx, sessID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.State != vendorstore.StateClosed || snap.StopProof != vendorstore.StopUnknown {
		t.Fatalf("session state/proof = (%s, %s), want (%s, %s)", snap.State, snap.StopProof, vendorstore.StateClosed, vendorstore.StopUnknown)
	}
	if resErr := m.Resume(ctx, sessID, &vendor.StartSpec{SessionID: sessID}); errcode.CodeOf(resErr) != errcode.FailedPrecondition {
		t.Fatalf("Resume error = %v, want failed_precondition", resErr)
	}
	verifyWakeSkipsClosedSession(ctx, t, f, name, sessID)
	mustCloseActorSessions(t, m)
}

func verifyWakeSkipsClosedSession(ctx context.Context, t *testing.T, f *transcriptFixture, name, sessID string) {
	t.Helper()
	spec := pdMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Edge wake check"})
	teamID := "edge-wake-" + name
	taskID := teamID + ".m1"
	leadID := teamID + ".lead"
	if _, _, err := f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: pdWorkspaceID, SessionID: sessID,
		ProposedEventID: teamID + "-prop", ProposedPayload: spec,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: leadID, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: teamID + "-c0", ProtectedSpec: spec},
			{TaskID: taskID, ParentTaskID: leadID, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: teamID + "-c1", ProtectedSpec: spec},
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := f.store.AppendTeamEvent(ctx, teamID, 3, &vendorstore.AppendTeamEventInput{
		EventID: teamID + "-mail", TaskID: taskID, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateCancelled),
		Mailbox: []vendorstore.MailboxEnqueueInput{{RecipientTaskID: leadID, Kind: vendorstore.MailboxKindAnswer, Payload: digestItemPayload(t, taskID)}},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}
	fake := &fakeTaskSessions{}
	wake := newTeamWake(&teamWakeDeps{
		Store: f.store, Sessions: fake, Roots: pdRootResolver{dir: t.TempDir()},
		Clock: f.clock, IDs: ids.NewSequence("ew-"), Vault: f.vault, DataDir: t.TempDir(),
	})
	if err := wake.Pump(ctx); err != nil {
		t.Fatalf("wake.Pump: %v", err)
	}
	if fake.resumeCount() != 0 {
		t.Fatalf("ResumeTaskSession calls = %d, want 0 for closed session", fake.resumeCount())
	}
}

func TestPacedSendTimeoutKeepsWakeDeliveringAndDoesNotStallVendor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	holdCh := make(chan struct{})
	defer close(holdCh)
	stuckSess := newEdgeSession("s-stuck", process.StopProof{Status: process.StopProofProved}, nil, holdCh)
	healthySess := newEdgeSession("s-healthy", process.StopProof{Status: process.StopProofProved}, nil, nil)
	ad := &edgeAdapter{sessions: map[string]*edgeSession{
		"s-stuck":   stuckSess,
		"s-healthy": healthySess,
	}}
	m := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad,
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 0, PromptPacingMaxSeconds: 0}, nil
		},
		Vault: f.vault, Clock: f.clock, IDs: ids.NewSequence("tmo-"),
	})
	startPDSession(t, m, "s-stuck", VendorClaude, vendorstore.RoleMember, "", "", "")
	startPDSession(t, m, "s-healthy", VendorClaude, vendorstore.RoleMember, "", "", "")

	// Wedge s-stuck's actor inside session.Send so subsequent actor commands cannot be serviced.
	wedgedErr := make(chan error, 1)
	if sendErr := m.SendAsync("s-stuck", vendor.Input{Text: "block-actor", Origin: vendor.OriginUser}, func(err error) { wedgedErr <- err }); sendErr != nil {
		t.Fatalf("SendAsync block: %v", sendErr)
	}
	<-stuckSess.sendStarted

	wake := setupStuckWakeDeliveringBatch(ctx, t, f, m)
	if err := wake.Pump(ctx); err != nil {
		t.Fatalf("wake.Pump: %v", err)
	}
	healthyDone := mustSendAsyncOK(t, m, "s-healthy", "healthy-after-timeout")

	// Wait for the 30s paced send timeout timer on s-stuck, then advance fake clock 30s.
	if err := f.clock.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait pacer timeout timer: %v", err)
	}
	f.clock.Advance(pacedSendTimeout)

	if err := <-healthyDone; err != nil {
		t.Fatalf("healthy session send failed after timeout: %v", err)
	}
	assertWakeBatchStillDelivering(ctx, t, f.store, healthySess)
	mustCloseActorSessions(t, m)
	if wErr := <-wedgedErr; wErr != nil {
		t.Logf("wedged send exited: %v", wErr)
	}
}

const edgeTimeoutLead = "tmo.lead"

func setupStuckWakeDeliveringBatch(ctx context.Context, t *testing.T, f *transcriptFixture, m *VendorSessions) *teamWake {
	t.Helper()
	spec := pdMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Stuck timeout batch"})
	if _, _, err := f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "tmo-team", WorkspaceID: pdWorkspaceID, SessionID: "s-stuck",
		ProposedEventID: "tmo-prop", ProposedPayload: spec,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: edgeTimeoutLead, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "tmo-c0", ProtectedSpec: spec},
			{TaskID: "tmo.m1", ParentTaskID: edgeTimeoutLead, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "tmo-c1", ProtectedSpec: spec},
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := f.store.AppendTeamEvent(ctx, "tmo-team", 3, &vendorstore.AppendTeamEventInput{
		EventID: "tmo-mail", TaskID: "tmo.m1", EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateCancelled),
		Mailbox: []vendorstore.MailboxEnqueueInput{{RecipientTaskID: edgeTimeoutLead, Kind: vendorstore.MailboxKindAnswer, Payload: digestItemPayload(t, "tmo.m1")}},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}
	svc := &VendorService{sessions: m, store: f.store}
	return newTeamWake(&teamWakeDeps{
		Store: f.store, Sessions: vendorTaskSessions{svc: svc}, Roots: pdRootResolver{dir: t.TempDir()},
		Clock: f.clock, IDs: ids.NewSequence("tw-"), Vault: f.vault, DataDir: t.TempDir(),
	})
}

func assertWakeBatchStillDelivering(ctx context.Context, t *testing.T, store *vendorstore.Store, healthy *edgeSession) {
	t.Helper()
	testutil.Spin(t, "s-healthy sent text", func() bool {
		healthy.mu.Lock()
		defer healthy.mu.Unlock()
		return len(healthy.sentTexts) == 1 && healthy.sentTexts[0] == "healthy-after-timeout"
	})
	testutil.Spin(t, "stuck batch stays delivering", func() bool {
		claims, err := store.DeliveringMailboxBatches(ctx)
		if err != nil || len(claims) != 1 {
			return false
		}
		pend, pendErr := store.PendingMailbox(ctx)
		return pendErr == nil && len(pend) == 0
	})
}
