package appstate

// The store's error paths: a closed database fails every method typed, a
// corrupt row fails its scan, the journal's CHECK constraint rejects an
// unknown change kind, and the conflict error prints its code. The tests
// stay in the owning package so they may corrupt or prune the tables they
// own.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
)

// newCorruptibleFixture opens a migrated database and hands the handle back,
// so a test can close it or corrupt a row and watch the typed failure.
func newCorruptibleFixture(t *testing.T) (*Store, func()) {
	t.Helper()
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if migErr := storage.Migrate(t.Context(), db, clock.NewFake(testNow())); migErr != nil {
		t.Fatalf("migrate storage: %v", migErr)
	}
	return NewStore(db), func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close the store database: %v", closeErr)
		}
	}
}

func newClosedStoreFixture(t *testing.T) *Store {
	t.Helper()
	store, closeDB := newCorruptibleFixture(t)
	closeDB()
	return store
}

func TestAClosedStoreFailsEveryMethodTyped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newClosedStoreFixture(t)

	if _, err := store.ListWindows(ctx); !storeUnavailable(err, "list the app windows") {
		t.Fatalf("ListWindows on a closed store = %v", err)
	}
	if _, _, err := store.PutWindow(ctx, testUUID(1), 0, []byte(`{"schema_version":1}`), testNow()); !storeInternal(err, "begin the app state write") {
		t.Fatalf("PutWindow on a closed store = %v", err)
	}
	if _, err := store.DeleteWindow(ctx, testUUID(1), 1, testNow()); !storeInternal(err, "begin the app state write") {
		t.Fatalf("DeleteWindow on a closed store = %v", err)
	}
	if _, err := store.Preferences(ctx); !storeUnavailable(err, "read the app preferences") {
		t.Fatalf("Preferences on a closed store = %v", err)
	}
	if _, _, err := store.PutPreferences(ctx, 0, []byte(`{"schema_version":1}`), testNow()); !storeInternal(err, "begin the app state write") {
		t.Fatalf("PutPreferences on a closed store = %v", err)
	}
	if _, err := store.ChangesSince(ctx, 0); !storeUnavailable(err, "begin the app-state catch-up") {
		t.Fatalf("ChangesSince on a closed store = %v", err)
	}
	if _, err := store.Snapshot(ctx); !storeUnavailable(err, "begin the app-state snapshot") {
		t.Fatalf("Snapshot on a closed store = %v", err)
	}
}

// storeUnavailable reports whether err is the typed unavailable read failure.
func storeUnavailable(err error, what string) bool {
	coded := errcode.CodeOf(err) == errcode.Unavailable
	return err != nil && coded && strings.Contains(err.Error(), what)
}

// storeInternal reports whether err is the typed internal write failure.
func storeInternal(err error, what string) bool {
	coded := errcode.CodeOf(err) == errcode.Internal
	return err != nil && coded && strings.Contains(err.Error(), what)
}

// TestPutWindowConflictsOnAMissingWindow pins the create-with-CAS rule: a
// non-zero expected revision on a window that does not exist conflicts with
// the current revision 0, for windows and preferences alike.
func TestPutConflictsOnAMissingDocument(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newStoreFixture(t)

	_, _, err := store.PutWindow(ctx, testUUID(1), 5, []byte(`{"schema_version":1}`), testNow())
	assertConflict(t, err, 0)
	_, _, err = store.PutPreferences(ctx, 5, []byte(`{"schema_version":1}`), testNow())
	assertConflict(t, err, 0)
}

// TestACorruptRowFailsItsScan feeds a non-numeric revision through the
// schema's type affinity and expects the typed scan failure on the list and
// the snapshot.
func TestACorruptRowFailsItsScan(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, closeDB := newCorruptibleFixture(t)
	defer closeDB()

	mustPutWindows(t, ctx, store, 1)
	db := storeDB(store)
	// INTEGER affinity keeps the non-numeric text as TEXT, so scanning it
	// into the revision fails.
	if _, err := db.ExecContext(ctx,
		`UPDATE app_windows SET revision = 'corrupt' WHERE window_id = ?`, testUUID(1)); err != nil {
		t.Fatalf("corrupt the row: %v", err)
	}
	if _, err := store.ListWindows(ctx); !storeUnavailable(err, "scan an app window") {
		t.Fatalf("ListWindows over a corrupt row = %v", err)
	}
	if _, err := store.Snapshot(ctx); !storeUnavailable(err, "scan an app window") {
		t.Fatalf("Snapshot over a corrupt row = %v", err)
	}
}

// TestTheJournalRejectsAnUnknownChangeKind pins the journal's CHECK
// constraint: an unknown kind cannot enter the table, so the topic's event
// types are closed.
func TestTheJournalRejectsAnUnknownChangeKind(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, closeDB := newCorruptibleFixture(t)
	defer closeDB()

	mustPutWindows(t, ctx, store, 1)
	tx, err := storeDB(store).BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer rollback(ctx, tx)
	if _, err := appendChange(ctx, tx, "bogus-kind", testUUID(1), 1, testNow()); err == nil {
		t.Fatal("the journal accepted an unknown change kind")
	}
}

// TestChangeSequenceConvertsGuarded pins the journal-sequence conversion:
// sequences are positive, and a negative one fails typed instead of wrapping
// around.
func TestChangeSequenceConvertsGuarded(t *testing.T) {
	t.Parallel()
	seq, err := ChangeSequence(7)
	if err != nil || seq != 7 {
		t.Fatalf("ChangeSequence(7) = (%d, %v), want (7, nil)", seq, err)
	}
	if _, err := ChangeSequence(-1); err == nil {
		t.Fatal("ChangeSequence(-1) converted a negative sequence")
	}
}

func TestConflictErrorPrintsItsCode(t *testing.T) {
	t.Parallel()
	err := newConflict(3)
	if err.Error() != "appstate.revision_conflict: the document was changed by another writer" {
		t.Fatalf("ConflictError.Error() = %q", err.Error())
	}
	if err.CurrentRevision() != 3 {
		t.Fatalf("CurrentRevision() = %d, want 3", err.CurrentRevision())
	}
	if errcode.CodeOf(err) != errcode.AppStateRevisionConflict {
		t.Fatalf("code = %q", errcode.CodeOf(err))
	}
}
