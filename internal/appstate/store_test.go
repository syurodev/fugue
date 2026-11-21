package appstate

import (
	"context"
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

// testNow is the one moment the store tests run at.
func testNow() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) }

// newStoreFixture opens a migrated database for one store test.
func newStoreFixture(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close the store database: %v", closeErr)
		}
	})
	if migErr := storage.Migrate(t.Context(), db, clock.NewFake(testNow())); migErr != nil {
		t.Fatalf("migrate storage: %v", migErr)
	}
	return NewStore(db)
}

// testUUID is a deterministic valid window UUID, unique for n < 256.
func testUUID(n int) string {
	const digits = "0123456789abcdef"
	x, y := digits[n%16], digits[(n/16)%16]
	return "0d20ab" + string(x) + string(y) + "-0000-4000-8000-0000000000" + string(x) + string(y)
}

func TestWindowCreateUpdateAndList(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	layout := []byte(`{"schema_version":1,"slots":["main"]}`)
	rev, seq, err := store.PutWindow(ctx, testUUID(1), 0, layout, testNow())
	assertWindowPut(t, rev, seq, err, 1, 1)
	layout2 := []byte(`{"schema_version":1,"slots":["main","side"]}`)
	rev, seq, err = store.PutWindow(ctx, testUUID(1), 1, layout2, testNow())
	assertWindowPut(t, rev, seq, err, 2, 2)

	windows, listErr := store.ListWindows(ctx)
	if listErr != nil || len(windows) != 1 {
		t.Fatalf("list = (%+v, %v)", windows, listErr)
	}
	assertStoredWindow(t, windows[0], testUUID(1), 2, layout2)
}

// assertWindowPut asserts one CAS write's revision, journal sequence and
// error.
func assertWindowPut(t *testing.T, rev uint64, seq int64, err error, wantRev uint64, wantSeq int64) {
	t.Helper()
	if err != nil || rev != wantRev || seq != wantSeq {
		t.Fatalf("put = (%d, %d, %v), want (%d, %d, nil)", rev, seq, err, wantRev, wantSeq)
	}
}

// assertStoredWindow asserts one listed window's identity, revision, exact
// layout and timestamp.
func assertStoredWindow(t *testing.T, got Window, windowID string, revision uint64, layout []byte) {
	t.Helper()
	if got.WindowID != windowID || got.Revision != revision || string(got.Layout) != string(layout) {
		t.Fatalf("stored window = %+v", got)
	}
	if !got.UpdatedAt.Equal(testNow()) {
		t.Fatalf("updated_at = %v, want %v", got.UpdatedAt, testNow())
	}
}

func TestWindowCASConflicts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	layout := []byte(`{"schema_version":1}`)
	if _, _, err := store.PutWindow(ctx, testUUID(1), 0, layout, testNow()); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := store.PutWindow(ctx, testUUID(1), 9, layout, testNow())
	assertConflict(t, err, 1)
	_, _, err = store.PutWindow(ctx, testUUID(1), 0, layout, testNow())
	assertConflict(t, err, 1)
}

func TestWindowDeleteUnderCAS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	layout := []byte(`{"schema_version":1}`)
	if _, _, err := store.PutWindow(ctx, testUUID(1), 0, layout, testNow()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.DeleteWindow(ctx, testUUID(1), 3, testNow()); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("stale delete = %v, want a conflict", err)
	}
	deletedSeq, err := store.DeleteWindow(ctx, testUUID(1), 1, testNow())
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deletedSeq < 1 {
		t.Fatalf("delete sequence = %d, want a committed journal row", deletedSeq)
	}
	windows, listErr := store.ListWindows(ctx)
	if listErr != nil || len(windows) != 0 {
		t.Fatalf("list after delete = (%+v, %v)", windows, listErr)
	}
	if _, err := store.DeleteWindow(ctx, testUUID(1), 1, testNow()); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("delete of an absent window = %v, want a conflict", err)
	}
}

func TestWindowIDMustBeAUUID(t *testing.T) {
	t.Parallel()
	store := newStoreFixture(t)
	for _, id := range []string{"", "not-a-uuid", "0d20ab0a-0000-4000-8000-00000000000A", "0d20ab0a0000400080000000000000a1"} {
		if _, _, err := store.PutWindow(t.Context(), id, 0, []byte(`{"schema_version":1}`), testNow()); !errors.Is(err, ErrWindowIDInvalid) {
			t.Fatalf("window_id %q = %v, want ErrWindowIDInvalid", id, err)
		}
	}
}

// TestWindowLimitIsSixteen pins the saved-window cap of host-api §5.2.
func TestWindowLimitIsSixteen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	for n := 1; n <= MaxWindows; n++ {
		if _, _, err := store.PutWindow(ctx, testUUID(n), 0, []byte(`{"schema_version":1}`), testNow()); err != nil {
			t.Fatalf("put window %d: %v", n, err)
		}
	}
	if _, _, err := store.PutWindow(ctx, testUUID(MaxWindows+1), 0, []byte(`{"schema_version":1}`), testNow()); !errors.Is(err, ErrTooManyWindows) {
		t.Fatalf("the 17th window = %v, want ErrTooManyWindows", err)
	}
	// Updating an existing window past the cap still works.
	if _, _, err := store.PutWindow(ctx, testUUID(1), 1, []byte(`{"schema_version":2}`), testNow()); err != nil {
		t.Fatalf("update past the cap: %v", err)
	}
}

func TestPreferencesStartEmptyAndCreate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	prefs, err := store.Preferences(ctx)
	if err != nil || prefs.Revision != 0 || len(prefs.Document) != 0 {
		t.Fatalf("empty preferences = (%+v, %v), want revision 0 and no document", prefs, err)
	}
	first := []byte(`{"schema_version":1,"theme":"dark"}`)
	rev, seq, err := store.PutPreferences(ctx, 0, first, testNow())
	if err != nil || rev != 1 || seq != 1 {
		t.Fatalf("create = (%d, %d, %v), want (1, 1, nil)", rev, seq, err)
	}
	_, _, err = store.PutPreferences(ctx, 0, first, testNow())
	assertConflict(t, err, 1)
}

func TestPreferencesCASUpdate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	if _, _, err := store.PutPreferences(ctx, 0, []byte(`{"schema_version":1}`), testNow()); err != nil {
		t.Fatalf("create: %v", err)
	}
	second := []byte(`{"schema_version":1,"theme":"light"}`)
	rev, _, err := store.PutPreferences(ctx, 1, second, testNow())
	if err != nil || rev != 2 {
		t.Fatalf("update = (%d, %v), want (2, nil)", rev, err)
	}
	_, _, err = store.PutPreferences(ctx, 1, second, testNow())
	assertConflict(t, err, 2)
	got, err := store.Preferences(ctx)
	if err != nil || got.Revision != 2 || string(got.Document) != string(second) {
		t.Fatalf("preferences = (%+v, %v)", got, err)
	}
}

func TestSnapshotStartsEmpty(t *testing.T) {
	t.Parallel()
	snap, err := newStoreFixture(t).Snapshot(t.Context())
	if err != nil || snap.Sequence != 0 || len(snap.Windows) != 0 || snap.Preferences.Revision != 0 {
		t.Fatalf("empty snapshot = %+v, %v", snap, err)
	}
}

func TestSnapshotMatchesTheJournal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	layout := []byte(`{"schema_version":1}`)
	if _, _, err := store.PutWindow(ctx, testUUID(2), 0, layout, testNow()); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := store.PutPreferences(ctx, 0, layout, testNow()); err != nil {
		t.Fatalf("put preferences: %v", err)
	}
	snap, err := store.Snapshot(ctx)
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

func TestChangesSinceOrdersTheJournal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	changes, err := store.ChangesSince(ctx, 0)
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes on an empty journal = (%+v, %v)", changes, err)
	}
	mustPutWindows(t, ctx, store, 3)
	changes, err = store.ChangesSince(ctx, 0)
	if err != nil || len(changes) != 3 {
		t.Fatalf("changes = (%+v, %v), want 3", changes, err)
	}
	for i, c := range changes {
		assertJournalRow(t, c, int64(i+1), changeWindowPut, testUUID(i+1), 1)
	}
	if changes, err = store.ChangesSince(ctx, 3); err != nil || len(changes) != 0 {
		t.Fatalf("changes after the last row = (%+v, %v)", changes, err)
	}
}

// mustPutWindows creates n valid windows.
func mustPutWindows(t *testing.T, ctx context.Context, store *Store, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, _, err := store.PutWindow(ctx, testUUID(i), 0, []byte(`{"schema_version":1}`), testNow()); err != nil {
			t.Fatalf("put window %d: %v", i, err)
		}
	}
}

// assertJournalRow asserts one journal row's identity and revision.
func assertJournalRow(t *testing.T, c Change, seq int64, kind, windowID string, rev uint64) {
	t.Helper()
	if c.Sequence != seq || c.Kind != kind || c.WindowID != windowID || c.Revision != rev {
		t.Fatalf("change %d = %+v, want (%d, %s, %s, %d)", c.Sequence, c, seq, kind, windowID, rev)
	}
}

// TestChangesSinceRejectsAPrunedCursor pins the gap rule: a cursor older
// than the oldest retained row fails typed instead of catching up over
// silent holes. The prune of rows 1 and 2 simulates the retention eviction
// the store performs on every append; the test is in the owning package, so
// it may prune the journal directly.
func TestChangesSinceRejectsAPrunedCursor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)
	mustPutWindows(t, ctx, store, 3)
	db := storeDB(store)
	if _, err := db.ExecContext(ctx, `DELETE FROM app_state_events WHERE seq <= 2`); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := store.ChangesSince(ctx, 0); !errors.Is(err, ErrChangeCursorTooOld) {
		t.Fatalf("a pruned cursor = %v, want ErrChangeCursorTooOld", err)
	}
	changes, err := store.ChangesSince(ctx, 2)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes from the oldest kept row = (%+v, %v)", changes, err)
	}
}

// storeDB returns the fixture's handle for the retention simulation above.
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
	for _, tc := range invalidEnvelopes() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateWindowLayout([]byte(tc.doc)); err == nil {
				t.Fatalf("%s accepted", tc.doc)
			}
		})
	}
	assertDocumentSizeLimits(t)
}

// invalidEnvelopes lists the documents that fail the envelope check.
func invalidEnvelopes() []struct{ name, doc string } {
	return []struct{ name, doc string }{
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
}

// assertDocumentSizeLimits pins the size limits: at the limit exactly is
// valid, one byte over is too large, and the limits are per document kind.
func assertDocumentSizeLimits(t *testing.T) {
	t.Helper()
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
	if err := ValidatePreferences([]byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", MaxLayoutBytes-MaxPreferencesBytes) + `"}`)); !errors.Is(err, ErrPreferencesTooLarge) {
		t.Fatal("a layout-sized preferences document was accepted")
	}
}
