package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

var (
	errProcScanFailed      = errors.New("proc scan failed")
	errFakeKillFailed      = errors.New("kill failed")
	errFakeCloseFailed     = errors.New("close failed")
	errFakeHandshakeFailed = errors.New("handshake failed")
)

type verifiableRetainedSession struct {
	*actorTestSession
	mu           sync.Mutex
	ownedPIDs    []int
	ownsErr      error
	treeEmpty    bool
	treeEmptyErr error
	closeProof   process.StopProof
	closeErr     error
}

func (s *verifiableRetainedSession) OwnsPID(pid int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ownsErr != nil {
		return false, s.ownsErr
	}
	return slices.Contains(s.ownedPIDs, pid), nil
}

func (s *verifiableRetainedSession) TreeEmpty() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.treeEmptyErr != nil {
		return false, s.treeEmptyErr
	}
	return s.treeEmpty, nil
}

func (s *verifiableRetainedSession) Close(ctx context.Context, reason vendor.CloseReason) (process.StopProof, error) {
	if _, err := s.actorTestSession.Close(ctx, reason); err != nil {
		return s.closeProof, err
	}
	return s.closeProof, s.closeErr
}

type staticRetainedSession struct {
	*actorTestSession
	ownedPIDs  []int
	closeProof process.StopProof
	closeErr   error
}

func (s *staticRetainedSession) OwnsPID(pid int) (bool, error) {
	return slices.Contains(s.ownedPIDs, pid), nil
}

func (s *staticRetainedSession) Close(ctx context.Context, reason vendor.CloseReason) (process.StopProof, error) {
	if _, err := s.actorTestSession.Close(ctx, reason); err != nil {
		return s.closeProof, err
	}
	return s.closeProof, s.closeErr
}

type retainedTestAdapter struct {
	session vendor.Session
	owner   vendor.PeerOwner
	failErr error
}

func (a retainedTestAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{}, nil
}

func (a retainedTestAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	if spec.OnSpawned != nil && a.owner != nil {
		spec.OnSpawned(a.owner)
	}
	if a.failErr != nil {
		return nil, a.failErr
	}
	return a.session, nil
}

// bareStopSession implements vendor.Session and PeerOwner without
// Observations() or TreeEmpty() so Start/Resume hits the missing-observations
// closeAndSettlePending path.
type bareStopSession struct {
	ownedPIDs  []int
	closeProof process.StopProof
	closeErr   error
}

func (s *bareStopSession) Send(context.Context, vendor.Input) (vendor.TurnRef, error) {
	return vendor.TurnRef{}, vendor.ErrSessionClosed
}

func (s *bareStopSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error {
	return vendor.ErrSessionClosed
}

func (s *bareStopSession) Interrupt(context.Context, vendor.TurnRef) error {
	return vendor.ErrSessionClosed
}

func (s *bareStopSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return vendor.ErrSessionClosed
}

func (s *bareStopSession) Close(context.Context, vendor.CloseReason) (process.StopProof, error) {
	return s.closeProof, s.closeErr
}

func (s *bareStopSession) OwnsPID(pid int) (bool, error) {
	return slices.Contains(s.ownedPIDs, pid), nil
}

// TestEndedSessionSurvivingDescendantIsStillDenied verifies that an ended
// session keeps its PeerOwner in the retained set unless StopProof proved the
// tree empty, denies surviving descendants, fails closed on tree-read errors,
// and prunes verified-empty owners on the next check.
func TestEndedSessionSurvivingDescendantIsStillDenied(t *testing.T) {
	t.Parallel()
	assertRetainedVerifiableLifecycle(t)
	assertRetainedUnverifiableAndProvedEnded(t)
}

func assertRetainedVerifiableLifecycle(t *testing.T) {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	const (
		survivingPID = 7171
		unrelatedPID = 8888
		sessID       = "retained-verifiable"
	)
	sess := &verifiableRetainedSession{
		actorTestSession: newActorTestSession(),
		ownedPIDs:        []int{survivingPID},
		closeProof:       process.StopProof{Status: process.StopProofUnknown, Reason: process.ReasonGroupNotEmpty},
	}
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: retainedTestAdapter{session: sess}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("ret-v-"),
	})
	row := &vendorstore.Session{ID: sessID, Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}
	if err := manager.Start(t.Context(), row, &vendor.StartSpec{SessionID: sessID}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(t.Context(), sessID, vendor.CloseUserRequest); err != nil {
		t.Fatal(err)
	}
	if owned, err := manager.OwnsPeer(t.Context(), survivingPID); err != nil || !owned {
		t.Fatalf("OwnsPeer(surviving=%d) after ended session = %v, %v; want true, nil", survivingPID, owned, err)
	}
	if owned, err := manager.OwnsPeer(t.Context(), unrelatedPID); err != nil || owned {
		t.Fatalf("OwnsPeer(unrelated=%d) = %v, %v; want false, nil", unrelatedPID, owned, err)
	}
	if got := retainedOwnerCount(manager); got != 1 {
		t.Fatalf("retained owners while tree non-empty = %d, want 1", got)
	}

	assertRetainedReadErrorsFailClosed(t, manager, sess, unrelatedPID)

	sess.mu.Lock()
	sess.ownsErr = nil
	sess.ownedPIDs = nil
	sess.treeEmpty = true
	sess.mu.Unlock()
	if owned, err := manager.OwnsPeer(t.Context(), unrelatedPID); err != nil || owned {
		t.Fatalf("OwnsPeer after tree verified empty = %v, %v; want false, nil", owned, err)
	}
	if got := retainedOwnerCount(manager); got != 0 {
		t.Fatalf("retained owners after tree verified empty = %d, want 0", got)
	}
}

func assertRetainedReadErrorsFailClosed(t *testing.T, manager *VendorSessions, sess *verifiableRetainedSession, pid int) {
	t.Helper()
	sess.mu.Lock()
	sess.treeEmptyErr = errProcScanFailed
	sess.mu.Unlock()
	if owned, err := manager.OwnsPeer(t.Context(), pid); err == nil || owned {
		t.Fatalf("OwnsPeer with retained TreeEmpty error = %v, %v; want error", owned, err)
	}

	sess.mu.Lock()
	sess.treeEmptyErr = nil
	sess.ownsErr = errProcScanFailed
	sess.mu.Unlock()
	if owned, err := manager.OwnsPeer(t.Context(), pid); err == nil || owned {
		t.Fatalf("OwnsPeer with retained OwnsPID error = %v, %v; want error", owned, err)
	}
}

func assertRetainedUnverifiableAndProvedEnded(t *testing.T) {
	t.Helper()
	f := newTranscriptFixture(t, nil)
	const survivingPID = 7272
	unverifiable := &staticRetainedSession{
		actorTestSession: newActorTestSession(),
		ownedPIDs:        []int{survivingPID},
		closeProof:       process.StopProof{Status: process.StopProofUnknown, Reason: process.ReasonKillFailed},
		closeErr:         errFakeKillFailed,
	}
	proved := &staticRetainedSession{
		actorTestSession: newActorTestSession(),
		ownedPIDs:        []int{7373},
		closeProof:       process.StopProof{Status: process.StopProofProved, Reason: process.ReasonCgroupEmpty},
	}

	mUnverifiable := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: retainedTestAdapter{session: unverifiable}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("ret-u-"),
	})
	if err := mUnverifiable.Start(t.Context(), &vendorstore.Session{ID: "s-unv", Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}, &vendor.StartSpec{SessionID: "s-unv"}); err != nil {
		t.Fatal(err)
	}
	if err := mUnverifiable.Close(t.Context(), "s-unv", vendor.CloseUserRequest); err != nil {
		t.Fatal(err)
	}
	if owned, err := mUnverifiable.OwnsPeer(t.Context(), survivingPID); err != nil || !owned {
		t.Fatalf("OwnsPeer(surviving=%d) after failed close = %v, %v; want true, nil", survivingPID, owned, err)
	}
	if owned, err := mUnverifiable.OwnsPeer(t.Context(), 9999); err != nil || owned {
		t.Fatalf("OwnsPeer(9999) = %v, %v; want false, nil", owned, err)
	}
	if got := retainedOwnerCount(mUnverifiable); got != 1 {
		t.Fatalf("retained owners without TreeEmpty verifier = %d, want 1", got)
	}

	mProved := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: retainedTestAdapter{session: proved}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("ret-p-"),
	})
	if err := mProved.Start(t.Context(), &vendorstore.Session{ID: "s-prv", Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}, &vendor.StartSpec{SessionID: "s-prv"}); err != nil {
		t.Fatal(err)
	}
	if err := mProved.Close(t.Context(), "s-prv", vendor.CloseUserRequest); err != nil {
		t.Fatal(err)
	}
	if got := retainedOwnerCount(mProved); got != 0 {
		t.Fatalf("retained owners after StopProofProved = %d, want 0", got)
	}
}

type failedStopCase struct {
	proof      process.StopProof
	closeErr   error
	startErr   error
	wantRetain bool
}

// TestFailedStopKeepsPendingOwner verifies that pending owners are moved to the
// retained set when closing an untracked session returns ReasonGroupNotEmpty,
// ReasonKillFailed, a close error, or when Adapter.Start fails after OnSpawned,
// and are dropped only when StopProofProved succeeds with nil error.
func TestFailedStopKeepsPendingOwner(t *testing.T) {
	t.Parallel()
	cases := map[string]failedStopCase{
		"group not empty retains pending owner": {
			proof:      process.StopProof{Status: process.StopProofUnknown, Reason: process.ReasonGroupNotEmpty},
			wantRetain: true,
		},
		"kill failed with error retains pending owner": {
			proof:      process.StopProof{Status: process.StopProofUnknown, Reason: process.ReasonKillFailed},
			closeErr:   errFakeKillFailed,
			wantRetain: true,
		},
		"proved proof with error still retains pending owner": {
			proof:      process.StopProof{Status: process.StopProofProved, Reason: process.ReasonCgroupEmpty},
			closeErr:   errFakeCloseFailed,
			wantRetain: true,
		},
		"adapter start failure after OnSpawned retains pending owner": {
			startErr:   errFakeHandshakeFailed,
			wantRetain: true,
		},
		"proved empty stop drops pending owner": {
			proof:      process.StopProof{Status: process.StopProofProved, Reason: process.ReasonCgroupEmpty},
			wantRetain: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertFailedStopCase(t, tc)
		})
	}
}

func assertFailedStopCase(t *testing.T, tc failedStopCase) {
	t.Helper()
	const survivingPID = 8181
	f := newTranscriptFixture(t, nil)
	bare := &bareStopSession{
		ownedPIDs:  []int{survivingPID},
		closeProof: tc.proof,
		closeErr:   tc.closeErr,
	}
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service,
		Adapter: retainedTestAdapter{session: bare, owner: bare, failErr: tc.startErr},
		Vault:   f.vault, Clock: f.clock, IDs: ids.NewSequence("pend-"),
	})
	err := manager.Start(t.Context(), &vendorstore.Session{
		ID: "pend-s1", Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole,
	}, &vendor.StartSpec{SessionID: "pend-s1"})
	if err == nil {
		t.Fatal("Start succeeded unexpectedly")
	}
	if gotPending := pendingOwnerCount(manager); gotPending != 0 {
		t.Fatalf("pendingOwners after failed start = %d, want 0", gotPending)
	}
	wantCount := 0
	if tc.wantRetain {
		wantCount = 1
	}
	if gotRetained := retainedOwnerCount(manager); gotRetained != wantCount {
		t.Fatalf("retainedOwners = %d, want %d", gotRetained, wantCount)
	}
	owned, ownErr := manager.OwnsPeer(t.Context(), survivingPID)
	if ownErr != nil || owned != tc.wantRetain {
		t.Fatalf("OwnsPeer(%d) = %v, %v; want %v, nil", survivingPID, owned, ownErr, tc.wantRetain)
	}
}

func retainedOwnerCount(m *VendorSessions) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.retainedOwners)
}

func pendingOwnerCount(m *VendorSessions) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.pendingOwners)
}
