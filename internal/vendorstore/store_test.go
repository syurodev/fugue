package vendorstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestSequenceConflictAndSnapshot(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if operationErr2 := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}); operationErr2 != nil {
		t.Fatal(operationErr2)
	}
	if _, operationErr4 := s.Append(t.Context(), "s", 0, &Event{Type: UsageObserved}); !errors.Is(operationErr4, ErrSequenceConflict) {
		t.Fatalf("conflict: %v", operationErr4)
	}
	snap, err := s.Snapshot(t.Context(), "s")
	if err != nil || snap.LastSequence != 1 {
		t.Fatalf("snapshot: %+v, %v", snap, err)
	}
}

func TestRecoveryDetachesSessionsAndMarksOpenTurnsUnknown(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	if operationErr6 := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, ProcessPID: 42, ProcessStartTime: testProcessStart}); operationErr6 != nil {
		t.Fatal(operationErr6)
	}
	_, appendErr := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, appendErr)
	got, err := s.Recover(t.Context())
	if err != nil || len(got) != 1 || got[0].PID != 42 || got[0].StartTime != testProcessStart {
		t.Fatalf("recovery identities: %+v, %v", got, err)
	}
	snap, err := s.Snapshot(t.Context(), "s")
	if err != nil || snap.State != "detached" {
		t.Fatalf("recovered session: %+v, %v", snap, err)
	}
	var outcome string
	if operationErr9 := db.QueryRowContext(t.Context(), `SELECT outcome FROM vendor_turns WHERE session_id='s' AND turn_id='t'`).Scan(&outcome); operationErr9 != nil || outcome != OutcomeUnknown {
		t.Fatalf("recovered outcome: %s, %v", outcome, operationErr9)
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
	if operationErr1 := storage.Migrate(t.Context(), db, clk); operationErr1 != nil {
		t.Fatal(operationErr1)
	}
	s, err := New(t.Context(), db, nil, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	return s, db
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const testProcessStart = "start"

const testResumeHandle = "resume"
