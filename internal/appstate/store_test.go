package appstate

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newStoreFixture opens a migrated database for one store test.
func newStoreFixture(t *testing.T) *Store {
	t.Helper()
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(ctx, db, clock.NewFake(time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("migrate storage: %v", err)
	}
	return NewStore(db)
}

// testUUID is a deterministic valid window UUID, unique for n < 256.
func testUUID(n int) string {
	const digits = "0123456789abcdef"
	x, y := digits[n%16], digits[(n/16)%16]
	return "0d20ab" + string(x) + string(y) + "-0000-4000-8000-0000000000" + string(x) + string(y)
}

func TestWindowRoundTripAndCAS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

	layout := []byte(`{"schema_version":1,"slots":["main"]}`)
	rev, seq, err := store.PutWindow(ctx, testUUID(1), 0, layout, now)
	if err != nil || rev != 1 || seq != 1 {
		t.Fatalf("create = (%d, %d, %v), want (1, 1, nil)", rev, seq, err)
	}
	layout2 := []byte(`{"schema_version":1,"slots":["main","side"]}`)
	rev, seq, err = store.PutWindow(ctx, testUUID(1), 1, layout2, now)
	if err != nil || rev != 2 || seq != 2 {
		t.Fatalf("update = (%d, %d, %v), want (2, 2, nil)", rev, seq, err)
	}

	windows, err := store.ListWindows(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("list = (%+v, %v)", windows, err)
	}
	if windows[0].WindowID != testUUID(1) || windows[0].Revision != 2 || string(windows[0].Layout) != string(layout2) {
		t.Fatalf("stored window = %+v", windows[0])
	}
	if !windows[0].UpdatedAt.Equal(now) {
		t.Fatalf("updated_at = %v, want %v", windows[0].UpdatedAt, now)
	}

	_, _, err = store.PutWindow(ctx, testUUID(1), 9, layout, now)
	assertConflict(t, err, 2)
	_, _, err = store.PutWindow(ctx, testUUID(1), 0, layout, now)
	assertConflict(t, err, 2)
	if _, err := store.DeleteWindow(ctx, testUUID(1), 3, now); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("stale delete = %v, want a conflict", err)
	}

	deletedSeq, err := store.DeleteWindow(ctx, testUUID(1), 2, now)
	if err != nil || deletedSeq != 3 {
		t.Fatalf("delete = (%d, %v), want (3, nil)", deletedSeq, err)
	}
	if windows, listErr := store.ListWindows(ctx); listErr != nil || len(windows) != 0 {
		t.Fatalf("list after delete = (%+v, %v)", windows, listErr)
	}
	if _, err := store.DeleteWindow(ctx, testUUID(1), 2, now); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("delete of an absent window = %v, want a conflict", err)
	}
}

func TestWindowIDMustBeAUUID(t *testing.T) {
	t.Parallel()
	store := newStoreFixture(t)
	for _, id := range []string{"", "not-a-uuid", "0d20ab0a-0000-4000-8000-00000000000A", "0d20ab0a0000400080000000000000a1"} {
		if _, _, err := store.PutWindow(t.Context(), id, 0, []byte(`{"schema_version":1}`), time.Now().UTC()); !errors.Is(err, ErrWindowIDInvalid) {
			t.Fatalf("window_id %q = %v, want ErrWindowIDInvalid", id, err)
		}
	}
}

// TestWindowLimitIsSixteen pins the saved-window cap of host-api §5.2.
func TestWindowLimitIsSixteen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	for n := 1; n <= MaxWindows; n++ {
		if _, _, err := store.PutWindow(ctx, testUUID(n), 0, []byte(`{"schema_version":1}`), now); err != nil {
			t.Fatalf("put window %d: %v", n, err)
		}
	}
	if _, _, err := store.PutWindow(ctx, testUUID(MaxWindows+1), 0, []byte(`{"schema_version":1}`), now); !errors.Is(err, ErrTooManyWindows) {
		t.Fatalf("the 17th window = %v, want ErrTooManyWindows", err)
	}
	// Updating an existing window past the cap still works.
	if _, _, err := store.PutWindow(ctx, testUUID(1), 1, []byte(`{"schema_version":2}`), now); err != nil {
		t.Fatalf("update past the cap: %v", err)
	}
}

func TestPreferencesSingleRecordCAS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

	prefs, err := store.Preferences(ctx)
	if err != nil || prefs.Revision != 0 || len(prefs.Document) != 0 {
		t.Fatalf("empty preferences = (%+v, %v), want revision 0 and no document", prefs, err)
	}
	first := []byte(`{"schema_version":1,"theme":"dark"}`)
	rev, seq, err := store.PutPreferences(ctx, 0, first, now)
	if err != nil || rev != 1 || seq != 1 {
		t.Fatalf("create = (%d, %d, %v), want (1, 1, nil)", rev, seq, err)
	}
	second := []byte(`{"schema_version":1,"theme":"light"}`)
	rev, _, err = store.PutPreferences(ctx, 1, second, now)
	if err != nil || rev != 2 {
		t.Fatalf("update = (%d, %v), want (2, nil)", rev, err)
	}
	if _, _, err := store.PutPreferences(ctx, 1, first, now); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("stale put = %v, want a conflict", err)
	}
	got, err := store.Preferences(ctx)
	if err != nil || got.Revision != 2 || string(got.Document) != string(second) {
		t.Fatalf("preferences = (%+v, %v)", got, err)
	}
}

func TestSnapshotMatchesTheJournal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

	snap, err := store.Snapshot(ctx)
	if err != nil || snap.Sequence != 0 || len(snap.Windows) != 0 || snap.Preferences.Revision != 0 {
		t.Fatalf("empty snapshot = %+v, %v", snap, err)
	}
	layout := []byte(`{"schema_version":1}`)
	if _, _, err := store.PutWindow(ctx, testUUID(2), 0, layout, now); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := store.PutPreferences(ctx, 0, layout, now); err != nil {
		t.Fatalf("put preferences: %v", err)
	}
	snap, err = store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Sequence != 2 || len(snap.Windows) != 1 {
		t.Fatalf("snapshot = (seq %d, %d windows), want (2, 1)", snap.Sequence, len(snap.Windows))
	}
	if snap.Windows[0].WindowID != testUUID(2) || string(snap.Windows[0].Layout) != string(layout) {
		t.Fatalf("snapshot window = %+v", snap.Windows[0])
	}
	if snap.Preferences.Revision != 1 || string(snap.Preferences.Document) != string(layout) {
		t.Fatalf("snapshot preferences = %+v", snap.Preferences)
	}
}

// TestChangesSinceAndRetention pins catch-up and the gap rule: a cursor
// older than the oldest retained row fails typed instead of catching up over
// silent holes.
func TestChangesSinceAndRetention(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

	changes, err := store.ChangesSince(ctx, 0)
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes on an empty journal = (%+v, %v)", changes, err)
	}
	for n := 1; n <= 3; n++ {
		if _, _, err := store.PutWindow(ctx, testUUID(n), 0, []byte(`{"schema_version":1}`), now); err != nil {
			t.Fatalf("put window %d: %v", n, err)
		}
	}
	changes, err = store.ChangesSince(ctx, 0)
	if err != nil || len(changes) != 3 {
		t.Fatalf("changes = (%+v, %v), want 3", changes, err)
	}
	for i, c := range changes {
		if c.Sequence != int64(i+1) || c.Kind != changeWindowPut || c.WindowID != testUUID(i+1) || c.Revision != 1 {
			t.Fatalf("change %d = %+v", i, c)
		}
	}
	if changes, err = store.ChangesSince(ctx, 3); err != nil || len(changes) != 0 {
		t.Fatalf("changes after the last row = (%+v, %v)", changes, err)
	}

	// Simulate the retention prune of rows 1 and 2; a cursor at 0 now points
	// into pruned history.
	db := storeDB(store)
	if _, err := db.ExecContext(ctx, `DELETE FROM app_state_events WHERE seq <= 2`); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := store.ChangesSince(ctx, 0); !errors.Is(err, ErrChangeCursorTooOld) {
		t.Fatalf("a pruned cursor = %v, want ErrChangeCursorTooOld", err)
	}
	if changes, err = store.ChangesSince(ctx, 2); err != nil || len(changes) != 1 {
		t.Fatalf("changes from the oldest kept row = (%+v, %v)", changes, err)
	}
}

// storeDB returns the fixture's handle for the retention simulation above:
// the test is in the table's owning package, so it may prune the journal
// directly.
func storeDB(s *Store) *sql.DB { return s.db }

func assertConflict(t *testing.T, err error, current uint64) {
	t.Helper()
	if err == nil {
		t.Fatal("the write succeeded, want a conflict")
	}
	if errcode.CodeOf(err) != errcode.AppStateRevisionConflict {
		t.Fatalf("code = %q, want appstate.revision_conflict", errcode.CodeOf(err))
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("the conflict carries no typed current revision: %v", err)
	}
	if conflict.CurrentRevision() != current {
		t.Fatalf("current revision = %d, want %d", conflict.CurrentRevision(), current)
	}
}

func TestDocumentEnvelopeValidation(t *testing.T) {
	t.Parallel()
	valid := []byte(`{"schema_version":1,"anything":["goes","here"],"nested":{"x":1}}`)
	if err := ValidateWindowLayout(valid); err != nil {
		t.Fatalf("a valid layout = %v", err)
	}
	if err := ValidatePreferences([]byte(`{"schema_version":42}`)); err != nil {
		t.Fatalf("a valid preferences = %v", err)
	}
	cases := []struct {
		name string
		doc  string
	}{
		{"empty", ""},
		{"not an object", `5`},
		{"bare null", `null`},
		{"missing schema_version", `{}`},
		{"schema_version zero", `{"schema_version":0}`},
		{"schema_version negative", `{"schema_version":-1}`},
		{"schema_version quoted", `{"schema_version":"1"}`},
		{"schema_version fractional", `{"schema_version":1.5}`},
		{"schema_version boolean", `{"schema_version":true}`},
		{"schema_version null", `{"schema_version":null}`},
		{"schema_version object", `{"schema_version":{"n":1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateWindowLayout([]byte(tc.doc)); err == nil {
				t.Fatalf("%s accepted", tc.doc)
			}
		})
	}
	// At the limit exactly is valid; one byte over is too large, for both
	// documents.
	atLimit := []byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", MaxLayoutBytes-len(`{"schema_version":1,"pad":""}`)) + `"}`)
	if len(atLimit) != MaxLayoutBytes {
		t.Fatalf("the boundary document is %d bytes, want exactly %d", len(atLimit), MaxLayoutBytes)
	}
	if err := ValidateWindowLayout(atLimit); err != nil {
		t.Fatalf("a %d-byte layout = %v", len(atLimit), err)
	}
	if err := ValidateWindowLayout(append(atLimit, 'x')); !errors.Is(err, ErrLayoutTooLarge) {
		t.Fatalf("a %d-byte layout = %v, want ErrLayoutTooLarge", len(atLimit)+1, err)
	}
	prefsAtLimit := []byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", MaxPreferencesBytes-len(`{"schema_version":1,"pad":""}`)) + `"}`)
	if err := ValidatePreferences(prefsAtLimit); err != nil {
		t.Fatalf("a %d-byte preferences = %v", len(prefsAtLimit), err)
	}
	if err := ValidatePreferences(append(prefsAtLimit, 'x')); !errors.Is(err, ErrPreferencesTooLarge) {
		t.Fatalf("an oversized preferences = %v, want ErrPreferencesTooLarge", err)
	}
	// The limits are per document kind: a document at the layout limit still
	// fails the smaller preferences limit.
	if err := ValidatePreferences([]byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", MaxLayoutBytes-MaxPreferencesBytes) + `"}`)); !errors.Is(err, ErrPreferencesTooLarge) {
		t.Fatal("a layout-sized preferences document was accepted")
	}
}
