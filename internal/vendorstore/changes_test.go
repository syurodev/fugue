package vendorstore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

// Every creation, projected state change and recovery detach lands in the
// cross-session journal in commit order, and the list's sequence is the
// journal's last one, so list plus catch-up has no gap (Q-58 A).
func TestSessionChangeJournalRecordsCreateStateAndRecovery(t *testing.T) {
	t.Parallel()
	store := seedSessionJournal(t)
	ctx := t.Context()
	changes, err := store.SessionChangesSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []SessionChange{
		{Sequence: 1, SessionID: "a", Kind: ChangeCreated, State: StateStarting},
		{Sequence: 2, SessionID: "b", Kind: ChangeCreated, State: StateStarting},
		{Sequence: 3, SessionID: "a", Kind: ChangeStateChanged, State: StateReady},
		{Sequence: 4, SessionID: "a", Kind: ChangeStateChanged, State: StateDetached},
		{Sequence: 5, SessionID: "b", Kind: ChangeStateChanged, State: StateDetached},
	}
	if diff := cmp.Diff(want, changes); diff != "" {
		t.Fatalf("journal (-want +got):\n%s", diff)
	}
	assertSessionListAtJournalEnd(t, store, 5)
	tail, err := store.SessionChangesSince(ctx, 4, 100)
	if err != nil || len(tail) != 1 || tail[0].Sequence != 5 {
		t.Fatalf("changes after 4 = %+v, %v", tail, err)
	}
}

// seedSessionJournal creates two sessions, moves one to ready and recovers.
func seedSessionJournal(t *testing.T) *Store {
	t.Helper()
	store, _ := testStore(t)
	for _, id := range []string{"a", "b"} {
		if err := store.CreateSession(t.Context(), &Session{ID: id, Vendor: journalTestVendor, AccountID: journalTestAccount, Role: journalTestRole, State: StateStarting}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Append(t.Context(), "a", 1, &Event{Type: VendorSessionStateChanged, State: StateReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store
}

func assertSessionListAtJournalEnd(t *testing.T, store *Store, want int64) {
	t.Helper()
	sessions, last, err := store.SessionList(t.Context())
	if err != nil || last != want || len(sessions) != 2 || sessions[0].State != StateDetached {
		t.Fatalf("list = %+v, last %d, err %v", sessions, last, err)
	}
}

// The journal keeps its newest ChangeJournalRetention rows, pruned in the
// insert's transaction, and a catch-up from a cursor older than the oldest
// kept row fails typed instead of streaming over a pruned gap (F9.9e).
func TestSessionChangeJournalRetentionAndStaleCursor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := testStoreWithJournalRetention(t, 3)
	if err := store.CreateSession(ctx, &Session{ID: "j", Vendor: journalTestVendor, AccountID: journalTestAccount, Role: journalTestRole, State: StateStarting}); err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 4; seq++ {
		if _, err := store.Append(ctx, "j", seq, &Event{Type: VendorSessionStateChanged, State: StateReady}); err != nil {
			t.Fatal(err)
		}
	}
	// Five changes crossed the bound of three: the insert that crossed pruned
	// the older rows in its own transaction.
	if _, err := store.SessionChangesSince(ctx, 0, 100); !errors.Is(err, ErrChangeCursorTooOld) {
		t.Fatalf("catch-up from before the journal = %v, want %v", err, ErrChangeCursorTooOld)
	}
	tail, err := store.SessionChangesSince(ctx, 2, 100)
	if err != nil || len(tail) != 3 || tail[0].Sequence != 3 || tail[2].Sequence != 5 {
		t.Fatalf("journal tail = %+v, %v; want sequences 3..5", tail, err)
	}
	if _, err := store.SessionChangesSince(ctx, 5, 100); err != nil {
		t.Fatalf("catch-up from the newest row = %v", err)
	}
}

// A non-positive retention is a store misconfiguration, not a request to keep
// nothing: the default applies.
func TestNonPositiveJournalRetentionFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := testStoreWithJournalRetention(t, 0)
	for _, id := range []string{"a", "b", "c"} {
		if err := store.CreateSession(ctx, &Session{ID: id, Vendor: journalTestVendor, AccountID: journalTestAccount, Role: journalTestRole, State: StateStarting}); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := store.SessionChangesSince(ctx, 0, 100)
	if err != nil || len(changes) != 3 {
		t.Fatalf("journal = %+v, %v; want all three rows kept", changes, err)
	}
}

// SessionTurns reports open and ended turns with their outcomes; recovery
// verification reads it after Recover turns an open turn unknown (F9.9e).
func TestSessionTurnsReportOpenAndEndedTurns(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, _ := testStore(t)
	if err := store.CreateSession(ctx, &Session{ID: "t1", Vendor: journalTestVendor, AccountID: journalTestAccount, Role: journalTestRole, State: StateStarting}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "t1", 1, &Event{Type: VendorTurnStarted, TurnID: journalEndedTurn, TurnOrigin: TurnOriginFugue}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "t1", 2, &Event{Type: VendorTurnEnded, TurnID: journalEndedTurn, Outcome: OutcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	vendorTurn := "vendor-3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	if _, err := store.Append(ctx, "t1", 3, &Event{Type: VendorTurnStarted, TurnID: vendorTurn, TurnOrigin: TurnOriginVendor}); err != nil {
		t.Fatal(err)
	}
	want := []TurnState{
		{ID: journalEndedTurn, Origin: TurnOriginFugue, State: TurnStateEnded, Outcome: OutcomeCompleted},
		{ID: vendorTurn, Origin: TurnOriginVendor, State: TurnStateRunning},
	}
	turns, err := store.SessionTurns(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, turns); diff != "" {
		t.Fatalf("turns before recovery (-want +got):\n%s", diff)
	}
	if _, err = store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	turns, err = store.SessionTurns(ctx, "t1")
	if err != nil || len(turns) != 2 || turns[1].ID != vendorTurn || turns[1].Outcome != OutcomeUnknown {
		t.Fatalf("turns after recovery = %+v, %v; want the open turn unknown", turns, err)
	}
}

const (
	journalTestVendor  = "claude"
	journalTestAccount = "acct"
	journalTestRole    = "assistant"
	journalEndedTurn   = "fugue-turn-1"
)

func testStoreWithJournalRetention(t *testing.T, retention int) *Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	if err = storage.Migrate(t.Context(), db, clk); err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), db, nil, clk, nil, WithChangeJournalRetention(retention))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	return s
}
