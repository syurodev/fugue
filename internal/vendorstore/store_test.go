package vendorstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestSequenceConflictAndSnapshot(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if err := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), "s", 0, &Event{Type: UsageObserved}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("conflict: %v", err)
	}
	snap, err := s.Snapshot(t.Context(), "s")
	if err != nil || snap.LastSequence != 1 {
		t.Fatalf("snapshot: %+v, %v", snap, err)
	}
}

func TestRecoveryDetachesSessionsAndMarksOpenTurnsUnknown(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	if err := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, ProcessPID: 42, ProcessStartToken: testStartToken, ProcessScopeUnit: testScopeUnit}); err != nil {
		t.Fatal(err)
	}
	_, appendErr := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, appendErr)
	got, err := s.Recover(t.Context())
	want := []ProcessIdentity{{SessionID: "s", PID: 42, StartToken: testStartToken, ScopeUnit: testScopeUnit}}
	if diff := cmp.Diff(want, got); err != nil || diff != "" {
		t.Fatalf("recovery identities (-want +got): %s, %v", diff, err)
	}
	snap, err := s.Snapshot(t.Context(), "s")
	if err != nil || snap.State != StateDetached {
		t.Fatalf("recovered session: %+v, %v", snap, err)
	}
	var outcome string
	if err := db.QueryRowContext(t.Context(), `SELECT outcome FROM vendor_turns WHERE session_id='s' AND turn_id='t'`).Scan(&outcome); err != nil || outcome != OutcomeUnknown {
		t.Fatalf("recovered outcome: %s, %v", outcome, err)
	}
}

func testStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	err = storage.Migrate(t.Context(), db, clk)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), db, nil, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	return s, db
}

func requireNoError(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatal(err)
	}
}

const (
	testStartToken = "start"
	testScopeUnit  = "fugue-vendor-s.scope"
)

const testResumeHandle = "resume"
