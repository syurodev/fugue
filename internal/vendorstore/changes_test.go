package vendorstore

import (
	"testing"

	"github.com/google/go-cmp/cmp"
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
		if err := store.CreateSession(t.Context(), &Session{ID: id, Vendor: "claude", AccountID: "acct", Role: "assistant", State: StateStarting}); err != nil {
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
