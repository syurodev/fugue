package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	pdWorkspaceID  = "ws-pd"
	pdEffortLow    = "low"
	pdResumeHandle = "223e4567-e89b-12d3-a456-426614174000"
	pdPaceLeadTask = "pt-lead"
	pdTaskLead     = "pd-t-lead"
	pdTaskManager  = "pd-t-mgr"
	pdTaskAsking   = "pd-t-asking"
	pdTaskHelper   = "pd-t-helper"
	pdSessDetach   = "m-detach"
	pdSessAsking   = "m-asking"
)

type pdSendRecord struct {
	sessionID string
	text      string
	origin    vendor.InputOrigin
	sentAt    time.Time
	resumeID  string
}

type pdSendLog struct {
	mu      sync.Mutex
	records []pdSendRecord
}

func (l *pdSendLog) append(r *pdSendRecord) {
	l.mu.Lock()
	l.records = append(l.records, *r)
	l.mu.Unlock()
}

func (l *pdSendLog) snapshot() []pdSendRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]pdSendRecord, len(l.records))
	copy(out, l.records)
	return out
}

type pdSession struct {
	id           string
	clk          clock.Clock
	resumeID     string
	observations chan vendor.Observation
	closedCh     chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
	turnSeq      *atomic.Int64
	sendErr      error
	sent         []pdSendRecord
	closeCalls   int
	allSends     *pdSendLog
}

func newPDSession(id string, clk clock.Clock, resumeID string, log *pdSendLog, turnSeq *atomic.Int64) *pdSession {
	if turnSeq == nil {
		turnSeq = &atomic.Int64{}
	}
	return &pdSession{
		id:           id,
		clk:          clk,
		resumeID:     resumeID,
		observations: make(chan vendor.Observation, 128),
		closedCh:     make(chan struct{}),
		turnSeq:      turnSeq,
		allSends:     log,
	}
}

func (s *pdSession) Observations() <-chan vendor.Observation { return s.observations }
func (s *pdSession) Identity() process.Identity {
	return process.Identity{PID: 5150, StartToken: "pd-start"}
}
func (s *pdSession) PID() int { return 5150 }

func (s *pdSession) Send(_ context.Context, input vendor.Input) (vendor.TurnRef, error) {
	s.mu.Lock()
	if s.sendErr != nil {
		err := s.sendErr
		s.mu.Unlock()
		return vendor.TurnRef{}, err
	}
	seq := s.turnSeq.Add(1)
	turnID := fmt.Sprintf("pd-turn-%s-%d", s.id, seq)
	rec := pdSendRecord{sessionID: s.id, text: input.Text, origin: input.Origin, sentAt: s.clk.Now(), resumeID: s.resumeID}
	s.sent = append(s.sent, rec)
	s.mu.Unlock()
	if s.allSends != nil {
		s.allSends.append(&rec)
	}
	return vendor.TurnRef{ID: turnID}, nil
}

func (s *pdSession) Interrupt(context.Context, vendor.TurnRef) error           { return nil }
func (s *pdSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error { return nil }
func (s *pdSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return vendor.ErrUnsupported
}

func (s *pdSession) Close(context.Context, vendor.CloseReason) (process.StopProof, error) {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closedCh) })
	return process.StopProof{Status: process.StopProofProved}, nil
}

type pdAdapter struct {
	clk      clock.Clock
	cap      vendor.Capability
	log      *pdSendLog
	mu       sync.Mutex
	sessions map[string]*pdSession
	turnSeqs map[string]*atomic.Int64
	starts   []*vendor.StartSpec
}

func newPDAdapter(clk clock.Clock, capView vendor.Capability, log *pdSendLog) *pdAdapter {
	return &pdAdapter{
		clk:      clk,
		cap:      capView,
		log:      log,
		sessions: make(map[string]*pdSession),
		turnSeqs: make(map[string]*atomic.Int64),
	}
}

func (a *pdAdapter) Capability() vendor.Capability { return a.cap }
func (a *pdAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{Capability: a.cap}, nil
}

func (a *pdAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	a.mu.Lock()
	cp := *spec
	a.starts = append(a.starts, &cp)
	seq := a.turnSeqs[spec.SessionID]
	if seq == nil {
		seq = &atomic.Int64{}
		a.turnSeqs[spec.SessionID] = seq
	}
	s := newPDSession(spec.SessionID, a.clk, spec.ResumeSessionID, a.log, seq)
	a.sessions[spec.SessionID] = s
	a.mu.Unlock()
	return s, nil
}

func (a *pdAdapter) session(id string) *pdSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

func (a *pdAdapter) startSpecs() []*vendor.StartSpec {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*vendor.StartSpec, len(a.starts))
	copy(out, a.starts)
	return out
}

type pdRootResolver struct{ dir string }

func (r pdRootResolver) SelectSessionCwd(context.Context, string, string) (string, error) {
	return r.dir, nil
}

func completePDTurn(s *pdSession, turnID string) {
	s.observations <- vendor.Observation{TurnEnd: &vendor.TurnEnd{TurnID: turnID, Outcome: string(vendor.OutcomeCompleted)}}
}

func startPDSession(t *testing.T, m *VendorSessions, id, vendorName string, role vendorstore.TeamRole, teamID, taskID, resumeHandle string) {
	t.Helper()
	sess := &vendorstore.Session{
		ID: id, Vendor: vendorName, AccountID: vendorDefaultAccount,
		Role: string(role), WorkspaceID: pdWorkspaceID, WorkspaceRoot: teamTestRootName,
		TeamID: teamID, TaskID: taskID,
	}
	spec := &vendor.StartSpec{SessionID: id, ResumeSessionID: resumeHandle}
	if err := m.Start(t.Context(), sess, spec); err != nil {
		t.Fatalf("Start(%s): %v", id, err)
	}
}

func pdMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

func awaitPDReady(ctx context.Context, t *testing.T, store *vendorstore.Store, sessionID string, minSeq int64) {
	t.Helper()
	testutil.Spin(t, sessionID+" ready", func() bool {
		snap, err := store.Snapshot(ctx, sessionID)
		return err == nil && snap.State == vendorstore.StateReady && snap.LastSequence >= minSeq
	})
}

type pdPacingHarness struct {
	f        *transcriptFixture
	log      *pdSendLog
	claudeAd *pdAdapter
	codexAd  *pdAdapter
	m        *VendorSessions
	svc      *VendorService
	t0       time.Time
}

func newPDPacingHarness(t *testing.T) *pdPacingHarness {
	t.Helper()
	f := newEmptySessionsFixture(t)
	log := &pdSendLog{}
	claudeAd := newPDAdapter(f.clock, vendor.Capability{Resume: true}, log)
	codexAd := newPDAdapter(f.clock, vendor.Capability{Resume: true}, log)
	m := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service,
		Adapters: map[string]vendor.Adapter{VendorClaude: claudeAd, VendorCodex: codexAd},
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 10, PromptPacingMaxSeconds: 15}, nil
		},
		PromptPacingJitter: func(span time.Duration) time.Duration { return span / 2 },
		Vault:              f.vault, Clock: f.clock, IDs: ids.NewSequence("pace-"),
	})
	svc := &VendorService{
		sessions: m, store: f.store, vault: f.vault,
		ids: ids.NewSequence("svc-"), ops: newVendorOps(f.clock, m.spawn),
	}
	startPDSession(t, m, "c1", VendorClaude, vendorstore.RoleMember, "", "", "")
	startPDSession(t, m, "c2", VendorClaude, vendorstore.RoleMember, "", "", "")
	startPDSession(t, m, "x1", VendorCodex, vendorstore.RoleMember, "", "", "")
	return &pdPacingHarness{f: f, log: log, claudeAd: claudeAd, codexAd: codexAd, m: m, svc: svc, t0: f.clock.Now()}
}

func TestFuguePromptsArePacedPerVendorWhileUserPromptsBypass(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newPDPacingHarness(t)
	verifyPerVendorAndUserBypass(ctx, t, h)
	verifyTranscriptOrigins(ctx, t, h.f.store, "c1")
	verifyWakeAsyncAndClaimHandling(ctx, t, h)
	verifyPacerQueueBoundsAndDrain(t)
	mustCloseActorSessions(t, h.m)
}

func mustSendAsyncOK(t *testing.T, m *VendorSessions, sessionID, text string) <-chan error {
	t.Helper()
	ch := make(chan error, 1)
	if err := m.SendAsync(sessionID, vendor.Input{Text: text, Origin: vendor.OriginFugue}, func(err error) { ch <- err }); err != nil {
		t.Fatalf("SendAsync(%s, %s): %v", sessionID, text, err)
	}
	return ch
}

func verifyPerVendorAndUserBypass(ctx context.Context, t *testing.T, h *pdPacingHarness) {
	t.Helper()
	if err := <-mustSendAsyncOK(t, h.m, "c1", "f1"); err != nil {
		t.Fatalf("f1 err: %v", err)
	}
	completePDTurn(h.claudeAd.session("c1"), "pd-turn-c1-1")

	f2Done := mustSendAsyncOK(t, h.m, "c2", "f2")
	f3Done := mustSendAsyncOK(t, h.m, "c1", "f3")
	f4Done := mustSendAsyncOK(t, h.m, "c1", "f4")
	if !h.m.hasPendingPacedSend(VendorClaude, "c1") {
		t.Fatal("expected pending paced send on c1")
	}
	if err := <-mustSendAsyncOK(t, h.m, "x1", "xf1"); err != nil {
		t.Fatalf("xf1 err: %v", err)
	}
	awaitPDReady(ctx, t, h.f.store, "c1", 0)
	if err := h.f.clock.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait f2 timer: %v", err)
	}
	if err := h.svc.Send(ctx, testAppPrincipal, VendorInput{CommandID: "user-cmd-1", SessionID: "c1", Text: "user hi"}); err != nil {
		t.Fatalf("VendorService.Send: %v", err)
	}
	completePDTurn(h.claudeAd.session("c1"), "pd-turn-c1-2")
	awaitPDReady(ctx, t, h.f.store, "c1", 0)
	advanceAndVerifyFugueOrder(ctx, t, h, f2Done, f3Done, f4Done)
}

func advanceAndVerifyFugueOrder(ctx context.Context, t *testing.T, h *pdPacingHarness, f2Done, f3Done, f4Done <-chan error) {
	t.Helper()
	h.f.clock.Advance(12 * time.Second)
	if recs := h.log.snapshot(); len(recs) != 3 {
		t.Fatalf("records at +12s = %+v, want 3", recs)
	}
	h.f.clock.Advance(500 * time.Millisecond)
	if err := <-f2Done; err != nil {
		t.Fatalf("f2 err: %v", err)
	}
	if err := h.f.clock.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait f3 timer: %v", err)
	}
	h.f.clock.Advance(12500 * time.Millisecond)
	if err := <-f3Done; err != nil {
		t.Fatalf("f3 err: %v", err)
	}
	completePDTurn(h.claudeAd.session("c1"), "pd-turn-c1-3")
	awaitPDReady(ctx, t, h.f.store, "c1", 0)
	if err := h.f.clock.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait f4 timer: %v", err)
	}
	h.f.clock.Advance(12500 * time.Millisecond)
	if err := <-f4Done; err != nil {
		t.Fatalf("f4 err: %v", err)
	}
	assertPacedSendLog(t, h.log.snapshot(), h.t0)
}

func assertPacedSendLog(t *testing.T, recs []pdSendRecord, t0 time.Time) {
	t.Helper()
	wantOrder := []string{"f1", "xf1", "user hi", "f2", "f3", "f4"}
	if len(recs) != len(wantOrder) {
		t.Fatalf("send count = %d, want %d (%+v)", len(recs), len(wantOrder), recs)
	}
	for i, w := range wantOrder {
		if recs[i].text != w {
			t.Fatalf("send[%d] = %q, want %q", i, recs[i].text, w)
		}
	}
	if recs[2].origin != vendor.OriginUser || !recs[2].sentAt.Equal(t0) {
		t.Fatalf("user prompt rec = %+v, want origin=user at t0", recs[2])
	}
	for _, idx := range []int{0, 1, 3, 4, 5} {
		if recs[idx].origin != vendor.OriginFugue {
			t.Fatalf("fugue rec[%d] origin = %q, want fugue", idx, recs[idx].origin)
		}
	}
}

func verifyTranscriptOrigins(ctx context.Context, t *testing.T, store *vendorstore.Store, sessionID string) {
	t.Helper()
	items, err := store.Transcript(ctx, sessionID, 0, 50)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	var seenUser, seenFugue bool
	for i := range items {
		if items[i].Kind != vendorstore.KindUserMessage {
			continue
		}
		if items[i].Metadata.Origin == string(vendor.OriginUser) {
			seenUser = true
		} else if items[i].Metadata.Origin == string(vendor.OriginFugue) {
			seenFugue = true
		}
	}
	if !seenUser || !seenFugue {
		t.Fatalf("transcript origins seenUser=%v seenFugue=%v, want both", seenUser, seenFugue)
	}
	evs, err := store.EventsSince(ctx, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for i := range evs {
		if evs[i].Type == vendorstore.VendorTurnStarted && evs[i].Metadata.TurnOrigin != vendorstore.TurnOriginFugue {
			t.Fatalf("VendorTurnStarted.TurnOrigin = %q, want %q", evs[i].Metadata.TurnOrigin, vendorstore.TurnOriginFugue)
		}
	}
}

func verifyWakeAsyncAndClaimHandling(ctx context.Context, t *testing.T, h *pdPacingHarness) {
	t.Helper()
	spec := pdMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Check pacing"})
	if _, _, err := h.f.store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: "pace-team", WorkspaceID: pdWorkspaceID, SessionID: "c2",
		ProposedEventID: "pt-prop", ProposedPayload: spec,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: pdPaceLeadTask, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "pt-c1", ProtectedSpec: spec},
			{TaskID: "pt-m1", ParentTaskID: pdPaceLeadTask, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "pt-c2", ProtectedSpec: spec},
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := h.f.store.AppendTeamEvent(ctx, "pace-team", 3, &vendorstore.AppendTeamEventInput{
		EventID: "pt-ended", TaskID: "pt-m1", EventType: vendorstore.EventTaskEnded,
		Outcome: vendorstore.TaskOutcomeFailed, FailureCode: "err",
		Mailbox: []vendorstore.MailboxEnqueueInput{{RecipientTaskID: pdPaceLeadTask, Kind: vendorstore.MailboxKindAnswer, Payload: digestItemPayload(t, "pt-m1")}},
	}); err != nil {
		t.Fatalf("AppendTeamEvent: %v", err)
	}
	completePDTurn(h.claudeAd.session("c2"), "pd-turn-c2-1")
	awaitPDReady(ctx, t, h.f.store, "c2", 0)

	wake := newTeamWake(&teamWakeDeps{
		Store: h.f.store, Sessions: vendorTaskSessions{svc: h.svc}, Roots: pdRootResolver{dir: t.TempDir()},
		Clock: h.f.clock, IDs: ids.NewSequence("pw-"), Vault: h.f.vault, DataDir: t.TempDir(),
	})
	if err := wake.Pump(ctx); err != nil {
		t.Fatalf("wake.Pump: %v", err)
	}
	if !wake.mu.TryLock() {
		t.Fatal("teamWake.mu was held while paced send was queued")
	}
	wake.mu.Unlock()
	if !h.m.hasPendingPacedSend(VendorClaude, "c2") {
		t.Fatal("expected wake send queued in pacer")
	}
	assertWakeClaimHandling(ctx, t, h.f.store, wake)
}

func assertWakeClaimHandling(ctx context.Context, t *testing.T, store *vendorstore.Store, wake *teamWake) {
	t.Helper()
	claims, err := store.DeliveringMailboxBatches(ctx)
	if err != nil || len(claims) != 1 {
		t.Fatalf("DeliveringMailboxBatches = %+v (err=%v), want 1", claims, err)
	}
	batchID := claims[0].BatchID
	if outcomeErr := wake.handleBatchSendOutcome(ctx, "pace-team", batchID, errInjectedAppendFailure); outcomeErr != nil {
		t.Fatalf("unknown send err: %v", outcomeErr)
	}
	if still, delErr := store.DeliveringMailboxBatches(ctx); delErr != nil || len(still) != 1 {
		t.Fatalf("unknown outcome delivering = %+v (err=%v), want 1", still, delErr)
	}
	if outcomeErr := wake.handleBatchSendOutcome(ctx, "pace-team", batchID, vendor.ErrSessionClosed); outcomeErr != nil {
		t.Fatalf("refused send err: %v", outcomeErr)
	}
	if still, delErr := store.DeliveringMailboxBatches(ctx); delErr != nil || len(still) != 0 {
		t.Fatalf("refused outcome delivering = %+v (err=%v), want 0", still, delErr)
	}
	if pend, pendErr := store.PendingMailbox(ctx); pendErr != nil || len(pend) != 1 {
		t.Fatalf("refused outcome pending = %+v (err=%v), want 1", pend, pendErr)
	}
}

func verifyPacerQueueBoundsAndDrain(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	f2 := newEmptySessionsFixture(t)
	ad2 := newPDAdapter(f2.clock, vendor.Capability{Resume: true}, &pdSendLog{})
	m2 := mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f2.store, Transcripts: f2.service, Adapter: ad2,
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 10, PromptPacingMaxSeconds: 10}, nil
		},
		PromptPacingJitter: func(time.Duration) time.Duration { return 0 },
		Vault:              f2.vault, Clock: f2.clock, IDs: ids.NewSequence("bnd-"),
	})
	startPDSession(t, m2, "s-close", VendorClaude, vendorstore.RoleMember, "", "", "")
	startPDSession(t, m2, "s-drain", VendorClaude, vendorstore.RoleMember, "", "", "")
	if err := <-mustSendAsyncOK(t, m2, "s-drain", "first"); err != nil {
		t.Fatalf("first send err: %v", err)
	}
	completePDTurn(ad2.session("s-drain"), "pd-turn-s-drain-1")

	closeCancelled := make(chan error, 1)
	if err := m2.SendAsync("s-close", vendor.Input{Text: "close-me", Origin: vendor.OriginFugue}, func(err error) { closeCancelled <- err }); err != nil {
		t.Fatalf("SendAsync s-close: %v", err)
	}
	if err := f2.clock.BlockUntilPending(ctx, 1); err != nil {
		t.Fatalf("wait close-me timer: %v", err)
	}
	drainCancelled := make(chan error, maxPacedQueuePerVendor)
	for i := range maxPacedQueuePerVendor {
		if err := m2.SendAsync("s-drain", vendor.Input{Text: fmt.Sprintf("q-%d", i), Origin: vendor.OriginFugue}, func(err error) { drainCancelled <- err }); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if err := m2.SendAsync("s-drain", vendor.Input{Text: "overflow", Origin: vendor.OriginFugue}, nil); !errors.Is(err, ErrPacingQueueFull) {
		t.Fatalf("overflow err = %v, want ErrPacingQueueFull", err)
	}
	if err := m2.Close(ctx, "s-close", vendor.CloseUserRequest); err != nil {
		t.Fatalf("Close s-close: %v", err)
	}
	mustCloseActorSessions(t, m2)
	if err := <-closeCancelled; !errors.Is(err, ErrHostStopping) && !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("closed session queued item err = %v", err)
	}
	for range maxPacedQueuePerVendor {
		if err := <-drainCancelled; !errors.Is(err, ErrHostStopping) {
			t.Fatalf("drain item err = %v, want ErrHostStopping", err)
		}
	}
}

type pdDetachHarness struct {
	f               *transcriptFixture
	idGen           ids.Generator
	log             *pdSendLog
	ad              *pdAdapter
	agyAd           *pdAdapter
	pendingApproval atomic.Bool
	m               *VendorSessions
	teamID          string
}

func newPDDetachHarness(t *testing.T) *pdDetachHarness {
	t.Helper()
	ctx := t.Context()
	f := newEmptySessionsFixture(t)
	idGen := ids.NewUUIDv7(f.clock, ids.NewDeterministic("detach-test"))
	log := &pdSendLog{}
	ad := newPDAdapter(f.clock, vendor.Capability{Resume: true}, log)
	agyAd := newPDAdapter(f.clock, vendor.Capability{Resume: true}, log)
	h := &pdDetachHarness{f: f, idGen: idGen, log: log, ad: ad, agyAd: agyAd, teamID: "detach-team"}
	h.pendingApproval.Store(true)
	h.m = mustNewVendorSessions(t, ctx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service,
		Adapters: map[string]vendor.Adapter{VendorClaude: ad, VendorAGY: agyAd},
		HostConfig: func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{PromptPacingMinSeconds: 10, PromptPacingMaxSeconds: 10}, nil
		},
		TeamsConfig: func(context.Context, string) (workspace.TeamsConfig, error) {
			return workspace.TeamsConfig{IdleDetachSeconds: 300}, nil
		},
		HasPendingApproval: func(sessionID string) bool {
			return sessionID == "m-approval" && h.pendingApproval.Load()
		},
		PromptPacingJitter: func(time.Duration) time.Duration { return 0 },
		HostResources:      fakeHostResources{ramBytes: 16 << 30, cores: 8},
		Vault:              f.vault, Clock: f.clock, IDs: idGen,
	})
	return h
}

func TestIdleTaskSessionDetachesAndResumesWithItsContextOnWake(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	h := newPDDetachHarness(t)
	seedDetachTeamAndTasks(ctx, t, h.f.store, h.teamID)
	startDetachSessions(t, h)
	verifyIdleResetAndDetachThreshold(ctx, t, h)
	verifyNonDetachableSessionsKept(ctx, t, h)
	verifyWakeResumeAndDelivery(ctx, t, h)
	mustCloseActorSessions(t, h.m)
}

func seedDetachTeamAndTasks(ctx context.Context, t *testing.T, store *vendorstore.Store, teamID string) {
	t.Helper()
	specBytes := pdMarshalJSON(t, teams.ProtectedTaskContent{Goal: "Idle detach resume"})
	createEV := pdMarshalJSON(t, teamDecisionPayload{
		Roles: []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Model: "sonnet", Effort: pdEffortLow, Permission: string(teams.PermissionRead)}},
	})
	if _, _, err := store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: pdWorkspaceID, SessionID: "lead-1",
		ProposedEventID: "dt-prop", ProposedPayload: specBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: pdTaskLead, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "dt-c0", ProtectedSpec: specBytes},
			{TaskID: pdTaskManager, ParentTaskID: pdTaskLead, HierarchyRole: vendorstore.RoleTeamManager, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "dt-c1", ProtectedSpec: specBytes},
			{TaskID: pdTaskAsking, ParentTaskID: pdTaskLead, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "dt-c2", ProtectedSpec: specBytes},
			{TaskID: pdTaskHelper, ParentTaskID: pdTaskManager, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "dt-c3", ProtectedSpec: specBytes},
		},
	}); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	events := []vendorstore.AppendTeamEventInput{
		{EventID: "dt-decided", EventType: vendorstore.EventTeamStarted, ProtectedPayload: createEV},
		{EventID: "dt-assn-m", TaskID: pdTaskManager, EventType: vendorstore.EventTaskAssigned, SessionID: pdSessDetach},
		{EventID: "dt-start-m", TaskID: pdTaskManager, EventType: vendorstore.EventTaskStarted, SessionID: pdSessDetach},
		{EventID: "dt-assn-a", TaskID: pdTaskAsking, EventType: vendorstore.EventTaskAssigned, SessionID: pdSessAsking},
		{EventID: "dt-start-a", TaskID: pdTaskAsking, EventType: vendorstore.EventTaskStarted, SessionID: pdSessAsking},
		{EventID: "dt-ask-a", TaskID: pdTaskAsking, EventType: vendorstore.EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"q","to":"parent"}`)},
	}
	for i := range events {
		if _, err := store.AppendTeamEvent(ctx, teamID, int64(5+i), &events[i]); err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", events[i].EventID, err)
		}
	}
}

func startDetachSessions(t *testing.T, h *pdDetachHarness) {
	t.Helper()
	ctx := t.Context()
	startPDSession(t, h.m, pdSessDetach, VendorClaude, vendorstore.RoleTeamManager, h.teamID, pdTaskManager, "")
	h.ad.session(pdSessDetach).observations <- vendor.Observation{Init: &vendor.Init{SessionID: pdResumeHandle}}
	testutil.Spin(t, "m-detach learned resume handle", func() bool {
		snap, err := h.f.store.Snapshot(ctx, pdSessDetach)
		return err == nil && snap.ResumeHandle == pdResumeHandle
	})
	startPDSession(t, h.m, "lead-1", VendorClaude, vendorstore.RoleLead, h.teamID, pdTaskLead, pdResumeHandle)
	startPDSession(t, h.m, "solo-1", VendorClaude, vendorstore.RoleSolo, "", "", pdResumeHandle)
	startPDSession(t, h.m, "agy-1", VendorAGY, vendorstore.RoleMember, h.teamID, "", pdResumeHandle)
	startPDSession(t, h.m, "m-approval", VendorClaude, vendorstore.RoleMember, h.teamID, "", pdResumeHandle)
	startPDSession(t, h.m, pdSessAsking, VendorClaude, vendorstore.RoleMember, h.teamID, pdTaskAsking, pdResumeHandle)
	startPDSession(t, h.m, "m-busy", VendorClaude, vendorstore.RoleMember, h.teamID, "", pdResumeHandle)
	if err := h.m.Send(ctx, "m-busy", vendor.Input{Text: "stay busy", Origin: vendor.OriginUser}); err != nil {
		t.Fatalf("m-busy Send: %v", err)
	}
	totalBefore, _, err := h.m.slotUsage(ctx)
	if err != nil || totalBefore != 7 {
		t.Fatalf("slotUsage before detach = %d (err=%v), want 7", totalBefore, err)
	}
}

func verifyIdleResetAndDetachThreshold(ctx context.Context, t *testing.T, h *pdDetachHarness) {
	t.Helper()
	if err := h.f.clock.BlockUntilPending(ctx, 4); err != nil {
		t.Fatalf("wait initial idle timers: %v", err)
	}
	h.f.clock.Advance(200 * time.Second)
	h.m.TouchSession(pdSessDetach)
	if sendErr := h.m.Send(ctx, pdSessDetach, vendor.Input{Text: "keep alive turn", Origin: vendor.OriginUser}); sendErr != nil {
		t.Fatalf("m-detach keep alive Send: %v", sendErr)
	}
	completePDTurn(h.ad.session(pdSessDetach), "pd-turn-m-detach-1")
	awaitPDReady(ctx, t, h.f.store, pdSessDetach, 5)
	if blockErr := h.f.clock.BlockUntilPending(ctx, 4); blockErr != nil {
		t.Fatalf("wait re-armed idle timers: %v", blockErr)
	}
	h.f.clock.Advance(299 * time.Second)
	if snap, snapErr := h.f.store.Snapshot(ctx, pdSessDetach); snapErr != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("m-detach at +299s = %v, err=%v, want ready", snap.State, snapErr)
	}
	h.f.clock.Advance(1 * time.Second)
	testutil.Spin(t, "m-detach auto-detached", func() bool {
		snap, snapErr := h.f.store.Snapshot(ctx, pdSessDetach)
		return snapErr == nil && snap.State == vendorstore.StateDetached && snap.ResumeHandle == pdResumeHandle
	})
	testutil.Spin(t, "agy-1 auto-detached", func() bool {
		snap, snapErr := h.f.store.Snapshot(ctx, "agy-1")
		return snapErr == nil && snap.State == vendorstore.StateDetached
	})
	assertDetachExitedAndSlotFreed(ctx, t, h.f.store, h.m)
}

func assertDetachExitedAndSlotFreed(ctx context.Context, t *testing.T, store *vendorstore.Store, m *VendorSessions) {
	t.Helper()
	evs, err := store.EventsSince(ctx, pdSessDetach, 0, 50)
	if err != nil {
		t.Fatalf("EventsSince m-detach: %v", err)
	}
	var sawExited bool
	for i := range evs {
		if evs[i].Type == vendorstore.VendorProcessExited && evs[i].Metadata.StopProof == string(process.StopProofProved) {
			sawExited = true
		}
	}
	if !sawExited {
		t.Fatal("missing VendorProcessExited with StopProofProved on detach")
	}
	totalAfter, _, err := m.slotUsage(ctx)
	if err != nil || totalAfter != 5 {
		t.Fatalf("slotUsage after detach = %d (err=%v), want 5", totalAfter, err)
	}
}

func verifyNonDetachableSessionsKept(ctx context.Context, t *testing.T, h *pdDetachHarness) {
	t.Helper()
	h.f.clock.Advance(400 * time.Second)
	for _, kept := range []struct{ id, want string }{
		{"lead-1", vendorstore.StateReady},
		{"solo-1", vendorstore.StateReady},
		{"agy-1", vendorstore.StateDetached},
		{"m-approval", vendorstore.StateReady},
		{pdSessAsking, vendorstore.StateReady},
		{"m-busy", vendorstore.StateRunning},
	} {
		snap, err := h.f.store.Snapshot(ctx, kept.id)
		if err != nil || snap.State != kept.want {
			t.Fatalf("%s state = %q (err=%v), want %q", kept.id, snap.State, err, kept.want)
		}
	}
}

func verifyWakeResumeAndDelivery(ctx context.Context, t *testing.T, h *pdDetachHarness) {
	t.Helper()
	if _, err := h.f.store.AppendTeamEvent(ctx, h.teamID, 11, &vendorstore.AppendTeamEventInput{
		EventID: "dt-wake-mail", TaskID: pdTaskHelper, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateCancelled),
		Mailbox: []vendorstore.MailboxEnqueueInput{{RecipientTaskID: pdTaskManager, Kind: vendorstore.MailboxKindAnswer, Payload: digestItemPayload(t, pdTaskHelper)}},
	}); err != nil {
		t.Fatalf("enqueue child answer: %v", err)
	}
	svc := &VendorService{
		sessions: h.m, store: h.f.store, adapters: map[string]vendor.Adapter{VendorClaude: h.ad, VendorAGY: h.agyAd},
		vault: h.f.vault, ids: h.idGen, ops: newVendorOps(h.f.clock, h.m.spawn), tokens: newSessionTokens(ids.CryptoRandom{}),
	}
	wake := newTeamWake(&teamWakeDeps{
		Store: h.f.store, Sessions: vendorTaskSessions{svc: svc}, Roots: pdRootResolver{dir: filepath.Clean(t.TempDir())},
		Clock: h.f.clock, IDs: h.idGen, Vault: h.f.vault, DataDir: t.TempDir(),
	})
	if err := wake.Pump(ctx); err != nil {
		t.Fatalf("wake.Pump #1: %v", err)
	}
	awaitPDReady(ctx, t, h.f.store, pdSessDetach, 0)
	starts := h.ad.startSpecs()
	resumedSpec := starts[len(starts)-1]
	if resumedSpec.SessionID != pdSessDetach || resumedSpec.ResumeSessionID != pdResumeHandle || resumedSpec.Model != "sonnet" || resumedSpec.Effort != pdEffortLow {
		t.Fatalf("resumed StartSpec = %+v, want resume handle + decided profile", resumedSpec)
	}
	if err := wake.Pump(ctx); err != nil {
		t.Fatalf("wake.Pump #2: %v", err)
	}
	h.f.clock.Advance(11 * time.Second)
	awaitResumedWakeDelivery(ctx, t, h)
}

func awaitResumedWakeDelivery(ctx context.Context, t *testing.T, h *pdDetachHarness) {
	t.Helper()
	testutil.Spin(t, "resumed m-detach received wake prompt", func() bool {
		for _, rec := range h.log.snapshot() {
			if rec.sessionID == pdSessDetach && rec.resumeID == pdResumeHandle && rec.origin == vendor.OriginFugue {
				return true
			}
		}
		return false
	})
	testutil.Spin(t, "mailbox batch marked delivered", func() bool {
		claims, err := h.f.store.DeliveringMailboxBatches(ctx)
		return err == nil && len(claims) == 0
	})
}
