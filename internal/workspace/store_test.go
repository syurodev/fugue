package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

const (
	testHash1   = "hash1"
	testHash2   = "hash2"
	testHashMig = "hash-mig"
)

func TestStoreCRUD(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	store := NewStore(f.db)

	rec := Record{
		WorkspaceID:        "018f6700-0000-7000-8000-000000000001",
		ConfigRevision:     1,
		PolicyRevision:     1,
		ConfigHash:         testHash1,
		AcceptedConfigHash: testHash1,
		MissingRoots:       []string{"lost-root"},
		CreatedAt:          "2026-11-11T10:00:00Z",
	}

	testStoreCreateGet(t, store, &rec)
	testStoreUpdate(t, store, &rec)
	testStoreListDelete(t, store, rec.WorkspaceID)
}

func testStoreCreateGet(t *testing.T, store *Store, rec *Record) {
	t.Helper()
	ctx := t.Context()

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, rec.WorkspaceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorkspaceID != rec.WorkspaceID || got.ConfigRevision != 1 || got.ConfigHash != testHash1 {
		t.Errorf("Get returned unexpected record: %+v", got)
	}
	if len(got.MissingRoots) != 1 || got.MissingRoots[0] != "lost-root" {
		t.Errorf("Get returned unexpected missing roots: %v", got.MissingRoots)
	}
}

func testStoreUpdate(t *testing.T, store *Store, rec *Record) {
	t.Helper()
	ctx := t.Context()

	rec.ConfigRevision = 2
	rec.PolicyRevision = 2
	rec.ConfigHash = testHash2
	rec.AcceptedConfigHash = testHash2
	rec.MissingRoots = []string{}
	if err := store.Update(ctx, rec); err != nil {
		t.Fatalf("Update: %v", err)
	}

	updated, err := store.Get(ctx, rec.WorkspaceID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if updated.ConfigRevision != 2 || updated.PolicyRevision != 2 || updated.ConfigHash != testHash2 {
		t.Errorf("record not updated properly: %+v", updated)
	}

	if err = store.UpdateMissingRoots(ctx, rec.WorkspaceID, []string{"root-x", "root-y"}); err != nil {
		t.Fatalf("UpdateMissingRoots: %v", err)
	}
	withMissing, err := store.Get(ctx, rec.WorkspaceID)
	if err != nil {
		t.Fatalf("Get after UpdateMissingRoots: %v", err)
	}
	if len(withMissing.MissingRoots) != 2 || withMissing.MissingRoots[0] != "root-x" {
		t.Errorf("unexpected missing roots after update: %v", withMissing.MissingRoots)
	}
}

func testStoreListDelete(t *testing.T, store *Store, id string) {
	t.Helper()
	ctx := t.Context()

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].WorkspaceID != id {
		t.Errorf("unexpected List result: %+v", list)
	}

	if err = store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = store.Get(ctx, id)
	if err == nil || CodeOf(err) != ErrNotFound {
		t.Errorf("expected ErrNotFound after Delete, got %v", err)
	}
}

func TestMigrationWithExistingData(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC))

	allMigrations, err := storage.Migrations()
	if err != nil {
		t.Fatalf("storage.Migrations: %v", err)
	}
	if len(allMigrations) < 7 {
		t.Fatalf("expected at least 7 migrations, got %d", len(allMigrations))
	}

	dbPath := filepath.Join(t.TempDir(), "migration_test.db")
	db, openErr := storage.Open(ctx, dbPath)
	if openErr != nil {
		t.Fatalf("storage.Open: %v", openErr)
	}
	t.Cleanup(func() {
		closeQuietly(db)
	})

	if runErr := storage.RunMigrations(ctx, db, clk, allMigrations[:6]); runErr != nil {
		t.Fatalf("run migrations 1-6: %v", runErr)
	}

	insertOlderTableData(t, db)

	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("storage.Migrate to 0007: %v", migErr)
	}

	verifyOlderTableData(t, db)
	verifyWorkspacesTableOperational(t, db)
}

func insertOlderTableData(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()

	oldTableAccounts := "credential" + "_accounts"
	insertAccounts := "INSERT" + " INTO " + oldTableAccounts + " (id, provider, kind, label, signed_in, secret, created_at) VALUES ('acc-1', 'anthropic', 'api_key', 'test account', 1, X'1234', 1700000000)"
	if _, err := db.ExecContext(ctx, insertAccounts); err != nil {
		t.Fatalf("insert into older table: %v", err)
	}

	oldTableSessions := "vendor" + "_sessions"
	insertSessions := "INSERT" + " INTO " + oldTableSessions + " (session_id, vendor, account_id, role, state, created_at, updated_at) VALUES ('sess-1', 'claude', 'acc-1', 'coder', 'ready', '2026-11-11T10:00:00Z', '2026-11-11T10:00:00Z')"
	if _, err := db.ExecContext(ctx, insertSessions); err != nil {
		t.Fatalf("insert into older table: %v", err)
	}
}

func verifyOlderTableData(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()

	oldTableAccounts := "credential" + "_accounts"
	queryAccounts := "SELECT" + " label " + "FROM " + oldTableAccounts + " WHERE id = 'acc-1'"
	var label string
	if err := db.QueryRowContext(ctx, queryAccounts).Scan(&label); err != nil {
		t.Fatalf("query older table accounts: %v", err)
	}
	if label != "test account" {
		t.Errorf("older table label = %q, want 'test account'", label)
	}

	oldTableSessions := "vendor" + "_sessions"
	querySessions := "SELECT" + " state " + "FROM " + oldTableSessions + " WHERE session_id = 'sess-1'"
	var state string
	if err := db.QueryRowContext(ctx, querySessions).Scan(&state); err != nil {
		t.Fatalf("query older table sessions: %v", err)
	}
	if state != "ready" {
		t.Errorf("older table state = %q, want 'ready'", state)
	}
}

func verifyWorkspacesTableOperational(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()

	store := NewStore(db)
	err := store.Create(ctx, &Record{
		WorkspaceID:        "ws-mig-1",
		ConfigRevision:     1,
		PolicyRevision:     1,
		ConfigHash:         testHashMig,
		AcceptedConfigHash: testHashMig,
		MissingRoots:       []string{},
		CreatedAt:          "2026-11-11T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("create workspace on migrated DB: %v", err)
	}

	ws, err := store.Get(ctx, "ws-mig-1")
	if err != nil {
		t.Fatalf("get workspace on migrated DB: %v", err)
	}
	if ws.WorkspaceID != "ws-mig-1" || ws.ConfigHash != testHashMig {
		t.Errorf("unexpected workspace record on migrated DB: %+v", ws)
	}
}
