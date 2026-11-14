package app

// F9.9e evidence: host lifecycle integration for vendor sessions. The crash
// scenarios seed a data directory the way a killed host leaves it — a running
// session with an open turn and a recorded process identity — and prove what a
// fresh StartHost does with it: recover before any client is accepted, come
// back detached, turn open turns unknown, reap the leftover tree, and never
// respawn or replay. The fake vendor is the re-executed test binary
// (main_test.go), so resume and graceful stop drive the real adapter and
// supervisor.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// The seeded crash leftovers. PID 4242 belongs to no live test process, and
// Reap verifies the start token before it signals anything, so reaping these
// identities is refused and logged instead of killing a recycled PID.
const (
	seedVendorPID    = int64(4242)
	seedVendorToken  = "seed-start-token"
	seedResumeHandle = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	seedVendorTurnID = "vendor-9b2e4f10-5c1d-4e8a-9f3b-2d7c6a1e0b55"
)

// errUnclassifiedVendor stands for an error no typed code covers.
var errUnclassifiedVendor = errors.New("unclassified vendor failure")

// seedCrashedVendorSession writes a running vendor session with an open turn
// and a recorded process identity into a fresh data directory, the way a host
// kill leaves it, and closes the store so StartHost can take the lock.
func seedCrashedVendorSession(t *testing.T, dir string) string {
	t.Helper()
	clk := clock.Real{}
	store, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk,
		ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-seed")))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := vendorstore.New(t.Context(), store.DB(), nil, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := &vendorstore.Session{
		ID: "seed-session", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
		State: vendorstore.StateRunning, ProcessPID: seedVendorPID, ProcessStartToken: seedVendorToken,
		ResumeHandle: seedResumeHandle,
	}
	if err := vs.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := vs.Append(t.Context(), session.ID, 1, &vendorstore.Event{
		Type: vendorstore.VendorTurnStarted, TurnID: seedVendorTurnID, TurnOrigin: vendorstore.TurnOriginVendor,
	}); err != nil {
		t.Fatal(err)
	}
	if err := vs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return session.ID
}

func startLifecycleHost(t *testing.T, dir string) *HostProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(0, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: dir, Config: testHostConfig(), Clock: clk,
		IDs:          ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-lifecycle")),
		Transport:    newScriptedTransport(),
		VendorBinary: exe,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	return hp
}

// unlockLifecycleVault initializes and unlocks the host's vault; recovery is
// vault-independent, but resume needs an unlocked one, and the unlock RPC
// itself arrives with F10.5, so the test holds the vault.
func unlockLifecycleVault(t *testing.T, hp *HostProcess) {
	t.Helper()
	if _, err := hp.Vault().Setup(t.Context(), redact.NewSecret("lifecycle-password")); err != nil {
		t.Fatal(err)
	}
}

// The first client a recovered host serves already sees the detached session:
// recovery runs before StartHost returns and therefore before the Host API
// accepts anyone.
func TestServeRecoversVendorSessionsBeforeAcceptingClients(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := seedCrashedVendorSession(t, dir)
	hp := startLifecycleHost(t, dir)
	sub, err := hp.Runtime().Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	state, seq, ok := sub.Snapshot()
	if !ok {
		t.Fatal("the first client received no snapshot")
	}
	if seq == 0 {
		t.Fatal("the recovered session's detached change is missing from the topic sequence")
	}
	sessions := decodeSessionsSnapshot(t, state)
	if len(sessions) != 1 || sessions[0].ID != sessionID || sessions[0].State != vendorstore.StateDetached {
		t.Fatalf("first client saw %+v, want the seeded session detached", sessions)
	}
}

func decodeSessionsSnapshot(t *testing.T, state []byte) []VendorSessionView {
	t.Helper()
	var snap vendorSessionsSnapshot
	if err := json.Unmarshal(state, &snap); err != nil {
		t.Fatal(err)
	}
	return snap.Sessions
}

// A restart leaves the crashed session detached, its open turn unknown, no
// actor installed for it, and a second restart changes nothing: no respawn,
// no replay, no duplicate detached change.
func TestHostRestartLeavesSessionsDetachedAndTurnsUnknownWithoutRespawn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := seedCrashedVendorSession(t, dir)
	hp := startLifecycleHost(t, dir)
	if actors := hp.rt.vendors.sessions.actors; len(actors) != 0 {
		t.Fatalf("recovery installed %d actors; it must never respawn", len(actors))
	}
	// The journal holds the seeded created change plus the detached change
	// recovery wrote, and the second restart adds nothing to it.
	assertRecoveredSession(t, hp, sessionID, 2)

	if err := hp.Close(); err != nil {
		t.Fatal(err)
	}
	hp2 := startLifecycleHost(t, dir)
	if actors := hp2.rt.vendors.sessions.actors; len(actors) != 0 {
		t.Fatalf("the second host respawned a detached session: %d actors", len(actors))
	}
	assertRecoveredSession(t, hp2, sessionID, 2)
}

// assertRecoveredSession checks the durable recovery result: the session is
// detached, its previously open turn ended unknown, and the journal holds
// exactly wantChanges changes for it.
func assertRecoveredSession(t *testing.T, hp *HostProcess, sessionID string, wantChanges int) {
	t.Helper()
	snap, err := hp.vendorStore.Snapshot(t.Context(), sessionID)
	if err != nil || snap.State != vendorstore.StateDetached {
		t.Fatalf("recovered session = %+v, %v; want detached", snap, err)
	}
	turns, err := hp.vendorStore.SessionTurns(t.Context(), sessionID)
	if err != nil || len(turns) != 1 || turns[0].ID != seedVendorTurnID || turns[0].Outcome != vendorstore.OutcomeUnknown {
		t.Fatalf("recovered turns = %+v, %v; want %s ended unknown", turns, err, seedVendorTurnID)
	}
	changes, err := hp.vendorStore.SessionChangesSince(t.Context(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, change := range changes {
		if change.SessionID == sessionID {
			count++
		}
	}
	if count != wantChanges {
		t.Fatalf("journal holds %d changes for %s, want %d", count, sessionID, wantChanges)
	}
}

// Graceful stop runs the F9.2 stop for every live session and records each
// stop result durably before the store closes.
func TestGracefulStopRunsTheF92StopOrderForEverySession(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	first := f.start(t, "graceful-1")
	second := f.start(t, "graceful-2")
	awaitSessionState(t, sub, first.ID, vendorstore.StateReady)
	awaitSessionState(t, sub, second.ID, vendorstore.StateReady)

	f.hp.Stop("graceful-stop")
	<-f.hp.DrainDone()
	if err := f.hp.Close(); err != nil {
		t.Fatal(err)
	}
	stopProofs := readStopResults(t, f.hp.deps.DataDir)
	for _, id := range []string{first.ID, second.ID} {
		result, ok := stopProofs[id]
		if !ok {
			t.Fatalf("session %s has no recorded stop result", id)
		}
		if result.state != vendorstore.StateClosed {
			t.Fatalf("session %s = %s after graceful stop, want closed", id, result.state)
		}
		if result.proof == "" || result.reason != string(vendor.CloseHostStop) {
			t.Fatalf("session %s stop result = proof %q reason %q, want a proof and %s",
				id, result.proof, result.reason, vendor.CloseHostStop)
		}
	}
}

type vendorStopResult struct {
	state  string
	proof  string
	reason string
}

// readStopResults reopens the closed data directory and reads the stop result
// every session recorded during the drain.
func readStopResults(t *testing.T, dir string) map[string]vendorStopResult {
	t.Helper()
	clk := clock.Real{}
	store, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk,
		ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-stop-read")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	vs, err := vendorstore.New(t.Context(), store.DB(), nil, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := vs.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	sessions, err := vs.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]vendorStopResult, len(sessions))
	for i := range sessions {
		out[sessions[i].ID] = vendorStopResult{
			state: sessions[i].State, proof: sessions[i].StopProof, reason: sessions[i].CloseReason,
		}
	}
	return out
}

// Reopen and resume of a detached session go through the existing VendorService
// API: the stored handle drives a fresh process, the workspace binding holds,
// and no old turn is replayed.
func TestReopenAndResumeDetachedSessionE2E(t *testing.T) {
	t.Parallel()
	hp := startLifecycleHost(t, t.TempDir())
	unlockLifecycleVault(t, hp)
	f := &vendorServiceFixture{svc: mustVendorService(t, hp), rt: hp.Runtime(), hp: hp}
	f.ws = f.addWorkspace(t, "resume-ws")
	detached := &vendorstore.Session{
		ID: "detached-resume", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
		State: vendorstore.StateDetached, ResumeHandle: seedResumeHandle,
		WorkspaceID: f.ws, WorkspaceRoot: testRootName,
	}
	if err := hp.vendorStore.CreateSession(t.Context(), detached); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.Resume(t.Context(), testAppPrincipal, &ResumeVendorSession{
		CommandID: "resume-e2e", SessionID: detached.ID, WorkspaceID: f.ws, RootName: testRootName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.State != vendorstore.StateReady {
		t.Fatalf("resumed session = %s, want ready", view.State)
	}
	resumed, err := hp.vendorStore.Snapshot(t.Context(), detached.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ProcessPID <= 0 || resumed.ProcessStartToken == "" || resumed.ProcessPID == seedVendorPID {
		t.Fatalf("resume did not start a fresh process: %+v", resumed)
	}
	// The vendor's own init session id supersedes the stored handle, so the
	// next resume targets the live conversation; it is never empty.
	if resumed.ResumeHandle == "" {
		t.Fatal("resume lost the session handle")
	}
	_, turns, _, overviewErr := hp.vendorStore.Overview(t.Context(), detached.ID)
	if overviewErr != nil || len(turns) != 0 {
		t.Fatalf("resume replayed turns: %+v, %v", turns, overviewErr)
	}
}

func mustVendorService(t *testing.T, hp *HostProcess) *VendorService {
	t.Helper()
	svc, err := hp.Runtime().VendorService()
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// The supervisor wiring passes the raw XDG_RUNTIME_DIR of the injected
// environment — never a config fallback — and owns exactly one systemd probe.
func TestSupervisorUsesRawXDGRuntimeDir(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(0, 0))
	deps := func(env []string) *HostDeps {
		return &HostDeps{
			DataDir: t.TempDir(), Config: testHostConfig(), Clock: clk,
			IDs:           ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-xdg")),
			Transport:     newScriptedTransport(),
			VendorHostEnv: env,
		}
	}
	hp, err := StartHost(t.Context(), deps([]string{
		"XDG_RUNTIME_DIR=/run/user/4242", testPATHEnv, "HOME=/tmp/fake-home",
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if hp.vendorAdapter.XDGRuntimeDir != "/run/user/4242" {
		t.Fatalf("adapter XDG_RUNTIME_DIR = %q, want the raw environment value", hp.vendorAdapter.XDGRuntimeDir)
	}
	if hp.vendorAdapter.SystemdProbe == nil {
		t.Fatal("the vendor runtime owns no systemd probe")
	}
	if closeErr := hp.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	bare, err := StartHost(t.Context(), deps([]string{testPATHEnv}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := bare.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if bare.vendorAdapter.XDGRuntimeDir != "" {
		t.Fatalf("adapter XDG_RUNTIME_DIR = %q without an environment value, want empty", bare.vendorAdapter.XDGRuntimeDir)
	}
}

// Every vendor-runtime §13 limit fails typed at its enforcement point: the
// session cap at the service, the handshake and frame limits in the adapter.
func TestVendorSessionLimitsReturnTypedErrors(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	f.start(t, "limit-1")
	f.start(t, "limit-2")
	_, err := f.svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{
		CommandID: "limit-3", Vendor: VendorClaude, WorkspaceID: f.ws,
	})
	requireCode(t, err, errcode.VendorCapacity)
	// The handshake and frame limits fail typed at the adapter boundary; their
	// codes are public and survive the public mapping unchanged.
	for _, tc := range []struct {
		name string
		err  error
		want errcode.Code
	}{
		{"handshake 30s", vendor.ErrHandshakeTimeout, errcode.DeadlineExceeded},
		{"8 MiB frame", process.ErrFrameTooLarge, errcode.FrameTooLarge},
	} {
		if got := errcode.CodeOf(tc.err); got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
		}
		if !tc.want.IsPublic() {
			t.Fatalf("%s code %q is not public", tc.name, tc.want)
		}
		mapped := publicVendorError(tc.err)
		if got := errcode.CodeOf(mapped); got != tc.want {
			t.Fatalf("publicVendorError(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Every vendor-session error that can reach the Host API maps to one stable
// public code, and no dynamic detail leaks through the message.
func TestVendorErrorsMapToStablePublicCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want errcode.Code
	}{
		{"nil", nil, ""},
		{"session not found", vendorstore.ErrSessionNotFound, errcode.VendorSessionNotFound},
		{"capacity", ErrVendorSessionCapacity, errcode.VendorCapacity},
		{"turn not running", vendor.ErrTurnNotRunning, errcode.VendorTurnNotActive},
		{"closed session", vendor.ErrSessionClosed, errcode.VendorSessionState},
		{"busy session", vendor.ErrBusy, errcode.VendorSessionState},
		{"handshake timeout", vendor.ErrHandshakeTimeout, errcode.DeadlineExceeded},
		{"oversized frame", process.ErrFrameTooLarge, errcode.FrameTooLarge},
		{"vault locked", errcode.New(errcode.VendorVaultLocked, "the vault is locked"), errcode.VendorVaultLocked},
		{"invalid profile", errcode.New(errcode.VendorInvalidProfile, "a session profile value is not allowed"), errcode.VendorInvalidProfile},
		{"caller deadline", fmt.Errorf("wait for the vendor operation: %w", context.DeadlineExceeded), errcode.DeadlineExceeded},
		{"unclassified failure", errUnclassifiedVendor, errcode.Internal},
	} {
		assertPublicVendorMapping(t, tc.name, tc.err, tc.want)
	}
	// A vendor-prefixed typed error passes through with its own code: the
	// mapping never rewrites a code the client contract already names.
	for _, entry := range errcode.Registry() {
		if !entry.Public || !isVendorCode(entry.Code) {
			continue
		}
		mapped := publicVendorError(errcode.New(entry.Code, "fixed message"))
		if got := errcode.CodeOf(mapped); got != entry.Code {
			t.Fatalf("vendor code %q remapped to %q", entry.Code, got)
		}
	}
}

func assertPublicVendorMapping(t *testing.T, name string, err error, want errcode.Code) {
	t.Helper()
	mapped := publicVendorError(err)
	if err == nil {
		if mapped != nil {
			t.Fatal("publicVendorError(nil) is not nil")
		}
		return
	}
	if got := errcode.CodeOf(mapped); got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
	// internal is the designed hidden fallback: the client sees the stable
	// generic code, never the cause.
	if want != errcode.Internal && !want.IsPublic() {
		t.Fatalf("%s mapped to the hidden code %q", name, want)
	}
	if code, _ := errcode.Expose(mapped); code != want {
		t.Fatalf("%s exposes %q, want %q", name, code, want)
	}
}

func isVendorCode(code errcode.Code) bool {
	return len(code) > 7 && string(code[:7]) == "vendor."
}

// The change journal keeps its newest rows, prunes them in the same
// transaction as the insert that crosses the bound, and refuses a catch-up
// whose cursor is older than the oldest kept row: the hub answers with a
// snapshot, and a live subscriber is ended with resync_required instead of
// streaming over a silent gap.
func TestSessionChangesRetentionAndResyncRequired(t *testing.T) {
	t.Parallel()
	vs := newJournalStore(t)
	session := seedJournalChanges(t, vs, 4)
	assertJournalRetention(t, vs, session)
	assertStaleCursorResync(t, vs, session.ID, 5)
}

// newJournalStore opens a migrated database whose change journal keeps its
// newest three rows.
func newJournalStore(t *testing.T) *vendorstore.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "vendor-changes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err = storage.Migrate(t.Context(), db, clk); err != nil {
		t.Fatal(err)
	}
	vs, err := vendorstore.New(t.Context(), db, nil, clk, nil, vendorstore.WithChangeJournalRetention(3))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := vs.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return vs
}

// seedJournalChanges creates a session and appends extra state changes; the
// next append must carry the returned sequence.
func seedJournalChanges(t *testing.T, vs *vendorstore.Store, extra int) *vendorstore.Session {
	t.Helper()
	ctx := t.Context()
	session := &vendorstore.Session{
		ID: "journal-session", Vendor: VendorClaude, AccountID: "a", Role: "assistant", State: vendorstore.StateReady,
	}
	if err := vs.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	session.LastSequence = 1 // CreateSession commits the first journal row.
	for range extra {
		next, err := vs.Append(ctx, session.ID, session.LastSequence, &vendorstore.Event{
			Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateRunning,
		})
		if err != nil {
			t.Fatal(err)
		}
		session.LastSequence = next
	}
	return session
}

// assertJournalRetention proves five changes crossed the bound of three: the
// insert that crossed pruned the older rows in its own transaction, a catch-up
// from before the journal fails typed, and a catch-up from the newest row is
// an empty success.
func assertJournalRetention(t *testing.T, vs *vendorstore.Store, session *vendorstore.Session) {
	t.Helper()
	ctx := t.Context()
	if _, err := vs.SessionChangesSince(ctx, 0, 100); !errors.Is(err, vendorstore.ErrChangeCursorTooOld) {
		t.Fatalf("catch-up from before the journal = %v, want %v", err, vendorstore.ErrChangeCursorTooOld)
	}
	tail, err := vs.SessionChangesSince(ctx, 2, 100)
	if err != nil || len(tail) != 3 || tail[0].Sequence != 3 || tail[2].Sequence != 5 {
		t.Fatalf("journal tail = %+v, %v; want sequences 3..5", tail, err)
	}
	if _, err := vs.SessionChangesSince(ctx, session.LastSequence, 100); err != nil {
		t.Fatalf("catch-up from the newest row = %v, want nil", err)
	}
}

// assertStaleCursorResync proves the topic maps a pruned cursor to the hidden
// resync signal: a subscribe whose cursor predates the journal gets a fresh
// snapshot, never a partial catch-up, and a live subscriber whose publish
// window fell behind is ended with resync_required while the cursor jumps to
// the newest row so later publishes resume.
func assertStaleCursorResync(t *testing.T, vs *vendorstore.Store, sessionID string, newest int64) {
	t.Helper()
	ctx := t.Context()
	hub := NewEventHub("journal-inc")
	topic, err := newVendorSessionsTopic(ctx, vs, hub)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(ctx, SubscribeParams{
		Principal: testAppPrincipal, Topic: TopicVendorSessions,
		Cursor: &EventCursor{Topic: TopicVendorSessions, Sequence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	state, _, ok := sub.Snapshot()
	if !ok {
		t.Fatal("a cursor older than the journal got no snapshot")
	}
	sessions := decodeSessionsSnapshot(t, state)
	if len(sessions) != 1 || sessions[0].State != vendorstore.StateRunning {
		t.Fatalf("snapshot after a stale cursor = %+v, want the latest state", sessions)
	}

	live, err := hub.Subscribe(ctx, SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	topic.mu.Lock()
	topic.published = 1
	topic.mu.Unlock()
	if _, err := vs.Append(ctx, sessionID, newest, &vendorstore.Event{
		Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	topic.notify(ctx)
	if _, ok := live.Next(ctx); ok {
		t.Fatal("the live stream kept delivering over the gap")
	}
	if !live.ResyncRequired() {
		t.Fatal("the live stream did not end with resync_required")
	}
	topic.mu.Lock()
	published := topic.published
	topic.mu.Unlock()
	// The newest journal row is create(1), the four seeded changes (2..5)
	// and this ready change (6).
	if published != 6 {
		t.Fatalf("published cursor = %d after resync, want 6", published)
	}
}
